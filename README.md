# SadakWatch

> Turning moving vehicles into real-time road-condition sensors.

SadakWatch is a road-condition intelligence platform that captures road defects from moving dashcams, enriches detections with GPS and vehicle telemetry, stores geospatial events, identifies the responsible authority, contractor and officer, and tracks remediation over time.

## Architecture

```text
Dashcam + GPS
     |
     v
Edge Agent -> local durable queue -> API
                                      |
              +-----------------------+----------------+
              |                       |                |
           PostGIS              Object Storage    Vision Service
              |                       |                |
              +-----------------------+----------------+
                                      |
                              Responsibility Engine
                                      |
                                 Web Dashboard
```

## Initial capabilities

- Vehicle-side edge-agent foundation
- GPS-aware road-event domain model
- PostgreSQL/PostGIS schema
- REST API foundation
- Vision-service integration boundary
- Road/authority/contract/officer relationship model
- Local Docker/PostGIS environment
- GitHub Actions CI

## Road-event lifecycle

```text
camera frame
 -> detection
 -> GPS enrichment
 -> event validation
 -> local durable queue
 -> API ingestion
 -> geospatial road match
 -> responsibility lookup
 -> deduplication/aggregation
 -> remediation tracking
```

## Repository layout

```text
cmd/api                 Go API entrypoint
cmd/edge-agent          vehicle-side agent
internal/events         road-event domain model
services/vision         computer-vision boundary
db/migrations           PostgreSQL/PostGIS schema
deployments/docker       local infrastructure
docs                    architecture and design notes
web                     future dashboard
tests                   integration/E2E tests
```

## Local development

Prerequisites: Go 1.24+, Docker, Docker Compose, and Git.

```bash
docker compose up -d postgres
go run ./cmd/api
go run ./cmd/edge-agent
```

API health check:

```bash
curl http://localhost:8080/healthz
```

## Vision targets

The initial detection taxonomy includes potholes, road cracks, broken surfaces, waterlogging, debris, damaged road edges, missing lane markings, damaged manholes, and speed breakers.

## Geospatial responsibility model

Road events are matched to road segments using PostGIS. Road segments can be associated with authorities, divisions, contracts, contractors, and officers. Contract validity dates allow responsibility to change over time, and source references are retained for administrative mappings.

## Status

**Bootstrap phase.** The foundation is in place for edge capture, geospatial ingestion, computer-vision integration, and responsibility mapping. Production detection models, durable edge queues, media storage, deduplication, and the web dashboard will be implemented incrementally.
