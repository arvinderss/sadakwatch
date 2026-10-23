// Package media stores event images and clips on the local filesystem. The
// URIs it returns are relative (/media/...) so a deployment can later move the
// files to object storage and rewrite the prefix without touching events.
package media

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
)

// MaxSize is the largest accepted upload.
const MaxSize = 15 << 20

var (
	ErrUnsupported = errors.New("unsupported media type: use JPEG, PNG, WebP or MP4")
	ErrTooLarge    = fmt.Errorf("media larger than %d MB", MaxSize>>20)
)

var extensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"video/mp4":  ".mp4",
}

// Store writes media under a root directory.
type Store struct {
	root string
}

// New creates the root directory if needed.
func New(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// Save sniffs the content type, writes the file atomically and returns its URI.
// The declared type is only a fallback for formats the sniffer cannot detect.
func (s *Store) Save(r io.Reader, declared string, now time.Time) (string, error) {
	br := bufio.NewReaderSize(r, 512)
	head, _ := br.Peek(512)
	ctype := http.DetectContentType(head)
	if i := strings.Index(ctype, ";"); i >= 0 {
		ctype = ctype[:i]
	}
	if _, ok := extensions[ctype]; !ok {
		// net/http does not sniff MP4 brands reliably.
		if declared == "video/mp4" && len(head) > 12 && string(head[4:8]) == "ftyp" {
			ctype = "video/mp4"
		} else {
			return "", ErrUnsupported
		}
	}
	rel := filepath.Join(now.UTC().Format("2006"), now.UTC().Format("01"), now.UTC().Format("02"), events.NewID()+extensions[ctype])
	full := filepath.Join(s.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(br, MaxSize+1))
	if err != nil {
		tmp.Close()
		return "", err
	}
	if n > MaxSize {
		tmp.Close()
		return "", ErrTooLarge
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		return "", err
	}
	return "/media/" + filepath.ToSlash(rel), nil
}

// Handler serves stored files without directory listings.
func (s *Store) Handler() http.Handler {
	fsys := http.FileServer(noDirFS{http.Dir(s.root)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		fsys.ServeHTTP(w, r)
	})
}

type noDirFS struct{ fs http.FileSystem }

func (n noDirFS) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}
