package awtrix

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lizthegrey/awtrix-flights/internal/adsbdb"
	"github.com/lizthegrey/awtrix-flights/internal/filter"
)

func TestNiceFlight(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"QFA75", "QF75"},     // mainline shortened
		{"QLK1944", "QF1944"}, // QantasLink wire callsign shortened to QF
		{"RXA123", "ZL123"},   // Rex: ICAO designator RXA (not radio "REX")
		{"REX123", "REX123"},  // radio callsign isn't an ADS-B designator; passes through
		{"CXA801", "MF801"},   // Xiamen, newly mapped
		{"XAX201", "D7201"},   // AirAsia X, IATA code with leading letter+digit
		{"CEB39", "5J39"},     // Cebu Pacific, numeric-leading IATA code
		{"POL28", "POL28"},    // PolAir: unmapped, renders raw (and never fires anyway)
		{"ZUD", "ZUD"},        // tail registration, no digits
	}
	for _, c := range cases {
		if got := niceFlight(c.in); got != c.want {
			t.Errorf("niceFlight(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name  string
		state filter.State
		route adsbdb.Route
		want  string // expected Text
	}{
		{
			name:  "qantas_widebody_to_vancouver",
			state: filter.State{Callsign: "QFA75", ICAOType: "B789"},
			route: adsbdb.Route{DestIATA: "YVR", DestICAO: "CYVR"},
			want:  "QF75 YVR 789",
		},
		{
			name:  "jetstar_narrowbody_to_perth",
			state: filter.State{Callsign: "JST601", ICAOType: "A21N"},
			route: adsbdb.Route{DestIATA: "PER", DestICAO: "YPPH"},
			want:  "JQ601 PER A21N",
		},
		{
			name:  "korean_air_to_seoul",
			state: filter.State{Callsign: "KAL402", ICAOType: "B77W"},
			route: adsbdb.Route{DestIATA: "ICN", DestICAO: "RKSI"},
			want:  "KE402 ICN B77W",
		},
		{
			name:  "unknown_callsign_prefix_passes_through",
			state: filter.State{Callsign: "XYZ99", ICAOType: "B738"},
			route: adsbdb.Route{DestIATA: "MEL"},
			want:  "XYZ99 MEL 738",
		},
		{
			name:  "no_route_data",
			state: filter.State{Callsign: "QFA75", ICAOType: "B789"},
			route: adsbdb.Route{},
			want:  "QF75 789",
		},
		{
			name:  "fallback_to_icao_dest",
			state: filter.State{Callsign: "ANZ110", ICAOType: "B789"},
			route: adsbdb.Route{DestICAO: "NZAA"},
			want:  "NZ110 NZAA 789",
		},
		{
			name:  "missing_callsign_uses_icao24",
			state: filter.State{ICAO24: "7c1abc", ICAOType: "B789"},
			route: adsbdb.Route{DestIATA: "MEL"},
			want:  "MEL 789",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Format(tc.state, tc.route, "")
			if diff := cmp.Diff(tc.want, got.Text); diff != "" {
				t.Errorf("Text mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEncode(t *testing.T) {
	p := Payload{Text: "QF75 YVR 789", Duration: 30, Color: "FFFFFF", PushIcon: 2}
	b, err := p.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Round-trip: decode to map and check fields are present.
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["text"] != "QF75 YVR 789" {
		t.Errorf("text = %v", got["text"])
	}
	if _, ok := got["icon"]; ok {
		t.Errorf("empty icon should be omitted from JSON, got %v", got["icon"])
	}
}
