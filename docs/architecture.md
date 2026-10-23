# SadakWatch Architecture

## Edge

The vehicle edge agent receives camera frames and GNSS telemetry, runs or invokes computer-vision inference, creates a road event, persists it to a local durable queue, and uploads it when connectivity is available.

## Ingestion

The API accepts validated road events and media references. The ingestion layer is intentionally independent from the vision implementation so detections can originate from different models or devices.

## Geospatial enrichment

A road event is matched to a road segment using PostGIS. The matched segment provides the administrative context used for responsibility resolution.

## Responsibility resolution

Responsibility is time-aware. A road can have different contractors or responsible officers over different contract periods. Every assignment should retain a source reference.

## Media

Images and event video clips are stored outside PostgreSQL. PostgreSQL stores object references and event metadata.

## Deduplication

Each event is merged into the nearest open defect of the same type within a per-type radius (15 m for point defects such as potholes, 40 m for area defects such as waterlogging, widened by poor GPS accuracy up to 60 m). The defect position is the running mean of its observations, its severity is the worst observed, and raw events stay linked and auditable. A transaction-scoped advisory lock per type and ~1 km cell prevents two vehicles from creating twin defects at the same moment. A detection near a defect closed in the last 180 days opens a new defect with `recurrence_of` pointing at the old one — evidence that a repair did not hold.

## Remediation

Defects move through `open → acknowledged → in_progress → resolved` (or `rejected`), and can be reopened. Every change is stored in `defect_status_history` with a note and actor.

## Clients

* `android/` — phones as sensors (accelerometer, camera, GPS, offline queue, WorkManager uploads).
* `cmd/edge-agent` — Linux dashcam computers (frames directory, vision service, NMEA GPS, file queue).
* `web/` — dashboard embedded in the API binary.

## Planned services

```text
edge-agent
api
vision
geospatial
responsibility
media
web
```
