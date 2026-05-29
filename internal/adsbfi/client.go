// Package adsbfi is a thin client for adsb.fi's free public state-vector API.
//
//	https://github.com/adsbfi/opendata
//
// We use the geographic search endpoint:
//
//	GET https://opendata.adsb.fi/api/v2/lat/{lat}/lon/{lon}/dist/{nm}
package adsbfi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/lizthegrey/awtrix-flights/internal/filter"
	"github.com/lizthegrey/awtrix-flights/internal/geo"
)

const defaultBaseURL = "https://opendata.adsb.fi/api/v2"

// Client queries adsb.fi.
type Client struct {
	HTTP      *http.Client
	BaseURL   string
	UserAgent string
}

// New returns a Client with sensible defaults. The HTTP client is OTel-wrapped
// so outbound calls show up as spans alongside the Lambda handler span.
func New() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout:   5 * time.Second,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
		BaseURL:   defaultBaseURL,
		UserAgent: "awtrix-flights/0.1 (github.com/lizthegrey/awtrix-flights)",
	}
}

// rawAircraft mirrors the subset of adsb.fi's aircraft object we use.
// alt_baro is "ground" (string) or a number, so we decode into json.RawMessage
// and parse it ourselves.
type rawAircraft struct {
	Hex      string          `json:"hex"`
	Flight   string          `json:"flight"`
	Type     string          `json:"t"`
	Lat      *float64        `json:"lat"`
	Lon      *float64        `json:"lon"`
	AltBaro  json.RawMessage `json:"alt_baro"`
	GS       *float64        `json:"gs"`
	Track    *float64        `json:"track"`
	BaroRate *float64        `json:"baro_rate"`
}

type rawResp struct {
	Aircraft []rawAircraft `json:"aircraft"`
}

// Search returns state vectors for all aircraft within distNM of center.
// Aircraft missing fields required by filter.State are skipped.
func (c *Client) Search(ctx context.Context, center geo.Point, distNM int) ([]filter.State, error) {
	url := fmt.Sprintf("%s/lat/%.6f/lon/%.6f/dist/%d", c.BaseURL, center.Lat, center.Lon, distNM)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("adsb.fi GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("adsb.fi status %d: %s", resp.StatusCode, body)
	}

	var data rawResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode adsb.fi response: %w", err)
	}

	out := make([]filter.State, 0, len(data.Aircraft))
	for _, a := range data.Aircraft {
		s, ok := toState(a)
		if !ok {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// toState converts a raw aircraft to a filter.State, dropping any with
// missing required fields (we can't filter what we can't measure).
func toState(a rawAircraft) (filter.State, bool) {
	if a.Lat == nil || a.Lon == nil || a.GS == nil || a.Track == nil || a.BaroRate == nil {
		return filter.State{}, false
	}
	alt, ok := parseAlt(a.AltBaro)
	if !ok {
		return filter.State{}, false
	}
	return filter.State{
		ICAO24:      strings.ToLower(strings.TrimSpace(a.Hex)),
		Callsign:    strings.TrimSpace(a.Flight),
		ICAOType:    strings.TrimSpace(a.Type),
		Position:    geo.Point{Lat: *a.Lat, Lon: *a.Lon},
		AltitudeFt:  alt,
		GroundSpdKt: *a.GS,
		HeadingDeg:  *a.Track,
		VertRateFpm: *a.BaroRate,
	}, true
}

// parseAlt handles alt_baro being either a number or the string "ground".
func parseAlt(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		if s == "ground" {
			return 0, true
		}
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return f, true
}
