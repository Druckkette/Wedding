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
MATCH_THRESHOLD = float(os.environ.get("FACE_MATCH_THRESHOLD", "0.42"))
EXPANSION_THRESHOLD = float(os.environ.get("FACE_EXPANSION_THRESHOLD", "0.36"))
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


def _embedding_from_bytes(payload: bytes) -> np.ndarray:
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
        detected_image = None
        detected_faces = None

        for score in (SELFIE_DETECTOR_SCORE, SELFIE_FALLBACK_SCORE):
            detector.setScoreThreshold(score)
            for candidate in candidates:
                candidate_height, candidate_width = candidate.shape[:2]
                detector.setInputSize((candidate_width, candidate_height))
                _, faces = detector.detect(candidate)
                if faces is not None and len(faces) > 0:
                    detected_image = candidate
                    detected_faces = faces
                    break
            if detected_faces is not None:
                break

        detector.setScoreThreshold(SELFIE_DETECTOR_SCORE)

        if detected_image is None or detected_faces is None:
            raise ValueError("Auf dem Selfie wurde kein Gesicht erkannt.")

        face = max(detected_faces, key=lambda row: float(row[2] * row[3]))
        aligned = recognizer.alignCrop(detected_image, face)
        feature = recognizer.feature(aligned).reshape(-1).astype(np.float32)

    norm = float(np.linalg.norm(feature))
    if norm <= 1e-12:
        raise ValueError("Das Gesicht konnte nicht ausgewertet werden.")
    return feature / norm


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
        query = _embedding_from_bytes(payload)
        embeddings, person_ids, search_embeddings, search_media = _load_index()
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    except RuntimeError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc

    if search_embeddings.shape[0] == 0:
        return SearchResult(found=False)

    search_scores = search_embeddings @ query
    similarity = float(np.max(search_scores))
    if similarity < MATCH_THRESHOLD:
        return SearchResult(found=False, similarity=similarity)

    best_by_media: dict[str, float] = {}
    for media, score in zip(search_media.tolist(), search_scores.tolist()):
        current = best_by_media.get(media)
        if current is None or score > current:
            best_by_media[media] = float(score)
    matched_media = [
        media
        for media, score in sorted(best_by_media.items(), key=lambda item: item[1], reverse=True)
        if score >= EXPANSION_THRESHOLD
    ]

    person_id: str | None = None
    if embeddings.shape[0] > 0:
        scores = embeddings @ query
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
