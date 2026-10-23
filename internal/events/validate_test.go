package events

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func validEvent() RoadEvent {
	return RoadEvent{
		EventID: NewID(), VehicleID: "bus-17", EventType: EventPothole, Severity: SeverityHigh,
		Confidence: 0.82, Latitude: 28.6139, Longitude: 77.2090, DetectedAt: now.Add(-time.Minute),
		SpeedKPH: 32, HeadingDegrees: 90, Source: SourceVision,
	}
}

func TestValidEventPasses(t *testing.T) {
	e := validEvent()
	if err := e.Validate(now); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	e := RoadEvent{EventType: " Pothole ", DetectedAt: now.In(time.FixedZone("IST", 19800))}
	e.Normalize()
	if !IsUUID(e.EventID) || e.Severity != SeverityUnknown || e.Source != SourceVision || e.EventType != EventPothole {
		t.Fatalf("unexpected normalisation: %+v", e)
	}
	if e.DetectedAt.Location() != time.UTC {
		t.Fatal("detected_at should be converted to UTC")
	}
}

func TestValidationProblems(t *testing.T) {
	cases := map[string]func(*RoadEvent){
		"event_id":        func(e *RoadEvent) { e.EventID = "nope" },
		"vehicle_id":      func(e *RoadEvent) { e.VehicleID = "" },
		"taxonomy":        func(e *RoadEvent) { e.EventType = "alien" },
		"severity":        func(e *RoadEvent) { e.Severity = "apocalyptic" },
		"confidence":      func(e *RoadEvent) { e.Confidence = 1.5 },
		"latitude":        func(e *RoadEvent) { e.Latitude = 91 },
		"longitude":       func(e *RoadEvent) { e.Longitude = -181 },
		"0,0":             func(e *RoadEvent) { e.Latitude, e.Longitude = 0, 0 },
		"future":          func(e *RoadEvent) { e.DetectedAt = now.Add(time.Hour) },
		"older":           func(e *RoadEvent) { e.DetectedAt = now.Add(-100 * 24 * time.Hour) },
		"speed_kph":       func(e *RoadEvent) { e.SpeedKPH = -1 },
		"heading_degrees": func(e *RoadEvent) { e.HeadingDegrees = 400 },
		"source":          func(e *RoadEvent) { e.Source = "psychic" },
	}
	for want, mutate := range cases {
		e := validEvent()
		mutate(&e)
		err := e.Validate(now)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: expected problem mentioning %q, got %v", want, want, err)
		}
		if err != nil && !IsValidationError(err) {
			t.Errorf("%s: expected ValidationError", want)
		}
	}
}

func TestSeverityOrdering(t *testing.T) {
	if MaxSeverity(SeverityLow, SeverityCritical) != SeverityCritical {
		t.Fatal("critical should win")
	}
	if MaxSeverity(SeverityHigh, SeverityUnknown) != SeverityHigh {
		t.Fatal("unknown should never downgrade")
	}
	if MaxSeverity("", SeverityUnknown) != SeverityUnknown {
		t.Fatal("empty should normalise to unknown")
	}
}

func TestDistanceMeters(t *testing.T) {
	// One thousandth of a degree of latitude is ~111 m.
	d := DistanceMeters(28.0, 77.0, 28.001, 77.0)
	if d < 110 || d > 112 {
		t.Fatalf("got %.2f m", d)
	}
	if DistanceMeters(1, 1, 1, 1) != 0 {
		t.Fatal("zero distance expected")
	}
}

func TestNewIDIsUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if !IsUUID(id) || id[14] != '4' {
			t.Fatalf("bad uuid %s", id)
		}
		if seen[id] {
			t.Fatal("duplicate id")
		}
		seen[id] = true
	}
}
