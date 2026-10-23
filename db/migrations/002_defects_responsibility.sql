-- Defect aggregation, time-aware officer responsibility and remediation history.
-- Every statement is idempotent so the file is safe to re-run.

ALTER TABLE authorities ADD COLUMN IF NOT EXISTS contact_details JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE divisions ADD COLUMN IF NOT EXISTS contact_details JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE UNIQUE INDEX IF NOT EXISTS authorities_name_uidx ON authorities (lower(name));
CREATE UNIQUE INDEX IF NOT EXISTS contractors_name_uidx ON contractors (lower(name));
CREATE UNIQUE INDEX IF NOT EXISTS roads_external_id_uidx ON roads (external_id) WHERE external_id IS NOT NULL;

-- Geography expression indexes let ST_DWithin(..::geography, .., metres) use GiST.
CREATE INDEX IF NOT EXISTS roads_geography_gix ON roads USING GIST ((geometry::geography));

-- Officers are assigned either to a specific road or to a whole division for a
-- validity period. Road-level assignments take precedence over division-level.
CREATE TABLE IF NOT EXISTS officer_assignments (
    assignment_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    officer_id UUID NOT NULL REFERENCES officers(officer_id),
    road_id UUID REFERENCES roads(road_id),
    division_id UUID REFERENCES divisions(division_id),
    role TEXT,
    valid_from DATE,
    valid_to DATE,
    source_reference TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT officer_assignment_scope CHECK (road_id IS NOT NULL OR division_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS officer_assignments_road_idx ON officer_assignments (road_id);
CREATE INDEX IF NOT EXISTS officer_assignments_division_idx ON officer_assignments (division_id);
CREATE INDEX IF NOT EXISTS road_contracts_road_idx ON road_contracts (road_id, valid_from DESC);

-- A defect is the persistent physical issue; road_events are its observations.
CREATE TABLE IF NOT EXISTS defects (
    defect_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'unknown',
    max_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    latitude DOUBLE PRECISION NOT NULL,
    longitude DOUBLE PRECISION NOT NULL,
    location geometry(Point, 4326) NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL,
    last_seen TIMESTAMPTZ NOT NULL,
    observation_count INTEGER NOT NULL DEFAULT 0,
    vehicle_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'acknowledged', 'in_progress', 'resolved', 'rejected')),
    status_updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    latest_image_uri TEXT,
    recurrence_of UUID REFERENCES defects(defect_id),
    road_id UUID REFERENCES roads(road_id),
    authority_id UUID REFERENCES authorities(authority_id),
    division_id UUID REFERENCES divisions(division_id),
    contract_id UUID REFERENCES road_contracts(contract_id),
    contractor_id UUID REFERENCES contractors(contractor_id),
    officer_id UUID REFERENCES officers(officer_id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS defects_location_gix ON defects USING GIST (location);
CREATE INDEX IF NOT EXISTS defects_geography_gix ON defects USING GIST ((location::geography));
CREATE INDEX IF NOT EXISTS defects_type_status_idx ON defects (event_type, status);
CREATE INDEX IF NOT EXISTS defects_last_seen_idx ON defects (last_seen DESC);
CREATE INDEX IF NOT EXISTS defects_authority_idx ON defects (authority_id);
CREATE INDEX IF NOT EXISTS defects_contractor_idx ON defects (contractor_id);

ALTER TABLE road_events ADD COLUMN IF NOT EXISTS defect_id UUID REFERENCES defects(defect_id);
ALTER TABLE road_events ADD COLUMN IF NOT EXISTS contractor_id UUID REFERENCES contractors(contractor_id);
ALTER TABLE road_events ADD COLUMN IF NOT EXISTS accuracy_m DOUBLE PRECISION;
ALTER TABLE road_events ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'vision';
ALTER TABLE road_events ADD COLUMN IF NOT EXISTS received_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS road_events_defect_idx ON road_events (defect_id);

CREATE TABLE IF NOT EXISTS defect_status_history (
    history_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    defect_id UUID NOT NULL REFERENCES defects(defect_id),
    from_status TEXT NOT NULL,
    to_status TEXT NOT NULL,
    note TEXT,
    actor TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS defect_status_history_defect_idx ON defect_status_history (defect_id, created_at);
