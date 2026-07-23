package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesSafeDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapGetenv(map[string]string{"DATABASE_URL": "postgres://runtime", "ZHIPU_API_KEY": "test-key"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != ":3000" || cfg.Environment != "development" {
		t.Fatalf("Address/Environment = %q/%q", cfg.Address, cfg.Environment)
	}
}

func TestLoadUsesVercelPortAndEnvironment(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapGetenv(map[string]string{
		"DATABASE_URL":  "postgres://runtime",
		"ZHIPU_API_KEY": "test-key",
		"HTTP_ADDR":     ":3001",
		"PORT":          "8080",
		"VERCEL_ENV":    "preview",
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
		"APP_ENV": "staging", "DATABASE_URL": "postgres://runtime", "VERCEL_ENV": "preview", "ZHIPU_API_KEY": "test-key",
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
		"DATABASE_URL": "postgres://runtime", "ZHIPU_API_KEY": "test-key", "DB_CONNECT_TIMEOUT": "7s", "REQUEST_TIMEOUT": "40s",
		"GRAPH_TIMEOUT": "75s", "SHUTDOWN_TIMEOUT": "12s", "LOG_LEVEL": "debug",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseConnectTimeout != 7*time.Second {
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
		{name: "invalid duration", values: map[string]string{"GRAPH_TIMEOUT": "soon"}, want: "GRAPH_TIMEOUT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.values["DATABASE_URL"] = "postgres://runtime"
			tc.values["ZHIPU_API_KEY"] = "test-key"
			_, err := Load(mapGetenv(tc.values))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadIgnoresRemovedPoolTuning(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapGetenv(map[string]string{
		"DATABASE_URL":          "postgres://runtime",
		"ZHIPU_API_KEY":         "test-key",
		"DB_MAX_CONNS":          "invalid",
		"DB_MIN_CONNS":          "invalid",
		"DB_MAX_CONN_IDLE_TIME": "invalid",
		"DB_CONNECT_TIMEOUT":    "7s",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseConnectTimeout != 7*time.Second {
		t.Fatalf("DatabaseConnectTimeout = %s, want 7s", cfg.DatabaseConnectTimeout)
	}
	for _, name := range []string{"DatabaseMaxConns", "DatabaseMinConns", "DatabaseMaxConnIdleTime"} {
		if _, ok := reflect.TypeOf(cfg).FieldByName(name); ok {
			t.Fatalf("Config still exposes removed field %s", name)
		}
	}
}

func TestLoadUsesRAGIngestionDefaults(t *testing.T) {
	cfg, err := Load(mapGetenv(map[string]string{
		"DATABASE_URL":  "postgres://runtime",
		"ZHIPU_API_KEY": "test-key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RAGIngestionWorkers != 1 || cfg.RAGIngestionQueueCapacity != 8 {
		t.Fatalf("worker config = %d/%d", cfg.RAGIngestionWorkers, cfg.RAGIngestionQueueCapacity)
	}
	if cfg.RAGMaxUploadBytes != 5<<20 || cfg.ZhipuEmbeddingDimensions != 1024 || cfg.ZhipuEmbeddingBatchSize != 32 {
		t.Fatalf("rag config = %+v", cfg)
	}
}

func TestLoadRequiresZhipuAPIKey(t *testing.T) {
	_, err := Load(mapGetenv(map[string]string{"DATABASE_URL": "postgres://runtime"}))
	if err == nil || !strings.Contains(err.Error(), "ZHIPU_API_KEY") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsChangedEmbeddingSpace(t *testing.T) {
	for key, value := range map[string]string{
		"ZHIPU_EMBEDDING_MODEL":      "another-model",
		"ZHIPU_EMBEDDING_DIMENSIONS": "512",
	} {
		values := map[string]string{"DATABASE_URL": "postgres://runtime", "ZHIPU_API_KEY": "test-key", key: value}
		if _, err := Load(mapGetenv(values)); err == nil {
			t.Fatalf("Load() accepted %s=%s", key, value)
		}
	}
}

func mapGetenv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
