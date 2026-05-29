// Command publisher is the AWS Lambda entry point for the polling tick.
//
// Triggered by an EventBridge schedule (every 30 s by default). Environment:
//
//	TABLE_NAME       DynamoDB table for dedupe + route cache
//	MQTT_TOPIC       IoT Core topic to publish to, e.g. "awtrix_xxxxxx/custom/overhead"
//	IOT_ENDPOINT     IoT data-plane endpoint host, e.g. xxxxx-ats.iot.ap-southeast-2.amazonaws.com
//	HOME_LAT         observer latitude in decimal degrees (kept out of source)
//	HOME_LON         observer longitude in decimal degrees
//	ICON_ID          optional AWTRIX icon ID
//	LOG_LEVEL        optional, default "info"
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/iotdataplane"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-lambda-go/otellambda"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
	"go.opentelemetry.io/otel"

	"github.com/lizthegrey/awtrix-flights/internal/adsbdb"
	"github.com/lizthegrey/awtrix-flights/internal/adsbfi"
	"github.com/lizthegrey/awtrix-flights/internal/geo"
	"github.com/lizthegrey/awtrix-flights/internal/iotpub"
	"github.com/lizthegrey/awtrix-flights/internal/otelinit"
	"github.com/lizthegrey/awtrix-flights/internal/publisher"
	"github.com/lizthegrey/awtrix-flights/internal/store"
)

var (
	pub   *publisher.Publisher
	otelH otelinit.Handle
)

func main() {
	ctx := context.Background()
	h, err := otelinit.Setup(ctx, "awtrix-flights-publisher")
	if err != nil {
		slog.Error("otel init failed", "err", err)
		os.Exit(1)
	}
	otelH = h

	p, err := build(ctx)
	if err != nil {
		slog.Error("init failed", "err", err)
		os.Exit(1)
	}
	pub = p

	lambda.Start(otellambda.InstrumentHandler(handler,
		otellambda.WithTracerProvider(otel.GetTracerProvider()),
		otellambda.WithFlusher(flusher{}),
	))
}

func handler(ctx context.Context) error {
	res, err := pub.Tick(ctx)
	if err != nil {
		return fmt.Errorf("tick: %w", err)
	}
	slog.Info("tick done",
		"scanned", res.Scanned,
		"candidates", res.Candidates,
		"published", res.Published,
		"suppressed", res.Suppressed,
	)
	return nil
}

// flusher satisfies otellambda.Flusher so the SDK flushes spans before Lambda
// freezes the runtime between invocations.
type flusher struct{}

func (flusher) ForceFlush(ctx context.Context) error {
	if otelH.Flush == nil {
		return nil
	}
	return otelH.Flush(ctx)
}

func build(ctx context.Context) (*publisher.Publisher, error) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel(),
	})))

	tableName := mustEnv("TABLE_NAME")
	mqttTopic := mustEnv("MQTT_TOPIC")
	iotEndpoint := mustEnv("IOT_ENDPOINT")
	observer, err := parseObserver()
	if err != nil {
		return nil, err
	}

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}

	otelaws.AppendMiddlewares(&awsCfg.APIOptions)

	ddb := dynamodb.NewFromConfig(awsCfg)
	iot := iotdataplane.NewFromConfig(awsCfg, func(o *iotdataplane.Options) {
		o.BaseEndpoint = strPtr("https://" + iotEndpoint)
	})

	cfg := publisher.Default(mqttTopic, observer)
	cfg.IconID = os.Getenv("ICON_ID")

	return &publisher.Publisher{
		Source: adsbfi.New(),
		Routes: adsbdb.New(),
		Cache:  store.New(ddb, tableName),
		Dedupe: store.New(ddb, tableName),
		MQTT:   iotpub.New(iot),
		Cfg:    cfg,
	}, nil
}

func logLevel() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic("missing required env: " + key)
	}
	return v
}

func parseObserver() (geo.Point, error) {
	latStr := mustEnv("HOME_LAT")
	lonStr := mustEnv("HOME_LON")
	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		return geo.Point{}, fmt.Errorf("parse HOME_LAT: %w", err)
	}
	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		return geo.Point{}, fmt.Errorf("parse HOME_LON: %w", err)
	}
	return geo.Point{Lat: lat, Lon: lon}, nil
}

func strPtr(s string) *string { return &s }
