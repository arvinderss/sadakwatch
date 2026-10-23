package edge

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arvinderss/sadakwatch/internal/api"
	"github.com/arvinderss/sadakwatch/internal/events"
	"github.com/arvinderss/sadakwatch/internal/gps"
	"github.com/arvinderss/sadakwatch/internal/media"
	"github.com/arvinderss/sadakwatch/internal/queue"
	"github.com/arvinderss/sadakwatch/internal/store"
)

type fakeDetector struct{ dets []Detection }

func (f fakeDetector) Detect(context.Context, []byte) ([]Detection, error) { return f.dets, nil }

// A real JPEG header so the API's content sniffing accepts it.
var tinyJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9}

func TestFrameToDashboardPipeline(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "queue"), 0)
	if err != nil {
		t.Fatal(err)
	}
	tracker := &gps.Tracker{}
	tracker.Set(gps.Fix{Time: time.Now().UTC(), Latitude: 30.7333, Longitude: 76.7794, SpeedKPH: 35, Heading: 90, AccuracyM: 4})

	framesDir := filepath.Join(dir, "frames")
	os.MkdirAll(framesDir, 0o755)
	agent := NewAgent(Config{VehicleID: "bus-12", FramesDir: framesDir, MinConfidence: 0.5, MinSpeedKPH: 5},
		fakeDetector{dets: []Detection{
			{Class: "pothole", Confidence: 0.91, BBox: [4]float64{0.4, 0.6, 0.3, 0.3}},
			{Class: "pothole", Confidence: 0.30, BBox: [4]float64{0.1, 0.6, 0.1, 0.1}}, // below threshold
			{Class: "cow", Confidence: 0.99},                                           // not in taxonomy
		}}, tracker, q, log)

	frame := filepath.Join(framesDir, "000001.jpg")
	os.WriteFile(frame, tinyJPEG, 0o644)
	agent.processFrame(context.Background(), frame, time.Now())
	if q.Len() != 1 {
		t.Fatalf("expected 1 queued event, got %d", q.Len())
	}
	if _, err := os.Stat(frame); !os.IsNotExist(err) {
		t.Fatal("processed frame should be removed")
	}

	// The same pothole on the next frame is debounced.
	frame2 := filepath.Join(framesDir, "000002.jpg")
	os.WriteFile(frame2, tinyJPEG, 0o644)
	agent.processFrame(context.Background(), frame2, time.Now())
	if q.Len() != 1 {
		t.Fatalf("repeat detection should be suppressed, queue=%d", q.Len())
	}

	// Upload to a real API instance backed by the memory store.
	st := store.NewMemory()
	m, _ := media.New(filepath.Join(dir, "media"))
	srv := httptest.NewServer(api.New(api.Config{Store: st, Media: m, IngestKeys: []string{"k"}}))
	defer srv.Close()

	bad := &Uploader{APIURL: srv.URL, APIKey: "wrong", Queue: q, Log: log}
	if _, err := bad.Flush(context.Background()); err == nil {
		t.Fatal("wrong key should fail and keep the item")
	}
	if q.Len() != 1 {
		t.Fatal("item must stay queued after a failed upload")
	}

	up := &Uploader{APIURL: srv.URL, APIKey: "k", Queue: q, Log: log}
	n, err := up.Flush(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("flush: n=%d err=%v", n, err)
	}
	if q.Len() != 0 {
		t.Fatal("queue should be drained")
	}
	defects, _ := st.ListDefects(context.Background(), store.Filter{})
	if len(defects) != 1 {
		t.Fatalf("expected 1 defect, got %d", len(defects))
	}
	d := defects[0]
	if d.EventType != events.EventPothole || d.Severity != events.SeverityHigh || d.LatestImageURI == "" {
		t.Fatalf("unexpected defect %+v", d)
	}
}

func TestEventsWithoutFixAreDropped(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	q, _ := queue.Open(dir, 0)
	agent := NewAgent(Config{VehicleID: "v", FramesDir: dir}, fakeDetector{dets: []Detection{{Class: "pothole", Confidence: 1}}}, &gps.Tracker{}, q, log)
	frame := filepath.Join(dir, "f.jpg")
	os.WriteFile(frame, tinyJPEG, 0o644)
	agent.processFrame(context.Background(), frame, time.Now())
	if q.Len() != 0 {
		t.Fatal("no GPS fix means no event")
	}
}

func TestSeverityFor(t *testing.T) {
	if SeverityFor(Detection{Severity: "critical"}) != events.SeverityCritical {
		t.Fatal("explicit severity wins")
	}
	if SeverityFor(Detection{BBox: [4]float64{0, 0, 0.1, 0.1}}) != events.SeverityLow {
		t.Fatal("small box is low")
	}
	if SeverityFor(Detection{BBox: [4]float64{0, 0, 0.3, 0.3}}) != events.SeverityHigh {
		t.Fatal("large box is high")
	}
}
