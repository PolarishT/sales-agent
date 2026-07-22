package database

import (
	"context"
	"errors"
	"testing"
)

func TestReadinessPingAcceptsAnEmptyTable(t *testing.T) {
	readiness := &Readiness{exists: func(context.Context) (bool, error) { return false, nil }}
	if err := readiness.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestReadinessPingPropagatesQueryError(t *testing.T) {
	want := errors.New("query failed")
	readiness := &Readiness{exists: func(context.Context) (bool, error) { return false, want }}
	if err := readiness.Ping(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Ping() error = %v, want %v", err, want)
	}
}

func TestReadinessPingRejectsMissingClient(t *testing.T) {
	if err := NewReadiness(nil).Ping(context.Background()); err == nil {
		t.Fatal("Ping() error = nil, want missing client error")
	}
}
