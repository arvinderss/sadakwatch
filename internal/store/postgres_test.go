package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/arvinderss/sadakwatch/internal/events"
)

// These tests need PostgreSQL with PostGIS. CI provides one; locally run e.g.
//
//	docker compose up -d postgres
//	TEST_DATABASE_URL=postgres://sadakwatch:sadakwatch@localhost:5432/sadakwatch?sslmode=disable go test ./internal/store
func openTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := OpenPostgres(ctx, dsn, PostgresOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestPostgresStoreContract(t *testing.T) {
	RunStoreContract(t, openTestPostgres(t))
}

func TestPostgresMigrateIsIdempotent(t *testing.T) {
	p := openTestPostgres(t)
	if err := p.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresResponsibility(t *testing.T) {
	p := openTestPostgres(t)
	ctx := context.Background()
	suffix := events.NewID()[:8]

	// A short east-west road segment in Chandigarh.
	coords := [][2]float64{{76.7790, 30.7330}, {76.7840, 30.7330}}
	sum, err := p.ImportRoads(ctx, []RoadImport{
		{
			ExternalID: "test-road-" + suffix, Name: "Test Marg " + suffix, Coordinates: coords,
			Authority: "Test PWD " + suffix, Division: "Division A", Contractor: "Old Builders " + suffix,
			ContractReference: "OLD/1", ValidFrom: "2024-01-01", ValidTo: "2025-12-31",
			Officer: "Officer Old " + suffix, OfficerTitle: "Executive Engineer", SourceReference: "test-fixture",
		},
		{
			ExternalID: "test-road-" + suffix, Name: "Test Marg " + suffix, Coordinates: coords,
			Authority: "Test PWD " + suffix, Division: "Division A", Contractor: "New Builders " + suffix,
			ContractReference: "NEW/2", ValidFrom: "2026-01-01",
			Officer: "Officer New " + suffix, OfficerTitle: "Assistant Engineer", SourceReference: "test-fixture",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Roads != 2 || sum.Contracts != 2 || sum.Officers != 2 || len(sum.Errors) != 0 {
		t.Fatalf("unexpected import summary %+v", sum)
	}

	// 10 m north of the centreline.
	lat, lon := 30.73309, 76.7815
	old, err := p.Responsibility(ctx, lat, lon, time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if old.ContractorName != "Old Builders "+suffix || old.OfficerName != "Officer Old "+suffix || old.ContractReference != "OLD/1" {
		t.Fatalf("2025 responsibility wrong: %+v", old)
	}
	cur, err := p.Responsibility(ctx, lat, lon, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if cur.ContractorName != "New Builders "+suffix || cur.AuthorityName != "Test PWD "+suffix || cur.DivisionName != "Division A" {
		t.Fatalf("2026 responsibility wrong: %+v", cur)
	}

	// 500 m away there is no road.
	none, err := p.Responsibility(ctx, lat+0.0045, lon, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if none.RoadID != "" {
		t.Fatalf("expected no road match, got %+v", none)
	}

	// Ingestion stamps the defect with responsibility.
	e := ev("bus-9", events.EventWaterlogging, lat, lon, events.SeverityHigh, time.Now().UTC().Add(-time.Minute))
	res, err := p.Ingest(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.GetDefect(ctx, res.DefectID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Responsibility.RoadName != "Test Marg "+suffix || d.Responsibility.ContractorName != "New Builders "+suffix {
		t.Fatalf("defect responsibility wrong: %+v", d.Responsibility)
	}
	st, err := p.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.TopContractors) == 0 {
		t.Fatal("expected contractor leaderboard")
	}
}
