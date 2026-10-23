// Package gps provides GNSS fixes to the edge agent: an NMEA 0183 reader for
// real receivers (USB/serial GPS pucks, dashcam GPS streams, gpsd raw output)
// and a simulator that drives a loop for development.
package gps

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fix is one position report.
type Fix struct {
	Time       time.Time
	Latitude   float64
	Longitude  float64
	SpeedKPH   float64
	Heading    float64
	AccuracyM  float64 // estimated horizontal accuracy; 0 if unknown
	Satellites int
}

// Tracker holds the most recent fix and is safe for concurrent use.
type Tracker struct {
	mu   sync.RWMutex
	last Fix
	ok   bool
}

// Set records a new fix.
func (t *Tracker) Set(f Fix) {
	t.mu.Lock()
	t.last, t.ok = f, true
	t.mu.Unlock()
}

// Latest returns the most recent fix if it is younger than maxAge.
func (t *Tracker) Latest(now time.Time, maxAge time.Duration) (Fix, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.ok || now.Sub(t.last.Time) > maxAge {
		return Fix{}, false
	}
	return t.last, true
}

// ErrChecksum is returned for sentences whose checksum does not match.
var ErrChecksum = errors.New("nmea checksum mismatch")

// Parser assembles fixes from RMC (position, speed, course, time) and GGA
// (fix quality, satellites, HDOP) sentences.
type Parser struct {
	hdop       float64
	satellites int
}

// Parse consumes one NMEA sentence. It returns a fix for every valid RMC
// sentence; other sentences only update auxiliary state.
func (p *Parser) Parse(line string) (Fix, bool, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "$") {
		return Fix{}, false, nil
	}
	body := line[1:]
	if i := strings.IndexByte(body, '*'); i >= 0 {
		want, err := strconv.ParseUint(body[i+1:], 16, 8)
		if err != nil {
			return Fix{}, false, ErrChecksum
		}
		var sum byte
		for j := 0; j < i; j++ {
			sum ^= body[j]
		}
		if byte(want) != sum {
			return Fix{}, false, ErrChecksum
		}
		body = body[:i]
	}
	f := strings.Split(body, ",")
	if len(f[0]) < 5 {
		return Fix{}, false, nil
	}
	switch f[0][2:] {
	case "GGA":
		if len(f) > 8 {
			p.satellites, _ = strconv.Atoi(f[7])
			p.hdop, _ = strconv.ParseFloat(f[8], 64)
		}
		return Fix{}, false, nil
	case "RMC":
		return p.parseRMC(f)
	}
	return Fix{}, false, nil
}

func (p *Parser) parseRMC(f []string) (Fix, bool, error) {
	if len(f) < 10 || f[2] != "A" {
		return Fix{}, false, nil // no fix yet
	}
	lat, err := parseCoord(f[3], f[4], 2)
	if err != nil {
		return Fix{}, false, err
	}
	lon, err := parseCoord(f[5], f[6], 3)
	if err != nil {
		return Fix{}, false, err
	}
	knots, _ := strconv.ParseFloat(f[7], 64)
	course, _ := strconv.ParseFloat(f[8], 64)
	ts, err := time.Parse("020106 150405", f[9]+" "+trimFraction(f[1]))
	if err != nil {
		return Fix{}, false, fmt.Errorf("rmc time: %w", err)
	}
	if frac := fraction(f[1]); frac > 0 {
		ts = ts.Add(frac)
	}
	fix := Fix{
		Time: ts.UTC(), Latitude: lat, Longitude: lon,
		SpeedKPH: knots * 1.852, Heading: math.Mod(course, 360), Satellites: p.satellites,
	}
	if p.hdop > 0 {
		// A common rule of thumb for consumer receivers: ~5 m per unit HDOP.
		fix.AccuracyM = math.Min(500, p.hdop*5)
	}
	return fix, true, nil
}

func trimFraction(s string) string {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i]
	}
	return s
}

func fraction(s string) time.Duration {
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return 0
	}
	v, err := strconv.ParseFloat("0"+s[i:], 64)
	if err != nil {
		return 0
	}
	return time.Duration(v * float64(time.Second))
}

// parseCoord converts NMEA ddmm.mmmm / dddmm.mmmm with hemisphere to degrees.
func parseCoord(v, hemi string, degDigits int) (float64, error) {
	if len(v) < degDigits+2 {
		return 0, fmt.Errorf("bad coordinate %q", v)
	}
	deg, err := strconv.ParseFloat(v[:degDigits], 64)
	if err != nil {
		return 0, err
	}
	min, err := strconv.ParseFloat(v[degDigits:], 64)
	if err != nil {
		return 0, err
	}
	out := deg + min/60
	switch hemi {
	case "S", "W":
		out = -out
	case "N", "E":
	default:
		return 0, fmt.Errorf("bad hemisphere %q", hemi)
	}
	return out, nil
}

// ReadNMEA reads sentences from r until it ends or ctx is cancelled, feeding
// every valid fix to the tracker.
func ReadNMEA(ctx context.Context, r io.Reader, t *Tracker) error {
	var p Parser
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if fix, ok, err := p.Parse(sc.Text()); err == nil && ok {
			t.Set(fix)
		}
	}
	return sc.Err()
}

// Simulate drives a circular route around a centre point at the given speed,
// emitting a fix every second. It is used for development and demos.
func Simulate(ctx context.Context, t *Tracker, lat, lon, radiusM, speedKPH float64) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	angle := 0.0
	step := (speedKPH / 3.6) / radiusM // radians per second
	for {
		mLat := radiusM * math.Cos(angle)
		mLon := radiusM * math.Sin(angle)
		heading := math.Mod(math.Atan2(math.Cos(angle), -math.Sin(angle))*180/math.Pi+360, 360)
		t.Set(Fix{
			Time:      time.Now().UTC(),
			Latitude:  lat + mLat/111320,
			Longitude: lon + mLon/(111320*math.Cos(lat*math.Pi/180)),
			SpeedKPH:  speedKPH, Heading: heading, AccuracyM: 5, Satellites: 9,
		})
		angle += step
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
