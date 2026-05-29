// Command publisher is the AWS Lambda entry point for the polling tick.
//
// Triggered in two ways:
//   - EventBridge schedule (rate(1 minute)): fanout path. The handler
//     enqueues 4 SQS messages with DelaySeconds = 0/15/30/45 and exits.
//   - SQS event source mapping: tick path. Runs the actual filter +
//     publish cycle, once per delayed message.
//
// AWS EventBridge Scheduler can't natively fire faster than every 1
// minute, and start_date sub-minute offsets aren't honored on rate()
// schedules — so we fan out via SQS DelaySeconds for ~15 s effective
// cadence. See https://zaccharles.medium.com/...b8459544b166
//
// Environment:
//
//	TABLE_NAME         DynamoDB table for dedupe + route cache
//	MQTT_TOPIC         IoT Core topic to publish to
//	IOT_ENDPOINT       IoT data-plane endpoint host
//	HOME_LAT, HOME_LON observer location (kept out of source)
//	ICON_ID            optional AWTRIX icon ID
//	LOG_LEVEL          optional, default "info"
//	FANOUT_QUEUE_URL   SQS queue the EventBridge path enqueues into
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/iotdataplane"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
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
	pub            *publisher.Publisher
	otelH          otelinit.Handle
	sqsClient      *sqs.Client
	fanoutQueueURL string
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

// rawEvent is the minimum schema we need to discriminate event sources.
// SQS events have a non-empty Records array; EventBridge events don't.
type rawEvent struct {
	Records []json.RawMessage `json:"Records,omitempty"`
}

func handler(ctx context.Context, raw json.RawMessage) error {
	var ev rawEvent
	_ = json.Unmarshal(raw, &ev)
	if len(ev.Records) > 0 {
		return doTick(ctx)
	}
	return fanOut(ctx)
}

// doTick runs one filter-and-publish cycle.
func doTick(ctx context.Context) error {
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

// fanOut enqueues 4 SQS messages with offset DelaySeconds so the SQS
// event source mapping triggers doTick() at ~15 s intervals across the
// next minute.
func fanOut(ctx context.Context) error {
	entries := []sqstypes.SendMessageBatchRequestEntry{
		{Id: aws.String("t0"), MessageBody: aws.String(`{"tick":0}`), DelaySeconds: 0},
		{Id: aws.String("t1"), MessageBody: aws.String(`{"tick":1}`), DelaySeconds: 15},
		{Id: aws.String("t2"), MessageBody: aws.String(`{"tick":2}`), DelaySeconds: 30},
		{Id: aws.String("t3"), MessageBody: aws.String(`{"tick":3}`), DelaySeconds: 45},
	}
	out, err := sqsClient.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
		QueueUrl: aws.String(fanoutQueueURL),
		Entries:  entries,
	})
	if err != nil {
		return fmt.Errorf("fanout SendMessageBatch: %w", err)
	}
	slog.Info("fanout queued",
		"successful", len(out.Successful),
		"failed", len(out.Failed),
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
	fanoutQueueURL = mustEnv("FANOUT_QUEUE_URL")
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
	sqsClient = sqs.NewFromConfig(awsCfg)

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
