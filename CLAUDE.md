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
  southbound-flow localizer. Suppressed by requiring route
  origin = SYD/YSSY — OR, when origin isn't SYD, by a climb-out
  override (see below). Descending arrivals fail the override's climb
  check, so they stay suppressed either way.

## The departure gate and adsbdb's origin caveat

A flight fires only if it's leaving Sydney. The primary signal is
adsbdb reporting route origin = SYD/YSSY. But **adsbdb keys routes on
the callsign's primary leg**, so tag/continuation flights that reuse
one callsign are mislabeled: e.g. Emirates `UAE3HJ` flies DXB-SYD-CHC
on one callsign, and adsbdb only knows it as DXB-SYD (origin DXB). On
the SYD-CHC leg it's physically departing YSSY but adsbdb calls it a
Dubai-origin arrival. So origin-matching alone misses it.

The fix (`cmd`/`internal/publisher`): a **climb-out override** — when
origin isn't SYD, fire anyway if the aircraft is climbing
(`VertRateFpm ≥ 500`), low (`≤ 10000 ft`), and within 8 NM of the 34L
threshold, having already matched the overhead geometry. This trusts
the energy state over adsbdb's origin field. Bonus: it also catches
**go-arounds / missed approaches** (adsbdb labels them arrivals, but
they climb out over the observer just like a departure). Don't
re-tighten this to origin-only without re-checking UAE3HJ.

Also note **two callsign-format gotchas** in `internal/publisher`
(`airlineCallsign` regex) and `internal/awtrix` (`icaoToIATA` /
`niceFlight`): ICAO ATC callsigns can carry a 2-letter alphanumeric
suffix (`UAE3HJ`), so the regex allows `[A-Z]{0,2}` after the digits,
not `[A-Z]?`. And the display's ICAO→IATA prefix map must list any
operator you want shortened (`KAL`→`KE`, etc.) or it renders the raw
ICAO prefix. adsbdb's `callsign_iata` is just a mechanical prefix swap
(`UAE3HJ`→`EK3HJ`), NOT the marketing flight number (`EK412`), so we
can't show the commercial number from adsbdb alone.

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
- **Meandering GA traffic** from nearby small-airport feeders match
  the geometry but aren't real overflight events. Suppressed by
  requiring an adsbdb route at all (the same predicate also catches
  tail-registration callsigns with no airline route lookup).
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
