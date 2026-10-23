// Package queue is a crash-safe, file-per-item FIFO used by the edge agent to
// hold road events while a vehicle is offline. Every write goes to a temporary
// file that is fsynced and atomically renamed, so a power cut leaves either the
// old item or the new one, never a torn file.
package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Item is a queued payload with delivery bookkeeping.
type Item struct {
	Key      string          `json:"-"`
	Payload  json.RawMessage `json:"payload"`
	Attempts int             `json:"attempts"`
	Enqueued time.Time       `json:"enqueued"`
	// Attachments are files owned by the item (e.g. a captured frame); they are
	// deleted when the item is acknowledged or evicted.
	Attachments []string `json:"attachments,omitempty"`
}

// Queue is a directory-backed FIFO. It is safe for concurrent use.
type Queue struct {
	dir      string
	maxItems int
	mu       sync.Mutex
	seq      uint64
}

// Open creates the queue directory if needed. maxItems bounds disk use; when
// exceeded the oldest items are evicted (0 means unbounded).
func Open(dir string, maxItems int) (*Queue, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Remove temp files left by a crash mid-write.
	tmps, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	for _, t := range tmps {
		os.Remove(t)
	}
	return &Queue{dir: dir, maxItems: maxItems}, nil
}

// Dir returns the queue directory (attachments may be stored beneath it).
func (q *Queue) Dir() string { return q.dir }

// Enqueue durably stores a payload and returns its key.
func (q *Queue) Enqueue(payload any, attachments ...string) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	key := fmt.Sprintf("%020d-%06d.json", time.Now().UnixNano(), q.seq%1000000)
	item := Item{Key: key, Payload: raw, Enqueued: time.Now().UTC(), Attachments: attachments}
	if err := q.writeLocked(item); err != nil {
		return "", err
	}
	if q.maxItems > 0 {
		q.evictLocked()
	}
	return key, nil
}

func (q *Queue) writeLocked(item Item) error {
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(q.dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(q.dir, item.Key)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return syncDir(q.dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	// Some filesystems do not support directory fsync; that is not fatal.
	_ = d.Sync()
	return nil
}

func (q *Queue) keysLocked() ([]string, error) {
	entries, err := os.ReadDir(q.dir)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
			keys = append(keys, n)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (q *Queue) evictLocked() {
	keys, err := q.keysLocked()
	if err != nil || len(keys) <= q.maxItems {
		return
	}
	for _, k := range keys[:len(keys)-q.maxItems] {
		q.removeLocked(k)
	}
}

// Len returns the number of queued items.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	keys, _ := q.keysLocked()
	return len(keys)
}

// Peek returns up to n of the oldest items without removing them. Corrupt
// files are quarantined (renamed with a .bad suffix) rather than blocking the
// queue forever.
func (q *Queue) Peek(n int) ([]Item, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	keys, err := q.keysLocked()
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, k := range keys {
		if len(out) >= n {
			break
		}
		data, err := os.ReadFile(filepath.Join(q.dir, k))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, err
		}
		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			os.Rename(filepath.Join(q.dir, k), filepath.Join(q.dir, k+".bad"))
			continue
		}
		item.Key = k
		out = append(out, item)
	}
	return out, nil
}

// Update rewrites an item in place (e.g. to record attempts or an uploaded media URI).
func (q *Queue) Update(item Item) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, err := os.Stat(filepath.Join(q.dir, item.Key)); err != nil {
		return err
	}
	return q.writeLocked(item)
}

// Ack removes an item and its attachments after successful delivery.
func (q *Queue) Ack(key string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.removeLocked(key)
}

func (q *Queue) removeLocked(key string) error {
	path := filepath.Join(q.dir, key)
	data, err := os.ReadFile(path)
	if err == nil {
		var item Item
		if json.Unmarshal(data, &item) == nil {
			for _, a := range item.Attachments {
				os.Remove(a)
			}
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
