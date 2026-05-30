// Package filter selects aircraft whose projected ground track will pass
// close enough overhead, and low enough, to be visible/audible from the
// observer's window.
package filter

import (
	"math"

	"github.com/lizthegrey/awtrix-flights/internal/geo"
)

// State is the subset of an ADS-B state vector we need to make a decision.
type State struct {
	ICAO24      string // hex transponder ID
	Callsign    string // e.g. "QFA75"
	ICAOType    string // e.g. "B789"
	Position    geo.Point
	AltitudeFt  float64 // pressure altitude in feet
	GroundSpdKt float64 // ground speed in knots
	HeadingDeg  float64 // track over ground in degrees true
	VertRateFpm float64 // climb rate in feet per minute (negative for descent)
}

// Match describes why an aircraft was (or wasn't) selected.
type Match struct {
	OK              bool
	CrossTrackNM    float64 // signed; |.| is lateral miss distance at CPA
	AlongTrackNM    float64 // distance along path from current pos to CPA
	SecondsToCPA    float64 // negative if CPA is behind aircraft
	AltitudeAtCPAFt float64 // projected altitude at the closest approach
	Reason          string  // populated when !OK
}

// Config tunes the perceptibility thresholds. Zero values are not useful; use Default.
type Config struct {
	Observer         geo.Point
	MaxCrossTrackNM  float64 // lateral miss at CPA must be ≤ this to count as overhead
	MaxAltAtCPAFt    float64 // projected altitude at CPA must be ≤ this to be perceivable
	MaxSecondsToCPA  float64 // CPA must be within this many seconds (future)
	MinSecondsToCPA  float64 // CPA must be at least this far in the future (else it's already past)
	MinGroundSpdKt   float64 // ignore essentially stationary contacts
	IgnoreDescending bool    // skip aircraft descending (arrivals at YSSY)
}

// Default returns the perceptibility configuration centered on observer.
// The observer location is supplied by the caller (kept out of source).
func Default(observer geo.Point) Config {
	return Config{
		Observer:         observer,
		MaxCrossTrackNM:  1.5,  // ~2.8 km lateral — close enough to see/hear from window
		MaxAltAtCPAFt:    8000, // higher than this and you won't really notice
		MaxSecondsToCPA:  240,  // 4 minutes — enough lead time but not so far we lose accuracy
		MinSecondsToCPA:  -30,  // tiny grace so we don't flap right as it overflies
		MinGroundSpdKt:   60,   // exclude helicopters hovering, ground vehicles
		IgnoreDescending: true, // exclude descending arrivals (e.g. 16R approaches over the observer); we show departures + level transit overflights
	}
}

// Evaluate computes the CPA geometry for s and returns whether it matches.
func (c Config) Evaluate(s State) Match {
	if s.GroundSpdKt < c.MinGroundSpdKt {
		return Match{Reason: "too slow"}
	}
	if c.IgnoreDescending && s.VertRateFpm < 0 {
		return Match{Reason: "descending"}
	}

	xt := geo.CrossTrackNM(s.Position, c.Observer, s.HeadingDeg)
	at := geo.AlongTrackNM(s.Position, c.Observer, s.HeadingDeg)

	// Time to closest point of approach. Negative if CPA is behind aircraft.
	hours := at / s.GroundSpdKt
	secs := hours * 3600

	// Project altitude forward at climb rate (clamp to 0 ft floor).
	altCPA := s.AltitudeFt + (s.VertRateFpm * hours * 60)
	if altCPA < 0 {
		altCPA = 0
	}

	m := Match{
		CrossTrackNM:    xt,
		AlongTrackNM:    at,
		SecondsToCPA:    secs,
		AltitudeAtCPAFt: altCPA,
	}

	if math.Abs(xt) > c.MaxCrossTrackNM {
		m.Reason = "too lateral"
		return m
	}
	if secs < c.MinSecondsToCPA {
		m.Reason = "already past"
		return m
	}
	if secs > c.MaxSecondsToCPA {
		m.Reason = "too far in future"
		return m
	}
	if altCPA > c.MaxAltAtCPAFt {
		m.Reason = "too high at overhead"
		return m
	}
	m.OK = true
	return m
}
