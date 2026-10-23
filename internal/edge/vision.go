package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
)

// Detection is one object returned by the vision service. BBox is
// [x, y, width, height] normalised to the frame (0..1).
type Detection struct {
	Class      string     `json:"class"`
	Confidence float64    `json:"confidence"`
	BBox       [4]float64 `json:"bbox"`
	Severity   string     `json:"severity,omitempty"`
	TrackID    string     `json:"track_id,omitempty"`
}

// Detector finds road defects in a JPEG frame.
type Detector interface {
	Detect(ctx context.Context, jpeg []byte) ([]Detection, error)
}

// VisionClient calls the SadakWatch vision service (services/vision).
type VisionClient struct {
	URL    string // e.g. http://localhost:8090
	Client *http.Client
}

// Detect implements Detector.
func (v *VisionClient) Detect(ctx context.Context, jpeg []byte) ([]Detection, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("image", "frame.jpg")
	if err != nil {
		return nil, err
	}
	fw.Write(jpeg)
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.URL+"/v1/detect", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	client := v.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("vision service returned %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Detections []Detection `json:"detections"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Detections, nil
}

// SeverityFor uses the detector's severity when given, otherwise estimates it
// from how much of the frame the defect occupies: close, large defects in the
// lower part of a forward-facing frame are the ones a vehicle is about to hit.
func SeverityFor(d Detection) events.Severity {
	if s := events.Severity(d.Severity); s.Valid() && s != "" {
		return s
	}
	area := d.BBox[2] * d.BBox[3]
	switch {
	case area >= 0.08:
		return events.SeverityHigh
	case area >= 0.025:
		return events.SeverityMedium
	case area > 0:
		return events.SeverityLow
	default:
		return events.SeverityUnknown
	}
}
