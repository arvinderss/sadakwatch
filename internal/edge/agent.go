// Package edge implements the vehicle-side pipeline:
//
//	frame -> detection -> GPS enrichment -> validation -> durable queue -> upload
package edge

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
	"github.com/arvinderss/sadakwatch/internal/gps"
	"github.com/arvinderss/sadakwatch/internal/queue"
)

// Config tunes the capture pipeline.
type Config struct {
	VehicleID     string
	FramesDir     string        // directory an external capture process writes JPEG frames into
	MinConfidence float64       // detections below this are ignored
	MaxFixAge     time.Duration // frames without a fresh GPS fix are dropped
	MinSpeedKPH   float64       // ignore detections while parked/crawling
	Debounce      time.Duration // suppress repeats of the same class at the same spot
	DebounceM     float64
}

// Pending is what the agent stores in the durable queue.
type Pending struct {
	Event     events.RoadEvent `json:"event"`
	ImagePath string           `json:"image_path,omitempty"`
}

// Agent turns frames into queued road events.
type Agent struct {
	cfg      Config
	detector Detector
	tracker  *gps.Tracker
	queue    *queue.Queue
	log      *slog.Logger
	now      func() time.Time

	mu     sync.Mutex
	recent []recentDetection
}

type recentDetection struct {
	class    events.EventType
	lat, lon float64
	at       time.Time
}

// NewAgent wires the pipeline.
func NewAgent(cfg Config, d Detector, t *gps.Tracker, q *queue.Queue, log *slog.Logger) *Agent {
	if cfg.MaxFixAge == 0 {
		cfg.MaxFixAge = 3 * time.Second
	}
	if cfg.Debounce == 0 {
		cfg.Debounce = 30 * time.Second
	}
	if cfg.DebounceM == 0 {
		cfg.DebounceM = 12
	}
	return &Agent{cfg: cfg, detector: d, tracker: t, queue: q, log: log, now: time.Now}
}

// RunFrames polls the frames directory and processes each complete JPEG once.
func (a *Agent) RunFrames(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		frames, _ := filepath.Glob(filepath.Join(a.cfg.FramesDir, "*.jpg"))
		sort.Strings(frames)
		// Keep only the newest few frames: if inference falls behind, stale
		// frames would be enriched with a GPS fix that no longer matches.
		if len(frames) > 4 {
			for _, f := range frames[:len(frames)-4] {
				os.Remove(f)
			}
			frames = frames[len(frames)-4:]
		}
		for _, f := range frames {
			st, err := os.Stat(f)
			if err != nil || a.now().Sub(st.ModTime()) < 200*time.Millisecond {
				continue // still being written
			}
			a.processFrame(ctx, f, st.ModTime())
		}
	}
}

func (a *Agent) processFrame(ctx context.Context, path string, captured time.Time) {
	defer os.Remove(path)
	fix, ok := a.tracker.Latest(captured, a.cfg.MaxFixAge)
	if !ok {
		a.log.Debug("dropping frame without fresh GPS fix", "frame", path)
		return
	}
	if fix.SpeedKPH < a.cfg.MinSpeedKPH {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	dets, err := a.detector.Detect(dctx, data)
	cancel()
	if err != nil {
		a.log.Warn("detection failed", "error", err)
		return
	}
	var imagePath string
	for _, d := range dets {
		typ := events.EventType(strings.ToLower(d.Class))
		if !typ.Valid() || d.Confidence < a.cfg.MinConfidence {
			continue
		}
		if a.isRepeat(typ, fix.Latitude, fix.Longitude, captured) {
			continue
		}
		if imagePath == "" {
			imagePath = filepath.Join(a.queue.Dir(), "media", filepath.Base(path))
			if err := os.MkdirAll(filepath.Dir(imagePath), 0o755); err == nil {
				if err := os.WriteFile(imagePath, data, 0o644); err != nil {
					imagePath = ""
				}
			}
		}
		e := events.RoadEvent{
			VehicleID: a.cfg.VehicleID, EventType: typ, Severity: SeverityFor(d), Confidence: d.Confidence,
			Latitude: fix.Latitude, Longitude: fix.Longitude, AccuracyM: fix.AccuracyM,
			DetectedAt: captured.UTC(), SpeedKPH: fix.SpeedKPH, HeadingDegrees: fix.Heading,
			Source: events.SourceVision,
		}
		a.enqueue(e, imagePath)
	}
}

// Emit validates and queues an event produced outside the frame pipeline
// (simulator, accelerometer, manual button).
func (a *Agent) Emit(e events.RoadEvent, imagePath string) {
	a.enqueue(e, imagePath)
}

func (a *Agent) enqueue(e events.RoadEvent, imagePath string) {
	e.Normalize()
	if err := e.Validate(a.now()); err != nil {
		a.log.Warn("discarding invalid event", "error", err)
		return
	}
	var att []string
	if imagePath != "" {
		att = []string{imagePath}
	}
	if _, err := a.queue.Enqueue(Pending{Event: e, ImagePath: imagePath}, att...); err != nil {
		a.log.Error("enqueue failed", "error", err)
		return
	}
	a.log.Info("event queued", "type", e.EventType, "severity", e.Severity, "lat", e.Latitude, "lon", e.Longitude, "queue", a.queue.Len())
}

// isRepeat suppresses the same defect being reported on consecutive frames.
func (a *Agent) isRepeat(t events.EventType, lat, lon float64, at time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	keep := a.recent[:0]
	repeat := false
	for _, r := range a.recent {
		if at.Sub(r.at) > a.cfg.Debounce {
			continue
		}
		keep = append(keep, r)
		if r.class == t && events.DistanceMeters(r.lat, r.lon, lat, lon) <= a.cfg.DebounceM {
			repeat = true
		}
	}
	a.recent = keep
	if !repeat {
		a.recent = append(a.recent, recentDetection{class: t, lat: lat, lon: lon, at: at})
	}
	return repeat
}

// RunSimulator emits plausible detections along the simulated route so the
// whole pipeline can be exercised without a camera or model.
func (a *Agent) RunSimulator(ctx context.Context, every time.Duration) {
	weights := []struct {
		t events.EventType
		w int
	}{
		{events.EventPothole, 40}, {events.EventRoadCrack, 20}, {events.EventBrokenSurface, 10},
		{events.EventWaterlogging, 8}, {events.EventDebris, 6}, {events.EventMissingMarking, 6},
		{events.EventDamagedManhole, 4}, {events.EventRoadEdgeDamage, 4}, {events.EventSpeedBreaker, 2},
	}
	sevs := []events.Severity{events.SeverityLow, events.SeverityMedium, events.SeverityMedium, events.SeverityHigh, events.SeverityCritical}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		fix, ok := a.tracker.Latest(a.now(), a.cfg.MaxFixAge)
		if !ok {
			continue
		}
		n := rng.Intn(100)
		typ := weights[0].t
		for _, w := range weights {
			if n < w.w {
				typ = w.t
				break
			}
			n -= w.w
		}
		a.Emit(events.RoadEvent{
			VehicleID: a.cfg.VehicleID, EventType: typ, Severity: sevs[rng.Intn(len(sevs))],
			Confidence: 0.55 + rng.Float64()*0.4, Latitude: fix.Latitude, Longitude: fix.Longitude,
			AccuracyM: fix.AccuracyM, DetectedAt: fix.Time, SpeedKPH: fix.SpeedKPH,
			HeadingDegrees: fix.Heading, Source: events.SourceVision,
		}, "")
	}
}

// decodePending extracts a queued payload.
func decodePending(item queue.Item) (Pending, error) {
	var p Pending
	err := json.Unmarshal(item.Payload, &p)
	return p, err
}
