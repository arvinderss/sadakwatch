// Command edge-agent runs on a vehicle computer (e.g. a Raspberry Pi or
// Jetson next to the dashcam). It reads GPS, runs detection on camera frames
// via the vision service, keeps events in a durable on-disk queue and uploads
// them whenever the API is reachable.
//
// Frames are produced by any external capture process that writes JPEGs into
// FRAMES_DIR, for example:
//
//	ffmpeg -f v4l2 -i /dev/video0 -vf fps=2 -q:v 4 /var/lib/sadakwatch/frames/%012d.jpg
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/arvinderss/sadakwatch/internal/edge"
	"github.com/arvinderss/sadakwatch/internal/gps"
	"github.com/arvinderss/sadakwatch/internal/queue"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	vehicleID := getenv("VEHICLE_ID", "vehicle-dev-001")
	dataDir := getenv("EDGE_DATA_DIR", "./data/edge")
	q, err := queue.Open(dataDir+"/queue", getint("QUEUE_MAX_ITEMS", 20000))
	if err != nil {
		logger.Error("open queue", "error", err)
		os.Exit(1)
	}

	tracker := &gps.Tracker{}
	simulate := os.Getenv("SIMULATE") == "1" || os.Getenv("SIMULATE") == "true"
	if simulate {
		lat := getfloat("SIM_LAT", 28.6139)
		lon := getfloat("SIM_LON", 77.2090)
		go gps.Simulate(ctx, tracker, lat, lon, getfloat("SIM_RADIUS_M", 800), getfloat("SIM_SPEED_KPH", 30))
		logger.Info("GPS simulator running", "lat", lat, "lon", lon)
	} else if dev := os.Getenv("GPS_DEVICE"); dev != "" {
		go readGPS(ctx, logger, dev, tracker)
	} else {
		logger.Warn("no GPS_DEVICE configured and SIMULATE is off: frames will be dropped until a fix arrives")
	}

	agent := edge.NewAgent(edge.Config{
		VehicleID:     vehicleID,
		FramesDir:     getenv("FRAMES_DIR", dataDir+"/frames"),
		MinConfidence: getfloat("MIN_CONFIDENCE", 0.45),
		MinSpeedKPH:   getfloat("MIN_SPEED_KPH", 5),
	}, &edge.VisionClient{URL: getenv("VISION_URL", "http://localhost:8090")}, tracker, q, logger)

	if simulate {
		go agent.RunSimulator(ctx, time.Duration(getint("SIM_EVERY_SECONDS", 8))*time.Second)
	} else {
		os.MkdirAll(getenv("FRAMES_DIR", dataDir+"/frames"), 0o755)
		go agent.RunFrames(ctx)
	}

	up := &edge.Uploader{
		APIURL: getenv("API_URL", "http://localhost:8080"),
		APIKey: os.Getenv("API_KEY"),
		Queue:  q,
		Log:    logger,
	}
	logger.Info("SadakWatch edge agent starting", "vehicle_id", vehicleID, "api", up.APIURL, "queued", q.Len())
	up.Run(ctx)
	logger.Info("edge agent stopped", "queued", q.Len())
}

// readGPS reopens the device whenever it disappears (USB receivers get
// unplugged and re-enumerated on vehicles).
func readGPS(ctx context.Context, logger *slog.Logger, dev string, t *gps.Tracker) {
	for ctx.Err() == nil {
		f, err := os.Open(dev)
		if err != nil {
			logger.Warn("open GPS device", "device", dev, "error", err)
		} else {
			logger.Info("reading NMEA", "device", dev)
			err = gps.ReadNMEA(ctx, f, t)
			f.Close()
			if err != nil && ctx.Err() == nil {
				logger.Warn("GPS read ended", "error", err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getfloat(key string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return v
	}
	return fallback
}

func getint(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return fallback
}
