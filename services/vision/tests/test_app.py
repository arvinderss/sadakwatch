import io

from fastapi.testclient import TestClient
from PIL import Image

from app import DEFAULT_CLASS_MAP, Detection, create_app, normalise_class


class FakeBackend:
    name = "fake"

    def detect(self, image):
        return [Detection("pothole", 0.9, (0.1, 0.5, 0.2, 0.2))]


def jpeg_bytes() -> bytes:
    buf = io.BytesIO()
    Image.new("RGB", (64, 48), (90, 90, 90)).save(buf, "JPEG")
    return buf.getvalue()


def test_detect_returns_contract():
    client = TestClient(create_app(FakeBackend()))
    r = client.post("/v1/detect", files={"image": ("f.jpg", jpeg_bytes(), "image/jpeg")})
    assert r.status_code == 200
    body = r.json()
    assert body["width"] == 64 and body["height"] == 48
    assert body["detections"] == [{"class": "pothole", "confidence": 0.9, "bbox": [0.1, 0.5, 0.2, 0.2], "track_id": None}]


def test_rejects_non_images():
    client = TestClient(create_app(FakeBackend()))
    r = client.post("/v1/detect", files={"image": ("f.jpg", b"nope", "image/jpeg")})
    assert r.status_code == 400


def test_health():
    client = TestClient(create_app(FakeBackend()))
    assert client.get("/healthz").json() == {"status": "ok", "backend": "fake"}


def test_class_normalisation():
    assert normalise_class("D40", DEFAULT_CLASS_MAP) == "pothole"
    assert normalise_class("Speed Bump", DEFAULT_CLASS_MAP) == "speed_breaker"
    assert normalise_class("Road-Crack", DEFAULT_CLASS_MAP) == "road_crack"
    assert normalise_class("car", DEFAULT_CLASS_MAP) is None
