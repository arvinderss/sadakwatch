package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/arvinderss/sadakwatch/db"
	"github.com/arvinderss/sadakwatch/internal/events"
)

// Postgres is the production Store backed by PostgreSQL + PostGIS.
type Postgres struct {
	db *sql.DB
	// loc is the administrative time zone used to turn detection timestamps
	// into the calendar dates that contracts and assignments are valid on.
	loc *time.Location
	// roadMatchM is how far from a road centreline an event may be and still
	// be attributed to that road.
	roadMatchM float64
}

// PostgresOptions configures the Postgres store.
type PostgresOptions struct {
	TimeZone   string  // IANA zone, default Asia/Kolkata
	RoadMatchM float64 // default 30 metres
}

// OpenPostgres connects, verifies the connection and applies migrations.
func OpenPostgres(ctx context.Context, dsn string, opts PostgresOptions) (*Postgres, error) {
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(20)
	conn.SetMaxIdleConns(10)
	conn.SetConnMaxLifetime(30 * time.Minute)
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	tz := opts.TimeZone
	if tz == "" {
		tz = "Asia/Kolkata"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("load time zone %q: %w", tz, err)
	}
	match := opts.RoadMatchM
	if match <= 0 {
		match = 30
	}
	p := &Postgres{db: conn, loc: loc, roadMatchM: match}
	if err := p.Migrate(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return p, nil
}

// DB exposes the underlying pool (used by integration tests).
func (p *Postgres) DB() *sql.DB { return p.db }

// Migrate applies every embedded migration that has not been applied yet.
// A session advisory lock keeps concurrent API replicas from racing.
func (p *Postgres) Migrate(ctx context.Context) error {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	const lockKey = 727274001
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey)

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := fs.Glob(db.Migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, err := db.Migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// respIDs holds the identifiers resolved for a location.
type respIDs struct {
	road, authority, division, contract, contractor, officer sql.NullString
}

func nullable(s sql.NullString) any {
	if s.Valid && s.String != "" {
		return s.String
	}
	return nil
}

const pointSQL = `ST_SetSRID(ST_MakePoint($1, $2), 4326)`

// resolve finds the road, authority, division, contract, contractor and
// officer responsible for the point on the given date.
func (p *Postgres) resolve(ctx context.Context, q queryer, lat, lon float64, at time.Time) (respIDs, error) {
	var r respIDs
	err := q.QueryRowContext(ctx, `
		SELECT r.road_id, r.authority_id, r.division_id
		FROM roads r
		WHERE ST_DWithin(r.geometry::geography, `+pointSQL+`::geography, $3)
		ORDER BY ST_Distance(r.geometry::geography, `+pointSQL+`::geography)
		LIMIT 1`, lon, lat, p.roadMatchM).Scan(&r.road, &r.authority, &r.division)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("road match: %w", err)
	}
	if !r.division.Valid {
		var div, auth sql.NullString
		err := q.QueryRowContext(ctx, `
			SELECT division_id, authority_id FROM divisions
			WHERE geometry IS NOT NULL AND ST_Covers(geometry, `+pointSQL+`)
			LIMIT 1`, lon, lat).Scan(&div, &auth)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("division match: %w", err)
		}
		r.division = div
		if !r.authority.Valid {
			r.authority = auth
		}
	}
	if !r.authority.Valid && r.division.Valid {
		if err := q.QueryRowContext(ctx, `SELECT authority_id FROM divisions WHERE division_id = $1`, r.division.String).Scan(&r.authority); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, err
		}
	}
	day := at.In(p.loc).Format("2006-01-02")
	if r.road.Valid {
		err := q.QueryRowContext(ctx, `
			SELECT contract_id, contractor_id FROM road_contracts
			WHERE road_id = $1
			  AND (valid_from IS NULL OR valid_from <= $2::date)
			  AND (valid_to IS NULL OR valid_to >= $2::date)
			ORDER BY valid_from DESC NULLS LAST, created_at DESC
			LIMIT 1`, r.road.String, day).Scan(&r.contract, &r.contractor)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("contract lookup: %w", err)
		}
	}
	if r.road.Valid || r.division.Valid {
		err := q.QueryRowContext(ctx, `
			SELECT officer_id FROM officer_assignments
			WHERE (road_id = $1::uuid OR (road_id IS NULL AND division_id = $2::uuid))
			  AND (valid_from IS NULL OR valid_from <= $3::date)
			  AND (valid_to IS NULL OR valid_to >= $3::date)
			ORDER BY (road_id IS NOT NULL) DESC, valid_from DESC NULLS LAST, created_at DESC
			LIMIT 1`, nullable(r.road), nullable(r.division), day).Scan(&r.officer)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("officer lookup: %w", err)
		}
	}
	return r, nil
}

// Ingest implements Store.
func (p *Postgres) Ingest(ctx context.Context, e events.RoadEvent) (events.IngestResult, error) {
	res := events.IngestResult{EventID: e.EventID}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	inserted, err := tx.ExecContext(ctx, `
		INSERT INTO road_events (event_id, vehicle_id, event_type, severity, confidence,
			latitude, longitude, location, detected_at, speed_kph, heading_degrees,
			image_uri, video_uri, accuracy_m, source)
		VALUES ($3, $4, $5, $6, $7, $2, $1, `+pointSQL+`, $8, $9, $10, NULLIF($11, ''), NULLIF($12, ''), $13, $14)
		ON CONFLICT (event_id) DO NOTHING`,
		e.Longitude, e.Latitude, e.EventID, e.VehicleID, string(e.EventType), string(e.Severity), e.Confidence,
		e.DetectedAt, e.SpeedKPH, e.HeadingDegrees, e.ImageURI, e.VideoURI, e.AccuracyM, string(e.Source))
	if err != nil {
		return res, fmt.Errorf("insert event: %w", err)
	}
	if n, _ := inserted.RowsAffected(); n == 0 {
		var defect sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT defect_id FROM road_events WHERE event_id = $1`, e.EventID).Scan(&defect); err != nil {
			return res, err
		}
		res.Duplicate = true
		res.DefectID = defect.String
		return res, nil
	}

	ids, err := p.resolve(ctx, tx, e.Latitude, e.Longitude, e.DetectedAt)
	if err != nil {
		return res, err
	}

	// Serialise defect matching for this type in this ~1 km cell so two
	// vehicles reporting the same pothole at once cannot create twins.
	cell := fmt.Sprintf("%s:%d:%d", e.EventType, int(math.Floor(e.Latitude*100)), int(math.Floor(e.Longitude*100)))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, cell); err != nil {
		return res, err
	}

	radius := dedupRadius(e)
	var (
		defectID  string
		curLat    float64
		curLon    float64
		count     int
		severity  string
		maxConf   float64
		firstSeen time.Time
		lastSeen  time.Time
		latestImg sql.NullString
		foundOpen = true
	)
	err = tx.QueryRowContext(ctx, `
		SELECT defect_id, latitude, longitude, observation_count, severity, max_confidence,
		       first_seen, last_seen, latest_image_uri
		FROM defects
		WHERE event_type = $3
		  AND status NOT IN ('resolved', 'rejected')
		  AND ST_DWithin(location::geography, `+pointSQL+`::geography, $4)
		ORDER BY ST_Distance(location::geography, `+pointSQL+`::geography)
		LIMIT 1
		FOR UPDATE`, e.Longitude, e.Latitude, string(e.EventType), radius).
		Scan(&defectID, &curLat, &curLon, &count, &severity, &maxConf, &firstSeen, &lastSeen, &latestImg)
	if errors.Is(err, sql.ErrNoRows) {
		foundOpen = false
	} else if err != nil {
		return res, fmt.Errorf("defect match: %w", err)
	}

	if foundOpen {
		n := float64(count)
		newLat := (curLat*n + e.Latitude) / (n + 1)
		newLon := (curLon*n + e.Longitude) / (n + 1)
		sev := events.MaxSeverity(events.Severity(severity), e.Severity)
		if e.Confidence > maxConf {
			maxConf = e.Confidence
		}
		if e.DetectedAt.Before(firstSeen) {
			firstSeen = e.DetectedAt
		}
		img := latestImg.String
		if !e.DetectedAt.Before(lastSeen) {
			lastSeen = e.DetectedAt
			if e.ImageURI != "" {
				img = e.ImageURI
			}
		} else if img == "" {
			img = e.ImageURI
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE defects SET latitude = $2, longitude = $1, location = `+pointSQL+`,
				observation_count = observation_count + 1, severity = $3, max_confidence = $4,
				first_seen = $5, last_seen = $6, latest_image_uri = NULLIF($7, '')
			WHERE defect_id = $8`,
			newLon, newLat, string(sev), maxConf, firstSeen, lastSeen, img, defectID)
		if err != nil {
			return res, fmt.Errorf("update defect: %w", err)
		}
	} else {
		var recurrence sql.NullString
		err = tx.QueryRowContext(ctx, `
			SELECT defect_id FROM defects
			WHERE event_type = $3
			  AND status IN ('resolved', 'rejected')
			  AND status_updated_at >= now() - make_interval(secs => $5)
			  AND ST_DWithin(location::geography, `+pointSQL+`::geography, $4)
			ORDER BY ST_Distance(location::geography, `+pointSQL+`::geography)
			LIMIT 1`, e.Longitude, e.Latitude, string(e.EventType), radius, RecurrenceWindow.Seconds()).Scan(&recurrence)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return res, fmt.Errorf("recurrence lookup: %w", err)
		}
		err = tx.QueryRowContext(ctx, `
			INSERT INTO defects (event_type, severity, max_confidence, latitude, longitude, location,
				first_seen, last_seen, observation_count, latest_image_uri, recurrence_of,
				road_id, authority_id, division_id, contract_id, contractor_id, officer_id)
			VALUES ($3, $4, $5, $2, $1, `+pointSQL+`, $6, $6, 1, NULLIF($7, ''), $8,
				$9, $10, $11, $12, $13, $14)
			RETURNING defect_id`,
			e.Longitude, e.Latitude, string(e.EventType), string(e.Severity), e.Confidence, e.DetectedAt,
			e.ImageURI, nullable(recurrence),
			nullable(ids.road), nullable(ids.authority), nullable(ids.division),
			nullable(ids.contract), nullable(ids.contractor), nullable(ids.officer)).Scan(&defectID)
		if err != nil {
			return res, fmt.Errorf("insert defect: %w", err)
		}
		res.NewDefect = true
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE road_events SET defect_id = $2, road_id = $3, authority_id = $4, division_id = $5,
			contract_id = $6, contractor_id = $7, officer_id = $8
		WHERE event_id = $1`,
		e.EventID, defectID, nullable(ids.road), nullable(ids.authority), nullable(ids.division),
		nullable(ids.contract), nullable(ids.contractor), nullable(ids.officer))
	if err != nil {
		return res, fmt.Errorf("link event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE defects SET vehicle_count = (
			SELECT count(DISTINCT vehicle_id) FROM road_events WHERE defect_id = $1)
		WHERE defect_id = $1`, defectID); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.DefectID = defectID
	return res, nil
}

// whereBuilder accumulates SQL predicates with positional arguments.
type whereBuilder struct {
	clauses []string
	args    []any
}

func (w *whereBuilder) add(clause string, args ...any) {
	// Each %d placeholder in clause is replaced by the next argument position.
	parts := make([]any, len(args))
	for i := range args {
		w.args = append(w.args, args[i])
		parts[i] = len(w.args)
	}
	w.clauses = append(w.clauses, fmt.Sprintf(clause, parts...))
}

func (w *whereBuilder) sql() string {
	if len(w.clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.clauses, " AND ")
}

func typeStrings(ts []events.EventType) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}

// ListEvents implements Store.
func (p *Postgres) ListEvents(ctx context.Context, f Filter) ([]events.RoadEvent, error) {
	var w whereBuilder
	if f.BBox != nil {
		w.add("e.location && ST_MakeEnvelope($%d, $%d, $%d, $%d, 4326)", f.BBox.MinLon, f.BBox.MinLat, f.BBox.MaxLon, f.BBox.MaxLat)
	}
	if len(f.Types) > 0 {
		w.add("e.event_type = ANY($%d)", pq.Array(typeStrings(f.Types)))
	}
	if f.VehicleID != "" {
		w.add("e.vehicle_id = $%d", f.VehicleID)
	}
	if f.DefectID != "" {
		w.add("e.defect_id = $%d", f.DefectID)
	}
	if !f.Since.IsZero() {
		w.add("e.detected_at >= $%d", f.Since)
	}
	rows, err := p.db.QueryContext(ctx, eventSelect+w.sql()+fmt.Sprintf(" ORDER BY e.detected_at DESC LIMIT %d", f.EffectiveLimit()), w.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []events.RoadEvent{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const eventSelect = `
	SELECT e.event_id::text, e.vehicle_id, e.event_type, e.severity, COALESCE(e.confidence, 0),
	       e.latitude, e.longitude, COALESCE(e.accuracy_m, 0), e.detected_at,
	       COALESCE(e.speed_kph, 0), COALESCE(e.heading_degrees, 0), e.source,
	       COALESCE(e.image_uri, ''), COALESCE(e.video_uri, ''),
	       COALESCE(e.defect_id::text, ''), COALESCE(e.road_id::text, ''), e.received_at
	FROM road_events e`

type scanner interface{ Scan(dest ...any) error }

func scanEvent(s scanner) (events.RoadEvent, error) {
	var e events.RoadEvent
	var typ, sev, src string
	err := s.Scan(&e.EventID, &e.VehicleID, &typ, &sev, &e.Confidence, &e.Latitude, &e.Longitude,
		&e.AccuracyM, &e.DetectedAt, &e.SpeedKPH, &e.HeadingDegrees, &src, &e.ImageURI, &e.VideoURI,
		&e.DefectID, &e.RoadID, &e.ReceivedAt)
	e.EventType, e.Severity, e.Source = events.EventType(typ), events.Severity(sev), events.Source(src)
	e.DetectedAt, e.ReceivedAt = e.DetectedAt.UTC(), e.ReceivedAt.UTC()
	return e, err
}

// GetEvent implements Store.
func (p *Postgres) GetEvent(ctx context.Context, id string) (events.RoadEvent, error) {
	if !events.IsUUID(id) {
		return events.RoadEvent{}, ErrNotFound
	}
	e, err := scanEvent(p.db.QueryRowContext(ctx, eventSelect+` WHERE e.event_id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

const defectSelect = `
	SELECT d.defect_id::text, d.event_type, d.severity, d.max_confidence, d.latitude, d.longitude,
	       d.first_seen, d.last_seen, d.observation_count, d.vehicle_count, d.status, d.status_updated_at,
	       COALESCE(d.latest_image_uri, ''), COALESCE(d.recurrence_of::text, ''),
	       COALESCE(d.road_id::text, ''), COALESCE(r.name, ''),
	       COALESCE(d.authority_id::text, ''), COALESCE(a.name, ''),
	       COALESCE(d.division_id::text, ''), COALESCE(dv.name, ''),
	       COALESCE(d.contract_id::text, ''), COALESCE(c.contract_reference, ''),
	       COALESCE(d.contractor_id::text, ''), COALESCE(ct.name, ''),
	       COALESCE(d.officer_id::text, ''), COALESCE(o.name, ''), COALESCE(o.designation, ''),
	       COALESCE(c.source_reference, '')
	FROM defects d
	LEFT JOIN roads r ON r.road_id = d.road_id
	LEFT JOIN authorities a ON a.authority_id = d.authority_id
	LEFT JOIN divisions dv ON dv.division_id = d.division_id
	LEFT JOIN road_contracts c ON c.contract_id = d.contract_id
	LEFT JOIN contractors ct ON ct.contractor_id = d.contractor_id
	LEFT JOIN officers o ON o.officer_id = d.officer_id`

func scanDefect(s scanner) (events.Defect, error) {
	var d events.Defect
	var typ, sev string
	r := &d.Responsibility
	err := s.Scan(&d.DefectID, &typ, &sev, &d.MaxConfidence, &d.Latitude, &d.Longitude,
		&d.FirstSeen, &d.LastSeen, &d.ObservationCount, &d.VehicleCount, &d.Status, &d.StatusUpdatedAt,
		&d.LatestImageURI, &d.RecurrenceOf,
		&r.RoadID, &r.RoadName, &r.AuthorityID, &r.AuthorityName, &r.DivisionID, &r.DivisionName,
		&r.ContractID, &r.ContractReference, &r.ContractorID, &r.ContractorName,
		&r.OfficerID, &r.OfficerName, &r.OfficerTitle, &r.SourceReference)
	d.EventType, d.Severity = events.EventType(typ), events.Severity(sev)
	d.FirstSeen, d.LastSeen, d.StatusUpdatedAt = d.FirstSeen.UTC(), d.LastSeen.UTC(), d.StatusUpdatedAt.UTC()
	return d, err
}

// ListDefects implements Store.
func (p *Postgres) ListDefects(ctx context.Context, f Filter) ([]events.Defect, error) {
	var w whereBuilder
	if f.BBox != nil {
		w.add("d.location && ST_MakeEnvelope($%d, $%d, $%d, $%d, 4326)", f.BBox.MinLon, f.BBox.MinLat, f.BBox.MaxLon, f.BBox.MaxLat)
	}
	if len(f.Types) > 0 {
		w.add("d.event_type = ANY($%d)", pq.Array(typeStrings(f.Types)))
	}
	if len(f.Statuses) > 0 {
		w.add("d.status = ANY($%d)", pq.Array(f.Statuses))
	}
	if !f.Since.IsZero() {
		w.add("d.last_seen >= $%d", f.Since)
	}
	rows, err := p.db.QueryContext(ctx, defectSelect+w.sql()+fmt.Sprintf(" ORDER BY d.last_seen DESC LIMIT %d", f.EffectiveLimit()), w.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []events.Defect{}
	for rows.Next() {
		d, err := scanDefect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDefect implements Store.
func (p *Postgres) GetDefect(ctx context.Context, id string) (events.DefectDetail, error) {
	if !events.IsUUID(id) {
		return events.DefectDetail{}, ErrNotFound
	}
	d, err := scanDefect(p.db.QueryRowContext(ctx, defectSelect+` WHERE d.defect_id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return events.DefectDetail{}, ErrNotFound
	}
	if err != nil {
		return events.DefectDetail{}, err
	}
	detail := events.DefectDetail{Defect: d, History: []events.StatusChange{}}
	detail.Observations, err = p.ListEvents(ctx, Filter{DefectID: id, Limit: 200})
	if err != nil {
		return detail, err
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT from_status, to_status, COALESCE(note, ''), COALESCE(actor, ''), created_at
		FROM defect_status_history WHERE defect_id = $1 ORDER BY created_at`, id)
	if err != nil {
		return detail, err
	}
	defer rows.Close()
	for rows.Next() {
		h := events.StatusChange{DefectID: id}
		if err := rows.Scan(&h.FromStatus, &h.ToStatus, &h.Note, &h.Actor, &h.CreatedAt); err != nil {
			return detail, err
		}
		h.CreatedAt = h.CreatedAt.UTC()
		detail.History = append(detail.History, h)
	}
	return detail, rows.Err()
}

// UpdateDefectStatus implements Store.
func (p *Postgres) UpdateDefectStatus(ctx context.Context, id, status, note, actor string) (events.Defect, error) {
	if !events.ValidStatus(status) {
		return events.Defect{}, ErrInvalidTransition
	}
	if !events.IsUUID(id) {
		return events.Defect{}, ErrNotFound
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return events.Defect{}, err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT status FROM defects WHERE defect_id = $1 FOR UPDATE`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return events.Defect{}, ErrNotFound
	}
	if err != nil {
		return events.Defect{}, err
	}
	if current == status {
		return events.Defect{}, ErrInvalidTransition
	}
	if _, err := tx.ExecContext(ctx, `UPDATE defects SET status = $2, status_updated_at = now() WHERE defect_id = $1`, id, status); err != nil {
		return events.Defect{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO defect_status_history (defect_id, from_status, to_status, note, actor)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''))`, id, current, status, note, actor); err != nil {
		return events.Defect{}, err
	}
	if err := tx.Commit(); err != nil {
		return events.Defect{}, err
	}
	d, err := scanDefect(p.db.QueryRowContext(ctx, defectSelect+` WHERE d.defect_id = $1`, id))
	return d, err
}

// Stats implements Store.
func (p *Postgres) Stats(ctx context.Context) (Stats, error) {
	s := Stats{ByType: map[string]CountByName{}, ByStatus: map[string]int{}, TopAuthorities: []CountByName{}, TopContractors: []CountByName{}}
	err := p.db.QueryRowContext(ctx, `
		SELECT count(*), count(DISTINCT vehicle_id), count(*) FILTER (WHERE received_at >= now() - interval '24 hours')
		FROM road_events`).Scan(&s.TotalEvents, &s.Vehicles, &s.EventsLast24h)
	if err != nil {
		return s, err
	}
	rows, err := p.db.QueryContext(ctx, `
		SELECT event_type, status, count(*) FROM defects GROUP BY event_type, status`)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var typ, status string
		var n int
		if err := rows.Scan(&typ, &status, &n); err != nil {
			rows.Close()
			return s, err
		}
		c := s.ByType[typ]
		c.Name = typ
		c.Total += n
		s.TotalDefects += n
		s.ByStatus[status] += n
		if !events.Closed(status) {
			c.Open += n
			s.OpenDefects += n
		}
		if status == events.StatusResolved {
			s.ResolvedDefects += n
		}
		s.ByType[typ] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	top := func(query string) ([]CountByName, error) {
		rows, err := p.db.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []CountByName{}
		for rows.Next() {
			var c CountByName
			if err := rows.Scan(&c.ID, &c.Name, &c.Open, &c.Total); err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, rows.Err()
	}
	if s.TopAuthorities, err = top(`
		SELECT a.authority_id::text, a.name,
		       count(*) FILTER (WHERE d.status NOT IN ('resolved', 'rejected')), count(*)
		FROM defects d JOIN authorities a ON a.authority_id = d.authority_id
		GROUP BY a.authority_id, a.name ORDER BY 3 DESC, 4 DESC LIMIT 10`); err != nil {
		return s, err
	}
	if s.TopContractors, err = top(`
		SELECT c.contractor_id::text, c.name,
		       count(*) FILTER (WHERE d.status NOT IN ('resolved', 'rejected')), count(*)
		FROM defects d JOIN contractors c ON c.contractor_id = d.contractor_id
		GROUP BY c.contractor_id, c.name ORDER BY 3 DESC, 4 DESC LIMIT 10`); err != nil {
		return s, err
	}
	var avg sql.NullFloat64
	if err := p.db.QueryRowContext(ctx, `
		SELECT avg(extract(epoch FROM (status_updated_at - first_seen)) / 3600.0)
		FROM defects WHERE status = 'resolved'`).Scan(&avg); err != nil {
		return s, err
	}
	if avg.Valid {
		s.AvgResolutionHours = &avg.Float64
	}
	return s, nil
}

// Responsibility implements Store.
func (p *Postgres) Responsibility(ctx context.Context, lat, lon float64, at time.Time) (events.Responsibility, error) {
	ids, err := p.resolve(ctx, p.db, lat, lon, at)
	if err != nil {
		return events.Responsibility{}, err
	}
	var r events.Responsibility
	err = p.db.QueryRowContext(ctx, `
		SELECT COALESCE($1::uuid::text, ''), COALESCE((SELECT name FROM roads WHERE road_id = $1::uuid), ''),
		       COALESCE($2::uuid::text, ''), COALESCE((SELECT name FROM authorities WHERE authority_id = $2::uuid), ''),
		       COALESCE($3::uuid::text, ''), COALESCE((SELECT name FROM divisions WHERE division_id = $3::uuid), ''),
		       COALESCE($4::uuid::text, ''), COALESCE((SELECT contract_reference FROM road_contracts WHERE contract_id = $4::uuid), ''),
		       COALESCE((SELECT source_reference FROM road_contracts WHERE contract_id = $4::uuid), ''),
		       COALESCE($5::uuid::text, ''), COALESCE((SELECT name FROM contractors WHERE contractor_id = $5::uuid), ''),
		       COALESCE($6::uuid::text, ''), COALESCE((SELECT name FROM officers WHERE officer_id = $6::uuid), ''),
		       COALESCE((SELECT designation FROM officers WHERE officer_id = $6::uuid), '')`,
		nullable(ids.road), nullable(ids.authority), nullable(ids.division), nullable(ids.contract),
		nullable(ids.contractor), nullable(ids.officer)).
		Scan(&r.RoadID, &r.RoadName, &r.AuthorityID, &r.AuthorityName, &r.DivisionID, &r.DivisionName,
			&r.ContractID, &r.ContractReference, &r.SourceReference, &r.ContractorID, &r.ContractorName,
			&r.OfficerID, &r.OfficerName, &r.OfficerTitle)
	return r, err
}

// upsertByName returns the id of the row named name in table, creating it with
// the extra columns when missing. Names are matched case-insensitively.
func upsertByName(ctx context.Context, q queryer, table, idCol, name string, extraCols []string, extraVals []any) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s::text FROM %s WHERE lower(name) = lower($1) LIMIT 1`, idCol, table), name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	cols := append([]string{"name"}, extraCols...)
	vals := append([]any{name}, extraVals...)
	ph := make([]string, len(vals))
	for i := range vals {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	err = q.QueryRowContext(ctx, fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) RETURNING %s::text`,
		table, strings.Join(cols, ", "), strings.Join(ph, ", "), idCol), vals...).Scan(&id)
	return id, err
}

func nullStr(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// ImportRoads implements Store. Each road is imported in its own savepoint so
// one bad feature does not abort the batch.
func (p *Postgres) ImportRoads(ctx context.Context, roads []RoadImport) (ImportSummary, error) {
	sum := ImportSummary{Errors: []string{}}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return sum, err
	}
	defer tx.Rollback()
	for i, r := range roads {
		label := r.Name
		if label == "" {
			label = r.ExternalID
		}
		if label == "" {
			label = fmt.Sprintf("feature %d", i)
		}
		if _, err := tx.ExecContext(ctx, `SAVEPOINT road_import`); err != nil {
			return sum, err
		}
		contract, officer, err := p.importRoad(ctx, tx, r)
		if err != nil {
			sum.Errors = append(sum.Errors, fmt.Sprintf("%s: %v", label, err))
			if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT road_import`); err != nil {
				return sum, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT road_import`); err != nil {
			return sum, err
		}
		sum.Roads++
		if contract {
			sum.Contracts++
		}
		if officer {
			sum.Officers++
		}
	}
	return sum, tx.Commit()
}

func (p *Postgres) importRoad(ctx context.Context, q queryer, r RoadImport) (contract, officer bool, err error) {
	if len(r.Coordinates) < 2 {
		return false, false, errors.New("a road needs at least two coordinates")
	}
	for _, c := range r.Coordinates {
		if c[0] < -180 || c[0] > 180 || c[1] < -90 || c[1] > 90 {
			return false, false, fmt.Errorf("coordinate %v out of range", c)
		}
	}
	wkt := make([]string, len(r.Coordinates))
	for i, c := range r.Coordinates {
		wkt[i] = fmt.Sprintf("%f %f", c[0], c[1])
	}
	line := "LINESTRING(" + strings.Join(wkt, ", ") + ")"

	var authorityID, divisionID any
	if r.Authority != "" {
		typ := r.AuthorityType
		if typ == "" {
			typ = "road_authority"
		}
		id, err := upsertByName(ctx, q, "authorities", "authority_id", r.Authority, []string{"authority_type"}, []any{typ})
		if err != nil {
			return false, false, fmt.Errorf("authority: %w", err)
		}
		authorityID = id
	}
	if r.Division != "" {
		var id string
		err := q.QueryRowContext(ctx, `
			SELECT division_id::text FROM divisions
			WHERE lower(name) = lower($1) AND authority_id IS NOT DISTINCT FROM $2::uuid LIMIT 1`, r.Division, authorityID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = q.QueryRowContext(ctx, `INSERT INTO divisions (name, authority_id) VALUES ($1, $2) RETURNING division_id::text`, r.Division, authorityID).Scan(&id)
		}
		if err != nil {
			return false, false, fmt.Errorf("division: %w", err)
		}
		divisionID = id
	}

	var roadID string
	if r.ExternalID != "" {
		err = q.QueryRowContext(ctx, `
			INSERT INTO roads (external_id, name, authority_id, division_id, geometry)
			VALUES ($1, $2, $3, $4, ST_GeomFromText($5, 4326))
			ON CONFLICT (external_id) WHERE external_id IS NOT NULL DO UPDATE
			SET name = EXCLUDED.name, authority_id = EXCLUDED.authority_id,
			    division_id = EXCLUDED.division_id, geometry = EXCLUDED.geometry
			RETURNING road_id::text`, r.ExternalID, nullStr(r.Name), authorityID, divisionID, line).Scan(&roadID)
	} else {
		err = q.QueryRowContext(ctx, `
			INSERT INTO roads (name, authority_id, division_id, geometry)
			VALUES ($1, $2, $3, ST_GeomFromText($4, 4326)) RETURNING road_id::text`,
			nullStr(r.Name), authorityID, divisionID, line).Scan(&roadID)
	}
	if err != nil {
		return false, false, fmt.Errorf("road: %w", err)
	}

	if r.Contractor != "" {
		contractorID, err := upsertByName(ctx, q, "contractors", "contractor_id", r.Contractor, nil, nil)
		if err != nil {
			return false, false, fmt.Errorf("contractor: %w", err)
		}
		var exists bool
		err = q.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM road_contracts
			WHERE road_id = $1 AND contractor_id = $2
			  AND contract_reference IS NOT DISTINCT FROM $3
			  AND valid_from IS NOT DISTINCT FROM $4::date)`,
			roadID, contractorID, nullStr(r.ContractReference), nullStr(r.ValidFrom)).Scan(&exists)
		if err != nil {
			return false, false, fmt.Errorf("contract: %w", err)
		}
		if !exists {
			_, err = q.ExecContext(ctx, `
				INSERT INTO road_contracts (road_id, contractor_id, contract_reference, scope, valid_from, valid_to, source_reference)
				VALUES ($1, $2, $3, $4, $5::date, $6::date, $7)`,
				roadID, contractorID, nullStr(r.ContractReference), nullStr(r.ContractScope),
				nullStr(r.ValidFrom), nullStr(r.ValidTo), nullStr(r.SourceReference))
			if err != nil {
				return false, false, fmt.Errorf("contract: %w", err)
			}
			contract = true
		}
	}

	if r.Officer != "" {
		var officerID string
		err := q.QueryRowContext(ctx, `
			SELECT officer_id::text FROM officers
			WHERE lower(name) = lower($1) AND authority_id IS NOT DISTINCT FROM $2::uuid LIMIT 1`, r.Officer, authorityID).Scan(&officerID)
		if errors.Is(err, sql.ErrNoRows) {
			err = q.QueryRowContext(ctx, `
				INSERT INTO officers (name, designation, authority_id) VALUES ($1, $2, $3)
				RETURNING officer_id::text`, r.Officer, nullStr(r.OfficerTitle), authorityID).Scan(&officerID)
		}
		if err != nil {
			return false, false, fmt.Errorf("officer: %w", err)
		}
		var exists bool
		err = q.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM officer_assignments
			WHERE officer_id = $1 AND road_id = $2 AND valid_from IS NOT DISTINCT FROM $3::date)`,
			officerID, roadID, nullStr(r.ValidFrom)).Scan(&exists)
		if err != nil {
			return false, false, fmt.Errorf("officer assignment: %w", err)
		}
		if !exists {
			_, err = q.ExecContext(ctx, `
				INSERT INTO officer_assignments (officer_id, road_id, division_id, role, valid_from, valid_to, source_reference)
				VALUES ($1, $2, $3, $4, $5::date, $6::date, $7)`,
				officerID, roadID, divisionID, nullStr(r.OfficerTitle), nullStr(r.ValidFrom), nullStr(r.ValidTo), nullStr(r.SourceReference))
			if err != nil {
				return false, false, fmt.Errorf("officer assignment: %w", err)
			}
			officer = true
		}
	}
	return contract, officer, nil
}

// Ping implements Store.
func (p *Postgres) Ping(ctx context.Context) error { return p.db.PingContext(ctx) }

// Close implements Store.
func (p *Postgres) Close() error { return p.db.Close() }
