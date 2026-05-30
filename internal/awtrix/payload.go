// Package awtrix builds custom-app payloads for AWTRIX3 devices.
//
// See https://blueforcer.github.io/awtrix3/#/api for the schema. We use the
// MQTT custom-app channel: publishing to `<prefix>/custom/<appname>` with a
// JSON body creates/updates an app slot on the device.
package awtrix

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lizthegrey/awtrix-flights/internal/adsbdb"
	"github.com/lizthegrey/awtrix-flights/internal/filter"
)

// Payload is the AWTRIX custom-app JSON. Only the fields we use are modeled.
//
// Important: `duration` controls how long the app is rendered per rotation
// cycle, NOT how long the app stays in the rotation. To auto-remove a custom
// app after a period of no updates use `lifetime` (seconds since last push).
// Otherwise the slot persists forever and the device rotates back to it.
type Payload struct {
	Text     string `json:"text"`
	Icon     string `json:"icon,omitempty"`
	Duration int    `json:"duration,omitempty"` // seconds the app is shown per rotation
	Lifetime int    `json:"lifetime,omitempty"` // auto-remove app if no updates for N seconds
	Color    string `json:"color,omitempty"`    // hex RGB, e.g. "FFFFFF"
	Rainbow  bool   `json:"rainbow,omitempty"`
	Stack    bool   `json:"stack,omitempty"`    // append to existing rotation instead of replacing
	PushIcon int    `json:"pushIcon,omitempty"` // 0=stay, 1=push with text, 2=loop
}

// Format builds the display payload for an overhead aircraft.
//
// Display format: "<flight> → <dest> <type>", e.g. "QF75 → YVR 789".
// Falls back gracefully when callsign/route/type info is missing.
func Format(state filter.State, route adsbdb.Route, iconID string) Payload {
	flight := niceFlight(state.Callsign)
	dest := bestDest(route)
	typeShort := shortType(state.ICAOType)

	// 32 px is tight; every char shaves scroll time. Space-separated only,
	// no arrows or other decoration.
	var parts []string
	if flight != "" {
		parts = append(parts, flight)
	}
	if dest != "" {
		parts = append(parts, dest)
	}
	if typeShort != "" {
		parts = append(parts, typeShort)
	}
	text := strings.Join(parts, " ")
	if text == "" {
		text = state.ICAO24
	}

	return Payload{
		Text:     text,
		Icon:     iconID,
		Duration: 10, // shown 10 s per rotation cycle
		Lifetime: 90, // and auto-removed if no fresh push within 90 s
		Color:    "FFFFFF",
		PushIcon: 2,
	}
}

// MarshalJSON for convenience when publishing.
func (p Payload) Encode() ([]byte, error) {
	return json.Marshal(p)
}

// niceFlight collapses ICAO airline prefixes (QFA) to IATA-like (QF) where
// the trailing digits make it unambiguous. "QFA75" → "QF75", "VOZ123" → "VA123".
// Unknown prefixes pass through unchanged.
func niceFlight(callsign string) string {
	cs := strings.TrimSpace(callsign)
	if cs == "" {
		return ""
	}
	// Find boundary between letters and digits.
	i := 0
	for i < len(cs) && cs[i] >= 'A' && cs[i] <= 'Z' {
		i++
	}
	prefix, suffix := cs[:i], cs[i:]
	if suffix == "" {
		return cs
	}
	if iata, ok := icaoToIATA[prefix]; ok {
		return iata + suffix
	}
	return cs
}

// bestDest prefers IATA (3-letter, more recognizable on a tiny display)
// then falls back to ICAO (4-letter).
func bestDest(r adsbdb.Route) string {
	if r.DestIATA != "" {
		return r.DestIATA
	}
	return r.DestICAO
}

// shortType strips a leading 'A' or 'B' manufacturer letter only when the
// remaining suffix is all digits: "B789" → "789", "A388" → "388". For mixed
// suffixes ("A21N", "A35K", "B77W", "B38M"), the letter is information-bearing
// (neo, F, ER variants) so the full type is kept.
func shortType(t string) string {
	t = strings.TrimSpace(t)
	if len(t) != 4 {
		return t
	}
	switch t[0] {
	case 'A', 'B':
		for i := 1; i < 4; i++ {
			if t[i] < '0' || t[i] > '9' {
				return t
			}
		}
		return t[1:]
	}
	return t
}

// icaoToIATA covers airlines we'd realistically see climbing off YSSY 34L.
// Not exhaustive — anything missing just renders with its ICAO prefix.
var icaoToIATA = map[string]string{
	"QFA": "QF", // Qantas
	"QLK": "QF", // QantasLink (squawks QLK; routes filed under QFA)
	"QJE": "QF", // Qantas Jetconnect (NZ subsidiary)
	"JST": "JQ", // Jetstar
	"VOZ": "VA", // Virgin Australia
	"VBB": "BL", // Bonza
	"RXA": "ZL", // Regional Express (ICAO RXA; radio callsign is "REX")
	"ANZ": "NZ", // Air New Zealand
	"KAL": "KE", // Korean Air
	"UAE": "EK", // Emirates
	"QTR": "QR", // Qatar
	"SIA": "SQ", // Singapore
	"CPA": "CX", // Cathay
	"AAL": "AA", // American
	"UAL": "UA", // United
	"DAL": "DL", // Delta
	"ACA": "AC", // Air Canada
	"BAW": "BA", // British Airways
	"AFR": "AF", // Air France
	"KLM": "KL", // KLM
	"DLH": "LH", // Lufthansa
	"ANA": "NH", // ANA
	"JAL": "JL", // JAL
	"CES": "MU", // China Eastern
	"CSN": "CZ", // China Southern
	"CCA": "CA", // Air China
	"PAL": "PR", // Philippine
	"MAS": "MH", // Malaysia
	"THA": "TG", // Thai
	"AIC": "AI", // Air India
	"FJI": "FJ", // Fiji Airways
	"ACI": "QN", // Air Calin / Aircalin (callsign AIRCALIN)
	// Added from observed SYD traffic (24h sample, May 2026):
	"CXA": "MF", // Xiamen Air
	"AAR": "OZ", // Asiana
	"DKH": "HO", // Juneyao Airlines
	"ANG": "PX", // Air Niugini
	"CEB": "5J", // Cebu Pacific
	"GIA": "GA", // Garuda Indonesia
	"HVN": "VN", // Vietnam Airlines
	"LAN": "LA", // LATAM
	"MXD": "OD", // Batik Air Malaysia
	"TWB": "TW", // T'way Air
	"VJC": "VJ", // VietJet Air
	"XAX": "D7", // AirAsia X
}

// String returns a one-line description, useful for logs.
func (p Payload) String() string {
	return fmt.Sprintf("text=%q icon=%s duration=%ds", p.Text, p.Icon, p.Duration)
}
