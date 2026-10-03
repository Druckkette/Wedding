from __future__ import annotations

import os
import threading
from pathlib import Path

import cv2 as cv
import numpy as np
from fastapi import FastAPI, File, HTTPException, UploadFile
from pydantic import BaseModel, Field

INDEX_DIR = Path(os.environ.get("FACE_INDEX_DIR", "/data/uploads/.face-index"))
MODEL_DIR = INDEX_DIR / "models"
INDEX_FILE = INDEX_DIR / "embeddings.npz"
DETECTOR_MODEL = MODEL_DIR / "face_detection_yunet_2023mar.onnx"
RECOGNIZER_MODEL = MODEL_DIR / "face_recognition_sface_2021dec.onnx"
MATCH_THRESHOLD = float(os.environ.get("FACE_MATCH_THRESHOLD", "0.28"))
EXPANSION_THRESHOLD = float(os.environ.get("FACE_EXPANSION_THRESHOLD", "0.28"))
NEIGHBOR_THRESHOLD = float(os.environ.get("FACE_NEIGHBOR_THRESHOLD", "0.48"))
SEED_WINDOW = float(os.environ.get("FACE_SEED_WINDOW", "0.06"))
MAX_SEEDS = int(os.environ.get("FACE_MAX_SEEDS", "8"))
MAX_EXPANSION_SEEDS = int(os.environ.get("FACE_MAX_EXPANSION_SEEDS", "64"))
SECONDARY_NEIGHBOR_THRESHOLD = float(os.environ.get("FACE_SECONDARY_NEIGHBOR_THRESHOLD", "0.43"))
SECONDARY_SUPPORT_THRESHOLD = float(os.environ.get("FACE_SECONDARY_SUPPORT_THRESHOLD", "0.39"))
SECONDARY_MIN_SUPPORT = int(os.environ.get("FACE_SECONDARY_MIN_SUPPORT", "2"))
SECONDARY_DIRECT_FLOOR = float(os.environ.get("FACE_SECONDARY_DIRECT_FLOOR", "0.14"))
SELFIE_DETECTOR_SCORE = float(os.environ.get("FACE_SELFIE_DETECTOR_SCORE", "0.65"))
SELFIE_FALLBACK_SCORE = float(os.environ.get("FACE_SELFIE_FALLBACK_SCORE", "0.50"))
SELFIE_MAX_DIMENSION = int(os.environ.get("FACE_SELFIE_MAX_DIMENSION", "3200"))
MAX_SELFIE_BYTES = int(os.environ.get("FACE_MAX_SELFIE_BYTES", str(8 * 1024 * 1024)))

app = FastAPI(title="Wedding face search", docs_url=None, redoc_url=None)
_engine_lock = threading.Lock()
_cache_lock = threading.Lock()
_engine: tuple[cv.FaceDetectorYN, cv.FaceRecognizerSF] | None = None
_cache_mtime_ns = -1
_cache_embeddings: np.ndarray | None = None
_cache_people: np.ndarray | None = None
_cache_search_embeddings: np.ndarray | None = None
_cache_search_media: np.ndarray | None = None


class SearchResult(BaseModel):
    found: bool
    person_id: str | None = None
    similarity: float | None = None
    matched_media: list[str] = Field(default_factory=list)


def _models_ready() -> bool:
    return DETECTOR_MODEL.is_file() and RECOGNIZER_MODEL.is_file()


def _get_engine() -> tuple[cv.FaceDetectorYN, cv.FaceRecognizerSF]:
    global _engine
    if _engine is not None:
        return _engine
    if not _models_ready():
        raise RuntimeError("Face models are missing. Run the Mac face indexer first.")
    detector = cv.FaceDetectorYN.create(
        str(DETECTOR_MODEL), "", (320, 320), SELFIE_DETECTOR_SCORE, 0.3, 5000
    )
    recognizer = cv.FaceRecognizerSF.create(str(RECOGNIZER_MODEL), "")
    _engine = (detector, recognizer)
    return _engine


def _load_index() -> tuple[np.ndarray, np.ndarray, np.ndarray, np.ndarray]:
    global _cache_mtime_ns, _cache_embeddings, _cache_people
    global _cache_search_embeddings, _cache_search_media
    try:
        stat = INDEX_FILE.stat()
    except FileNotFoundError as exc:
        raise RuntimeError("Face index is missing. Run the Mac face indexer first.") from exc

    with _cache_lock:
        if (
            _cache_embeddings is not None
            and _cache_people is not None
            and _cache_search_embeddings is not None
            and _cache_search_media is not None
            and _cache_mtime_ns == stat.st_mtime_ns
        ):
            return (
                _cache_embeddings,
                _cache_people,
                _cache_search_embeddings,
                _cache_search_media,
            )

        with np.load(INDEX_FILE, allow_pickle=False) as data:
            embeddings = np.asarray(data["embeddings"], dtype=np.float32)
            people = np.asarray(data["person_ids"]).astype(str)
            if "search_embeddings" in data and "search_media" in data:
                search_embeddings = np.asarray(data["search_embeddings"], dtype=np.float32)
                search_media = np.asarray(data["search_media"]).astype(str)
            else:
                search_embeddings = embeddings.copy()
                search_media = np.asarray(data["media"]).astype(str)

        if embeddings.ndim != 2 or embeddings.shape[0] != people.shape[0]:
            raise RuntimeError("Face index has an invalid shape.")
        if search_embeddings.ndim != 2 or search_embeddings.shape[0] != search_media.shape[0]:
            raise RuntimeError("Selfie search index has an invalid shape.")

        norms = np.linalg.norm(embeddings, axis=1, keepdims=True)
        embeddings = embeddings / np.maximum(norms, 1e-12)
        search_norms = np.linalg.norm(search_embeddings, axis=1, keepdims=True)
        search_embeddings = search_embeddings / np.maximum(search_norms, 1e-12)

        _cache_embeddings = embeddings
        _cache_people = people
        _cache_search_embeddings = search_embeddings
        _cache_search_media = search_media
        _cache_mtime_ns = stat.st_mtime_ns
        return embeddings, people, search_embeddings, search_media


def _normalize_feature(feature: np.ndarray) -> np.ndarray:
    vector = feature.reshape(-1).astype(np.float32)
    norm = float(np.linalg.norm(vector))
    if norm <= 1e-12:
        raise ValueError("Das Gesicht konnte nicht ausgewertet werden.")
    return vector / norm


def _embeddings_from_bytes(payload: bytes) -> np.ndarray:
    image = cv.imdecode(np.frombuffer(payload, dtype=np.uint8), cv.IMREAD_COLOR)
    if image is None or image.size == 0:
        raise ValueError("Das Selfie konnte nicht gelesen werden.")

    height, width = image.shape[:2]
    scale = min(1.0, SELFIE_MAX_DIMENSION / max(height, width))
    if scale < 1.0:
        image = cv.resize(
            image,
            (max(1, round(width * scale)), max(1, round(height * scale))),
            interpolation=cv.INTER_AREA,
        )

    candidates = [
        image,
        cv.rotate(image, cv.ROTATE_90_CLOCKWISE),
        cv.rotate(image, cv.ROTATE_90_COUNTERCLOCKWISE),
        cv.rotate(image, cv.ROTATE_180),
    ]

    with _engine_lock:
        detector, recognizer = _get_engine()
        best_candidate = None
        best_face = None
        best_quality = -1.0

        for score_threshold in (SELFIE_DETECTOR_SCORE, SELFIE_FALLBACK_SCORE):
            detector.setScoreThreshold(score_threshold)
            for candidate in candidates:
                candidate_height, candidate_width = candidate.shape[:2]
                detector.setInputSize((candidate_width, candidate_height))
                _, faces = detector.detect(candidate)
                if faces is None:
                    continue
                for face in faces:
                    area = float(face[2] * face[3])
                    confidence = float(face[-1])
                    quality = area * max(confidence, 0.01)
                    if quality > best_quality:
                        best_quality = quality
                        best_candidate = candidate
                        best_face = face
            if best_face is not None:
                break

        detector.setScoreThreshold(SELFIE_DETECTOR_SCORE)

        if best_candidate is None or best_face is None:
            raise ValueError("Auf dem Selfie wurde kein Gesicht erkannt.")

        aligned = recognizer.alignCrop(best_candidate, best_face)
        variants = [aligned, cv.flip(aligned, 1)]
        features = [_normalize_feature(recognizer.feature(variant)) for variant in variants]

    return np.stack(features).astype(np.float32)

@app.get("/healthz")
def healthz() -> dict[str, object]:
    return {
        "ok": _models_ready() and INDEX_FILE.is_file(),
        "models": _models_ready(),
        "index": INDEX_FILE.is_file(),
    }


@app.post("/search", response_model=SearchResult)
async def search(file: UploadFile = File(...)) -> SearchResult:
    content_type = (file.content_type or "").lower()
    if content_type and not content_type.startswith("image/") and content_type != "application/octet-stream":
        raise HTTPException(status_code=415, detail="Es wird ein Bild benötigt.")

    payload = await file.read(MAX_SELFIE_BYTES + 1)
    if len(payload) > MAX_SELFIE_BYTES:
        raise HTTPException(status_code=413, detail="Das Selfie ist zu groß.")

    try:
        queries = _embeddings_from_bytes(payload)
        embeddings, person_ids, search_embeddings, search_media = _load_index()
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    except RuntimeError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc

    if search_embeddings.shape[0] == 0:
        return SearchResult(found=False)

    direct_matrix = search_embeddings @ queries.T
    direct_scores = np.max(direct_matrix, axis=1)
    similarity = float(np.max(direct_scores))
    if similarity < MATCH_THRESHOLD:
        return SearchResult(found=False, similarity=similarity)

    seed_floor = max(MATCH_THRESHOLD, similarity - SEED_WINDOW)
    order = np.argsort(direct_scores)[::-1]
    seed_indices: list[int] = []
    seed_media: set[str] = set()
    for index in order.tolist():
        if float(direct_scores[index]) < seed_floor:
            break
        media = str(search_media[index])
        if media in seed_media:
            continue
        seed_indices.append(index)
        seed_media.add(media)
        if len(seed_indices) >= MAX_SEEDS:
            break

    neighbor_scores = np.zeros_like(direct_scores)
    if seed_indices:
        seed_embeddings = search_embeddings[np.asarray(seed_indices, dtype=np.int32)]
        neighbor_scores = np.max(search_embeddings @ seed_embeddings.T, axis=1)

    adaptive_floor = max(EXPANSION_THRESHOLD, similarity - 0.16)
    match_mask = (direct_scores >= adaptive_floor) | (
        (neighbor_scores >= NEIGHBOR_THRESHOLD)
        & (direct_scores >= max(0.18, MATCH_THRESHOLD - 0.08))
    )

    # The first pass is deliberately precise. Use those matched wedding faces
    # as additional positive examples to recover profile views, distance and
    # difficult lighting. Requiring support from multiple positives limits
    # identity drift into another guest.
    first_pass_indices = np.flatnonzero(match_mask)
    if first_pass_indices.size:
        ranked = first_pass_indices[np.argsort(direct_scores[first_pass_indices])[::-1]]
        expansion_indices: list[int] = []
        expansion_media: set[str] = set()
        for index in ranked.tolist():
            media = str(search_media[index])
            if media in expansion_media:
                continue
            expansion_indices.append(index)
            expansion_media.add(media)
            if len(expansion_indices) >= MAX_EXPANSION_SEEDS:
                break

        expansion_embeddings = search_embeddings[np.asarray(expansion_indices, dtype=np.int32)]
        expansion_matrix = search_embeddings @ expansion_embeddings.T
        secondary_best = np.max(expansion_matrix, axis=1)
        secondary_support = np.sum(
            expansion_matrix >= SECONDARY_SUPPORT_THRESHOLD,
            axis=1,
        )
        secondary_mask = (
            (direct_scores >= SECONDARY_DIRECT_FLOOR)
            & (secondary_best >= SECONDARY_NEIGHBOR_THRESHOLD)
            & (secondary_support >= SECONDARY_MIN_SUPPORT)
        )
        match_mask = match_mask | secondary_mask
        neighbor_scores = np.maximum(neighbor_scores, secondary_best)

    best_by_media: dict[str, float] = {}
    for media, direct, neighbor, matched in zip(
        search_media.tolist(),
        direct_scores.tolist(),
        neighbor_scores.tolist(),
        match_mask.tolist(),
    ):
        if not matched:
            continue
        score = max(float(direct), float(neighbor))
        current = best_by_media.get(media)
        if current is None or score > current:
            best_by_media[media] = score

    matched_media = [
        media
        for media, _ in sorted(best_by_media.items(), key=lambda item: item[1], reverse=True)
    ]

    person_id: str | None = None
    if embeddings.shape[0] > 0:
        person_matrix = embeddings @ queries.T
        scores = np.max(person_matrix, axis=1)
        best_by_person: dict[str, float] = {}
        for candidate_id, score in zip(person_ids.tolist(), scores.tolist()):
            current = best_by_person.get(candidate_id)
            if current is None or score > current:
                best_by_person[candidate_id] = float(score)
        if best_by_person:
            person_id, _ = max(best_by_person.items(), key=lambda item: item[1])

    return SearchResult(
        found=True,
        person_id=person_id,
        similarity=similarity,
        matched_media=matched_media,
    )
