// Package store wraps a single DynamoDB table used for two things:
//
//   - Dedupe: have we already fired a notification for this callsign recently?
//   - Route cache: cached adsbdb.com lookups so we don't re-query for every poll.
//
// Both share one table to keep infrastructure minimal. PK is "type:key".
// Items expire via DynamoDB TTL on the "expires_at" attribute.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/lizthegrey/awtrix-flights/internal/adsbdb"
)

// ErrMiss is returned by cache lookups when the key is absent.
var ErrMiss = errors.New("store: miss")

// DDB is the subset of the AWS SDK client we use; helps with tests.
type DDB interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

// Store is the shared dedupe + route cache.
type Store struct {
	DB        DDB
	TableName string
}

// New constructs a Store.
func New(db DDB, tableName string) *Store {
	return &Store{DB: db, TableName: tableName}
}

type item struct {
	PK           string `dynamodbav:"pk"`
	ExpiresAt    int64  `dynamodbav:"expires_at"` // unix seconds, used by DDB TTL
	OriginICAO   string `dynamodbav:"origin_icao,omitempty"`
	DestICAO     string `dynamodbav:"dest_icao,omitempty"`
	OriginIATA   string `dynamodbav:"origin_iata,omitempty"`
	DestIATA     string `dynamodbav:"dest_iata,omitempty"`
	Callsign     string `dynamodbav:"callsign,omitempty"`
	CallsignIATA string `dynamodbav:"callsign_iata,omitempty"`
}

// MarkFired records that we just notified for callsign; returns the previous
// fire time (or zero if never). Use the return value to suppress repeats.
func (s *Store) MarkFired(ctx context.Context, callsign string, ttl time.Duration) error {
	pk := "dedupe:" + callsign
	it := item{PK: pk, ExpiresAt: time.Now().Add(ttl).Unix()}
	av, err := attributevalue.MarshalMap(it)
	if err != nil {
		return fmt.Errorf("marshal dedupe: %w", err)
	}
	_, err = s.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.TableName),
		Item:      av,
	})
	if err != nil {
		return fmt.Errorf("put dedupe: %w", err)
	}
	return nil
}

// RecentlyFired reports whether MarkFired was called for callsign within the
// item's TTL. Returns false on store errors (we'd rather over-notify than
// silently swallow legit overheads when DynamoDB blips).
func (s *Store) RecentlyFired(ctx context.Context, callsign string) bool {
	pk := "dedupe:" + callsign
	out, err := s.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.TableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pk},
		},
	})
	if err != nil || out.Item == nil {
		return false
	}
	var it item
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return false
	}
	// DynamoDB TTL is async (up to 48 h late), so we also check expiry inline.
	return it.ExpiresAt > time.Now().Unix()
}

// GetRoute returns a cached route, or ErrMiss if none.
func (s *Store) GetRoute(ctx context.Context, callsign string) (adsbdb.Route, error) {
	pk := "route:" + callsign
	out, err := s.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.TableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pk},
		},
	})
	if err != nil {
		return adsbdb.Route{}, fmt.Errorf("get route: %w", err)
	}
	if out.Item == nil {
		return adsbdb.Route{}, ErrMiss
	}
	var it item
	if err := attributevalue.UnmarshalMap(out.Item, &it); err != nil {
		return adsbdb.Route{}, fmt.Errorf("unmarshal route: %w", err)
	}
	if it.ExpiresAt <= time.Now().Unix() {
		return adsbdb.Route{}, ErrMiss
	}
	return adsbdb.Route{
		Callsign:     it.Callsign,
		CallsignIATA: it.CallsignIATA,
		OriginICAO:   it.OriginICAO,
		DestICAO:     it.DestICAO,
		OriginIATA:   it.OriginIATA,
		DestIATA:     it.DestIATA,
	}, nil
}

// PutRoute caches a route with the given TTL.
func (s *Store) PutRoute(ctx context.Context, callsign string, r adsbdb.Route, ttl time.Duration) error {
	pk := "route:" + callsign
	it := item{
		PK:           pk,
		ExpiresAt:    time.Now().Add(ttl).Unix(),
		Callsign:     r.Callsign,
		CallsignIATA: r.CallsignIATA,
		OriginICAO:   r.OriginICAO,
		DestICAO:     r.DestICAO,
		OriginIATA:   r.OriginIATA,
		DestIATA:     r.DestIATA,
	}
	av, err := attributevalue.MarshalMap(it)
	if err != nil {
		return fmt.Errorf("marshal route: %w", err)
	}
	_, err = s.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.TableName),
		Item:      av,
	})
	if err != nil {
		return fmt.Errorf("put route: %w", err)
	}
	return nil
}
