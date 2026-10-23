package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
)

// Memory is an in-process Store. It implements deduplication and remediation
// tracking but has no road network, so responsibility is always empty.
type Memory struct {
	mu      sync.RWMutex
	now     func() time.Time
	events  map[string]events.RoadEvent
	order   []string // event IDs in ingestion order
	defects map[string]*memDefect
}

type memDefect struct {
	events.Defect
	vehicles map[string]struct{}
	eventIDs []string
	history  []events.StatusChange
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		now:     time.Now,
		events:  map[string]events.RoadEvent{},
		defects: map[string]*memDefect{},
	}
}

// Ingest implements Store.
func (m *Memory) Ingest(_ context.Context, e events.RoadEvent) (events.IngestResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.events[e.EventID]; ok {
		return events.IngestResult{EventID: e.EventID, DefectID: existing.DefectID, Duplicate: true}, nil
	}

	now := m.now().UTC()
	e.ReceivedAt = now
	res := events.IngestResult{EventID: e.EventID}

	radius := dedupRadius(e)
	var match, recent *memDefect
	bestOpen, bestClosed := radius, radius
	for _, d := range m.defects {
		if d.EventType != e.EventType {
			continue
		}
		dist := events.DistanceMeters(d.Latitude, d.Longitude, e.Latitude, e.Longitude)
		if dist > radius {
			continue
		}
		if !events.Closed(d.Status) {
			if dist <= bestOpen {
				bestOpen, match = dist, d
			}
		} else if now.Sub(d.StatusUpdatedAt) <= RecurrenceWindow && dist <= bestClosed {
			bestClosed, recent = dist, d
		}
	}

	if match == nil {
		match = &memDefect{
			Defect: events.Defect{
				DefectID:        events.NewID(),
				EventType:       e.EventType,
				Severity:        e.Severity,
				Latitude:        e.Latitude,
				Longitude:       e.Longitude,
				FirstSeen:       e.DetectedAt,
				LastSeen:        e.DetectedAt,
				Status:          events.StatusOpen,
				StatusUpdatedAt: now,
			},
			vehicles: map[string]struct{}{},
		}
		if recent != nil {
			match.RecurrenceOf = recent.DefectID
		}
		m.defects[match.DefectID] = match
		res.NewDefect = true
	}

	d := match
	n := float64(d.ObservationCount)
	// Running mean of positions refines the defect location as observations accrue.
	d.Latitude = (d.Latitude*n + e.Latitude) / (n + 1)
	d.Longitude = (d.Longitude*n + e.Longitude) / (n + 1)
	d.ObservationCount++
	d.vehicles[e.VehicleID] = struct{}{}
	d.VehicleCount = len(d.vehicles)
	d.Severity = events.MaxSeverity(d.Severity, e.Severity)
	if e.Confidence > d.MaxConfidence {
		d.MaxConfidence = e.Confidence
	}
	if e.DetectedAt.Before(d.FirstSeen) {
		d.FirstSeen = e.DetectedAt
	}
	if !e.DetectedAt.Before(d.LastSeen) {
		d.LastSeen = e.DetectedAt
		if e.ImageURI != "" {
			d.LatestImageURI = e.ImageURI
		}
	} else if d.LatestImageURI == "" && e.ImageURI != "" {
		d.LatestImageURI = e.ImageURI
	}
	d.eventIDs = append(d.eventIDs, e.EventID)

	e.DefectID = d.DefectID
	m.events[e.EventID] = e
	m.order = append(m.order, e.EventID)
	res.DefectID = d.DefectID
	return res, nil
}

func matchesTypes(t events.EventType, types []events.EventType) bool {
	if len(types) == 0 {
		return true
	}
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

func matchesStatus(s string, statuses []string) bool {
	if len(statuses) == 0 {
		return true
	}
	for _, x := range statuses {
		if x == s {
			return true
		}
	}
	return false
}

// ListEvents implements Store. Results are newest first by detection time.
func (m *Memory) ListEvents(_ context.Context, f Filter) ([]events.RoadEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []events.RoadEvent{}
	for _, id := range m.order {
		e := m.events[id]
		if f.BBox != nil && !f.BBox.Contains(e.Latitude, e.Longitude) {
			continue
		}
		if !matchesTypes(e.EventType, f.Types) {
			continue
		}
		if f.VehicleID != "" && e.VehicleID != f.VehicleID {
			continue
		}
		if f.DefectID != "" && e.DefectID != f.DefectID {
			continue
		}
		if !f.Since.IsZero() && e.DetectedAt.Before(f.Since) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DetectedAt.After(out[j].DetectedAt) })
	if l := f.EffectiveLimit(); len(out) > l {
		out = out[:l]
	}
	return out, nil
}

// GetEvent implements Store.
func (m *Memory) GetEvent(_ context.Context, id string) (events.RoadEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.events[id]
	if !ok {
		return events.RoadEvent{}, ErrNotFound
	}
	return e, nil
}

// ListDefects implements Store. Results are most recently seen first.
func (m *Memory) ListDefects(_ context.Context, f Filter) ([]events.Defect, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []events.Defect{}
	for _, d := range m.defects {
		if f.BBox != nil && !f.BBox.Contains(d.Latitude, d.Longitude) {
			continue
		}
		if !matchesTypes(d.EventType, f.Types) || !matchesStatus(d.Status, f.Statuses) {
			continue
		}
		if !f.Since.IsZero() && d.LastSeen.Before(f.Since) {
			continue
		}
		out = append(out, d.Defect)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	if l := f.EffectiveLimit(); len(out) > l {
		out = out[:l]
	}
	return out, nil
}

// GetDefect implements Store.
func (m *Memory) GetDefect(_ context.Context, id string) (events.DefectDetail, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.defects[id]
	if !ok {
		return events.DefectDetail{}, ErrNotFound
	}
	detail := events.DefectDetail{Defect: d.Defect, History: append([]events.StatusChange{}, d.history...)}
	for _, id := range d.eventIDs {
		detail.Observations = append(detail.Observations, m.events[id])
	}
	sort.Slice(detail.Observations, func(i, j int) bool {
		return detail.Observations[i].DetectedAt.After(detail.Observations[j].DetectedAt)
	})
	return detail, nil
}

// UpdateDefectStatus implements Store.
func (m *Memory) UpdateDefectStatus(_ context.Context, id, status, note, actor string) (events.Defect, error) {
	if !events.ValidStatus(status) {
		return events.Defect{}, ErrInvalidTransition
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.defects[id]
	if !ok {
		return events.Defect{}, ErrNotFound
	}
	if d.Status == status {
		return events.Defect{}, ErrInvalidTransition
	}
	now := m.now().UTC()
	d.history = append(d.history, events.StatusChange{
		DefectID: id, FromStatus: d.Status, ToStatus: status, Note: note, Actor: actor, CreatedAt: now,
	})
	d.Status = status
	d.StatusUpdatedAt = now
	return d.Defect, nil
}

// Stats implements Store.
func (m *Memory) Stats(_ context.Context) (Stats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.now()
	s := Stats{ByType: map[string]CountByName{}, ByStatus: map[string]int{}, TopAuthorities: []CountByName{}, TopContractors: []CountByName{}}
	vehicles := map[string]struct{}{}
	for _, e := range m.events {
		s.TotalEvents++
		vehicles[e.VehicleID] = struct{}{}
		if now.Sub(e.ReceivedAt) <= 24*time.Hour {
			s.EventsLast24h++
		}
	}
	s.Vehicles = len(vehicles)
	var resolvedHours float64
	var resolvedN int
	for _, d := range m.defects {
		s.TotalDefects++
		s.ByStatus[d.Status]++
		c := s.ByType[string(d.EventType)]
		c.Name = string(d.EventType)
		c.Total++
		if !events.Closed(d.Status) {
			s.OpenDefects++
			c.Open++
		}
		s.ByType[string(d.EventType)] = c
		if d.Status == events.StatusResolved {
			s.ResolvedDefects++
			resolvedHours += d.StatusUpdatedAt.Sub(d.FirstSeen).Hours()
			resolvedN++
		}
	}
	if resolvedN > 0 {
		avg := resolvedHours / float64(resolvedN)
		s.AvgResolutionHours = &avg
	}
	return s, nil
}

// Ping implements Store.
func (m *Memory) Ping(context.Context) error { return nil }

// Close implements Store.
func (m *Memory) Close() error { return nil }

// Responsibility implements Store. The in-memory store has no road network.
func (m *Memory) Responsibility(context.Context, float64, float64, time.Time) (events.Responsibility, error) {
	return events.Responsibility{}, nil
}

// ImportRoads implements Store.
func (m *Memory) ImportRoads(context.Context, []RoadImport) (ImportSummary, error) {
	return ImportSummary{}, ErrUnsupported
}
