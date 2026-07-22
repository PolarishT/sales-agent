package postgres

import (
	"context"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	options := Options{MaxConns: 4, MinConns: 1, MaxConnIdleTime: 30 * time.Second}
	config, err := parseConfig("postgresql://user:password@localhost:5432/shop?sslmode=disable", options)
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}
	if config.MaxConns != options.MaxConns || config.MinConns != options.MinConns {
		t.Fatalf("connection limits = %d/%d", config.MaxConns, config.MinConns)
	}
	if config.MaxConnIdleTime != options.MaxConnIdleTime || config.AfterConnect == nil {
		t.Fatal("pool idle time or pgvector registration hook is missing")
	}
}

func TestNewPoolRejectsEmptyURL(t *testing.T) {
	pool, err := NewPool(context.Background(), "", Options{MaxConns: 4})
	if err == nil || pool != nil {
		t.Fatalf("NewPool() = %v, %v; want nil, error", pool, err)
	}
}

func TestParseConfigRejectsInvalidPoolSize(t *testing.T) {
	tests := []Options{{MaxConns: 0}, {MaxConns: 2, MinConns: 3}}
	for _, options := range tests {
		if _, err := parseConfig("postgresql://user:password@localhost:5432/shop?sslmode=disable", options); err == nil {
			t.Fatalf("parseConfig(%+v) error = nil", options)
		}
	}
}
