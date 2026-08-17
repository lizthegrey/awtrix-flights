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

// nonAirlineCallsignPrefix lists 3-letter operator prefixes that pass
// airlineCallsign's airline shape (prefix + digits) but belong to non-commercial
// operators we never want to fire on: police, medical, survey, and similar
// government/utility flights. These loiter and orbit the area, so the constant-
// heading CPA projection false-positives every time the circle re-crosses the
// overhead window — the same failure mode as meandering GA, but with an airline-
// format callsign the airlineCallsign gate can't catch. Dedupe doesn't help
// either: an orbit outlasts the 10-min dedupe TTL, so it re-fires once the TTL
// lapses. They're also typically filtered off consumer trackers (FR24/FlightAware
// LADD/PIA), so they appear on our unfiltered ADS-B feed but "show nothing" there.
// Curated, not inferred — add a prefix only after confirming the operator.
var nonAirlineCallsignPrefix = map[string]bool{
	"POL": true, // NSW Police "PolAir" surveillance (orbiting Cessna 208 etc.)
}

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

// qantasLinkSuffixForm matches QantasLink's trailing-letter callsign form
// "QLK#L" / "QLK##L" / "QLK###L" (1-3 digits + one letter, e.g. "QLK431D" or
// "QLK28D"). This is a TRUNCATED flight number: the transponder drops the
// leading digit(s) of the real 4-digit QF number, so from the wire callsign
// alone we cannot tell whether "431D" means QF1431, QF2431, QF9431, etc.
//
// A prior version of this code guessed the dropped digit was always "2"
// (zero-padding the remainder into a "QFA2###" block). That guess looked
// safe because it round-tripped one real example (QLK205D → QFA2205, SYD-ABX)
// without a 404 — but "doesn't 404" is not "is correct". Investigating a live
// report of QLK431D showing an obviously-wrong LHR-adjacent destination on a
// DH8D turboprop found the real flight was QF1431 (SYD-CBR, block "1"), not
// the guessed QF2431 (which doesn't exist). Spot-checking further confirmed
// the guess is unsound in general, not just wrong for this one case: querying
// adsbdb for the SAME 1-3 digit remainder under blocks 1, 2, and 3 each
// returns a real, live, entirely unrelated Qantas(-group) flight —
// e.g. "128" resolves under block nothing/"1" to QF128 (HKG-SYD widebody),
// under "2" to QF2028 (SYD-DBO turboprop), under "3" to QF3028 (a US
// codeshare, MSP-DFW). Qantas's flight-number space is dense enough that
// almost any guessed leading digit lands on SOME real flight, just not the
// right one — so guessing is worse than useless, it's confidently wrong.
//
// There is no way to recover the dropped digit(s) from the wire callsign
// alone, and the two blocks confirmed to be real QantasLink number ranges
// (see qantasLinkSuffixBlocks) both often have some flight filed under the
// same 1-3 digit remainder — so we can't just always guess one block either.
// Instead resolveQantasLinkSuffixRoute queries BOTH candidate blocks and
// only trusts the result if EXACTLY ONE resolves: two hits means we can't
// tell which is real (ambiguous, e.g. the "205" remainder: QFA1205 is a real
// CBR-MEL flight AND QFA2205 is a real SYD-ABX flight — picking either would
// sometimes be confidently wrong), so we fall back to "no destination"
// rather than guess. This is distinct from the 4-digit QLK#### form (e.g.
// QLK1944 → QFA1944, no digits dropped, no guessing involved) and the
// 4-digit-plus-suffix form (e.g. QLK1234A → QFA1234A), both handled safely
// by the generic routeCallsignAlias prefix swap below.
var qantasLinkSuffixForm = regexp.MustCompile(`^QLK(\d{1,3})[A-Z]$`)

// qantasLinkSuffixBlocks lists the QantasLink number blocks worth guessing
// for the truncated trailing-letter form. Only "1" and "2" have a real
// confirmed-QantasLink example behind them (QF1431 SYD-CBR; QF2028 SYD-DBO).
// Block "3" was checked and found to be US codeshare traffic (e.g. QF3028
// MSP-DFW) — genuinely a different, unrelated part of Qantas's number space,
// not QantasLink — so it's deliberately excluded rather than added; don't
// extend this list without an equally concrete positive example.
var qantasLinkSuffixBlocks = []string{"2", "1"}

// routeCallsign returns the callsign to query adsbdb with for operators
// aliased in routeCallsignAlias. QantasLink's truncated trailing-letter form
// is handled separately by resolveQantasLinkSuffixRoute, not here. Input
// must already match airlineCallsign (3 letters + digits + optional suffix).
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

		// Some non-airline operators (police, medical, survey) file airline-format
		// callsigns that clear airlineCallsign above. They loiter/orbit, so the
		// constant-heading projection keeps re-firing them. Drop by operator
		// prefix (see nonAirlineCallsignPrefix).
		if nonAirlineCallsignPrefix[v.Callsign[:3]] {
			log.Info("suppressing: non-airline operator", "callsign", v.Callsign)
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
	// QantasLink's truncated trailing-letter form needs multi-candidate
	// disambiguation (see qantasLinkSuffixForm), not a plain prefix rewrite.
	if qantasLinkSuffixForm.MatchString(callsign) {
		return p.resolveQantasLinkSuffixRoute(ctx, callsign)
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

// resolveQantasLinkSuffixRoute tries both candidate number blocks for a
// truncated QantasLink callsign (see qantasLinkSuffixForm/qantasLinkSuffixBlocks)
// and only trusts the result if exactly one block resolves to a route —
// two hits means the true block is ambiguous, so it returns ErrNotFound
// rather than guess. Cached (and cache-checked) under the original wire
// callsign, since the resolved candidate varies per lookup.
func (p *Publisher) resolveQantasLinkSuffixRoute(ctx context.Context, callsign string) (adsbdb.Route, error) {
	if r, err := p.Cache.GetRoute(ctx, callsign); err == nil {
		return r, nil
	}
	m := qantasLinkSuffixForm.FindStringSubmatch(callsign)
	padded := fmt.Sprintf("%03s", m[1])

	var found adsbdb.Route
	hits := 0
	for _, block := range qantasLinkSuffixBlocks {
		r, err := p.Routes.Lookup(ctx, "QFA"+block+padded)
		if err != nil {
			continue
		}
		found, hits = r, hits+1
	}
	if hits != 1 {
		return adsbdb.Route{}, adsbdb.ErrNotFound
	}
	if err := p.Cache.PutRoute(ctx, callsign, found, p.Cfg.RouteCacheTTL); err != nil {
		p.logger().Warn("route cache put failed", "err", err)
	}
	return found, nil
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
