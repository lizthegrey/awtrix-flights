// Package publisher orchestrates one polling tick: fetch state vectors,
// filter for overhead candidates, look up routes (cached), dedupe, format,
// and publish to MQTT.
package publisher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/lizthegrey/awtrix-flights/internal/adsbdb"
	"github.com/lizthegrey/awtrix-flights/internal/awtrix"
	"github.com/lizthegrey/awtrix-flights/internal/filter"
	"github.com/lizthegrey/awtrix-flights/internal/geo"
)

// airlineCallsign matches a typical ICAO airline callsign: 3-letter operator
// designator + 1-4 digits + up to two trailing letters (alphanumeric ATC
// suffix, e.g. Emirates "UAE3HJ"). Tail registrations (e.g. "VHZUD" after
// hyphen stripping) don't match because they have no digit, so we skip their
// adsbdb route lookup.
var airlineCallsign = regexp.MustCompile(`^[A-Z]{3}\d{1,4}[A-Z]{0,2}$`)

// routeCallsignAlias maps an operator's on-the-wire ICAO designator to the
// carrier its routes are actually FILED under in adsbdb's route table. adsbdb
// does a literal callsign-string lookup with no operator/alias translation, so
// a flight that squawks one code but is scheduled under another 404s on origin
// lookup and gets dropped by the "require a route at all" GA suppression.
//
// QantasLink (Eastern Australia / National Jet, ICAO "QLK") flies — including
// its A220s — under callsigns squawked as "QLK####", but Qantas group files
// those routes under mainline "QFA####" with the same flight number. So we
// rewrite the prefix before looking up the route. The display still uses the
// original wire callsign (see internal/awtrix icaoToIATA "QLK"→"QF").
var routeCallsignAlias = map[string]string{
	"QLK": "QFA", // QantasLink → Qantas mainline route filing
}

// routeCallsign returns the callsign to query adsbdb with, rewriting the
// 3-letter operator prefix per routeCallsignAlias. Input must already match
// airlineCallsign (3 letters + digits + optional suffix).
func routeCallsign(callsign string) string {
	if alias, ok := routeCallsignAlias[callsign[:3]]; ok {
		return alias + callsign[3:]
	}
	return callsign
}

// AircraftSource returns current ADS-B state vectors near a point.
type AircraftSource interface {
	Search(ctx context.Context, center geo.Point, distNM int) ([]filter.State, error)
}

// RouteLookup resolves a callsign to origin/destination airports.
type RouteLookup interface {
	Lookup(ctx context.Context, callsign string) (adsbdb.Route, error)
}

// RouteCache caches route lookups (DDB-backed in prod).
type RouteCache interface {
	GetRoute(ctx context.Context, callsign string) (adsbdb.Route, error)
	PutRoute(ctx context.Context, callsign string, r adsbdb.Route, ttl time.Duration) error
}

// Dedupe suppresses repeated notifications for the same callsign.
type Dedupe interface {
	RecentlyFired(ctx context.Context, callsign string) bool
	MarkFired(ctx context.Context, callsign string, ttl time.Duration) error
}

// MQTTPublisher sends AWTRIX-formatted payloads to a topic.
type MQTTPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// Config tunes the publisher.
type Config struct {
	SearchRadiusNM int
	Filter         filter.Config
	DedupeTTL      time.Duration // how long to suppress repeats for one callsign
	RouteCacheTTL  time.Duration // route validity
	MQTTTopic      string        // e.g. "awtrix_xxxxxx/custom/overhead"
	IconID         string        // optional AWTRIX icon ID
}

// Default returns sensible defaults, centered on observer.
func Default(mqttTopic string, observer geo.Point) Config {
	return Config{
		SearchRadiusNM: 8,
		Filter:         filter.Default(observer),
		DedupeTTL:      10 * time.Minute,
		RouteCacheTTL:  24 * time.Hour,
		MQTTTopic:      mqttTopic,
	}
}

// Publisher runs one tick of the polling loop.
type Publisher struct {
	Source AircraftSource
	Routes RouteLookup
	Cache  RouteCache
	Dedupe Dedupe
	MQTT   MQTTPublisher
	Cfg    Config
	Log    *slog.Logger
}

// Result reports what happened this tick (for logs / unit assertions).
type Result struct {
	Scanned    int      // number of state vectors received
	Candidates int      // number of state vectors that matched the filter
	Published  []string // callsigns we published this tick
	Suppressed []string // matched but suppressed by dedupe
}

// Tick performs one polling cycle.
func (p *Publisher) Tick(ctx context.Context) (Result, error) {
	log := p.logger()
	res := Result{}

	vectors, err := p.Source.Search(ctx, p.Cfg.Filter.Observer, p.Cfg.SearchRadiusNM)
	if err != nil {
		return res, fmt.Errorf("aircraft search: %w", err)
	}
	res.Scanned = len(vectors)
	log.Debug("fetched state vectors", "count", len(vectors))

	for _, v := range vectors {
		m := p.Cfg.Filter.Evaluate(v)
		if !m.OK {
			continue
		}
		res.Candidates++
		log.Info("overhead candidate",
			"icao24", v.ICAO24,
			"callsign", v.Callsign,
			"type", v.ICAOType,
			"alt_ft", v.AltitudeFt,
			"alt_at_cpa_ft", m.AltitudeAtCPAFt,
			"cross_track_nm", m.CrossTrackNM,
			"seconds_to_cpa", m.SecondsToCPA,
		)

		key := dedupeKey(v)
		if p.Dedupe.RecentlyFired(ctx, key) {
			res.Suppressed = append(res.Suppressed, key)
			continue
		}

		// Firing authority is the overhead geometry (matched above) plus a
		// "real flight" gate: the contact must carry an airline-style callsign.
		// We no longer require the route to start/end at SYD — any airliner that
		// flies the overhead corridor is shown, departure or transit. This drops
		// the old origin gate and the climb-out override entirely; the geometry
		// is the right level of abstraction (see filter-criterion notes).
		//
		// The callsign gate is what suppresses meandering GA (whose constant-
		// heading projection can false-positive the geometry) and tail
		// registrations — they have no airline callsign. Descending arrivals
		// maneuvering over the observer (e.g. JQ224 onto 16R) are excluded
		// upstream by the filter's IgnoreDescending check.
		if !airlineCallsign.MatchString(v.Callsign) {
			log.Info("suppressing: not an airline callsign", "callsign", v.Callsign)
			res.Suppressed = append(res.Suppressed, key)
			continue
		}

		// Route lookup is best-effort enrichment for the display (destination).
		// adsbdb's community route table has gaps (e.g. CXA802 SYD-XMN) and keys
		// routes on a callsign's primary leg, so a miss or a wrong origin no
		// longer blocks firing — we just publish without a destination.
		route, err := p.resolveRoute(ctx, v.Callsign)
		if err != nil {
			log.Info("route unknown; publishing without destination",
				"callsign", v.Callsign, "err", err)
			route = adsbdb.Route{}
		}

		payload := awtrix.Format(v, route, p.Cfg.IconID)
		// Keep the slot in the rotation until the aircraft is well past
		// overhead. The filter can fire up to 4 min before CPA; with a
		// fixed 90 s lifetime the display would clear long before the
		// plane actually arrives. 45 s of grace covers "what was that?!"
		// glance time after the overflight.
		lifetime := int(m.SecondsToCPA) + 45
		if lifetime < 60 {
			lifetime = 60
		}
		payload.Lifetime = lifetime
		body, err := payload.Encode()
		if err != nil {
			log.Error("encode payload", "err", err)
			continue
		}
		if err := p.MQTT.Publish(ctx, p.Cfg.MQTTTopic, body); err != nil {
			log.Error("mqtt publish failed", "err", err)
			continue
		}
		if err := p.Dedupe.MarkFired(ctx, key, p.Cfg.DedupeTTL); err != nil {
			log.Warn("dedupe mark failed", "err", err)
		}
		res.Published = append(res.Published, key)
		log.Info("published overhead", "key", key, "payload", payload.String())
	}
	return res, nil
}

// resolveRoute checks the cache first, falling back to live lookup. The caller
// gates on airlineCallsign before calling, so this assumes an airline callsign.
// Caches successful lookups; does not cache misses. Errors (including adsbdb
// 404s) are surfaced to the caller, which treats them as "no destination" and
// publishes anyway — route data is display-only, not a firing precondition.
func (p *Publisher) resolveRoute(ctx context.Context, callsign string) (adsbdb.Route, error) {
	if callsign == "" {
		return adsbdb.Route{}, errors.New("empty callsign")
	}
	// Rewrite operator prefixes that file routes under a different carrier
	// (e.g. QantasLink QLK→QFA) before the cache key and live lookup, so both
	// the squawked and filed forms resolve to the same route entry.
	callsign = routeCallsign(callsign)
	if r, err := p.Cache.GetRoute(ctx, callsign); err == nil {
		return r, nil
	}
	r, err := p.Routes.Lookup(ctx, callsign)
	if err != nil {
		return adsbdb.Route{}, err
	}
	if err := p.Cache.PutRoute(ctx, callsign, r, p.Cfg.RouteCacheTTL); err != nil {
		p.logger().Warn("route cache put failed", "err", err)
	}
	return r, nil
}

func dedupeKey(s filter.State) string {
	if s.Callsign != "" {
		return s.Callsign
	}
	return "icao:" + s.ICAO24
}

func (p *Publisher) logger() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}
