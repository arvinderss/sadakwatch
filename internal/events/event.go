package events

import "time"

type EventType string

const (
	EventPothole          EventType = "pothole"
	EventRoadCrack        EventType = "road_crack"
	EventBrokenSurface    EventType = "broken_surface"
	EventWaterlogging     EventType = "waterlogging"
	EventDebris           EventType = "debris"
	EventRoadEdgeDamage   EventType = "road_edge_damage"
	EventMissingMarking   EventType = "missing_lane_marking"
	EventDamagedManhole   EventType = "damaged_manhole"
	EventSpeedBreaker     EventType = "speed_breaker"
)

type Severity string

const (
	SeverityUnknown Severity = "unknown"
	SeverityLow     Severity = "low"
	SeverityMedium  Severity = "medium"
	SeverityHigh    Severity = "high"
	SeverityCritical Severity = "critical"
)

type RoadEvent struct {
	EventID        string    `json:"event_id"`
	VehicleID      string    `json:"vehicle_id"`
	EventType      EventType `json:"event_type"`
	Severity       Severity  `json:"severity"`
	Confidence     float64   `json:"confidence"`
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	DetectedAt     time.Time `json:"detected_at"`
	SpeedKPH       float64   `json:"speed_kph,omitempty"`
	HeadingDegrees float64   `json:"heading_degrees,omitempty"`
	ImageURI       string    `json:"image_uri,omitempty"`
	VideoURI       string    `json:"video_uri,omitempty"`
}
