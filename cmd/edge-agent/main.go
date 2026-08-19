package main

import (
	"log/slog"
	"os"
	"time"
)

type Agent struct {
	VehicleID string
	Logger    *slog.Logger
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	vehicleID := getenv("VEHICLE_ID", "vehicle-dev-001")

	agent := Agent{VehicleID: vehicleID, Logger: logger}
	logger.Info("SadakWatch edge agent starting", "vehicle_id", agent.VehicleID)

	// Capture, GPS, inference, durable queue and upload workers will be added here.
	// Keep the process long-lived so each hardware adapter can run independently.
	for {
		time.Sleep(30 * time.Second)
		logger.Debug("edge agent heartbeat", "vehicle_id", agent.VehicleID)
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
