#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import sys
import tempfile
import urllib.request
from collections import defaultdict
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

import cv2 as cv
import numpy as np
from PIL import Image, ImageOps
from pillow_heif import register_heif_opener

register_heif_opener()

MODEL_URLS = {
    "face_detection_yunet_2023mar.onnx":
        "https://github.com/opencv/opencv_zoo/raw/main/models/face_detection_yunet/face_detection_yunet_2023mar.onnx",
    "face_recognition_sface_2021dec.onnx":
        "https://github.com/opencv/opencv_zoo/raw/main/models/face_recognition_sface/face_recognition_sface_2021dec.onnx",
}
IMAGE_EXTENSIONS = {
    ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".avif", ".tif", ".tiff"
}
IGNORED_DIRS = {
    "@eadir", "#recycle", "thumbs", "thumbnails", "previews", "cache", "originals", ".face-index"
}
DEFAULT_GALLERY = "Gäste-Uploads"


@dataclass
class FaceRecord:
    media: str
    embedding: np.ndarray
    score: float
    bbox: tuple[float, float, float, float]


def download_models(model_dir: Path) -> None:
    model_dir.mkdir(parents=True, exist_ok=True)
    for name, url in MODEL_URLS.items():
        target = model_dir / name
        if target.is_file() and target.stat().st_size > 100_000:
            continue
        print(f"Lade Modell: {name}")
        fd, tmp_name = tempfile.mkstemp(prefix=f".{name}.", dir=model_dir)
        os.close(fd)
        tmp = Path(tmp_name)
        try:
            with urllib.request.urlopen(url, timeout=120) as response, tmp.open("wb") as out:
                while True:
                    chunk = response.read(1024 * 1024)
                    if not chunk:
                        break
                    out.write(chunk)
            if tmp.stat().st_size < 100_000:
                raise RuntimeError(f"Download von {name} ist unvollständig.")
            tmp.replace(target)
        finally:
            tmp.unlink(missing_ok=True)


def media_files(root: Path) -> list[tuple[Path, str]]:
    result: list[tuple[Path, str]] = []

    for entry in root.iterdir():
        if entry.is_file() and entry.suffix.lower() in IMAGE_EXTENSIONS and not entry.name.startswith("."):
            result.append((entry, f"{DEFAULT_GALLERY}/{entry.name}"))

    for gallery in root.iterdir():
        if (
            not gallery.is_dir()
            or gallery.name.startswith(".")
            or gallery.name.lower() in IGNORED_DIRS
        ):
            continue
        for entry in gallery.iterdir():
            if entry.is_file() and entry.suffix.lower() in IMAGE_EXTENSIONS and not entry.name.startswith("."):
                result.append((entry, f"{gallery.name}/{entry.name}"))

    result.sort(key=lambda item: item[1].casefold())
    return result


def load_bgr(path: Path, max_dimension: int) -> np.ndarray:
    with Image.open(path) as image:
        image = ImageOps.exif_transpose(image)
        if getattr(image, "n_frames", 1) > 1:
            image.seek(0)
        image = image.convert("RGB")
        width, height = image.size
        scale = min(1.0, max_dimension / max(width, height))
        if scale < 1.0:
            image = image.resize(
                (max(1, round(width * scale)), max(1, round(height * scale))),
                Image.Resampling.LANCZOS,
            )
        rgb = np.asarray(image, dtype=np.uint8)
    return cv.cvtColor(rgb, cv.COLOR_RGB2BGR)


def normalize(feature: np.ndarray) -> np.ndarray:
    vector = feature.reshape(-1).astype(np.float32)
    norm = float(np.linalg.norm(vector))
    if norm <= 1e-12:
        raise ValueError("Leeres Embedding")
    return vector / norm


def extract_faces(
    path: Path,
    media: str,
    detector: cv.FaceDetectorYN,
    recognizer: cv.FaceRecognizerSF,
    max_dimension: int,
    detector_score: float,
) -> list[FaceRecord]:
    image = load_bgr(path, max_dimension)
    height, width = image.shape[:2]
    detector.setInputSize((width, height))
    detector.setScoreThreshold(detector_score)
    _, faces = detector.detect(image)
    if faces is None:
        return []

    records: list[FaceRecord] = []
    for face in faces:
        try:
            aligned = recognizer.alignCrop(image, face)
            embedding = normalize(recognizer.feature(aligned))
        except cv.error:
            continue
        x, y, w, h = [float(value) for value in face[:4]]
        records.append(
            FaceRecord(
                media=media,
                embedding=embedding,
                score=float(face[-1]),
                bbox=(x, y, w, h),
            )
        )
    return records


def greedy_clusters(embeddings: np.ndarray, threshold: float) -> np.ndarray:
    if embeddings.shape[0] == 0:
        return np.empty((0,), dtype=np.int32)

    centroids: list[np.ndarray] = []
    sums: list[np.ndarray] = []
    counts: list[int] = []
    assignments: list[int] = []

    for embedding in embeddings:
        if not centroids:
            sums.append(embedding.copy())
            counts.append(1)
            centroids.append(embedding.copy())
            assignments.append(0)
            continue

        matrix = np.stack(centroids)
        similarities = matrix @ embedding
        best = int(np.argmax(similarities))
        if float(similarities[best]) >= threshold:
            sums[best] += embedding
            counts[best] += 1
            centroids[best] = normalize(sums[best])
            assignments.append(best)
        else:
            sums.append(embedding.copy())
            counts.append(1)
            centroids.append(embedding.copy())
            assignments.append(len(centroids) - 1)

    assignments_array = np.asarray(assignments, dtype=np.int32)
    if assignments_array.size and int(assignments_array.max()) >= len(centroids):
        raise RuntimeError("Interner Clusterindex ist ungültig.")
    if len(centroids) <= 1:
        return assignments_array

    merge_threshold = min(0.95, threshold + 0.04)
    parent = list(range(len(centroids)))

    def find(value: int) -> int:
        while parent[value] != value:
            parent[value] = parent[parent[value]]
            value = parent[value]
        return value

    def union(left: int, right: int) -> None:
        a, b = find(left), find(right)
        if a != b:
            parent[max(a, b)] = min(a, b)

    matrix = np.stack(centroids)
    for left in range(len(centroids)):
        similarities = matrix[left + 1:] @ matrix[left]
        for offset in np.where(similarities >= merge_threshold)[0].tolist():
            union(left, left + 1 + int(offset))

    roots = [find(index) for index in assignments_array.tolist()]
    ordered_roots = {root: index for index, root in enumerate(sorted(set(roots)))}
    return np.asarray([ordered_roots[root] for root in roots], dtype=np.int32)


def cluster_centroids(embeddings: np.ndarray, person_ids: np.ndarray) -> dict[str, np.ndarray]:
    grouped: dict[str, list[np.ndarray]] = defaultdict(list)
    for embedding, person_id in zip(embeddings, person_ids.tolist()):
        grouped[str(person_id)].append(embedding)
    return {
        person_id: normalize(np.sum(np.stack(rows), axis=0))
        for person_id, rows in grouped.items()
    }


def read_names(path: Path) -> dict[str, str]:
    try:
        data = json.loads(path.read_text("utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        return {}
    names = data.get("names", {})
    if not isinstance(names, dict):
        return {}
    return {
        str(key): " ".join(str(value).split())[:80]
        for key, value in names.items()
        if str(value).strip()
    }


def transfer_names(
    index_dir: Path,
    new_embeddings: np.ndarray,
    new_person_ids: np.ndarray,
    threshold: float,
) -> dict[str, str]:
    old_names = read_names(index_dir / "names.json")
    old_npz = index_dir / "embeddings.npz"
    if not old_names or not old_npz.is_file():
        return old_names

    try:
        with np.load(old_npz, allow_pickle=False) as data:
            old_embeddings = np.asarray(data["embeddings"], dtype=np.float32)
            old_person_ids = np.asarray(data["person_ids"]).astype(str)
    except Exception:
        return old_names

    old_centroids = cluster_centroids(old_embeddings, old_person_ids)
    new_centroids = cluster_centroids(new_embeddings, new_person_ids)
    candidates: list[tuple[float, str, str]] = []
    match_threshold = max(0.45, threshold)

    for old_id, name in old_names.items():
        old_centroid = old_centroids.get(old_id)
        if old_centroid is None:
            continue
        for new_id, new_centroid in new_centroids.items():
            similarity = float(old_centroid @ new_centroid)
            if similarity >= match_threshold:
                candidates.append((similarity, old_id, new_id))

    candidates.sort(reverse=True)
    used_old: set[str] = set()
    used_new: set[str] = set()
    transferred: dict[str, str] = {}
    for _, old_id, new_id in candidates:
        if old_id in used_old or new_id in used_new:
            continue
        transferred[new_id] = old_names[old_id]
        used_old.add(old_id)
        used_new.add(new_id)

    return transferred


def write_json_atomic(path: Path, payload: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(
        "w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", delete=False
    ) as handle:
        json.dump(payload, handle, ensure_ascii=False, indent=2)
        handle.write("\n")
        temp_path = Path(handle.name)
    temp_path.replace(path)


def write_npz_atomic(path: Path, **arrays: np.ndarray) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as handle:
            np.savez_compressed(handle, **arrays)
            handle.flush()
            os.fsync(handle.fileno())
        Path(temp_name).replace(path)
    finally:
        Path(temp_name).unlink(missing_ok=True)


def source_path(root: Path, media: str) -> Path:
    candidate = root / Path(media)
    if candidate.exists():
        return candidate
    gallery, _, filename = media.partition("/")
    if gallery == DEFAULT_GALLERY:
        legacy = root / filename
        if legacy.exists():
            return legacy
    return candidate


def save_person_thumbnails(
    root: Path,
    index_dir: Path,
    records: list[FaceRecord],
    assignments: np.ndarray,
    max_dimension: int,
) -> dict[str, str]:
    thumbs_dir = index_dir / "thumbs"
    thumbs_dir.mkdir(parents=True, exist_ok=True)
    for old in thumbs_dir.glob("p*.jpg"):
        old.unlink(missing_ok=True)

    samples: dict[int, int] = {}
    for index, cluster in enumerate(assignments.tolist()):
        current = samples.get(cluster)
        if current is None or records[index].score > records[current].score:
            samples[cluster] = index

    sample_media: dict[str, str] = {}
    for cluster, record_index in samples.items():
        record = records[record_index]
        image = load_bgr(source_path(root, record.media), max_dimension)
        height, width = image.shape[:2]
        x, y, w, h = record.bbox
        margin = 0.28
        x0 = max(0, math.floor(x - w * margin))
        y0 = max(0, math.floor(y - h * margin))
        x1 = min(width, math.ceil(x + w * (1 + margin)))
        y1 = min(height, math.ceil(y + h * (1 + margin)))
        crop = image[y0:y1, x0:x1]
        if crop.size == 0:
            continue
        person_id = f"p{cluster + 1:04d}"
        target = thumbs_dir / f"{person_id}.jpg"
        cv.imwrite(str(target), crop, [int(cv.IMWRITE_JPEG_QUALITY), 88])
        sample_media[person_id] = record.media
    return sample_media


def fingerprint_files(files: list[tuple[Path, str]]) -> str:
    digest = hashlib.sha256()
    for path, media in files:
        stat = path.stat()
        digest.update(media.encode("utf-8", "surrogatepass"))
        digest.update(str(stat.st_size).encode())
        digest.update(str(stat.st_mtime_ns).encode())
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Erzeugt den lokalen Gesichtsindex für die Wedding-Galerie."
    )
    parser.add_argument("--photos", required=True, type=Path, help="NAS-Hochzeitsordner")
    parser.add_argument("--index-dir", type=Path, help="Standard: <photos>/.face-index")
    parser.add_argument("--cluster-threshold", type=float, default=0.55)
    parser.add_argument("--detector-score", type=float, default=0.65)
    parser.add_argument(
        "--min-photos",
        type=int,
        default=2,
        help="Mindestzahl unterschiedlicher Fotos pro Personencluster (Standard: 2)",
    )
    parser.add_argument("--max-dimension", type=int, default=3200)
    args = parser.parse_args()

    root = args.photos.expanduser().resolve()
    if not root.is_dir():
        parser.error(f"Fotoordner nicht gefunden: {root}")
    if not 0.363 <= args.cluster_threshold <= 0.85:
        parser.error("--cluster-threshold muss zwischen 0.363 und 0.85 liegen.")
    if args.min_photos < 1:
        parser.error("--min-photos muss mindestens 1 sein.")

    index_dir = (args.index_dir or (root / ".face-index")).expanduser().resolve()
    index_dir.mkdir(parents=True, exist_ok=True)
    model_dir = index_dir / "models"
    download_models(model_dir)

    detector = cv.FaceDetectorYN.create(
        str(model_dir / "face_detection_yunet_2023mar.onnx"),
        "",
        (320, 320),
        args.detector_score,
        0.3,
        5000,
    )
    recognizer = cv.FaceRecognizerSF.create(
        str(model_dir / "face_recognition_sface_2021dec.onnx"), ""
    )

    files = media_files(root)
    print(f"{len(files)} Bilddateien gefunden.")
    records: list[FaceRecord] = []
    failed: list[str] = []

    for number, (path, media) in enumerate(files, start=1):
        try:
            records.extend(
                extract_faces(
                    path,
                    media,
                    detector,
                    recognizer,
                    args.max_dimension,
                    args.detector_score,
                )
            )
        except Exception as exc:
            failed.append(f"{media}: {exc}")
        if number % 25 == 0 or number == len(files):
            print(f"[{number}/{len(files)}] {len(records)} Gesichter erkannt")

    if not records:
        raise RuntimeError("Es wurden keine Gesichter erkannt.")

    detected_faces = len(records)
    embeddings = np.stack([record.embedding for record in records]).astype(np.float32)
    search_embeddings = embeddings.copy()
    search_media = np.asarray([record.media for record in records], dtype="<U1024")
    assignments = greedy_clusters(embeddings, args.cluster_threshold)

    cluster_photos: dict[int, set[str]] = defaultdict(set)
    for record, cluster in zip(records, assignments.tolist()):
        cluster_photos[cluster].add(record.media)

    kept_clusters = {
        cluster
        for cluster, photos in cluster_photos.items()
        if len(photos) >= args.min_photos
    }
    discarded_clusters = len(cluster_photos) - len(kept_clusters)
    if not kept_clusters:
        raise RuntimeError(
            f"Kein Personencluster kommt auf mindestens {args.min_photos} unterschiedlichen Fotos vor."
        )

    cluster_map = {
        old_cluster: new_cluster
        for new_cluster, old_cluster in enumerate(sorted(kept_clusters))
    }
    keep_mask = np.asarray(
        [cluster in kept_clusters for cluster in assignments.tolist()],
        dtype=bool,
    )
    records = [
        record
        for record, keep in zip(records, keep_mask.tolist())
        if keep
    ]
    embeddings = embeddings[keep_mask]
    assignments = np.asarray(
        [
            cluster_map[cluster]
            for cluster, keep in zip(assignments.tolist(), keep_mask.tolist())
            if keep
        ],
        dtype=np.int32,
    )
    person_ids = np.asarray(
        [f"p{cluster + 1:04d}" for cluster in assignments.tolist()],
        dtype="<U16",
    )
    media = np.asarray([record.media for record in records], dtype="<U1024")

    names = transfer_names(index_dir, embeddings, person_ids, args.cluster_threshold)
    write_json_atomic(index_dir / "names.json", {"version": 1, "names": names})

    sample_media = save_person_thumbnails(
        root, index_dir, records, assignments, args.max_dimension
    )

    media_people: dict[str, set[str]] = defaultdict(set)
    face_counts: dict[str, int] = defaultdict(int)
    photo_sets: dict[str, set[str]] = defaultdict(set)
    for record, person_id in zip(records, person_ids.tolist()):
        media_people[record.media].add(person_id)
        face_counts[person_id] += 1
        photo_sets[person_id].add(record.media)

    people = []
    for person_id in sorted(face_counts):
        people.append(
            {
                "id": person_id,
                "face_count": face_counts[person_id],
                "photo_count": len(photo_sets[person_id]),
                "sample_media": sample_media.get(person_id, ""),
            }
        )

    index_payload = {
        "version": 1,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "source_fingerprint": fingerprint_files(files),
        "model": {
            "detector": "OpenCV YuNet face_detection_yunet_2023mar",
            "recognizer": "OpenCV SFace face_recognition_sface_2021dec",
            "cluster_threshold": args.cluster_threshold,
            "detector_score": args.detector_score,
            "min_photos": args.min_photos,
        },
        "stats": {
            "images": len(files),
            "detected_faces": detected_faces,
            "faces": len(records),
            "people": len(people),
            "discarded_below_min_photos_clusters": discarded_clusters,
            "failed_images": len(failed),
        },
        "people": people,
        "media": {
            media_name: sorted(person_set)
            for media_name, person_set in sorted(media_people.items())
        },
    }

    write_json_atomic(index_dir / "index.json", index_payload)
    write_npz_atomic(
        index_dir / "embeddings.npz",
        embeddings=embeddings,
        media=media,
        person_ids=person_ids,
        search_embeddings=search_embeddings,
        search_media=search_media,
    )

    if failed:
        (index_dir / "failed.txt").write_text("\n".join(failed) + "\n", "utf-8")
    else:
        (index_dir / "failed.txt").unlink(missing_ok=True)

    print()
    print("Fertig.")
    print(f"Bilder:              {len(files)}")
    print(f"Gesichter erkannt:   {detected_faces}")
    print(f"Gesichter im Index:  {len(records)}")
    print(f"Personen:            {len(people)}")
    print(f"Verworfene Cluster:  {discarded_clusters} (< {args.min_photos} Fotos)")
    print(f"Index:     {index_dir}")
    if names:
        print(f"Übernommene Namen: {len(names)}")
    if failed:
        print(f"Nicht lesbare Bilder: {len(failed)} (siehe failed.txt)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
