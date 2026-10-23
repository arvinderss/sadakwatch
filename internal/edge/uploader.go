package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
	"github.com/arvinderss/sadakwatch/internal/queue"
)

// Uploader drains the durable queue to the API with exponential backoff.
type Uploader struct {
	APIURL    string
	APIKey    string
	Queue     *queue.Queue
	Log       *slog.Logger
	Client    *http.Client
	BatchSize int
}

// errPermanent marks an item the server will never accept.
var errPermanent = errors.New("permanent upload failure")

// Run uploads until ctx is cancelled.
func (u *Uploader) Run(ctx context.Context) {
	backoff := time.Second
	for {
		sent, err := u.Flush(ctx)
		wait := 2 * time.Second
		switch {
		case err != nil:
			u.Log.Warn("upload failed; will retry", "error", err, "retry_in", backoff.String(), "queued", u.Queue.Len())
			wait = backoff
			backoff = min(backoff*2, 5*time.Minute)
		case sent > 0:
			backoff = time.Second
			wait = 0
		default:
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Flush uploads one batch and returns how many items were delivered.
func (u *Uploader) Flush(ctx context.Context) (int, error) {
	size := u.BatchSize
	if size <= 0 {
		size = 50
	}
	items, err := u.Queue.Peek(size)
	if err != nil || len(items) == 0 {
		return 0, err
	}
	batch := make([]events.RoadEvent, 0, len(items))
	keys := make([]string, 0, len(items))
	for _, item := range items {
		p, err := decodePending(item)
		if err != nil {
			u.Queue.Ack(item.Key)
			continue
		}
		if p.ImagePath != "" && p.Event.ImageURI == "" {
			uri, err := u.uploadImage(ctx, p.ImagePath)
			switch {
			case errors.Is(err, os.ErrNotExist), errors.Is(err, errPermanent):
				// Send the event without its image rather than blocking the queue.
				u.Log.Warn("dropping image", "path", p.ImagePath, "error", err)
				p.ImagePath = ""
			case err != nil:
				return 0, err
			default:
				p.Event.ImageURI = uri
			}
			// Persist the media URI so a retry does not re-upload the image.
			raw, _ := json.Marshal(p)
			item.Payload = raw
			item.Attempts++
			if err := u.Queue.Update(item); err != nil {
				return 0, err
			}
		}
		batch = append(batch, p.Event)
		keys = append(keys, item.Key)
	}
	if len(batch) == 0 {
		return 0, nil
	}
	results, err := u.postEvents(ctx, batch)
	if err != nil {
		return 0, err
	}
	for i, key := range keys {
		if i < len(results) && results[i].Error != "" {
			u.Log.Warn("server rejected event; discarding", "event_id", batch[i].EventID, "error", results[i].Error)
		}
		u.Queue.Ack(key)
	}
	return len(keys), nil
}

func (u *Uploader) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (u *Uploader) authorize(req *http.Request) {
	if u.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+u.APIKey)
	}
}

func (u *Uploader) uploadImage(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.APIURL+"/v1/media", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	u.authorize(req)
	resp, err := u.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge {
		return "", fmt.Errorf("%w: media rejected with %d", errPermanent, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("media upload returned %d", resp.StatusCode)
	}
	var out struct {
		URI string `json:"uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.URI, nil
}

func (u *Uploader) postEvents(ctx context.Context, batch []events.RoadEvent) ([]events.IngestResult, error) {
	payload, _ := json.Marshal(map[string]any{"events": batch})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.APIURL+"/v1/events", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	u.authorize(req)
	resp, err := u.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("event upload returned %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Results []events.IngestResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Results, nil
}
