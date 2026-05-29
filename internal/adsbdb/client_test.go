package adsbdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const qfa75Fixture = `{
  "response": {
    "flightroute": {
      "callsign": "QFA75",
      "callsign_icao": "QFA",
      "callsign_iata": "QF",
      "airline": {"name": "Qantas"},
      "origin": {
        "iata_code": "SYD",
        "icao_code": "YSSY",
        "name": "Sydney Kingsford Smith"
      },
      "destination": {
        "iata_code": "YVR",
        "icao_code": "CYVR",
        "name": "Vancouver International"
      }
    }
  }
}`

func TestLookupOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callsign/QFA75" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(qfa75Fixture))
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	got, err := c.Lookup(context.Background(), "QFA75")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := Route{
		Callsign:   "QFA75",
		OriginICAO: "YSSY",
		DestICAO:   "CYVR",
		OriginIATA: "SYD",
		DestIATA:   "YVR",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Lookup mismatch (-want +got):\n%s", diff)
	}
}

func TestLookupNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := New()
	c.BaseURL = srv.URL
	_, err := c.Lookup(context.Background(), "UNKNOWN1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

func TestLookupEmpty(t *testing.T) {
	c := New()
	if _, err := c.Lookup(context.Background(), ""); err == nil {
		t.Error("expected error for empty callsign")
	}
}
