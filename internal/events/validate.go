package events

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// MaxEventAge bounds how old an event may be. Vehicles can stay offline for
// a long time, so this is generous; it exists to reject clock garbage.
const MaxEventAge = 90 * 24 * time.Hour

// MaxClockSkew is how far in the future a detection timestamp may be.
const MaxClockSkew = 10 * time.Minute

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidationError lists every problem found in an event.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid event: " + strings.Join(e.Problems, "; ")
}

// IsValidationError reports whether err is a ValidationError.
func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

// NewID returns a random RFC 4122 version 4 UUID.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// IsUUID reports whether s looks like a UUID.
func IsUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

// Normalize fills defaults (event ID, severity, source) and lower-cases enum
// values so that producers are not punished for cosmetic differences.
func (e *RoadEvent) Normalize() {
	e.EventID = strings.ToLower(strings.TrimSpace(e.EventID))
	if e.EventID == "" {
		e.EventID = NewID()
	}
	e.VehicleID = strings.TrimSpace(e.VehicleID)
	e.EventType = EventType(strings.ToLower(strings.TrimSpace(string(e.EventType))))
	e.Severity = Severity(strings.ToLower(strings.TrimSpace(string(e.Severity))))
	if e.Severity == "" {
		e.Severity = SeverityUnknown
	}
	e.Source = Source(strings.ToLower(strings.TrimSpace(string(e.Source))))
	if e.Source == "" {
		e.Source = SourceVision
	}
	if !e.DetectedAt.IsZero() {
		e.DetectedAt = e.DetectedAt.UTC()
	}
}

// Validate checks an event against the ingestion contract. now is passed in
// so that tests are deterministic.
func (e *RoadEvent) Validate(now time.Time) error {
	var p []string
	if !IsUUID(e.EventID) {
		p = append(p, "event_id must be a UUID")
	}
	if e.VehicleID == "" {
		p = append(p, "vehicle_id is required")
	} else if len(e.VehicleID) > 64 {
		p = append(p, "vehicle_id must be at most 64 characters")
	}
	if !e.EventType.Valid() {
		p = append(p, fmt.Sprintf("event_type %q is not in the taxonomy", e.EventType))
	}
	if !e.Severity.Valid() {
		p = append(p, fmt.Sprintf("severity %q is invalid", e.Severity))
	}
	if !e.Source.Valid() {
		p = append(p, fmt.Sprintf("source %q is invalid", e.Source))
	}
	if math.IsNaN(e.Confidence) || e.Confidence < 0 || e.Confidence > 1 {
		p = append(p, "confidence must be between 0 and 1")
	}
	if math.IsNaN(e.Latitude) || e.Latitude < -90 || e.Latitude > 90 {
		p = append(p, "latitude must be between -90 and 90")
	}
	if math.IsNaN(e.Longitude) || e.Longitude < -180 || e.Longitude > 180 {
		p = append(p, "longitude must be between -180 and 180")
	}
	if e.Latitude == 0 && e.Longitude == 0 {
		p = append(p, "location 0,0 indicates a missing GPS fix")
	}
	if e.AccuracyM < 0 || e.AccuracyM > 500 {
		p = append(p, "accuracy_m must be between 0 and 500")
	}
	if e.DetectedAt.IsZero() {
		p = append(p, "detected_at is required")
	} else {
		if e.DetectedAt.After(now.Add(MaxClockSkew)) {
			p = append(p, "detected_at is in the future")
		}
		if e.DetectedAt.Before(now.Add(-MaxEventAge)) {
			p = append(p, "detected_at is older than 90 days")
		}
	}
	if e.SpeedKPH < 0 || e.SpeedKPH > 300 {
		p = append(p, "speed_kph must be between 0 and 300")
	}
	if e.HeadingDegrees < 0 || e.HeadingDegrees > 360 {
		p = append(p, "heading_degrees must be between 0 and 360")
	}
	if len(e.ImageURI) > 1024 || len(e.VideoURI) > 1024 {
		p = append(p, "media URIs must be at most 1024 characters")
	}
	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}

const earthRadiusM = 6371008.8

// DistanceMeters returns the great-circle distance between two WGS84 points.
func DistanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(a)))
}

// DedupRadiusM returns how close a new detection must be to an existing
// open defect of the same type to be treated as the same physical issue.
// Linear or area defects get a wider radius than point defects.
func DedupRadiusM(t EventType) float64 {
	switch t {
	case EventWaterlogging, EventBrokenSurface, EventMissingMarking, EventRoadEdgeDamage:
		return 40
	default:
		return 15
	}
}
