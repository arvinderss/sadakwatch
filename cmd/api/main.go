// Command api runs the SadakWatch ingestion API and web dashboard.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arvinderss/sadakwatch/internal/api"
	"github.com/arvinderss/sadakwatch/internal/media"
	"github.com/arvinderss/sadakwatch/internal/store"
	"github.com/arvinderss/sadakwatch/web"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	if err := run(logger); err != nil {
		logger.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var st store.Store
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		matchM, _ := strconv.ParseFloat(getenv("ROAD_MATCH_METERS", "30"), 64)
		startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		pg, err := openWithRetry(startCtx, logger, dsn, store.PostgresOptions{
			TimeZone:   getenv("ADMIN_TIMEZONE", "Asia/Kolkata"),
			RoadMatchM: matchM,
		})
		cancel()
		if err != nil {
			return err
		}
		st = pg
		logger.Info("using PostgreSQL/PostGIS store")
	} else {
		st = store.NewMemory()
		logger.Warn("DATABASE_URL not set: using in-memory store (data is lost on restart, no road responsibility)")
	}
	defer st.Close()

	mediaStore, err := media.New(getenv("STORAGE_PATH", "./data/media"))
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr: getenv("HTTP_ADDR", ":8080"),
		Handler: api.New(api.Config{
			Store:      st,
			Media:      mediaStore,
			Logger:     logger,
			Web:        web.Static(),
			Version:    version,
			IngestKeys: splitKeys(os.Getenv("INGEST_API_KEYS")),
			AdminKey:   os.Getenv("ADMIN_API_KEY"),
			CORSOrigin: getenv("CORS_ORIGIN", "*"),
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("SadakWatch API starting", "addr", srv.Addr, "version", version)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// openWithRetry tolerates the database starting after the API (docker compose).
func openWithRetry(ctx context.Context, logger *slog.Logger, dsn string, opts store.PostgresOptions) (*store.Postgres, error) {
	for {
		pg, err := store.OpenPostgres(ctx, dsn, opts)
		if err == nil {
			return pg, nil
		}
		logger.Warn("database not ready, retrying", "error", err)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(2 * time.Second):
		}
	}
}

func splitKeys(v string) []string {
	var out []string
	for _, k := range strings.Split(v, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
