package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadUsesSafeDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapGetenv(map[string]string{"DATABASE_URL": "postgres://runtime"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != ":3000" || cfg.Environment != "development" {
		t.Fatalf("Address/Environment = %q/%q", cfg.Address, cfg.Environment)
	}
	if cfg.DatabaseMaxConns != 4 || cfg.DatabaseMinConns != 0 {
		t.Fatalf("connection limits = %d/%d", cfg.DatabaseMaxConns, cfg.DatabaseMinConns)
	}
	if cfg.DatabaseMaxConnIdleTime != 30*time.Second {
		t.Fatalf("DatabaseMaxConnIdleTime = %s, want 30s", cfg.DatabaseMaxConnIdleTime)
	}
}

func TestLoadUsesVercelPortAndEnvironment(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapGetenv(map[string]string{
		"DATABASE_URL": "postgres://runtime",
		"HTTP_ADDR":    ":3001",
		"PORT":         "8080",
		"VERCEL_ENV":   "preview",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != ":8080" || cfg.Environment != "preview" {
		t.Fatalf("Address/Environment = %q/%q", cfg.Address, cfg.Environment)
	}
}

func TestLoadPrefersExplicitApplicationEnvironment(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapGetenv(map[string]string{
		"APP_ENV": "staging", "DATABASE_URL": "postgres://runtime", "VERCEL_ENV": "preview",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "staging" {
		t.Fatalf("Environment = %q, want staging", cfg.Environment)
	}
}

func TestLoadOnlyRequiresRuntimeDatabaseURL(t *testing.T) {
	t.Parallel()
	_, err := Load(mapGetenv(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("Load() error = %v, want missing DATABASE_URL", err)
	}
}

func TestLoadParsesRuntimeTuning(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapGetenv(map[string]string{
		"DATABASE_URL": "postgres://runtime", "DB_MAX_CONNS": "8", "DB_MIN_CONNS": "1",
		"DB_MAX_CONN_IDLE_TIME": "45s", "DB_CONNECT_TIMEOUT": "7s", "REQUEST_TIMEOUT": "40s",
		"GRAPH_TIMEOUT": "75s", "SHUTDOWN_TIMEOUT": "12s", "LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseMaxConns != 8 || cfg.DatabaseMinConns != 1 || cfg.DatabaseConnectTimeout != 7*time.Second {
		t.Fatalf("unexpected runtime tuning: %+v", cfg)
	}
	if cfg.RequestTimeout != 40*time.Second || cfg.GraphTimeout != 75*time.Second || cfg.LogLevel != "debug" {
		t.Fatalf("unexpected timeouts/log level: %+v", cfg)
	}
}

func TestLoadRejectsInvalidRuntimeTuning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{name: "negative max connections", values: map[string]string{"DB_MAX_CONNS": "-1"}, want: "DB_MAX_CONNS"},
		{name: "minimum exceeds maximum", values: map[string]string{"DB_MAX_CONNS": "2", "DB_MIN_CONNS": "3"}, want: "DB_MIN_CONNS"},
		{name: "invalid duration", values: map[string]string{"GRAPH_TIMEOUT": "soon"}, want: "GRAPH_TIMEOUT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.values["DATABASE_URL"] = "postgres://runtime"
			_, err := Load(mapGetenv(tc.values))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func mapGetenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
