package geo

import (
	"math"
	"testing"
)

func TestDistanceNM(t *testing.T) {
	tests := []struct {
		name   string
		a, b   Point
		wantNM float64
		tolNM  float64
	}{
		{
			// Sanity check at a Sydney-area scale (~5-6 nm).
			name:   "short_local_distance",
			a:      Point{Lat: -33.96, Lon: 151.18},
			b:      Point{Lat: -33.88, Lon: 151.13},
			wantNM: 5.7,
			tolNM:  0.3,
		},
		{
			name:   "identity",
			a:      Point{Lat: -33.96, Lon: 151.18},
			b:      Point{Lat: -33.96, Lon: 151.18},
			wantNM: 0,
			tolNM:  0.001,
		},
		{
			name:   "syd_to_mel_approx",
			a:      Point{Lat: -33.94, Lon: 151.17},
			b:      Point{Lat: -37.67, Lon: 144.85},
			wantNM: 380,
			tolNM:  5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DistanceNM(tc.a, tc.b)
			if math.Abs(got-tc.wantNM) > tc.tolNM {
				t.Errorf("DistanceNM = %.3f nm, want %.3f ± %.3f", got, tc.wantNM, tc.tolNM)
			}
		})
	}
}

func TestBearingDeg(t *testing.T) {
	tests := []struct {
		name    string
		a, b    Point
		wantDeg float64
		tolDeg  float64
	}{
		{
			// YSSY-area NNW bearing sanity check (a SID-like vector).
			name:    "local_nnw_bearing",
			a:       Point{Lat: -33.96, Lon: 151.18},
			b:       Point{Lat: -33.88, Lon: 151.13},
			wantDeg: 330,
			tolDeg:  5,
		},
		{
			name:    "due_north",
			a:       Point{Lat: -34, Lon: 151},
			b:       Point{Lat: -33, Lon: 151},
			wantDeg: 0,
			tolDeg:  0.1,
		},
		{
			name:    "due_east",
			a:       Point{Lat: -34, Lon: 151},
			b:       Point{Lat: -34, Lon: 152},
			wantDeg: 90,
			tolDeg:  0.5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BearingDeg(tc.a, tc.b)
			diff := math.Mod(math.Abs(got-tc.wantDeg)+540, 360) - 180
			if math.Abs(diff) > tc.tolDeg {
				t.Errorf("BearingDeg = %.3f°, want %.3f° ± %.3f°", got, tc.wantDeg, tc.tolDeg)
			}
		})
	}
}

func TestHeadingWithin(t *testing.T) {
	tests := []struct {
		name           string
		h, center, tol float64
		want           bool
	}{
		{"on_target", 340, 340, 30, true},
		{"in_range_low", 315, 340, 30, true},
		{"in_range_high", 005, 340, 30, true},
		{"wrap_through_north", 359, 010, 20, true},
		{"out_of_range", 270, 340, 30, false},
		{"opposite", 160, 340, 30, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HeadingWithin(tc.h, tc.center, tc.tol); got != tc.want {
				t.Errorf("HeadingWithin(%g, %g, %g) = %v, want %v", tc.h, tc.center, tc.tol, got, tc.want)
			}
		})
	}
}
