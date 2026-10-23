// Package store persists road events and defects. The Postgres/PostGIS
// implementation is the production store; the in-memory store backs tests and
// a zero-dependency demo mode.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
)

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalidTransition is returned for a status change that is not allowed.
	ErrInvalidTransition = errors.New("invalid status transition")
	// ErrUnsupported is returned by stores that lack a capability (e.g. the
	// in-memory store has no road network).
	ErrUnsupported = errors.New("not supported by this store")
)

// RoadImport is one road segment with its administrative mapping. Authorities,
// divisions, contractors and officers are matched by name and created when
// missing, so a single GeoJSON file can bootstrap the responsibility model.
type RoadImport struct {
	ExternalID        string       `json:"external_id"`
	Name              string       `json:"name"`
	Coordinates       [][2]float64 `json:"coordinates"` // [lon, lat] pairs
	Authority         string       `json:"authority"`
	AuthorityType     string       `json:"authority_type"`
	Division          string       `json:"division"`
	Contractor        string       `json:"contractor"`
	ContractReference string       `json:"contract_reference"`
	ContractScope     string       `json:"contract_scope"`
	ValidFrom         string       `json:"valid_from"` // YYYY-MM-DD
	ValidTo           string       `json:"valid_to"`
	Officer           string       `json:"officer"`
	OfficerTitle      string       `json:"officer_designation"`
	SourceReference   string       `json:"source_reference"`
}

// ImportSummary reports what an import created or updated.
type ImportSummary struct {
	Roads     int      `json:"roads"`
	Contracts int      `json:"contracts"`
	Officers  int      `json:"officer_assignments"`
	Errors    []string `json:"errors"`
}

// BBox is a WGS84 bounding box.
type BBox struct {
	MinLon, MinLat, MaxLon, MaxLat float64
}

// Contains reports whether the point is inside the box.
func (b BBox) Contains(lat, lon float64) bool {
	return lat >= b.MinLat && lat <= b.MaxLat && lon >= b.MinLon && lon <= b.MaxLon
}

// Filter narrows list queries. Zero values mean "no constraint".
type Filter struct {
	BBox      *BBox
	Types     []events.EventType
	Statuses  []string
	VehicleID string
	DefectID  string
	Since     time.Time
	Limit     int
}

// DefaultLimit and MaxLimit bound list results.
const (
	DefaultLimit = 500
	MaxLimit     = 5000
)

// EffectiveLimit clamps the filter limit into the supported range.
func (f Filter) EffectiveLimit() int {
	if f.Limit <= 0 {
		return DefaultLimit
	}
	if f.Limit > MaxLimit {
		return MaxLimit
	}
	return f.Limit
}

// CountByName is a labelled count used in statistics.
type CountByName struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Open  int    `json:"open"`
	Total int    `json:"total"`
}

// Stats summarises the current state of the network.
type Stats struct {
	TotalEvents        int                    `json:"total_events"`
	TotalDefects       int                    `json:"total_defects"`
	OpenDefects        int                    `json:"open_defects"`
	ResolvedDefects    int                    `json:"resolved_defects"`
	Vehicles           int                    `json:"vehicles"`
	EventsLast24h      int                    `json:"events_last_24h"`
	ByType             map[string]CountByName `json:"by_type"`
	ByStatus           map[string]int         `json:"by_status"`
	TopAuthorities     []CountByName          `json:"top_authorities"`
	TopContractors     []CountByName          `json:"top_contractors"`
	AvgResolutionHours *float64               `json:"avg_resolution_hours"`
}

// Store is the persistence boundary used by the API.
type Store interface {
	// Ingest stores one validated event, resolves responsibility and merges it
	// into a defect. Re-submitting an event ID is reported as a duplicate.
	Ingest(ctx context.Context, e events.RoadEvent) (events.IngestResult, error)
	ListEvents(ctx context.Context, f Filter) ([]events.RoadEvent, error)
	GetEvent(ctx context.Context, id string) (events.RoadEvent, error)
	ListDefects(ctx context.Context, f Filter) ([]events.Defect, error)
	GetDefect(ctx context.Context, id string) (events.DefectDetail, error)
	UpdateDefectStatus(ctx context.Context, id, status, note, actor string) (events.Defect, error)
	Stats(ctx context.Context) (Stats, error)
	// Responsibility resolves who is answerable for a location at a time.
	Responsibility(ctx context.Context, lat, lon float64, at time.Time) (events.Responsibility, error)
	// ImportRoads upserts road segments with their administrative mapping.
	ImportRoads(ctx context.Context, roads []RoadImport) (ImportSummary, error)
	Ping(ctx context.Context) error
	Close() error
}

// dedupRadius widens the per-type radius when the GPS fix is poor, capped so a
// bad fix cannot swallow unrelated defects.
func dedupRadius(e events.RoadEvent) float64 {
	r := events.DedupRadiusM(e.EventType)
	if e.AccuracyM > r {
		r = e.AccuracyM
	}
	if r > 60 {
		r = 60
	}
	return r
}

// RecurrenceWindow is how long after closure a re-detection at the same spot is
// linked to the closed defect as a recurrence (e.g. a pothole that reopened).
const RecurrenceWindow = 180 * 24 * time.Hour
