from __future__ import annotations

import os
import threading
from pathlib import Path

import cv2 as cv
import numpy as np
from fastapi import FastAPI, File, HTTPException, UploadFile
from pydantic import BaseModel

INDEX_DIR = Path(os.environ.get("FACE_INDEX_DIR", "/data/uploads/.face-index"))
MODEL_DIR = INDEX_DIR / "models"
INDEX_FILE = INDEX_DIR / "embeddings.npz"
DETECTOR_MODEL = MODEL_DIR / "face_detection_yunet_2023mar.onnx"
RECOGNIZER_MODEL = MODEL_DIR / "face_recognition_sface_2021dec.onnx"
MATCH_THRESHOLD = float(os.environ.get("FACE_MATCH_THRESHOLD", "0.42"))
MAX_SELFIE_BYTES = int(os.environ.get("FACE_MAX_SELFIE_BYTES", str(8 * 1024 * 1024)))

app = FastAPI(title="Wedding face search", docs_url=None, redoc_url=None)
_engine_lock = threading.Lock()
_cache_lock = threading.Lock()
_engine: tuple[cv.FaceDetectorYN, cv.FaceRecognizerSF] | None = None
_cache_mtime_ns = -1
_cache_embeddings: np.ndarray | None = None
_cache_people: np.ndarray | None = None


class SearchResult(BaseModel):
    found: bool
    person_id: str | None = None
    similarity: float | None = None


def _models_ready() -> bool:
    return DETECTOR_MODEL.is_file() and RECOGNIZER_MODEL.is_file()


def _get_engine() -> tuple[cv.FaceDetectorYN, cv.FaceRecognizerSF]:
    global _engine
    if _engine is not None:
        return _engine
    if not _models_ready():
        raise RuntimeError("Face models are missing. Run the Mac face indexer first.")
    detector = cv.FaceDetectorYN.create(
        str(DETECTOR_MODEL), "", (320, 320), 0.75, 0.3, 5000
    )
    recognizer = cv.FaceRecognizerSF.create(str(RECOGNIZER_MODEL), "")
    _engine = (detector, recognizer)
    return _engine


def _load_index() -> tuple[np.ndarray, np.ndarray]:
    global _cache_mtime_ns, _cache_embeddings, _cache_people
    try:
        stat = INDEX_FILE.stat()
    except FileNotFoundError as exc:
        raise RuntimeError("Face index is missing. Run the Mac face indexer first.") from exc

    with _cache_lock:
        if (
            _cache_embeddings is not None
            and _cache_people is not None
            and _cache_mtime_ns == stat.st_mtime_ns
        ):
            return _cache_embeddings, _cache_people

        with np.load(INDEX_FILE, allow_pickle=False) as data:
            embeddings = np.asarray(data["embeddings"], dtype=np.float32)
            people = np.asarray(data["person_ids"]).astype(str)

        if embeddings.ndim != 2 or embeddings.shape[0] != people.shape[0]:
            raise RuntimeError("Face index has an invalid shape.")
        norms = np.linalg.norm(embeddings, axis=1, keepdims=True)
        embeddings = embeddings / np.maximum(norms, 1e-12)

        _cache_embeddings = embeddings
        _cache_people = people
        _cache_mtime_ns = stat.st_mtime_ns
        return embeddings, people


def _embedding_from_bytes(payload: bytes) -> np.ndarray:
    image = cv.imdecode(np.frombuffer(payload, dtype=np.uint8), cv.IMREAD_COLOR)
    if image is None or image.size == 0:
        raise ValueError("Das Selfie konnte nicht gelesen werden.")

    height, width = image.shape[:2]
    max_dimension = 2200
    scale = min(1.0, max_dimension / max(height, width))
    if scale < 1.0:
        image = cv.resize(
            image,
            (max(1, round(width * scale)), max(1, round(height * scale))),
            interpolation=cv.INTER_AREA,
        )
        height, width = image.shape[:2]

    with _engine_lock:
        detector, recognizer = _get_engine()
        detector.setInputSize((width, height))
        _, faces = detector.detect(image)
        if faces is None or len(faces) == 0:
            raise ValueError("Auf dem Selfie wurde kein Gesicht erkannt.")

        face = max(faces, key=lambda row: float(row[2] * row[3]))
        aligned = recognizer.alignCrop(image, face)
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
    if not content_type.startswith("image/"):
        raise HTTPException(status_code=415, detail="Es wird ein Bild benötigt.")

    payload = await file.read(MAX_SELFIE_BYTES + 1)
    if len(payload) > MAX_SELFIE_BYTES:
        raise HTTPException(status_code=413, detail="Das Selfie ist zu groß.")

    try:
        query = _embedding_from_bytes(payload)
        embeddings, person_ids = _load_index()
    except ValueError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    except RuntimeError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc

    if embeddings.shape[0] == 0:
        return SearchResult(found=False)

    scores = embeddings @ query
    best_by_person: dict[str, float] = {}
    for person_id, score in zip(person_ids.tolist(), scores.tolist()):
        current = best_by_person.get(person_id)
        if current is None or score > current:
            best_by_person[person_id] = float(score)

    person_id, similarity = max(best_by_person.items(), key=lambda item: item[1])
    if similarity < MATCH_THRESHOLD:
        return SearchResult(found=False, similarity=similarity)

    return SearchResult(found=True, person_id=person_id, similarity=similarity)
