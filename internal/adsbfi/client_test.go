package adsbfi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lizthegrey/awtrix-flights/internal/filter"
	"github.com/lizthegrey/awtrix-flights/internal/geo"
)

const fixture = `{
  "aircraft": [
    {
      "hex": "7C1ABC",
      "flight": "QFA75   ",
      "t": "B789",
      "lat": -33.925,
      "lon": 151.155,
      "alt_baro": 2500,
      "gs": 220.4,
      "track": 335.1,
      "baro_rate": 2400
    },
    {
      "hex": "7c0011",
      "flight": "VOZ123 ",
      "t": "B738",
      "lat": -33.96,
      "lon": 151.18,
      "alt_baro": "ground",
      "gs": 5,
      "track": 343,
      "baro_rate": 0
    },
    {
      "hex": "7c1ddd",
      "flight": "MISSING",
      "t": "B789",
      "alt_baro": 5000,
      "gs": 200,
      "track": 340,
      "baro_rate": 1000
    }
  ]
}`

func TestSearchParsing(t *testing.T) {
	center := geo.Point{Lat: -33.88, Lon: 151.13}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lat/-33.880000/lon/151.130000/dist/8" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	got, err := c.Search(context.Background(), center, 8)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	want := []filter.State{
		{
			ICAO24:      "7c1abc",
			Callsign:    "QFA75",
			ICAOType:    "B789",
			Position:    geo.Point{Lat: -33.925, Lon: 151.155},
			AltitudeFt:  2500,
			GroundSpdKt: 220.4,
			HeadingDeg:  335.1,
			VertRateFpm: 2400,
		},
		{
			ICAO24:      "7c0011",
			Callsign:    "VOZ123",
			ICAOType:    "B738",
			Position:    geo.Point{Lat: -33.96, Lon: 151.18},
			AltitudeFt:  0,
			GroundSpdKt: 5,
			HeadingDeg:  343,
			VertRateFpm: 0,
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Search mismatch (-want +got):\n%s", diff)
	}
}

func TestSearchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream sad"))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	if _, err := c.Search(context.Background(), geo.Point{Lat: -33.88, Lon: 151.13}, 8); err == nil {
		t.Fatal("expected error, got nil")
	}
}
