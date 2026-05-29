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
		"QFA75": {DestIATA: "YVR", DestICAO: "CYVR"},
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

func TestTick_RouteCacheHit(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{vectors: []filter.State{matching()}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{
		"QFA75": {DestIATA: "YVR", DestICAO: "CYVR"},
	}}
	cache := newCache()
	cache.routes["QFA75"] = adsbdb.Route{DestIATA: "CACHED"}
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

func TestTick_RouteLookupFailsStillPublishes(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{vectors: []filter.State{matching()}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{}} // empty → not found
	p := newPublisher(src, routes, newCache(), newDedupe(), &fakeMQTT{})
	mqtt := p.MQTT.(*fakeMQTT)

	if _, err := p.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(mqtt.pubs) != 1 {
		t.Fatalf("expected publish despite missing route, got %d", len(mqtt.pubs))
	}
	if got := string(mqtt.pubs[0].payload); !contains(got, "QF75") {
		t.Errorf("payload should still include callsign: %s", got)
	}
}

// Tail registrations (e.g. ZUD from "VHZUD") shouldn't trigger an adsbdb call.
func TestTick_TailRegistrationSkipsRouteLookup(t *testing.T) {
	ctx := context.Background()
	s := matching()
	s.Callsign = "ZUD" // tail without VH- prefix that ADS-B sometimes reports
	src := &fakeSource{vectors: []filter.State{s}}
	routes := &fakeRoutes{routes: map[string]adsbdb.Route{}}
	p := newPublisher(src, routes, newCache(), newDedupe(), &fakeMQTT{})
	mqtt := p.MQTT.(*fakeMQTT)

	if _, err := p.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if routes.calls != 0 {
		t.Errorf("expected zero adsbdb calls for tail, got %d", routes.calls)
	}
	if len(mqtt.pubs) != 1 {
		t.Fatalf("expected publish (no route data), got %d", len(mqtt.pubs))
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
