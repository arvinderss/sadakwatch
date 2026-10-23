# SadakWatch

> Turning moving vehicles into real-time road-condition sensors.

SadakWatch captures road defects from moving vehicles, enriches each detection with GPS and vehicle telemetry, stores it as a geospatial event, identifies the responsible authority, contractor and officer, merges repeated sightings into one defect, and tracks remediation until the road is fixed.

**Get the Android app:** [`sadakwatch.apk` (latest build)](https://github.com/arvinderss/sadakwatch/releases/download/android-latest/sadakwatch.apk) — built automatically from `main` by GitHub Actions.

## Architecture

```text
Android app / Dashcam + GPS
     |
     v
Edge Agent -> local durable queue -> API ---------------------------+
                                      |                             |
              +-----------------------+----------------+            |
              |                       |                |            |
           PostGIS              Media storage    Vision Service     |
              |                                                     |
     road match -> responsibility -> dedup/recurrence               |
              |                                                     |
              +---------------------> Web dashboard <---------------+
```

| Component | Path | What it does |
|---|---|---|
| API + dashboard | `cmd/api`, `internal/api`, `web/` | REST ingestion, media upload, defect queries, remediation, embedded Leaflet dashboard |
| Stores | `internal/store` | PostGIS store (road matching, time-aware responsibility, dedup) and an in-memory store for demos/tests |
| Edge agent | `cmd/edge-agent`, `internal/edge`, `internal/queue`, `internal/gps` | Linux dashcam pipeline: frames → vision → GPS enrichment → durable queue → upload |
| Android app | `android/` | Phone as a sensor: accelerometer bump detection, camera snapshots, GPS, offline queue |
| Vision service | `services/vision` | FastAPI detection boundary; plug in a YOLO model (RDD2022 classes mapped out of the box) |
| Schema | `db/migrations` | PostgreSQL/PostGIS schema, applied automatically by the API |

## Road-event lifecycle

```text
camera frame / accelerometer spike / manual tap
 -> detection
 -> GPS enrichment
 -> event validation
 -> local durable queue (survives power loss and offline stretches)
 -> API ingestion (idempotent by event_id)
 -> geospatial road match (PostGIS, within 30 m)
 -> responsibility lookup (authority, division, contract/contractor, officer — valid on the detection date)
 -> deduplication into a defect (same type within 15–40 m), recurrence linking after closure
 -> remediation tracking (open → acknowledged → in_progress → resolved/rejected, with history)
```

## Quick start

### Everything in Docker

```bash
docker compose up -d                    # PostGIS + API on http://localhost:8080
docker compose --profile sim up -d      # add a simulated vehicle driving a loop in Chandigarh

# Load the fictional demo road network so the simulated defects get owners:
curl -X POST localhost:8080/v1/admin/roads -H 'Content-Type: application/json' \
     --data @db/seed/demo-roads.geojson
```

Open http://localhost:8080 for the dashboard.

### Without Docker (no database needed)

```bash
go run ./cmd/api                                   # in-memory store, dashboard on :8080
SIMULATE=1 SIM_EVERY_SECONDS=2 go run ./cmd/edge-agent
```

In-memory mode has no road network (responsibility is empty) and forgets data on restart; set `DATABASE_URL` for PostGIS.

### Connect the Android app

1. Install the APK and open **Settings**.
2. Set **API URL** to your server, e.g. `http://192.168.1.20:8080` (phone and server on the same network) or your public HTTPS URL.
3. If the server sets `INGEST_API_KEYS`, enter one as the API key. **Test connection**, then **Save**.
4. Mount the phone facing the road and tap **Start drive**. See [`android/README.md`](android/README.md).

## Configuration (API)

| Variable | Default | Purpose |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Listen address |
| `DATABASE_URL` | – | PostgreSQL/PostGIS DSN. Unset = in-memory store |
| `STORAGE_PATH` | `./data/media` | Where uploaded photos are stored |
| `INGEST_API_KEYS` | – | Comma-separated keys for `POST /v1/events` and `/v1/media`. Unset = open |
| `ADMIN_API_KEY` | – | Key for status changes and road imports. Unset = open |
| `ADMIN_TIMEZONE` | `Asia/Kolkata` | Zone used to decide which contract/assignment was valid on a detection date |
| `ROAD_MATCH_METERS` | `30` | Max distance from a road centreline for attribution |
| `CORS_ORIGIN` | `*` | `Access-Control-Allow-Origin` value |

Edge agent variables are documented at the top of [`cmd/edge-agent/main.go`](cmd/edge-agent/main.go) and in [`docs/api.md`](docs/api.md).

## Responsibility model

Road segments are imported as GeoJSON (`POST /v1/admin/roads`) with properties naming the authority, division, contractor, contract reference, validity dates, officer and a `source_reference`. Authorities, contractors and officers are created by name if missing. When an event arrives the API finds the nearest road within `ROAD_MATCH_METERS`, then the contract and officer assignment valid on the detection date (road-level assignments beat division-level ones). If no road matches, the division polygon containing the point is used. The defect keeps the responsibility that applied when it was first seen, so later contract changes don't rewrite history.

## Development

```bash
go test ./...                     # unit + API tests (in-memory)
TEST_DATABASE_URL=postgres://sadakwatch:sadakwatch@localhost:5432/sadakwatch?sslmode=disable \
  go test ./internal/store        # PostGIS integration tests
cd services/vision && pytest      # vision service
cd android && ./gradlew testDebugUnitTest assembleDebug
```

CI runs all of the above (with a PostGIS service container) and the Android workflow publishes the APK.

## Vision targets

pothole, road_crack, broken_surface, waterlogging, debris, road_edge_damage, missing_lane_marking, damaged_manhole, speed_breaker.

## Status

Working end to end: Android and Linux edge capture, durable offline queues, idempotent ingestion, media storage, PostGIS road matching, time-aware responsibility, deduplication with recurrence tracking, remediation workflow and dashboard. Still to come: a trained detection model shipped with the vision service (the interface and class mapping are ready), object storage for media, authentication beyond API keys, and background (screen-off) recording on Android.
