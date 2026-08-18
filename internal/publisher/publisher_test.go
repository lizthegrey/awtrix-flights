package publisher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lizthegrey/awtrix-flights/internal/adsbdb"
	"github.com/lizthegrey/awtrix-flights/internal/filter"
	"github.com/lizthegrey/awtrix-flights/internal/geo"
)

type fakeSource struct{ vectors []filter.State }

func (f *fakeSource) Search(ctx context.Context, _ geo.Point, _ int) ([]filter.State, error) {
	return f.vectors, nil
}

type fakeRoutes struct {
	routes map[string]adsbdb.Route
	calls  int
}

func (f *fakeRoutes) Lookup(ctx context.Context, callsign string) (adsbdb.Route, error) {
	f.calls++
	r, ok := f.routes[callsign]
	if !ok {
		return adsbdb.Route{}, adsbdb.ErrNotFound
	}
	return r, nil
}

type fakeCache struct {
	routes map[string]adsbdb.Route
	puts   int
}

func newCache() *fakeCache { return &fakeCache{routes: map[string]adsbdb.Route{}} }

func (f *fakeCache) GetRoute(ctx context.Context, cs string) (adsbdb.Route, error) {
	r, ok := f.routes[cs]
	if !ok {
		return adsbdb.Route{}, errors.New("miss")
	}
	return r, nil
}

func (f *fakeCache) PutRoute(ctx context.Context, cs string, r adsbdb.Route, ttl time.Duration) error {
	f.routes[cs] = r
	f.puts++
	return nil
}

type fakeDedupe struct {
	fired map[string]bool
}

func newDedupe() *fakeDedupe { return &fakeDedupe{fired: map[string]bool{}} }

func (f *fakeDedupe) RecentlyFired(ctx context.Context, cs string) bool { return f.fired[cs] }
func (f *fakeDedupe) MarkFired(ctx context.Context, cs string, _ time.Duration) error {
	f.fired[cs] = true
	return nil
}

type fakeMQTT struct {
	pubs []pub
}

type pub struct {
	topic   string
	payload []byte
}

func (f *fakeMQTT) Publish(ctx context.Context, topic string, body []byte) error {
	f.pubs = append(f.pubs, pub{topic, body})
	return nil
}

// Synthetic geometry: observer at (0, 0), aircraft 3 nm south heading due
// north — same setup as filter_test.go.
var testObserver = geo.Point{Lat: 0, Lon: 0}

func matching() filter.State {
	return filter.State{
		ICAO24:      "7c1abc",
		Callsign:    "QFA75",
		ICAOType:    "B789",
		Position:    geo.Point{Lat: -0.05, Lon: 0},
		AltitudeFt:  2500,
		GroundSpdKt: 220,
		HeadingDeg:  0,
		VertRateFpm: 2400,
	}
}

func nonMatching() filter.State {
	s := matching()
	s.ICAO24 = "abcdef"
	s.Callsign = "BORING1"
	s.HeadingDeg = 180 // heading away from observer
	return s
}

func newPublisher(src *fakeSource, routes *fakeRoutes, cache *fakeCache, dedupe *fakeDedupe, mqtt *fakeMQTT) *Publisher {
	return &Publisher{
		Source: src,
		Routes: routes,
		Cache:  cache,
		Dedupe: dedupe,
		MQTT:   mqtt,
		Cfg:    Default("test/topic", testObserver),
	}
}

func TestTick_PublishesAndDedupes(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{vectors: []filter.State{matching(), nonMatching()}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"QFA75": {OriginIATA: "SYD", OriginICAO: "YSSY", DestIATA: "YVR", DestICAO: "CYVR"},
	}}
	cache := newCache()
	dedupe := newDedupe()
	mqtt := &fakeMQTT{}
	p := newPublisher(src, routes, cache, dedupe, mqtt)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Scanned != 2 || res.Candidates != 1 || len(res.Published) != 1 {
		t.Errorf("unexpected result: %+v", res)
	}
	if len(mqtt.pubs) != 1 {
		t.Fatalf("expected 1 mqtt publish, got %d", len(mqtt.pubs))
	}
	if mqtt.pubs[0].topic != "test/topic" {
		t.Errorf("topic = %q", mqtt.pubs[0].topic)
	}
	if got := string(mqtt.pubs[0].payload); !contains(got, `"QF75 YVR 789"`) {
		t.Errorf("payload missing expected text: %s", got)
	}
	if !dedupe.fired["QFA75"] {
		t.Errorf("dedupe should be marked")
	}

	// Second tick with same data: should be suppressed.
	mqtt.pubs = nil
	res2, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	if len(res2.Published) != 0 || len(res2.Suppressed) != 1 {
		t.Errorf("expected suppression: %+v", res2)
	}
	if len(mqtt.pubs) != 0 {
		t.Errorf("no new publish should occur, got %d", len(mqtt.pubs))
	}
}

func TestRouteCallsign(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"QLK1944", "QFA1944"},   // QantasLink 4-digit → Qantas mainline filing, no digit dropped
		{"QLK1234A", "QFA1234A"}, // 4-digit + suffix: plain swap, suffix preserved, no digit dropped
		{"QLK9999X", "QFA9999X"}, // 4 digits + letter: not the truncated form, plain swap
		{"QFA75", "QFA75"},       // already mainline, unchanged
		{"VOZ123", "VOZ123"},     // unrelated operator, unchanged
		{"UAE3HJ", "UAE3HJ"},     // alphanumeric-suffix callsign, unchanged
	}
	for _, c := range cases {
		if got := routeCallsign(c.in); got != c.want {
			t.Errorf("routeCallsign(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The truncated QantasLink trailing-letter form is handled by
// resolveQantasLinkSuffixRoute (via resolveRoute), not routeCallsign — it
// needs both candidate blocks queried to detect ambiguity, which a pure
// string-rewrite function can't do.
func TestResolveRoute_QantasLinkSuffixForm(t *testing.T) {
	cases := []struct {
		name     string
		callsign string
		routes   map[string]adsbdb.Route
		wantErr  bool
		wantDest string
	}{
		{
			name:     "single block hit resolves",
			callsign: "QLK431D",
			routes: map[string]adsbdb.Route{
				"QFA1431": {OriginICAO: "YSSY", DestIATA: "CBR"}, // QFA2431 deliberately absent: unambiguous
			},
			wantDest: "CBR",
		},
		{
			name:     "SYD-origin candidate wins over one that doesn't touch YSSY",
			callsign: "QLK205D",
			routes: map[string]adsbdb.Route{
				// real CBR-MEL flight; neither endpoint is YSSY, geographically
				// implausible for this geometry (CBR-MEL has no reason to detour
				// through Sydney)
				"QFA1205": {OriginICAO: "YSCB", DestIATA: "MEL", DestICAO: "YMML"},
				// real SYD-ABX flight; origin YSSY, the plausible one
				"QFA2205": {OriginICAO: "YSSY", DestIATA: "ABX", DestICAO: "YMAY"},
			},
			wantDest: "ABX",
		},
		{
			name:     "both candidates touch YSSY: still a tie, not resolved",
			callsign: "QLK444D",
			routes: map[string]adsbdb.Route{
				"QFA1444": {OriginICAO: "YSSY", DestIATA: "CBR"},
				"QFA2444": {OriginICAO: "YSSY", DestIATA: "DBO"},
			},
			wantErr: true,
		},
		{
			name:     "neither candidate touches YSSY: still a tie, not resolved",
			callsign: "QLK555D",
			routes: map[string]adsbdb.Route{
				"QFA1555": {OriginICAO: "YSCB", DestIATA: "MEL"},
				"QFA2555": {OriginICAO: "YBBN", DestIATA: "GLT"},
			},
			wantErr: true,
		},
		{
			name:     "neither block hits, not resolved",
			callsign: "QLK9D",
			routes:   map[string]adsbdb.Route{},
			wantErr:  true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			routes := &fakeRoutes{routes: c.routes}
			p := &Publisher{Routes: routes, Cache: newCache(), Cfg: Default("test/topic", testObserver)}
			r, err := p.resolveRoute(context.Background(), c.callsign)
			if c.wantErr {
				if err == nil {
					t.Fatalf("resolveRoute(%q) = %+v, want error", c.callsign, r)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRoute(%q): %v", c.callsign, err)
			}
			if r.DestIATA != c.wantDest {
				t.Errorf("resolveRoute(%q).DestIATA = %q, want %q", c.callsign, r.DestIATA, c.wantDest)
			}
		})
	}
}

// A QantasLink flight squawks "QLK####" but adsbdb only has the route filed
// under mainline "QFA####"; the lookup must rewrite the prefix so the SYD
// departure resolves instead of 404ing and being suppressed.
func TestTick_QantasLinkResolvesViaQFA(t *testing.T) {
	ctx := context.Background()
	s := matching()
	s.Callsign = "QLK1944"
	s.ICAOType = "A21N"
	src := &fakeSource{vectors: []filter.State{s}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"QFA1944": {OriginIATA: "SYD", OriginICAO: "YSSY", DestIATA: "CBR", DestICAO: "YSCB"},
	}}
	cache := newCache()
	mqtt := &fakeMQTT{}
	p := newPublisher(src, routes, cache, newDedupe(), mqtt)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(res.Published) != 1 {
		t.Fatalf("expected QantasLink departure to fire, got %+v", res)
	}
	if got := string(mqtt.pubs[0].payload); !contains(got, `"QF1944 CBR A21N"`) {
		t.Errorf("payload missing expected text: %s", got)
	}
	// Cache should be keyed under the rewritten (filed) callsign.
	if _, ok := cache.routes["QFA1944"]; !ok {
		t.Errorf("expected route cached under QFA1944, got keys %v", cache.routes)
	}
}

func TestTick_RouteCacheHit(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{vectors: []filter.State{matching()}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"QFA75": {OriginIATA: "SYD", DestIATA: "YVR", DestICAO: "CYVR"},
	}}
	cache := newCache()
	cache.routes["QFA75"] = adsbdb.Route{OriginIATA: "SYD", DestIATA: "CACHED"}
	dedupe := newDedupe()
	mqtt := &fakeMQTT{}
	p := newPublisher(src, routes, cache, dedupe, mqtt)

	if _, err := p.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if routes.calls != 0 {
		t.Errorf("expected cache hit, but live Lookup called %d times", routes.calls)
	}
	if got := string(mqtt.pubs[0].payload); !contains(got, "CACHED") {
		t.Errorf("expected cached dest in payload: %s", got)
	}
}

// A route 404 (adsbdb gap, e.g. CXA802) no longer suppresses: route data is
// display-only, so we publish the overhead flight without a destination.
func TestTick_RouteLookupFailsStillPublishes(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{vectors: []filter.State{matching()}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{}} // empty → not found
	p := newPublisher(src, routes, newCache(), newDedupe(), &fakeMQTT{})
	mqtt := p.MQTT.(*fakeMQTT)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if routes.calls != 1 {
		t.Errorf("expected one (failed) adsbdb lookup, got %d", routes.calls)
	}
	if len(res.Published) != 1 {
		t.Fatalf("expected publish despite route miss, got %+v", res)
	}
	// Flight + type render; no destination segment.
	if got := string(mqtt.pubs[0].payload); !contains(got, `"QF75 789"`) {
		t.Errorf("expected destination-less payload, got: %s", got)
	}
}

// Non-SYD origin (transit overflight or an adsbdb-mislabeled departure) now
// fires on geometry alone — the old origin gate is gone.
func TestTick_NonSYDOriginTransitPublishes(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{vectors: []filter.State{matching()}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"QFA75": {OriginIATA: "MEL", OriginICAO: "YMML", DestIATA: "BNE", DestICAO: "YBBN"},
	}}
	p := newPublisher(src, routes, newCache(), newDedupe(), &fakeMQTT{})
	mqtt := p.MQTT.(*fakeMQTT)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(res.Published) != 1 {
		t.Fatalf("expected non-SYD overflight to publish, got %+v", res)
	}
	if got := string(mqtt.pubs[0].payload); !contains(got, "BNE") {
		t.Errorf("expected dest BNE in payload, got: %s", got)
	}
}

// Tail registrations (e.g. ZUD from "VHZUD") fail the airline-callsign gate, so
// they're suppressed before any adsbdb call — this is what keeps GA out now
// that origin is no longer required.
func TestTick_TailRegistrationSuppressed(t *testing.T) {
	ctx := context.Background()
	s := matching()
	s.Callsign = "ZUD" // tail without VH- prefix that ADS-B sometimes reports
	src := &fakeSource{vectors: []filter.State{s}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{}}
	p := newPublisher(src, routes, newCache(), newDedupe(), &fakeMQTT{})
	mqtt := p.MQTT.(*fakeMQTT)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if routes.calls != 0 {
		t.Errorf("expected zero adsbdb calls for tail, got %d", routes.calls)
	}
	if len(mqtt.pubs) != 0 {
		t.Errorf("expected no publish for tail-only callsign, got %d", len(mqtt.pubs))
	}
	if len(res.Suppressed) != 1 {
		t.Errorf("expected suppression, got %+v", res.Suppressed)
	}
}

// Non-airline operators (e.g. NSW Police "POL28", a Cessna 208) file airline-
// format callsigns that clear the airlineCallsign gate, then re-fire endlessly
// because they orbit. nonAirlineCallsignPrefix must suppress them by operator
// prefix, before any adsbdb route lookup.
func TestTick_NonAirlineOperatorSuppressed(t *testing.T) {
	ctx := context.Background()
	s := matching()
	s.Callsign = "POL28" // NSW Police PolAir, airline-format but not an airline
	src := &fakeSource{vectors: []filter.State{s}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{}}
	p := newPublisher(src, routes, newCache(), newDedupe(), &fakeMQTT{})
	mqtt := p.MQTT.(*fakeMQTT)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if routes.calls != 0 {
		t.Errorf("expected zero adsbdb calls for non-airline operator, got %d", routes.calls)
	}
	if len(mqtt.pubs) != 0 {
		t.Errorf("expected no publish for non-airline operator, got %d", len(mqtt.pubs))
	}
	if len(res.Suppressed) != 1 {
		t.Errorf("expected suppression, got %+v", res.Suppressed)
	}
}

// airlineCallsign must accept ICAO alphanumeric ATC suffixes (1-2 trailing
// letters, e.g. Emirates "UAE3HJ") while still rejecting tail registrations.
// UAE3HJ regressed in the field: the old `[A-Z]?` allowed only one trailing
// letter, so a real overhead SYD-CHC departure was suppressed as "route
// unknown" and never alerted.
func TestAirlineCallsignMatch(t *testing.T) {
	cases := []struct {
		callsign string
		want     bool
	}{
		{"QFA75", true},    // operator + digits
		{"UAE412", true},   // operator + 3 digits
		{"BAW16M", true},   // operator + digits + one suffix letter
		{"UAE3HJ", true},   // operator + digit + two suffix letters (the regression)
		{"UAE3HJK", false}, // three suffix letters is not a real callsign
		{"VHZUD", false},   // tail registration, no digit
		{"ZUD", false},     // bare tail fragment
		{"", false},
	}
	for _, c := range cases {
		if got := airlineCallsign.MatchString(c.callsign); got != c.want {
			t.Errorf("airlineCallsign.MatchString(%q) = %v, want %v", c.callsign, got, c.want)
		}
	}
}

// A SYD departure with an alphanumeric ATC suffix (e.g. UAE3HJ) must get a
// route lookup and publish. Regression guard for the suppressed overhead pass.
func TestTick_AlphanumericSuffixSYDDeparturePublishes(t *testing.T) {
	ctx := context.Background()
	s := matching()
	s.Callsign = "UAE3HJ"
	src := &fakeSource{vectors: []filter.State{s}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"UAE3HJ": {OriginIATA: "SYD", OriginICAO: "YSSY", DestIATA: "CHC", DestICAO: "NZCH"},
	}}
	p := newPublisher(src, routes, newCache(), newDedupe(), &fakeMQTT{})
	mqtt := p.MQTT.(*fakeMQTT)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if routes.calls != 1 {
		t.Errorf("expected one adsbdb lookup, got %d", routes.calls)
	}
	if len(mqtt.pubs) != 1 {
		t.Fatalf("expected one publish for SYD departure, got %d", len(mqtt.pubs))
	}
	if len(res.Published) != 1 || res.Published[0] != "UAE3HJ" {
		t.Errorf("expected published [UAE3HJ], got %+v", res.Published)
	}
}

// A Sydney-based scenario: observer near YSSY, aircraft a few nm south
// climbing due north over the observer — realistic overhead geometry.
var sydObserver = geo.Point{Lat: -33.90, Lon: 151.15}

func sydClimbOut() filter.State {
	return filter.State{
		ICAO24:      "8964b3",
		Callsign:    "UAE3HJ",
		ICAOType:    "A388",
		Position:    geo.Point{Lat: -33.95, Lon: 151.15}, // ~3 nm south of observer
		AltitudeFt:  3000,
		GroundSpdKt: 200,
		HeadingDeg:  0, // due north, straight over the observer
		VertRateFpm: 2000,
	}
}

func newSydPublisher(src *fakeSource, routes *fakeRoutes) *Publisher {
	p := newPublisher(src, routes, newCache(), newDedupe(), &fakeMQTT{})
	p.Cfg = Default("test/topic", sydObserver)
	return p
}

// adsbdb mislabels tag/continuation flights by their inbound leg: UAE3HJ
// (SYD-CHC) resolves to its DXB-SYD leg, origin DXB. It now fires on geometry
// alone — climbing, airline callsign, over the observer — regardless of the
// reported origin. (Also covers go-arounds, which adsbdb labels as arrivals.)
func TestTick_NonSYDOriginClimbingPublishes(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{vectors: []filter.State{sydClimbOut()}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"UAE3HJ": {OriginIATA: "DXB", OriginICAO: "OMDB", DestIATA: "SYD", DestICAO: "YSSY"},
	}}
	p := newSydPublisher(src, routes)
	mqtt := p.MQTT.(*fakeMQTT)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(mqtt.pubs) != 1 {
		t.Fatalf("expected one publish, got %d", len(mqtt.pubs))
	}
	if len(res.Published) != 1 || res.Published[0] != "UAE3HJ" {
		t.Errorf("expected published [UAE3HJ], got %+v", res.Published)
	}
}

// A level transit overflight (non-SYD origin, 0 fpm) fires: the use case is
// "what's overhead", and the 8000 ft alt-at-CPA cap already excludes high
// cruise, so a low level airliner over the observer is shown.
func TestTick_LevelTransitOverflightPublishes(t *testing.T) {
	ctx := context.Background()
	s := sydClimbOut()
	s.Callsign = "SIA231"
	s.VertRateFpm = 0 // level, e.g. on a SID step or low transit
	src := &fakeSource{vectors: []filter.State{s}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"SIA231": {OriginIATA: "MEL", OriginICAO: "YMML", DestIATA: "SIN", DestICAO: "WSSS"},
	}}
	p := newSydPublisher(src, routes)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(res.Published) != 1 {
		t.Fatalf("expected level overflight to publish, got %+v", res)
	}
}

// A descending arrival over the observer (e.g. joining 16R from the north) is
// excluded upstream by the filter's IgnoreDescending check — it never becomes
// a candidate, so it's neither published nor counted as suppressed.
func TestTick_DescendingArrivalFilteredOut(t *testing.T) {
	ctx := context.Background()
	s := sydClimbOut()
	s.Callsign = "UAE412"
	s.VertRateFpm = -500 // descending toward a landing, not a departure
	src := &fakeSource{vectors: []filter.State{s}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"UAE412": {OriginIATA: "DXB", OriginICAO: "OMDB", DestIATA: "SYD", DestICAO: "YSSY"},
	}}
	p := newSydPublisher(src, routes)
	mqtt := p.MQTT.(*fakeMQTT)

	res, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Candidates != 0 {
		t.Errorf("descending arrival should not be a candidate, got %d", res.Candidates)
	}
	if len(mqtt.pubs) != 0 {
		t.Errorf("expected no publish for descending arrival, got %d", len(mqtt.pubs))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
