// Package api implements the SadakWatch REST API and serves the dashboard.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
	"github.com/arvinderss/sadakwatch/internal/media"
	"github.com/arvinderss/sadakwatch/internal/store"
)

// MaxBatch is the largest number of events accepted in one request.
const MaxBatch = 500

// Config configures the API server.
type Config struct {
	Store   store.Store
	Media   *media.Store // nil disables uploads
	Logger  *slog.Logger
	Web     fs.FS // dashboard assets; nil disables the dashboard
	Version string

	// IngestKeys authorise POST /v1/events and /v1/media. Empty means open.
	IngestKeys []string
	// AdminKey authorises status changes and road imports. Empty means open.
	AdminKey string
	// CORSOrigin is sent as Access-Control-Allow-Origin ("" disables CORS).
	CORSOrigin string

	Now func() time.Time
}

// Server is the HTTP handler for the API.
type Server struct {
	cfg Config
	mux *http.ServeMux
}

// New builds the API handler.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", s.health)
	m.HandleFunc("GET /readyz", s.ready)
	m.HandleFunc("GET /v1/meta", s.meta)

	m.HandleFunc("POST /v1/events", s.requireIngest(s.ingestEvents))
	m.HandleFunc("GET /v1/events", s.listEvents)
	m.HandleFunc("GET /v1/events/{id}", s.getEvent)

	m.HandleFunc("GET /v1/defects", s.listDefects)
	m.HandleFunc("GET /v1/defects/{id}", s.getDefect)
	m.HandleFunc("PATCH /v1/defects/{id}", s.requireAdmin(s.updateDefect))

	m.HandleFunc("GET /v1/stats", s.stats)
	m.HandleFunc("GET /v1/responsibility", s.responsibility)
	m.HandleFunc("POST /v1/admin/roads", s.requireAdmin(s.importRoads))

	if s.cfg.Media != nil {
		m.HandleFunc("POST /v1/media", s.requireIngest(s.uploadMedia))
		m.Handle("GET /media/", http.StripPrefix("/media/", s.cfg.Media.Handler()))
	}
	if s.cfg.Web != nil {
		m.Handle("GET /", http.FileServer(http.FS(s.cfg.Web)))
	}
}

// ServeHTTP applies recovery, CORS and access logging around the router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		if p := recover(); p != nil {
			s.cfg.Logger.Error("panic", "panic", p, "stack", string(debug.Stack()))
			writeError(rec, http.StatusInternalServerError, "internal error")
		}
		if r.URL.Path != "/healthz" {
			s.cfg.Logger.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"duration_ms", time.Since(start).Milliseconds())
		}
	}()
	if s.cfg.CORSOrigin != "" {
		h := rec.Header()
		h.Set("Access-Control-Allow-Origin", s.cfg.CORSOrigin)
		h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key")
		if r.Method == http.MethodOptions {
			rec.WriteHeader(http.StatusNoContent)
			return
		}
	}
	s.mux.ServeHTTP(rec, r)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ---- auth ----

func requestKey(r *http.Request) string {
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimPrefix(a, "Bearer ")
	}
	return ""
}

func keyMatches(got string, want ...string) bool {
	if got == "" {
		return false
	}
	for _, w := range want {
		if w != "" && subtle.ConstantTimeCompare([]byte(got), []byte(w)) == 1 {
			return true
		}
	}
	return false
}

func (s *Server) requireIngest(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.IngestKeys) > 0 {
			keys := append([]string{s.cfg.AdminKey}, s.cfg.IngestKeys...)
			if !keyMatches(requestKey(r), keys...) {
				writeError(w, http.StatusUnauthorized, "missing or invalid API key")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminKey != "" && !keyMatches(requestKey(r), s.cfg.AdminKey) {
			writeError(w, http.StatusUnauthorized, "missing or invalid admin key")
			return
		}
		next(w, r)
	}
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error    string   `json:"error"`
	Problems []string `json:"problems,omitempty"`
}

func writeError(w http.ResponseWriter, status int, msg string, problems ...string) {
	writeJSON(w, status, errorBody{Error: msg, Problems: problems})
}

func (s *Server) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrInvalidTransition):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, "this deployment runs without PostGIS; set DATABASE_URL to enable it")
	case errors.Is(err, context.Canceled):
		writeError(w, 499, "client closed request")
	default:
		s.cfg.Logger.Error("store error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func parseFilter(r *http.Request) (store.Filter, error) {
	q := r.URL.Query()
	var f store.Filter
	if v := q.Get("bbox"); v != "" {
		parts := strings.Split(v, ",")
		if len(parts) != 4 {
			return f, errors.New("bbox must be minLon,minLat,maxLon,maxLat")
		}
		var n [4]float64
		for i, p := range parts {
			x, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil {
				return f, errors.New("bbox values must be numbers")
			}
			n[i] = x
		}
		if n[0] > n[2] || n[1] > n[3] {
			return f, errors.New("bbox minimums must not exceed maximums")
		}
		f.BBox = &store.BBox{MinLon: n[0], MinLat: n[1], MaxLon: n[2], MaxLat: n[3]}
	}
	for _, t := range splitList(q.Get("type")) {
		et := events.EventType(t)
		if !et.Valid() {
			return f, fmt.Errorf("unknown type %q", t)
		}
		f.Types = append(f.Types, et)
	}
	for _, st := range splitList(q.Get("status")) {
		if st == "active" {
			f.Statuses = append(f.Statuses, events.StatusOpen, events.StatusAcknowledged, events.StatusInProgress)
			continue
		}
		if !events.ValidStatus(st) {
			return f, fmt.Errorf("unknown status %q", st)
		}
		f.Statuses = append(f.Statuses, st)
	}
	f.VehicleID = q.Get("vehicle_id")
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, errors.New("since must be RFC 3339")
		}
		f.Since = t
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return f, errors.New("limit must be a positive integer")
		}
		f.Limit = n
	}
	return f, nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- handlers ----

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "time": s.cfg.Now().UTC().Format(time.RFC3339), "version": s.cfg.Version})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.cfg.Store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "time": s.cfg.Now().UTC().Format(time.RFC3339)})
}

func (s *Server) meta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"event_types": events.EventTypes,
		"severities":  []events.Severity{events.SeverityUnknown, events.SeverityLow, events.SeverityMedium, events.SeverityHigh, events.SeverityCritical},
		"statuses":    events.Statuses,
		"sources":     []events.Source{events.SourceVision, events.SourceAccelerometer, events.SourceManual},
		"media":       s.cfg.Media != nil,
		"auth": map[string]bool{
			"ingest_key_required": len(s.cfg.IngestKeys) > 0,
			"admin_key_required":  s.cfg.AdminKey != "",
		},
		"version": s.cfg.Version,
	})
}

// ingestEvents accepts a single event object, a JSON array of events, or
// {"events": [...]}. Each event is validated and stored independently so one
// bad record does not block a vehicle's queue.
func (s *Server) ingestEvents(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	batch, single, err := decodeEvents(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(batch) == 0 {
		writeError(w, http.StatusBadRequest, "no events supplied")
		return
	}
	if len(batch) > MaxBatch {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("at most %d events per request", MaxBatch))
		return
	}

	now := s.cfg.Now()
	type response struct {
		Accepted   int                   `json:"accepted"`
		Duplicates int                   `json:"duplicates"`
		Rejected   int                   `json:"rejected"`
		Results    []events.IngestResult `json:"results"`
	}
	resp := response{Results: make([]events.IngestResult, 0, len(batch))}
	for _, e := range batch {
		e.Normalize()
		if err := e.Validate(now); err != nil {
			resp.Rejected++
			resp.Results = append(resp.Results, events.IngestResult{EventID: e.EventID, Error: err.Error()})
			if single {
				var v *events.ValidationError
				errors.As(err, &v)
				writeError(w, http.StatusUnprocessableEntity, "invalid event", v.Problems...)
				return
			}
			continue
		}
		res, err := s.cfg.Store.Ingest(r.Context(), e)
		if err != nil {
			// Infrastructure failures fail the whole request so the client retries.
			s.storeError(w, err)
			return
		}
		if res.Duplicate {
			resp.Duplicates++
		} else {
			resp.Accepted++
		}
		resp.Results = append(resp.Results, res)
	}
	status := http.StatusOK
	if single && resp.Accepted == 1 {
		status = http.StatusCreated
	}
	writeJSON(w, status, resp)
}

func decodeEvents(body []byte) (batch []events.RoadEvent, single bool, err error) {
	trimmed := strings.TrimSpace(string(body))
	switch {
	case strings.HasPrefix(trimmed, "["):
		err = json.Unmarshal(body, &batch)
	case strings.HasPrefix(trimmed, "{"):
		var probe struct {
			Events *[]events.RoadEvent `json:"events"`
		}
		if err = json.Unmarshal(body, &probe); err != nil {
			break
		}
		if probe.Events != nil {
			batch = *probe.Events
			break
		}
		var e events.RoadEvent
		err = json.Unmarshal(body, &e)
		batch, single = []events.RoadEvent{e}, true
	default:
		err = errors.New("body must be a JSON event, array or {\"events\": [...]}")
	}
	if err != nil {
		return nil, false, fmt.Errorf("invalid JSON: %w", err)
	}
	return batch, single, nil
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.DefectID = r.URL.Query().Get("defect_id")
	list, err := s.cfg.Store.ListEvents(r.Context(), f)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": list, "count": len(list)})
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	e, err := s.cfg.Store.GetEvent(r.Context(), strings.ToLower(r.PathValue("id")))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) listDefects(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	list, err := s.cfg.Store.ListDefects(r.Context(), f)
	if err != nil {
		s.storeError(w, err)
		return
	}
	if r.URL.Query().Get("format") == "geojson" {
		writeGeoJSON(w, list)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"defects": list, "count": len(list)})
}

func writeGeoJSON(w http.ResponseWriter, list []events.Defect) {
	type feature struct {
		Type       string         `json:"type"`
		Geometry   map[string]any `json:"geometry"`
		Properties events.Defect  `json:"properties"`
	}
	fc := struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}{Type: "FeatureCollection", Features: make([]feature, 0, len(list))}
	for _, d := range list {
		fc.Features = append(fc.Features, feature{
			Type:       "Feature",
			Geometry:   map[string]any{"type": "Point", "coordinates": []float64{d.Longitude, d.Latitude}},
			Properties: d,
		})
	}
	w.Header().Set("Content-Type", "application/geo+json")
	_ = json.NewEncoder(w).Encode(fc)
}

func (s *Server) getDefect(w http.ResponseWriter, r *http.Request) {
	d, err := s.cfg.Store.GetDefect(r.Context(), strings.ToLower(r.PathValue("id")))
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) updateDefect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
		Note   string `json:"note"`
		Actor  string `json:"actor"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	if !events.ValidStatus(req.Status) {
		writeError(w, http.StatusBadRequest, "status must be one of "+strings.Join(events.Statuses, ", "))
		return
	}
	if len(req.Note) > 2000 || len(req.Actor) > 200 {
		writeError(w, http.StatusBadRequest, "note or actor too long")
		return
	}
	d, err := s.cfg.Store.UpdateDefectStatus(r.Context(), strings.ToLower(r.PathValue("id")), req.Status, req.Note, req.Actor)
	if err != nil {
		if errors.Is(err, store.ErrInvalidTransition) {
			writeError(w, http.StatusConflict, "defect already has status "+req.Status)
			return
		}
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.cfg.Store.Stats(r.Context())
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) responsibility(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lon, err2 := strconv.ParseFloat(q.Get("lon"), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		writeError(w, http.StatusBadRequest, "lat and lon are required")
		return
	}
	at := s.cfg.Now()
	if v := q.Get("at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			if t, err = time.Parse("2006-01-02", v); err != nil {
				writeError(w, http.StatusBadRequest, "at must be RFC 3339 or YYYY-MM-DD")
				return
			}
			t = t.Add(12 * time.Hour) // midday avoids time-zone date flips
		}
		at = t
	}
	resp, err := s.cfg.Store.Responsibility(r.Context(), lat, lon, at)
	if err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// importRoads accepts a GeoJSON FeatureCollection of LineString (or
// MultiLineString, imported part by part) features whose properties follow
// store.RoadImport.
func (s *Server) importRoads(w http.ResponseWriter, r *http.Request) {
	var fc struct {
		Type     string `json:"type"`
		Features []struct {
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
			Properties store.RoadImport `json:"properties"`
		} `json:"features"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 50<<20)).Decode(&fc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid GeoJSON: "+err.Error())
		return
	}
	if fc.Type != "FeatureCollection" {
		writeError(w, http.StatusBadRequest, "expected a GeoJSON FeatureCollection")
		return
	}
	var roads []store.RoadImport
	var problems []string
	for i, f := range fc.Features {
		props := f.Properties
		switch f.Geometry.Type {
		case "LineString":
			var coords [][]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &coords); err != nil {
				problems = append(problems, fmt.Sprintf("feature %d: bad coordinates", i))
				continue
			}
			props.Coordinates = toPairs(coords)
			roads = append(roads, props)
		case "MultiLineString":
			var parts [][][]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &parts); err != nil {
				problems = append(problems, fmt.Sprintf("feature %d: bad coordinates", i))
				continue
			}
			for j, part := range parts {
				p := props
				if p.ExternalID != "" {
					p.ExternalID = fmt.Sprintf("%s#%d", props.ExternalID, j)
				}
				p.Coordinates = toPairs(part)
				roads = append(roads, p)
			}
		default:
			problems = append(problems, fmt.Sprintf("feature %d: geometry %q is not a LineString", i, f.Geometry.Type))
		}
	}
	sum, err := s.cfg.Store.ImportRoads(r.Context(), roads)
	if err != nil {
		s.storeError(w, err)
		return
	}
	sum.Errors = append(problems, sum.Errors...)
	if sum.Errors == nil {
		sum.Errors = []string{}
	}
	writeJSON(w, http.StatusOK, sum)
}

func toPairs(coords [][]float64) [][2]float64 {
	out := make([][2]float64, 0, len(coords))
	for _, c := range coords {
		if len(c) >= 2 {
			out = append(out, [2]float64{c[0], c[1]})
		}
	}
	return out
}

func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, media.MaxSize+1<<20)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart form with a file field")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()
	uri, err := s.cfg.Media.Save(file, header.Header.Get("Content-Type"), s.cfg.Now())
	if err != nil {
		if errors.Is(err, media.ErrUnsupported) || errors.Is(err, media.ErrTooLarge) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.cfg.Logger.Error("media save", "error", err)
		writeError(w, http.StatusInternalServerError, "could not store media")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"uri": uri})
}
