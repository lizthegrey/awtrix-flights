# awtrix-flights

Show overhead-bound aircraft on an [AWTRIX3](https://blueforcer.github.io/awtrix3/)
LED matrix display.

Polls free public ADS-B data, projects each aircraft's ground track forward,
and pushes a notification to the AWTRIX whenever something is about to pass
close enough overhead and low enough to actually see or hear from a window.

Tuned for departures off YSSY (Sydney) runway 34L, but the geometry isn't
hardcoded — pick any observer location.

## Architecture

```
EventBridge (rate 30s)
        │
        ▼
┌───────────────────┐    HTTPS    ┌──────────────┐
│ Publisher Lambda  │ ──────────▶ │ adsb.fi      │
│ (Go, arm64)       │             │ adsbdb.com   │
└────────┬──────────┘             └──────────────┘
         │ DynamoDB (dedupe + route cache)
         │ MQTT publish (QoS 1)
         ▼
┌───────────────────┐    MQTT/TLS  ┌──────────────┐
│ AWS IoT Core      │ ◀────────── │ AWTRIX3      │
│ (custom auth      │  TLS+u/pw    │ (subscriber) │
│  Lambda for u/pw) │              │              │
└───────────────────┘              └──────────────┘
```

All serverless. Free tier covers the ~2880 invocations/day at 30 s cadence.

## Layout

| Path                       | Purpose |
|----------------------------|---------|
| `cmd/publisher/`           | Lambda: scheduled poll → filter → publish |
| `cmd/authorizer/`          | Lambda: IoT Core custom authorizer (MQTT u/pw) |
| `internal/geo/`            | Great-circle + cross-track helpers |
| `internal/filter/`         | Perceptibility filter (cross-track + alt at CPA) |
| `internal/adsbfi/`         | adsb.fi state-vector client |
| `internal/adsbdb/`         | adsbdb.com callsign→route client |
| `internal/store/`          | DynamoDB dedupe + route cache |
| `internal/iotpub/`         | AWS IoT data-plane MQTT publish wrapper |
| `internal/awtrix/`         | AWTRIX custom-app payload formatter |
| `internal/publisher/`      | One polling tick, glue of the above |
| `internal/authorizer/`     | u/p check + IAM policy builder |
| `terraform/`               | Full infrastructure for ap-southeast-2 |

## Filter

The criterion is *perceptibility from the observer's window*, not aircraft type:

- Cross-track distance at closest point of approach (CPA) ≤ 1.5 nm
- Projected altitude at CPA ≤ 8000 ft (using current alt + climb rate × time-to-CPA)
- CPA in the future within 4 minutes

A regional turboprop on track to fly directly overhead at 3000 ft matches.
A 787 cruising at FL370 abeam by 2 nm does not. See `internal/filter/filter.go`.

## Setup

### Prereqs

- Go 1.26+
- Terraform 1.6+
- AWS account + credentials (e.g. `aws sso login` for an `ap-southeast-2` profile)
- An AWTRIX3 device on your LAN

### Deploy

```sh
cd terraform
cp terraform.tfvars.example terraform.tfvars   # edit for your home coords + client_id
terraform init
make plan         # from repo root
make apply
```

### Configure AWTRIX

Open the AWTRIX web UI (e.g. `http://192.168.1.103/`) → Settings → MQTT.

Pull the values from Terraform outputs:

```sh
cd terraform
terraform output mqtt_broker_host
terraform output mqtt_broker_port
terraform output mqtt_client_id
terraform output mqtt_username
terraform output -raw mqtt_password
terraform output mqtt_topic
```

In the AWTRIX MQTT settings:

| Field           | Value |
|-----------------|-------|
| Broker          | `<mqtt_broker_host>` |
| Port            | `8883` |
| TLS             | enabled |
| Username        | `awtrix?x-amz-customauthorizer-name=awtrix-flights-mqtt-authorizer` |
| Password        | (from `terraform output -raw mqtt_password`) |
| Client ID       | `awtrix_103` (or whatever you set in tfvars) |
| Topic prefix    | unchanged from device default |

The AWTRIX subscribes to `<prefix>/custom/<appname>` automatically. The
publisher sends to the topic in `mqtt_topic`, which the AWTRIX renders as a
custom app.

### Smoke test

```sh
# Tail the publisher logs.
aws logs tail --follow "$(cd terraform && terraform output -raw publisher_log_group)"
```

You should see a JSON log per invocation with `scanned`, `candidates`, and
`published` counts. The first matching overhead will publish to MQTT and
display on the AWTRIX.

## Development

```sh
make test    # go test ./...
make vet     # go vet + gofmt check
make fmt     # gofmt -w + terraform fmt
make build   # cross-compile both Lambdas for Lambda's arm64 runtime
```

## Data sources

- [adsb.fi](https://adsb.fi/) — community ADS-B feed, free, no auth
- [adsbdb.com](https://www.adsbdb.com/) — free callsign→route lookup

Both have generous rate limits for personal use. If they ever go away, the
client interfaces in `internal/publisher/` are small — swap in OpenSky or
your own ADS-B feeder.
