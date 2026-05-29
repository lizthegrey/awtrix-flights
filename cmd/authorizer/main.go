// Command authorizer is the AWS Lambda entry point for the IoT Core custom
// authorizer. It validates MQTT username/password (pulled from Secrets
// Manager on cold start) and returns an IAM policy scoped to the AWTRIX
// device's client ID and subscription topic.
//
// Environment:
//
//	SECRET_NAME       Secrets Manager secret holding {"username":..,"password":..} JSON
//	AWS_REGION        injected by Lambda runtime
//	AWS_ACCOUNT_ID    AWS account ID
//	ALLOWED_CLIENT_ID MQTT client ID the AWTRIX presents (e.g. "awtrix_103")
//	ALLOWED_TOPIC     topic to allow Subscribe + Receive on
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-lambda-go/otellambda"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
	"go.opentelemetry.io/otel"

	"github.com/lizthegrey/awtrix-flights/internal/authorizer"
	"github.com/lizthegrey/awtrix-flights/internal/otelinit"
)

var (
	auth  *authorizer.Authorizer
	otelH otelinit.Handle
)

func main() {
	ctx := context.Background()
	h, err := otelinit.Setup(ctx, "awtrix-flights-authorizer")
	if err != nil {
		slog.Error("otel init failed", "err", err)
		os.Exit(1)
	}
	otelH = h

	a, err := build(ctx)
	if err != nil {
		slog.Error("init failed", "err", err)
		os.Exit(1)
	}
	auth = a

	lambda.Start(otellambda.InstrumentHandler(handler,
		otellambda.WithTracerProvider(otel.GetTracerProvider()),
		otellambda.WithFlusher(flusher{}),
	))
}

type flusher struct{}

func (flusher) ForceFlush(ctx context.Context) error {
	if otelH.Flush == nil {
		return nil
	}
	return otelH.Flush(ctx)
}

func handler(ctx context.Context, req authorizer.Request) (authorizer.Response, error) {
	resp, err := auth.Handle(ctx, req)
	if err != nil {
		slog.Error("authorizer error", "err", err)
		return resp, err
	}
	slog.Info("auth decision",
		"authenticated", resp.IsAuthenticated,
		"client_id", req.ProtocolData.MQTT.ClientID,
	)
	return resp, nil
}

func build(ctx context.Context) (*authorizer.Authorizer, error) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	otelaws.AppendMiddlewares(&awsCfg.APIOptions)

	creds, err := loadCreds(ctx, secretsmanager.NewFromConfig(awsCfg), mustEnv("SECRET_NAME"))
	if err != nil {
		return nil, fmt.Errorf("load secret: %w", err)
	}

	return &authorizer.Authorizer{
		Creds: creds,
		Policy: authorizer.PolicyConfig{
			Region:          mustEnv("AWS_REGION"),
			AccountID:       mustEnv("AWS_ACCOUNT_ID"),
			AllowedClientID: mustEnv("ALLOWED_CLIENT_ID"),
			AllowedTopic:    mustEnv("ALLOWED_TOPIC"),
		},
	}, nil
}

type secretBlob struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func loadCreds(ctx context.Context, sm *secretsmanager.Client, name string) (authorizer.Credentials, error) {
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	if err != nil {
		return authorizer.Credentials{}, err
	}
	if out.SecretString == nil {
		return authorizer.Credentials{}, fmt.Errorf("secret %q has no string value", name)
	}
	var s secretBlob
	if err := json.Unmarshal([]byte(*out.SecretString), &s); err != nil {
		return authorizer.Credentials{}, fmt.Errorf("decode secret: %w", err)
	}
	if s.Username == "" || s.Password == "" {
		return authorizer.Credentials{}, fmt.Errorf("secret missing username or password")
	}
	return authorizer.Credentials{Username: s.Username, Password: s.Password}, nil
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic("missing required env: " + key)
	}
	return v
}
