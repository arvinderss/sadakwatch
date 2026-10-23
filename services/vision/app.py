"""SadakWatch vision service.

POST /v1/detect with a multipart ``image`` (JPEG/PNG) and receive detections:

    {"model": "...", "detections": [
        {"class": "pothole", "confidence": 0.87, "bbox": [x, y, w, h], "track_id": null}
    ]}

``bbox`` is normalised to the frame (0..1). GPS and vehicle telemetry are
attached by the edge pipeline, never by this service.

Backends (``VISION_BACKEND``):

* ``yolo`` – an Ultralytics YOLO model at ``MODEL_PATH``. Class names are
  mapped to the SadakWatch taxonomy; RDD2022 road-damage codes (D00, D10, D20,
  D40) are understood out of the box and ``CLASS_MAP`` (JSON) can extend it.
* ``none`` – returns no detections. Useful to exercise the pipeline before a
  model is available.
"""

from __future__ import annotations

import io
import json
import logging
import os
import re
from dataclasses import dataclass
from typing import Protocol

from fastapi import FastAPI, File, HTTPException, UploadFile
from PIL import Image, UnidentifiedImageError

log = logging.getLogger("sadakwatch.vision")

TAXONOMY = {
    "pothole",
    "road_crack",
    "broken_surface",
    "waterlogging",
    "debris",
    "road_edge_damage",
    "missing_lane_marking",
    "damaged_manhole",
    "speed_breaker",
}

# Common class names from public road-damage datasets.
DEFAULT_CLASS_MAP = {
    "d00": "road_crack",  # RDD: longitudinal crack
    "d10": "road_crack",  # RDD: transverse crack
    "d20": "broken_surface",  # RDD: alligator crack
    "d40": "pothole",  # RDD: pothole
    "longitudinal_crack": "road_crack",
    "transverse_crack": "road_crack",
    "alligator_crack": "broken_surface",
    "crack": "road_crack",
    "potholes": "pothole",
    "manhole": "damaged_manhole",
    "speed_bump": "speed_breaker",
    "speedbump": "speed_breaker",
    "water": "waterlogging",
    "flood": "waterlogging",
    "lane_marking_missing": "missing_lane_marking",
}

MAX_IMAGE_BYTES = 12 * 1024 * 1024


@dataclass
class Detection:
    cls: str
    confidence: float
    bbox: tuple[float, float, float, float]
    track_id: str | None = None

    def to_json(self) -> dict:
        return {
            "class": self.cls,
            "confidence": round(self.confidence, 4),
            "bbox": [round(v, 5) for v in self.bbox],
            "track_id": self.track_id,
        }


class Backend(Protocol):
    name: str

    def detect(self, image: Image.Image) -> list[Detection]: ...


def normalise_class(raw: str, class_map: dict[str, str]) -> str | None:
    key = re.sub(r"[^a-z0-9]+", "_", raw.strip().lower()).strip("_")
    if key in TAXONOMY:
        return key
    mapped = class_map.get(key)
    return mapped if mapped in TAXONOMY else None


class NullBackend:
    name = "none"

    def detect(self, image: Image.Image) -> list[Detection]:
        return []


class YoloBackend:
    def __init__(self, model_path: str, class_map: dict[str, str], conf: float, imgsz: int):
        from ultralytics import YOLO  # imported lazily: optional dependency

        self.model = YOLO(model_path)
        self.class_map = class_map
        self.conf = conf
        self.imgsz = imgsz
        self.name = f"yolo:{os.path.basename(model_path)}"

    def detect(self, image: Image.Image) -> list[Detection]:
        result = self.model.predict(image, conf=self.conf, imgsz=self.imgsz, verbose=False)[0]
        names = result.names
        out: list[Detection] = []
        width, height = image.size
        for box in result.boxes:
            cls = normalise_class(str(names[int(box.cls)]), self.class_map)
            if cls is None:
                continue
            x1, y1, x2, y2 = (float(v) for v in box.xyxy[0].tolist())
            out.append(
                Detection(
                    cls=cls,
                    confidence=float(box.conf),
                    bbox=(x1 / width, y1 / height, (x2 - x1) / width, (y2 - y1) / height),
                    track_id=None if box.id is None else str(int(box.id)),
                )
            )
        return out


def load_backend() -> Backend:
    kind = os.getenv("VISION_BACKEND", "yolo" if os.getenv("MODEL_PATH") else "none")
    class_map = dict(DEFAULT_CLASS_MAP)
    if extra := os.getenv("CLASS_MAP"):
        class_map.update({k.lower(): v for k, v in json.loads(extra).items()})
    if kind == "yolo":
        path = os.getenv("MODEL_PATH")
        if not path:
            raise RuntimeError("VISION_BACKEND=yolo needs MODEL_PATH")
        return YoloBackend(path, class_map, float(os.getenv("MIN_CONFIDENCE", "0.35")), int(os.getenv("IMG_SIZE", "640")))
    if kind == "none":
        log.warning("vision backend 'none': no detections will be produced")
        return NullBackend()
    raise RuntimeError(f"unknown VISION_BACKEND {kind!r}")


def create_app(backend: Backend | None = None) -> FastAPI:
    app = FastAPI(title="SadakWatch Vision", version="0.2.0")
    app.state.backend = backend

    def get_backend() -> Backend:
        if app.state.backend is None:
            app.state.backend = load_backend()
        return app.state.backend

    @app.get("/healthz")
    def health() -> dict:
        return {"status": "ok", "backend": get_backend().name}

    @app.get("/v1/classes")
    def classes() -> dict:
        return {"classes": sorted(TAXONOMY)}

    @app.post("/v1/detect")
    async def detect(image: UploadFile = File(...)) -> dict:
        data = await image.read(MAX_IMAGE_BYTES + 1)
        if len(data) > MAX_IMAGE_BYTES:
            raise HTTPException(413, "image too large")
        try:
            img = Image.open(io.BytesIO(data))
            img.load()
        except (UnidentifiedImageError, OSError):
            raise HTTPException(400, "not a decodable image")
        img = img.convert("RGB")
        be = get_backend()
        return {
            "model": be.name,
            "width": img.width,
            "height": img.height,
            "detections": [d.to_json() for d in be.detect(img)],
        }

    return app


app = create_app()
