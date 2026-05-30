package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/lizthegrey/awtrix-flights/internal/adsbdb"
)

// fakeDDB is a tiny in-memory DynamoDB stand-in.
type fakeDDB struct {
	items map[string]map[string]types.AttributeValue
}

func newFake() *fakeDDB {
	return &fakeDDB{items: map[string]map[string]types.AttributeValue{}}
}

func (f *fakeDDB) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	pk, ok := in.Key["pk"].(*types.AttributeValueMemberS)
	if !ok {
		return nil, errors.New("fake: expected string pk")
	}
	it := f.items[pk.Value]
	return &dynamodb.GetItemOutput{Item: it}, nil
}

func (f *fakeDDB) PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	pk, ok := in.Item["pk"].(*types.AttributeValueMemberS)
	if !ok {
		return nil, errors.New("fake: expected string pk in item")
	}
	f.items[pk.Value] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func TestDedupe(t *testing.T) {
	ctx := context.Background()
	s := New(newFake(), "test")

	if s.RecentlyFired(ctx, "QFA75") {
		t.Errorf("should not be fired before MarkFired")
	}
	if err := s.MarkFired(ctx, "QFA75", 10*time.Minute); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}
	if !s.RecentlyFired(ctx, "QFA75") {
		t.Errorf("should be fired after MarkFired")
	}
	if s.RecentlyFired(ctx, "OTHER1") {
		t.Errorf("other callsign should not register")
	}
}

func TestDedupeExpiry(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	s := New(f, "test")

	// Backdate the TTL so RecentlyFired treats it as expired.
	pk := "dedupe:QFA75"
	expired, _ := attributevalue.MarshalMap(item{PK: pk, ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	f.items[pk] = expired

	if s.RecentlyFired(ctx, "QFA75") {
		t.Errorf("expired item should not count as recently fired")
	}
}

func TestRouteCache(t *testing.T) {
	ctx := context.Background()
	s := New(newFake(), "test")

	if _, err := s.GetRoute(ctx, "QFA75"); !errors.Is(err, ErrMiss) {
		t.Errorf("expected ErrMiss, got %v", err)
	}
	r := adsbdb.Route{Callsign: "QFA75", CallsignIATA: "QF75", OriginICAO: "YSSY", DestICAO: "CYVR", OriginIATA: "SYD", DestIATA: "YVR"}
	if err := s.PutRoute(ctx, "QFA75", r, 24*time.Hour); err != nil {
		t.Fatalf("PutRoute: %v", err)
	}
	got, err := s.GetRoute(ctx, "QFA75")
	if err != nil {
		t.Fatalf("GetRoute: %v", err)
	}
	if got != r {
		t.Errorf("GetRoute mismatch:\n  got:  %+v\n  want: %+v", got, r)
	}
}

// Verify we pass the configured table name through.
func TestTableName(t *testing.T) {
	var captured string
	f := &fakeDDB{items: map[string]map[string]types.AttributeValue{}}
	wrapped := &nameCaptureDDB{inner: f, captured: &captured}
	s := New(wrapped, "my-table")
	_ = s.MarkFired(context.Background(), "X", time.Minute)
	if captured != "my-table" {
		t.Errorf("table name = %q, want my-table", captured)
	}
}

type nameCaptureDDB struct {
	inner    DDB
	captured *string
}

func (n *nameCaptureDDB) GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	*n.captured = aws.ToString(in.TableName)
	return n.inner.GetItem(ctx, in, opts...)
}

func (n *nameCaptureDDB) PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	*n.captured = aws.ToString(in.TableName)
	return n.inner.PutItem(ctx, in, opts...)
}
