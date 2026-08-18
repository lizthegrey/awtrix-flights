# Notes for Claude

The README explains what this project is and how to deploy it. This file
is for things you'd waste time rediscovering otherwise.

## Working conventions

- Commit directly to `main`. No feature branches in this repo. (The
  global CLAUDE.md rule against direct-to-main does not apply here.)
- Co-Authored-By: trailers are expected on Claude-generated commits.
- The owner reads diffs before pushes; clean, scoped commits matter.

## Things that have been tried and ruled out

Don't re-propose these without new evidence:

- **HTTP polling from the device.** Upstream AWTRIX firmware has no
  `pollUrl` feature and there's no on-device polling primitive. AWTRIX
  Flow is a *Node-RED* flow editor, not a thing the device runs.
- **AWS IoT Core port 8883.** Custom authorizers without client certs
  REQUIRE port 443 + ALPN `"mqtt"`. Port 8883 expects mTLS and hangs
  silently. The MbedTLSClient lib in our fork has the ALPN patch.
- **TLS 1.2 session resumption.** Empirically verified: AWS IoT Core ATS
  endpoints do not issue session tickets and do not honor session IDs.
  `openssl s_client -reconnect` returns `New` every time. There's a
  reverted-commits trail on the firmware fork if proof is needed.
- **EventBridge Scheduler sub-minute cadence.** `rate(N units)` has a
  hard 1-minute floor, AND `start_date` seconds-component is silently
  dropped for rate() schedules. Configured offsets of `:33, :48, :03, :18`
  actually fire at `:46, :01, :16, :31` with 7-7-3-43-s gaps. The SQS
  fanout in `terraform/sqs.tf` + `cmd/publisher/main.go` is the workaround.
- **AWS IoT Core max_fragment_length (RFC 6066) negotiation.**
  Inconclusive whether AWS honors it; openssl falls back silently if
  not. Probably not worth implementing.
- **`esp_task_wdt_delete(NULL)`** to opt the MQTT task out of the
  watchdog: crashes the firmware on boot. Use `esp_task_wdt_init(120, false)`
  to disable panic instead.
- **HA_DISCOVERY enabled.** Floods the broker at connect with ~50
  publishes, starves IDLE0, trips the watchdog. The fork forces it off
  in `MQTTManager::startTask()`.

## Known false positives and the filter rules that suppress them

Observed during real operation; don't accidentally regress:

- **Arrivals to SYD** look overhead-bound briefly while joining the
  localizer (e.g. 16R approaches from the north, over the observer).
  Suppressed by the filter's `IgnoreDescending` check: a descending
  aircraft is an arrival, not a departure or transit, so it's dropped
  before it's even a candidate. (Level and climbing traffic is kept.)

## The firing rule: geometry + airline callsign, NOT origin

A flight fires if it matches the **overhead CPA geometry** (cross-track
≤ 1.5 NM, projected altitude-at-CPA ≤ 8000 ft, CPA within the time
window, not descending) **and** carries an **airline-style callsign**.
That's it. There is deliberately **no requirement that the route start
or end at SYD** — any airliner flying the overhead corridor is shown,
departure or transit. The use case is "what's that plane overhead",
not "what's departing".

This replaced an earlier departure-only design (origin = SYD/YSSY gate
plus a climb-out override keyed to the 34L threshold). Both are gone.
History worth keeping in mind so you don't re-introduce them:

- **adsbdb keys routes on the callsign's primary leg**, so the old
  origin gate missed tag/continuation flights: e.g. Emirates `UAE3HJ`
  flies DXB-SYD-CHC on one callsign and adsbdb only knows it as DXB-SYD
  (origin DXB). On the SYD-CHC leg it's physically departing YSSY but
  adsbdb calls it a Dubai-origin arrival. Geometry-only firing makes
  this a non-issue — it fires on the energy/geometry regardless of what
  adsbdb thinks the origin is. (This also naturally catches go-arounds
  and missed approaches: they climb back over the observer.)
- **The route lookup is now display-only**, best-effort. A 404 (adsbdb
  route-table gaps, e.g. `CXA802` SYD-XMN) or a wrong origin no longer
  suppresses — we publish without a destination. So adsbdb's
  incompleteness for Pacific/Asian flight numbers costs a missing dest
  string, not a missed overflight.
- **The airline-callsign gate is what keeps GA/noise out** now that the
  route requirement is gone — see the meandering-GA note below.

Don't re-add an origin gate without re-checking UAE3HJ and CXA802;
both are reasons it was removed.

Also note **two callsign-format gotchas** in `internal/publisher`
(`airlineCallsign` regex) and `internal/awtrix` (`icaoToIATA` /
`niceFlight`): ICAO ATC callsigns can carry a 2-letter alphanumeric
suffix (`UAE3HJ`), so the regex allows `[A-Z]{0,2}` after the digits,
not `[A-Z]?`. And the display's ICAO→IATA prefix map must list any
operator you want shortened (`KAL`→`KE`, etc.) or it renders the raw
ICAO prefix. The display prefers the route's `callsign_iata` (parsed into
`adsbdb.Route.CallsignIATA`) for the flight number, falling back to
`niceFlight(wire callsign)` only when the route 404'd. For most flights
adsbdb's `callsign_iata` is just a mechanical prefix swap equal to the
wire shorten (`UAE3HJ`→`EK3HJ`, still NOT the marketing number `EK412`),
so preferring it is usually a no-op — and for UAE3HJ we still can't show
the commercial number. The one case where a remap *would* change the
digits shown (QantasLink's truncated trailing-letter form) only resolves
when disambiguation succeeds — see the QantasLink suffix-form note below —
so `callsign_iata` changes the displayed number only on that occasional hit;
otherwise it's a no-op like everything else here.

A related-but-distinct adsbdb miss: **operator codes that file routes
under a different carrier.** adsbdb does a literal callsign-string
lookup with no alias translation, so a flight squawking one ICAO code
whose route is scheduled under another just 404s. QantasLink (ICAO
`QLK`, including its A220s) squawks `QLK####` but Qantas group files
those routes under mainline `QFA####` with the same number — so
`/v0/callsign/QLK1944` is 404 while `QFA1944` returns SYD-origin route
data. `routeCallsignAlias` in `internal/publisher` rewrites the prefix
(`QLK`→`QFA`) before the cache key and live lookup; the display keeps
the wire callsign and shortens it via `icaoToIATA` (`QLK`→`QF`). This
is NOT the same as UAE3HJ (same callsign, wrong leg) — it's a different
callsign string entirely. Add new entries to `routeCallsignAlias` for
other subsidiaries that file under a parent (don't try to infer it).

QantasLink also has a **second, trailing-letter callsign form** —
`QLK#L` / `QLK##L` / `QLK###L` (1-3 digits + one letter, e.g. `QLK431D`
or `QLK28D`) — that the plain prefix swap can't safely handle. This form
is a truncated flight number: the transponder drops the leading digit(s)
of the real 4-digit QF number, so `431D` alone doesn't tell you whether
the real flight is QF1431, QF2431, QF9431, etc.

An earlier version of this code guessed the dropped digit was always
`2` (so `QLK205D` → `QFA2205`). That looked safe — it round-tripped one
real example without 404ing — but "doesn't 404" isn't "is correct": a
live report of `QLK431D` (a QantasLink DH8D) showing an LHR-adjacent
destination traced back to this guess resolving to a real but *wholly
unrelated* Qantas mainline flight. Checking the actual flight (QF1431,
SYD-CBR, block `1`) and then spot-checking further showed guessing a
single fixed block is unsound in general: Qantas's flight-number space
(mainline + codeshares) is dense enough that a 1-3 digit remainder
resolves to SOME real flight under nearly *every* leading digit tried
(`128` → block `1` is QF128 HKG-SYD widebody, block `2` is QF2028
SYD-DBO turboprop, block `3` is QF3028 a US codeshare MSP-DFW). And two
blocks can BOTH be real simultaneously for the same remainder — `205` →
block `1` is a real CBR-MEL flight (`QFA1205`) AND block `2` is a real
SYD-ABX flight (`QFA2205`) — so even the specific example this code was
originally built on was never actually verified correct, just "not 404".

`internal/publisher`'s `resolveQantasLinkSuffixRoute` now queries the two
blocks confirmed to be real QantasLink ranges (`qantasLinkSuffixBlocks`:
`"2"`, then `"1"` — order doesn't matter, both are always tried), then
breaks ties with `sydneyEndpointPriority`: the observer sits directly under
YSSY's departure corridor and the filter already drops descending aircraft
(`IgnoreDescending`), so anything reaching this code is a Sydney departure
or a transit, never an arrival. A resolved candidate whose origin is YSSY
is the plausible case (rank 0); destination YSSY (a transit, or the rarer
go-around) is next (rank 1); touching YSSY at neither end (rank 2) means
the flight has no reason to be anywhere near the observer at all — e.g.
`205` → `QFA1205` is a real CBR-MEL flight, but Canberra-to-Melbourne
doesn't detour north through Sydney, so it loses to `QFA2205` (SYD-ABX,
rank 0). The best rank is only trusted when exactly one candidate holds
it; a rank tie (both touch YSSY the same way, or neither does) is still
genuinely ambiguous and falls back to no destination rather than guess
further. Block `3` was checked and found to be unrelated US codeshare
traffic, not QantasLink — deliberately excluded; don't extend
`qantasLinkSuffixBlocks` without an equally concrete positive example.
This costs up to two live adsbdb calls per uncached truncated callsign
(cached under the original wire callsign once resolved). This is distinct
from the 4-digit `QLK####`
form (e.g. `QLK1944` → `QFA1944`, no digits dropped, no guessing) and the
4-digit-plus-suffix form (e.g. `QLK1234A` → `QFA1234A`), both still
handled by the generic `routeCallsignAlias` prefix swap.
- **Meandering GA traffic** from nearby small-airport feeders match
  the geometry (the constant-heading projection false-positives while
  they turn) but aren't real overflight events. Suppressed by the
  **airline-callsign gate**: GA tends to fly tail-registration
  callsigns (`VHABC`), which fail `airlineCallsign` and never fire.
  This is the gate that used to be implicit in "require an adsbdb
  route"; it's now explicit and is the only thing standing between
  geometry and a publish for non-airline contacts.
- **SIDs aren't always flown exactly.** ATC vectoring, aircraft
  performance variation, and shared initial legs mean published
  departure procedures off the same runway can produce visually-
  similar early tracks even when they're supposed to diverge.
  Don't try to filter false positives by encoding SID-specific
  track expectations — the constant-heading CPA projection from
  instantaneous state is the right level of abstraction. False
  positives where the actual ground track turns away later are
  accepted; dedupe holds for 10 min anyway.

## What the lead-time math says

Backtested against real adsbexchange traces of both narrowbody and
widebody departures on the overhead-relevant SID:

- Geometry — distance from runway threshold to the observer at typical
  climb-out ground speeds — sets the ceiling. The `MaxSecondsToCPA = 240`
  in the filter is just a safety cap; actual fires happen much later
  along the flight, well under a minute before overhead.
- Widebody vs narrowbody barely changes the lead time. Both converge to
  similar ground speeds during the threshold-to-observer portion, so
  fast narrowbodies aren't meaningfully harder to catch than heavies.
- Filter prediction is accurate: the trace replays show the
  cross-track + altitude-at-CPA projection matches the observed
  overhead moment to within ~1 s once the aircraft is on the firing leg.
- The previous 60-s scan cadence was sometimes too slow for the worst
  case. The current 15-s SQS-fanout cadence has comfortable margin.

## Live system identifiers

- IoT endpoint: `a11xvlt8ygwf53-ats.iot.ap-southeast-2.amazonaws.com`
- AWTRIX client_id and prefix: matches the device's chip ID (set in
  `terraform.tfvars`; defaults to a placeholder)
- Firmware version string: `0.99+lizf1`
- AWS region: `ap-southeast-2`

## Quick debug runbooks

- **Device isn't connecting**: tail
  `/aws/lambda/awtrix-flights-authorizer` first (zero invocations = TLS
  handshake never reached IoT Core), then `AWSIotLogsV2` for
  `AUTHORIZATION_FAILURE` reasons. The authorizer's `principalId` is
  validated as `[a-zA-Z0-9]{1,128}` and `RefreshAfterInSeconds` must be
  300-86400 — out-of-range values produce silent rejection at the
  `AUTHORIZATION_FAILURE` level (no Lambda error visible). Use
  `aws iot test-invoke-authorizer` for fast iteration.
- **Lambda firing but no display update**: check `IoT MQTT logs` for
  `Subscribe` failures (IAM policy too narrow), check the device's
  prefix in `DoNotTouch.json` matches `awtrix_topic` exactly.
- **Cadence anomalies**: `aws logs filter-log-events` for
  `'START'` on the publisher group; SQS fanout should yield 4
  invocations per minute at consistent in-minute offsets.
