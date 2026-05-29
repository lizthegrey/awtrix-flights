// Package iotpub wraps AWS IoT Core's data-plane Publish, exposing only the
// minimal interface we need (so it's trivial to mock).
package iotpub

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iotdataplane"
)

// API is the IoT Core data-plane operation we use.
type API interface {
	Publish(ctx context.Context, in *iotdataplane.PublishInput, opts ...func(*iotdataplane.Options)) (*iotdataplane.PublishOutput, error)
}

// Publisher publishes JSON payloads to IoT Core topics at QoS 1.
type Publisher struct {
	API API
}

// New constructs a Publisher.
func New(api API) *Publisher {
	return &Publisher{API: api}
}

// Publish sends payload to topic at QoS 1.
func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error {
	_, err := p.API.Publish(ctx, &iotdataplane.PublishInput{
		Topic:   aws.String(topic),
		Qos:     1,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("iot publish %q: %w", topic, err)
	}
	return nil
}
