// Package adsbdb is a thin client for adsbdb.com's free callsign→route API.
//
//	https://www.adsbdb.com/
//	GET https://api.adsbdb.com/v0/callsign/{callsign}
//
// We only need origin and destination airport ICAO codes.
package adsbdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const defaultBaseURL = "https://api.adsbdb.com/v0"

// ErrNotFound is returned when adsbdb has no record for the callsign.
var ErrNotFound = errors.New("adsbdb: callsign not found")

// Route is the minimal route info we extract.
type Route struct {
	Callsign   string
	OriginICAO string
	DestICAO   string
	OriginIATA string
	DestIATA   string
}

// Client queries adsbdb.com.
type Client struct {
	HTTP      *http.Client
	BaseURL   string
	UserAgent string
}

// New returns a Client with sensible defaults. The HTTP client is OTel-wrapped.
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

type rawResp struct {
	Response struct {
		FlightRoute struct {
			Callsign    string  `json:"callsign"`
			Origin      airport `json:"origin"`
			Destination airport `json:"destination"`
		} `json:"flightroute"`
	} `json:"response"`
}

type airport struct {
	ICAOCode string `json:"icao_code"`
	IATACode string `json:"iata_code"`
}

// Lookup returns the route for the given callsign (e.g. "QFA75").
// Returns ErrNotFound for unknown callsigns.
func (c *Client) Lookup(ctx context.Context, callsign string) (Route, error) {
	if callsign == "" {
		return Route{}, errors.New("adsbdb: empty callsign")
	}
	url := fmt.Sprintf("%s/callsign/%s", c.BaseURL, callsign)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Route{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Route{}, fmt.Errorf("adsbdb GET: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return Route{}, ErrNotFound
	default:
		return Route{}, fmt.Errorf("adsbdb status %d", resp.StatusCode)
	}

	var data rawResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return Route{}, fmt.Errorf("decode adsbdb response: %w", err)
	}
	fr := data.Response.FlightRoute
	if fr.Destination.ICAOCode == "" {
		return Route{}, ErrNotFound
	}
	return Route{
		Callsign:   fr.Callsign,
		OriginICAO: fr.Origin.ICAOCode,
		DestICAO:   fr.Destination.ICAOCode,
		OriginIATA: fr.Origin.IATACode,
		DestIATA:   fr.Destination.IATACode,
	}, nil
}
