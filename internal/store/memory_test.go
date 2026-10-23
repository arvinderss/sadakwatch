package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
)

func ev(vehicle string, typ events.EventType, lat, lon float64, sev events.Severity, at time.Time) events.RoadEvent {
	return events.RoadEvent{
		EventID: events.NewID(), VehicleID: vehicle, EventType: typ, Severity: sev, Confidence: 0.7,
		Latitude: lat, Longitude: lon, DetectedAt: at, Source: events.SourceVision,
	}
}

// RunStoreContract exercises behaviour every Store implementation must share.
func RunStoreContract(t *testing.T, s Store) {
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// Unique-ish coordinates per run keep the Postgres contract test independent of old rows.
	lat := 28.5 + float64(base.UnixNano()%100000)/1e7
	lon := 77.2

	first := ev("car-1", events.EventPothole, lat, lon, events.SeverityMedium, base)
	r1, err := s.Ingest(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if !r1.NewDefect || r1.DefectID == "" || r1.Duplicate {
		t.Fatalf("first event should create a defect: %+v", r1)
	}

	// Same event again is idempotent.
	again, err := s.Ingest(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.DefectID != r1.DefectID {
		t.Fatalf("expected duplicate pointing at same defect: %+v", again)
	}

	// ~5 m away, other vehicle, worse severity: merges.
	near := ev("car-2", events.EventPothole, lat+0.00004, lon, events.SeverityCritical, base.Add(time.Minute))
	near.ImageURI = "/media/x.jpg"
	r2, err := s.Ingest(ctx, near)
	if err != nil {
		t.Fatal(err)
	}
	if r2.NewDefect || r2.DefectID != r1.DefectID {
		t.Fatalf("nearby pothole should merge: %+v", r2)
	}

	// Different type at the same spot is a separate defect.
	crack, err := s.Ingest(ctx, ev("car-1", events.EventRoadCrack, lat, lon, events.SeverityLow, base))
	if err != nil {
		t.Fatal(err)
	}
	if !crack.NewDefect || crack.DefectID == r1.DefectID {
		t.Fatal("different type must not merge")
	}

	// ~200 m away is a separate defect.
	far, err := s.Ingest(ctx, ev("car-1", events.EventPothole, lat+0.0018, lon, events.SeverityLow, base))
	if err != nil {
		t.Fatal(err)
	}
	if !far.NewDefect {
		t.Fatal("distant pothole must not merge")
	}

	d, err := s.GetDefect(ctx, r1.DefectID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ObservationCount != 2 || d.VehicleCount != 2 || d.Severity != events.SeverityCritical || d.LatestImageURI != "/media/x.jpg" {
		t.Fatalf("unexpected aggregate: %+v", d.Defect)
	}
	if len(d.Observations) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(d.Observations))
	}

	// Remediation lifecycle.
	if _, err := s.UpdateDefectStatus(ctx, r1.DefectID, events.StatusOpen, "", ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("no-op transition should fail, got %v", err)
	}
	upd, err := s.UpdateDefectStatus(ctx, r1.DefectID, events.StatusResolved, "patched", "ee-north")
	if err != nil {
		t.Fatal(err)
	}
	if upd.Status != events.StatusResolved {
		t.Fatal("status not updated")
	}
	d, _ = s.GetDefect(ctx, r1.DefectID)
	if len(d.History) != 1 || d.History[0].Note != "patched" || d.History[0].FromStatus != events.StatusOpen {
		t.Fatalf("history not recorded: %+v", d.History)
	}

	// A re-detection after resolution opens a new defect linked as recurrence.
	rec, err := s.Ingest(ctx, ev("car-3", events.EventPothole, lat, lon, events.SeverityHigh, base.Add(30*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.NewDefect {
		t.Fatal("detection near a resolved defect should open a new defect")
	}
	rd, _ := s.GetDefect(ctx, rec.DefectID)
	if rd.RecurrenceOf != r1.DefectID {
		t.Fatalf("expected recurrence_of %s, got %q", r1.DefectID, rd.RecurrenceOf)
	}

	// Filters.
	box := &BBox{MinLon: lon - 0.001, MinLat: lat - 0.001, MaxLon: lon + 0.001, MaxLat: lat + 0.001}
	active, err := s.ListDefects(ctx, Filter{BBox: box, Statuses: []string{events.StatusOpen}, Types: []events.EventType{events.EventPothole}})
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].DefectID != rec.DefectID {
		t.Fatalf("expected only the recurrence to be open in the box, got %d", len(active))
	}
	evs, err := s.ListEvents(ctx, Filter{DefectID: r1.DefectID})
	if err != nil || len(evs) != 2 {
		t.Fatalf("expected 2 events for defect, got %d (%v)", len(evs), err)
	}
	if _, err := s.GetDefect(ctx, events.NewID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}

	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalEvents < 5 || st.ResolvedDefects < 1 || st.AvgResolutionHours == nil {
		t.Fatalf("unexpected stats %+v", st)
	}
}

func TestMemoryStoreContract(t *testing.T) {
	RunStoreContract(t, NewMemory())
}

func TestDedupRadiusUsesAccuracy(t *testing.T) {
	e := events.RoadEvent{EventType: events.EventPothole, AccuracyM: 25}
	if dedupRadius(e) != 25 {
		t.Fatal("poor accuracy should widen radius")
	}
	e.AccuracyM = 400
	if dedupRadius(e) != 60 {
		t.Fatal("radius must be capped")
	}
}
