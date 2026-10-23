# Vision Service

HTTP boundary for road-condition computer vision. The edge agent posts camera frames; the service returns detections. GPS and vehicle telemetry are attached by the edge pipeline, never by the model.

```bash
pip install -r requirements.txt
uvicorn app:app --port 8090
curl -F image=@frame.jpg localhost:8090/v1/detect
```

Response:

```json
{"model": "yolo:rdd.pt", "width": 1280, "height": 720,
 "detections": [{"class": "pothole", "confidence": 0.87, "bbox": [0.41, 0.62, 0.18, 0.12], "track_id": null}]}
```

`bbox` is `[x, y, w, h]` normalised to the frame. The edge agent derives severity from box size when the model gives none.

## Backends

| `VISION_BACKEND` | |
|---|---|
| `yolo` (default when `MODEL_PATH` is set) | Ultralytics YOLO weights at `MODEL_PATH`. Install `requirements-yolo.txt`. |
| `none` | Returns no detections — lets you run the pipeline before a model exists. |

Class names are normalised to the SadakWatch taxonomy. RDD2022 road-damage codes work out of the box (`D00`/`D10` → `road_crack`, `D20` → `broken_surface`, `D40` → `pothole`); add others with `CLASS_MAP='{"my_label": "debris"}'`. Other env vars: `MIN_CONFIDENCE` (0.35), `IMG_SIZE` (640).

Target classes: pothole, road_crack, broken_surface, waterlogging, debris, road_edge_damage, missing_lane_marking, damaged_manhole, speed_breaker.
