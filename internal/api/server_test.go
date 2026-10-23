package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
	"github.com/arvinderss/sadakwatch/internal/media"
	"github.com/arvinderss/sadakwatch/internal/store"
)

var fixedNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newTestServer(t *testing.T, ingestKey, adminKey string) *Server {
	t.Helper()
	m, err := media.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Store: store.NewMemory(), Media: m, Now: func() time.Time { return fixedNow },
		AdminKey: adminKey, CORSOrigin: "*",
		Web: fstest.MapFS{"index.html": {Data: []byte("<h1>dash</h1>")}},
	}
	if ingestKey != "" {
		cfg.IngestKeys = []string{ingestKey}
	}
	return New(cfg)
}

func do(t *testing.T, h http.Handler, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func eventJSON(id string, lat float64, typ string) string {
	return fmt.Sprintf(`{"event_id":%q,"vehicle_id":"auto-42","event_type":%q,"severity":"high","confidence":0.9,
		"latitude":%f,"longitude":77.2090,"detected_at":"2026-10-01T11:58:00+05:30","speed_kph":28,"source":"accelerometer"}`, id, typ, lat)
}

func TestHealthAndDashboard(t *testing.T) {
	s := newTestServer(t, "", "")
	if rec := do(t, s, "GET", "/healthz", ""); rec.Code != 200 {
		t.Fatalf("healthz %d", rec.Code)
	}
	if rec := do(t, s, "GET", "/readyz", ""); rec.Code != 200 {
		t.Fatalf("readyz %d", rec.Code)
	}
	rec := do(t, s, "GET", "/", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "dash") {
		t.Fatalf("dashboard not served: %d %s", rec.Code, rec.Body.String())
	}
	meta := do(t, s, "GET", "/v1/meta", "")
	if !strings.Contains(meta.Body.String(), "speed_breaker") {
		t.Fatal("meta should list taxonomy")
	}
}

func TestIngestSingleEventAndRead(t *testing.T) {
	s := newTestServer(t, "", "")
	id := events.NewID()
	rec := do(t, s, "POST", "/v1/events", eventJSON(id, 28.6139, "pothole"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body)
	}
	var resp struct {
		Accepted int                   `json:"accepted"`
		Results  []events.IngestResult `json:"results"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Accepted != 1 || resp.Results[0].DefectID == "" {
		t.Fatalf("bad response %s", rec.Body)
	}

	// Re-upload after a lost ACK is a duplicate, not an error.
	rec = do(t, s, "POST", "/v1/events", eventJSON(id, 28.6139, "pothole"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"duplicates":1`) {
		t.Fatalf("expected duplicate: %d %s", rec.Code, rec.Body)
	}

	rec = do(t, s, "GET", "/v1/events/"+id, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "auto-42") {
		t.Fatalf("get event: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, s, "GET", "/v1/defects?status=active&type=pothole&bbox=77,28,78,29", "")
	if !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Fatalf("defect list: %s", rec.Body)
	}
	rec = do(t, s, "GET", "/v1/defects?format=geojson", "")
	if !strings.Contains(rec.Body.String(), "FeatureCollection") || rec.Header().Get("Content-Type") != "application/geo+json" {
		t.Fatalf("geojson: %s", rec.Body)
	}
}

func TestIngestRejectsInvalidSingleEvent(t *testing.T) {
	s := newTestServer(t, "", "")
	rec := do(t, s, "POST", "/v1/events", eventJSON(events.NewID(), 0, "unicorn"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "taxonomy") {
		t.Fatalf("problems should be listed: %s", rec.Body)
	}
	if rec := do(t, s, "POST", "/v1/events", "not json"); rec.Code != 400 {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestIngestBatchReportsPerEventResults(t *testing.T) {
	s := newTestServer(t, "", "")
	body := `{"events":[` + eventJSON(events.NewID(), 28.61, "pothole") + `,` +
		eventJSON(events.NewID(), 28.61, "bogus") + `,` +
		eventJSON(events.NewID(), 28.61001, "pothole") + `]}`
	rec := do(t, s, "POST", "/v1/events", body)
	if rec.Code != 200 {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Accepted, Rejected int
		Results            []events.IngestResult
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Accepted != 2 || resp.Rejected != 1 || resp.Results[1].Error == "" {
		t.Fatalf("unexpected batch result %s", rec.Body)
	}
	if resp.Results[0].DefectID != resp.Results[2].DefectID {
		t.Fatal("events 1 m apart should merge into one defect")
	}
	// Bare arrays are accepted too.
	rec = do(t, s, "POST", "/v1/events", "["+eventJSON(events.NewID(), 28.7, "debris")+"]")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"accepted":1`) {
		t.Fatalf("array batch: %s", rec.Body)
	}
}

func TestAuth(t *testing.T) {
	s := newTestServer(t, "dev-key", "admin-key")
	if rec := do(t, s, "POST", "/v1/events", eventJSON(events.NewID(), 28.6, "pothole")); rec.Code != 401 {
		t.Fatalf("missing key should be 401, got %d", rec.Code)
	}
	rec := do(t, s, "POST", "/v1/events", eventJSON(events.NewID(), 28.6, "pothole"), "Authorization", "Bearer dev-key")
	if rec.Code != 201 {
		t.Fatalf("bearer key should work: %d", rec.Code)
	}
	var resp struct{ Results []events.IngestResult }
	json.Unmarshal(rec.Body.Bytes(), &resp)
	defectID := resp.Results[0].DefectID

	patch := `{"status":"in_progress","note":"work order 17","actor":"AE"}`
	if rec := do(t, s, "PATCH", "/v1/defects/"+defectID, patch, "X-API-Key", "dev-key"); rec.Code != 401 {
		t.Fatalf("ingest key must not change status, got %d", rec.Code)
	}
	rec = do(t, s, "PATCH", "/v1/defects/"+defectID, patch, "X-API-Key", "admin-key")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "in_progress") {
		t.Fatalf("admin patch: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s, "PATCH", "/v1/defects/"+defectID, patch, "X-API-Key", "admin-key"); rec.Code != 409 {
		t.Fatalf("repeat status should conflict, got %d", rec.Code)
	}
	rec = do(t, s, "GET", "/v1/defects/"+defectID, "")
	if !strings.Contains(rec.Body.String(), "work order 17") {
		t.Fatalf("history missing: %s", rec.Body)
	}
	// Reads stay public.
	if rec := do(t, s, "GET", "/v1/stats", ""); rec.Code != 200 {
		t.Fatalf("stats: %d", rec.Code)
	}
}

func TestFilterValidation(t *testing.T) {
	s := newTestServer(t, "", "")
	for _, q := range []string{"bbox=1,2,3", "bbox=a,b,c,d", "bbox=5,5,1,1", "type=nope", "status=nope", "since=yesterday", "limit=-2"} {
		if rec := do(t, s, "GET", "/v1/defects?"+q, ""); rec.Code != 400 {
			t.Errorf("%s: expected 400, got %d", q, rec.Code)
		}
	}
}

func TestResponsibilityAndImportInMemoryMode(t *testing.T) {
	s := newTestServer(t, "", "")
	if rec := do(t, s, "GET", "/v1/responsibility?lat=28.6&lon=77.2&at=2026-01-01", ""); rec.Code != 200 {
		t.Fatalf("responsibility: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s, "GET", "/v1/responsibility", ""); rec.Code != 400 {
		t.Fatalf("missing coords should be 400, got %d", rec.Code)
	}
	fc := `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"LineString","coordinates":[[77.2,28.6],[77.21,28.6]]},"properties":{"name":"X"}}]}`
	if rec := do(t, s, "POST", "/v1/admin/roads", fc); rec.Code != http.StatusNotImplemented {
		t.Fatalf("memory import should be 501, got %d", rec.Code)
	}
}

func TestMediaUploadAndServe(t *testing.T) {
	s := newTestServer(t, "", "")
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var jpg bytes.Buffer
	jpeg.Encode(&jpg, img, nil)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "frame.jpg")
	fw.Write(jpg.Bytes())
	mw.Close()

	req := httptest.NewRequest("POST", "/v1/media", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var out struct{ URI string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.HasPrefix(out.URI, "/media/2026/10/01/") || !strings.HasSuffix(out.URI, ".jpg") {
		t.Fatalf("unexpected uri %q", out.URI)
	}
	get := do(t, s, "GET", out.URI, "")
	got, _ := io.ReadAll(get.Body)
	if get.Code != 200 || !bytes.Equal(got, jpg.Bytes()) {
		t.Fatalf("served media mismatch: %d", get.Code)
	}
	if rec := do(t, s, "GET", "/media/2026/", ""); rec.Code != 404 {
		t.Fatalf("directory listing must be hidden, got %d", rec.Code)
	}

	// Non-images are refused even if they claim to be JPEG.
	body.Reset()
	mw = multipart.NewWriter(&body)
	fw, _ = mw.CreateFormFile("file", "evil.jpg")
	fw.Write([]byte("#!/bin/sh\necho pwned\n"))
	mw.Close()
	req = httptest.NewRequest("POST", "/v1/media", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("non-image should be rejected, got %d", rec.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	s := newTestServer(t, "", "")
	rec := do(t, s, "OPTIONS", "/v1/events", "")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}
}
