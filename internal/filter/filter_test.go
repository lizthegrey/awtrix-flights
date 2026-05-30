package filter

import (
	"math"
	"testing"

	"github.com/lizthegrey/awtrix-flights/internal/geo"
)

// Tests use synthetic geometry so they don't bake in a real observer location.
// Observer is at (0, 0); aircraft positions are expressed as small offsets in
// degrees. At the equator 1° ≈ 60 nm.
var testObserver = geo.Point{Lat: 0, Lon: 0}

// An aircraft 3 nm south of observer, heading due north (0°), climbing.
// Projected CPA: directly overhead, ~49 s out, ~4460 ft (2500 + 2400 * 49/60).
func climbingHeavy() State {
	return State{
		ICAO24:      "7c1abc",
		Callsign:    "QFA75",
		ICAOType:    "B789",
		Position:    geo.Point{Lat: -0.05, Lon: 0}, // 3 nm south
		AltitudeFt:  2500,
		GroundSpdKt: 220,
		HeadingDeg:  0,
		VertRateFpm: 2400,
	}
}

func TestEvaluate_HappyPath(t *testing.T) {
	m := Default(testObserver).Evaluate(climbingHeavy())
	if !m.OK {
		t.Fatalf("want OK, got %+v", m)
	}
	if math.Abs(m.CrossTrackNM) > 0.1 {
		t.Errorf("CrossTrackNM = %.3f, want ~0 for on-track aircraft", m.CrossTrackNM)
	}
	if m.SecondsToCPA < 40 || m.SecondsToCPA > 60 {
		t.Errorf("SecondsToCPA = %.0f, want ~49 s", m.SecondsToCPA)
	}
	if m.AltitudeAtCPAFt < 4300 || m.AltitudeAtCPAFt > 4600 {
		t.Errorf("AltitudeAtCPAFt = %.0f, want ~4460", m.AltitudeAtCPAFt)
	}
}

func TestEvaluate_Rejections(t *testing.T) {
	tests := []struct {
		name       string
		mut        func(*State)
		wantReason string
	}{
		{
			name:       "too_slow_ground_vehicle",
			mut:        func(s *State) { s.GroundSpdKt = 10 },
			wantReason: "too slow",
		},
		{
			name: "lateral_offset_passes_well_east",
			mut: func(s *State) {
				// Same heading but starting 3 nm east of on-track → CPA misses by ~3 nm
				s.Position = geo.Point{Lat: -0.05, Lon: 0.05}
			},
			wantReason: "too lateral",
		},
		{
			name: "cruise_altitude_at_overhead",
			mut: func(s *State) {
				s.AltitudeFt = 12000
				s.VertRateFpm = 1500 // still climbing → projected alt much higher
			},
			wantReason: "too high at overhead",
		},
		{
			name: "already_overhead_moving_away",
			mut: func(s *State) {
				// 2 nm north of observer, still heading 0° → already past
				s.Position = geo.Point{Lat: 0.033, Lon: 0}
			},
			wantReason: "already past",
		},
		{
			name: "wrong_heading_perpendicular_off_track",
			mut: func(s *State) {
				// Heading due east, starting south of observer — won't approach
				s.HeadingDeg = 90
				s.Position = geo.Point{Lat: -0.05, Lon: -0.05}
			},
			wantReason: "too lateral",
		},
		{
			name: "descending_arrival_over_observer",
			mut: func(s *State) {
				// On-track and low, but descending → an arrival, not a departure.
				s.VertRateFpm = -800
			},
			wantReason: "descending",
		},
	}
	cfg := Default(testObserver)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := climbingHeavy()
			tc.mut(&s)
			m := cfg.Evaluate(s)
			if m.OK {
				t.Fatalf("want rejection, got OK: %+v", m)
			}
			if m.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q (match=%+v)", m.Reason, tc.wantReason, m)
			}
		})
	}
}

// Level flight (0 fpm) on track and low must still match: it's a departure on
// a SID level-off or genuine low transit, both of which are perceptibly
// overhead. Only descending (arrival) traffic is excluded.
func TestEvaluate_LevelOverflightMatches(t *testing.T) {
	s := climbingHeavy()
	s.VertRateFpm = 0
	if m := Default(testObserver).Evaluate(s); !m.OK {
		t.Fatalf("want level overflight to match, got %+v", m)
	}
}

// A regional turboprop (DH8D) at low altitude on a track to overhead the
// observer should still match — perceptibility, not type, is the criterion.
func TestEvaluate_TurbopropMatches(t *testing.T) {
	s := State{
		ICAO24:      "7c2dh8",
		Callsign:    "QLK456",
		ICAOType:    "DH8D",
		Position:    geo.Point{Lat: -0.05, Lon: 0},
		AltitudeFt:  3000,
		GroundSpdKt: 180,
		HeadingDeg:  0,
		VertRateFpm: 1500,
	}
	m := Default(testObserver).Evaluate(s)
	if !m.OK {
		t.Errorf("turboprop on overhead track should match; got %+v", m)
	}
}
