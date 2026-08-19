CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS authorities (
    authority_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    authority_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS divisions (
    division_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    authority_id UUID REFERENCES authorities(authority_id),
    name TEXT NOT NULL,
    geometry geometry(MultiPolygon, 4326),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS contractors (
    contractor_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    registration_number TEXT,
    contact_details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS officers (
    officer_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    authority_id UUID REFERENCES authorities(authority_id),
    name TEXT NOT NULL,
    designation TEXT,
    contact_details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS roads (
    road_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    external_id TEXT,
    name TEXT,
    authority_id UUID REFERENCES authorities(authority_id),
    division_id UUID REFERENCES divisions(division_id),
    geometry geometry(LineString, 4326) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS roads_geometry_gix ON roads USING GIST (geometry);
CREATE INDEX IF NOT EXISTS divisions_geometry_gix ON divisions USING GIST (geometry);

CREATE TABLE IF NOT EXISTS road_contracts (
    contract_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    road_id UUID NOT NULL REFERENCES roads(road_id),
    contractor_id UUID NOT NULL REFERENCES contractors(contractor_id),
    contract_reference TEXT,
    scope TEXT,
    valid_from DATE,
    valid_to DATE,
    source_reference TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS road_events (
    event_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'unknown',
    confidence DOUBLE PRECISION,
    latitude DOUBLE PRECISION NOT NULL,
    longitude DOUBLE PRECISION NOT NULL,
    location geometry(Point, 4326) NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL,
    speed_kph DOUBLE PRECISION,
    heading_degrees DOUBLE PRECISION,
    image_uri TEXT,
    video_uri TEXT,
    road_id UUID REFERENCES roads(road_id),
    authority_id UUID REFERENCES authorities(authority_id),
    division_id UUID REFERENCES divisions(division_id),
    contract_id UUID REFERENCES road_contracts(contract_id),
    officer_id UUID REFERENCES officers(officer_id),
    status TEXT NOT NULL DEFAULT 'open',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS road_events_location_gix ON road_events USING GIST (location);
CREATE INDEX IF NOT EXISTS road_events_detected_at_idx ON road_events (detected_at DESC);
CREATE INDEX IF NOT EXISTS road_events_status_idx ON road_events (status);
CREATE INDEX IF NOT EXISTS road_events_vehicle_idx ON road_events (vehicle_id, detected_at DESC);
