package gps

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseRMCAndGGA(t *testing.T) {
	var p Parser
	// Connaught Place, New Delhi; checksums computed for these exact sentences.
	gga := "$GPGGA,063015.00,2837.6340,N,07712.5400,E,1,09,1.2,215.0,M,-35.0,M,,*" + checksum("GPGGA,063015.00,2837.6340,N,07712.5400,E,1,09,1.2,215.0,M,-35.0,M,,")
	rmc := "$GPRMC,063015.50,A,2837.6340,N,07712.5400,E,16.2,87.5,231026,,,A*" + checksum("GPRMC,063015.50,A,2837.6340,N,07712.5400,E,16.2,87.5,231026,,,A")
	if _, ok, err := p.Parse(gga); ok || err != nil {
		t.Fatalf("GGA should not yield a fix: %v", err)
	}
	fix, ok, err := p.Parse(rmc)
	if err != nil || !ok {
		t.Fatalf("RMC: ok=%v err=%v", ok, err)
	}
	if math.Abs(fix.Latitude-28.62723) > 1e-4 || math.Abs(fix.Longitude-77.209) > 1e-4 {
		t.Fatalf("position %f,%f", fix.Latitude, fix.Longitude)
	}
	if math.Abs(fix.SpeedKPH-30.0) > 0.1 || fix.Heading != 87.5 {
		t.Fatalf("speed %f heading %f", fix.SpeedKPH, fix.Heading)
	}
	want := time.Date(2026, 10, 23, 6, 30, 15, 500_000_000, time.UTC)
	if !fix.Time.Equal(want) {
		t.Fatalf("time %v", fix.Time)
	}
	if fix.AccuracyM != 6 || fix.Satellites != 9 {
		t.Fatalf("accuracy %f sats %d", fix.AccuracyM, fix.Satellites)
	}
}

func TestRejectsBadChecksumAndVoidFix(t *testing.T) {
	var p Parser
	if _, _, err := p.Parse("$GPRMC,063015.50,A,2837.6340,N,07712.5400,E,16.2,87.5,231026,,,A*00"); err != ErrChecksum {
		t.Fatalf("expected checksum error, got %v", err)
	}
	void := "GNRMC,063015.50,V,,,,,,,231026,,,N"
	if _, ok, err := p.Parse("$" + void + "*" + checksum(void)); ok || err != nil {
		t.Fatal("void fix must be ignored")
	}
}

func TestSouthWestAndReader(t *testing.T) {
	s := "GNRMC,120000,A,3351.5000,S,15112.6000,W,0.0,0.0,010126,,,A"
	tr := &Tracker{}
	if err := ReadNMEA(context.Background(), strings.NewReader("garbage\n$"+s+"*"+checksum(s)+"\n"), tr); err != nil {
		t.Fatal(err)
	}
	fix, ok := tr.Latest(time.Date(2026, 1, 1, 12, 0, 1, 0, time.UTC), 5*time.Second)
	if !ok || fix.Latitude > -33 || fix.Longitude > -151 {
		t.Fatalf("got %+v ok=%v", fix, ok)
	}
	if _, ok := tr.Latest(time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC), 5*time.Second); ok {
		t.Fatal("stale fix must not be returned")
	}
}

func checksum(body string) string {
	var s byte
	for i := 0; i < len(body); i++ {
		s ^= body[i]
	}
	const hex = "0123456789ABCDEF"
	return string([]byte{hex[s>>4], hex[s&0xF]})
}
