package bigmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/cloudwego/eino/components/embedding"
)

const (
	testAPIKey     = "unit-test-api-key"
	embeddingModel = "embedding-3"
	embeddingDims  = 1024
)

var _ embedding.Embedder = (*Embedder)(nil)

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestEmbedStringsSendsFixedContractAndRestoresOrder(t *testing.T) {
	ones := vector(1)
	twos := vector(2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/embeddings" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}

		var payload embeddingRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		want := embeddingRequest{
			Model:      embeddingModel,
			Input:      []string{"a", "b"},
			Dimensions: embeddingDims,
		}
		if !reflect.DeepEqual(payload, want) {
			t.Errorf("request payload = %#v, want %#v", payload, want)
		}

		writeResponse(t, writer, []responseItem{
			{Index: 1, Object: "embedding", Embedding: twos},
			{Index: 0, Object: "embedding", Embedding: ones},
		})
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	got, err := embedder.EmbedStrings(
		context.Background(),
		[]string{"a", "b"},
		embedding.WithModel("must-not-override-fixed-model"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !reflect.DeepEqual(got[0], ones) || !reflect.DeepEqual(got[1], twos) {
		t.Fatalf("embeddings were not restored to input order")
	}
}

func TestEmbedStringsCapsEveryBatchAt32(t *testing.T) {
	var mu sync.Mutex
	var batches []embeddingRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload embeddingRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		batches = append(batches, payload)
		mu.Unlock()

		items := make([]responseItem, len(payload.Input))
		for index := range payload.Input {
			items[index] = responseItem{Index: index, Object: "embedding", Embedding: vector(float64(index))}
		}
		writeResponse(t, writer, items)
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, func(config *Config) {
		// The configured range follows the supplier's 1..64 allowance, while
		// this adapter deliberately keeps the application contract at 32.
		config.BatchSize = 64
	})
	texts := make([]string, 33)
	for index := range texts {
		texts[index] = "chunk"
	}
	got, err := embedder.EmbedStrings(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(texts) {
		t.Fatalf("embedding count = %d, want %d", len(got), len(texts))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 2 || len(batches[0].Input) != 32 || len(batches[1].Input) != 1 {
		t.Fatalf("batch sizes = %v", []int{batchLength(batches, 0), batchLength(batches, 1)})
	}
	for _, batch := range batches {
		if batch.Model != embeddingModel || batch.Dimensions != embeddingDims {
			t.Fatalf("batch contract = %#v", batch)
		}
	}
}

func TestEmbedStringsRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		write func(http.ResponseWriter)
	}{
		{
			name:  "missing index",
			input: []string{"a", "b"},
			write: func(writer http.ResponseWriter) {
				writeResponse(t, writer, []responseItem{
					{Index: 0, Object: "embedding", Embedding: vector(1)},
				})
			},
		},
		{
			name:  "duplicate index",
			input: []string{"a", "b"},
			write: func(writer http.ResponseWriter) {
				writeResponse(t, writer, []responseItem{
					{Index: 0, Object: "embedding", Embedding: vector(1)},
					{Index: 0, Object: "embedding", Embedding: vector(2)},
				})
			},
		},
		{
			name:  "out of range index",
			input: []string{"a", "b"},
			write: func(writer http.ResponseWriter) {
				writeResponse(t, writer, []responseItem{
					{Index: 0, Object: "embedding", Embedding: vector(1)},
					{Index: 2, Object: "embedding", Embedding: vector(2)},
				})
			},
		},
		{
			name:  "wrong dimensions",
			input: []string{"a"},
			write: func(writer http.ResponseWriter) {
				writeResponse(t, writer, []responseItem{
					{Index: 0, Object: "embedding", Embedding: make([]float64, embeddingDims-1)},
				})
			},
		},
		{
			name:  "missing index field",
			input: []string{"a"},
			write: func(writer http.ResponseWriter) {
				_ = json.NewEncoder(writer).Encode(map[string]any{
					"data": []map[string]any{{
						"embedding": vector(1),
					}},
				})
			},
		},
		{
			name:  "null index field",
			input: []string{"a"},
			write: func(writer http.ResponseWriter) {
				_ = json.NewEncoder(writer).Encode(map[string]any{
					"data": []map[string]any{{
						"index":     nil,
						"embedding": vector(1),
					}},
				})
			},
		},
		{
			name:  "NaN",
			input: []string{"a"},
			write: func(writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, `{"data":[{"index":0,"embedding":[NaN]}]}`)
			},
		},
		{
			name:  "positive infinity",
			input: []string{"a"},
			write: func(writer http.ResponseWriter) {
				_, _ = io.WriteString(writer, `{"data":[{"index":0,"embedding":[1e999]}]}`)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				test.write(writer)
			}))
			defer server.Close()

			embedder := newTestEmbedder(t, server.URL, nil)
			_, err := embedder.EmbedStrings(context.Background(), test.input)
			assertCode(t, err, domain.CodeInvalidEmbeddingResponse)
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1", attempts.Load())
			}
		})
	}
}

func TestValidateResponseRejectsDecodedNonFiniteValues(t *testing.T) {
	for name, value := range map[string]float64{
		"NaN":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateResponse([]responseItem{{
				Index:     0,
				Embedding: append([]float64{value}, make([]float64, embeddingDims-1)...),
			}}, 1, embeddingDims)
			assertCode(t, err, domain.CodeInvalidEmbeddingResponse)
		})
	}
}

func TestEmbedStringsRejectsResponseLargerThan2MiB(t *testing.T) {
	const privateResponseMarker = "supplier-private-payload"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"padding":"`)
		_, _ = io.WriteString(writer, strings.Repeat("x", maxResponseBytes))
		_, _ = io.WriteString(writer, privateResponseMarker+`"}`)
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	_, err := embedder.EmbedStrings(context.Background(), []string{"a"})
	assertCode(t, err, domain.CodeInvalidEmbeddingResponse)
	if strings.Contains(err.Error(), privateResponseMarker) {
		t.Fatalf("error leaked response body: %v", err)
	}
}

func TestEmbedStringsRetries429ThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeResponse(t, writer, []responseItem{
			{Index: 0, Object: "embedding", Embedding: vector(1)},
		})
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	embedder.sleep = immediateSleep
	got, err := embedder.EmbedStrings(context.Background(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || attempts.Load() != 2 {
		t.Fatalf("result count/attempts = %d/%d", len(got), attempts.Load())
	}
}

func TestEmbedStringsRetriesTransportErrors(t *testing.T) {
	var attempts atomic.Int32
	embedder := newTestEmbedder(t, "https://example.invalid", nil)
	embedder.sleep = immediateSleep
	embedder.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("temporary transport failure")
		}
		body := responseJSON(t, []responseItem{
			{Index: 0, Object: "embedding", Embedding: vector(1)},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})

	got, err := embedder.EmbedStrings(context.Background(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || attempts.Load() != 3 {
		t.Fatalf("result count/attempts = %d/%d", len(got), attempts.Load())
	}
}

func TestEmbedStringsExhausts5xxWithoutLeakingSecrets(t *testing.T) {
	const privateResponseMarker = "supplier-private-error"
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(writer, privateResponseMarker)
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	embedder.sleep = immediateSleep
	_, err := embedder.EmbedStrings(context.Background(), []string{"a"})
	assertCode(t, err, domain.CodeEmbeddingFailed)
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
	if strings.Contains(err.Error(), privateResponseMarker) || strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("error leaked a supplier secret: %v", err)
	}
}

func TestEmbedStringsDoesNotRetryClientErrors(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const privateResponseMarker = "private client error response"
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, privateResponseMarker)
			}))
			defer server.Close()

			embedder := newTestEmbedder(t, server.URL, nil)
			embedder.sleep = immediateSleep
			_, err := embedder.EmbedStrings(context.Background(), []string{"a"})
			assertCode(t, err, domain.CodeEmbeddingFailed)
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1", attempts.Load())
			}
			if strings.Contains(err.Error(), privateResponseMarker) {
				t.Fatalf("error leaked response body: %v", err)
			}
		})
	}
}

func TestEmbedStringsDoesNotRetryStatusOutside5xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(600)
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	embedder.sleep = immediateSleep
	_, err := embedder.EmbedStrings(context.Background(), []string{"a"})
	assertCode(t, err, domain.CodeEmbeddingFailed)
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestEmbedStringsStopsWhenContextIsCanceled(t *testing.T) {
	var attempts atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		attempts.Add(1)
		close(started)
		<-release
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	embedder.sleep = immediateSleep
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := embedder.EmbedStrings(ctx, []string{"a"})
		result <- err
	}()
	<-started
	cancel()

	err := <-result
	close(release)
	assertCode(t, err, domain.CodeEmbeddingFailed)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestEmbedStringsRetriesClientTimeoutAtMostTwice(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		attempts.Add(1)
		<-release
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, func(config *Config) {
		config.Timeout = 10 * time.Millisecond
	})
	embedder.sleep = immediateSleep
	_, err := embedder.EmbedStrings(context.Background(), []string{"a"})
	close(release)
	assertCode(t, err, domain.CodeEmbeddingFailed)
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}

func TestEmbedStringsUsesBoundedExponentialBackoff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	var delays []time.Duration
	embedder.sleep = func(ctx context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return ctx.Err()
	}
	_, err := embedder.EmbedStrings(context.Background(), []string{"a"})
	assertCode(t, err, domain.CodeEmbeddingFailed)
	if len(delays) != 2 {
		t.Fatalf("delays = %v, want two delays", delays)
	}
	ranges := [][2]time.Duration{
		{100 * time.Millisecond, 125 * time.Millisecond},
		{200 * time.Millisecond, 250 * time.Millisecond},
	}
	for index, delay := range delays {
		if delay < ranges[index][0] || delay > ranges[index][1] {
			t.Fatalf("delay[%d] = %s, want %s..%s", index, delay, ranges[index][0], ranges[index][1])
		}
	}
}

func TestEmbedStringsStopsDuringRetryBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	embedder.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	_, err := embedder.EmbedStrings(ctx, []string{"a"})
	assertCode(t, err, domain.CodeEmbeddingFailed)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestEmbedStringsReturnsEmptyResultWithoutRequest(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		attempts.Add(1)
	}))
	defer server.Close()

	embedder := newTestEmbedder(t, server.URL, nil)
	got, err := embedder.EmbedStrings(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 || attempts.Load() != 0 {
		t.Fatalf("result/attempts = %#v/%d", got, attempts.Load())
	}
}

func TestNewEmbedderValidatesConfiguration(t *testing.T) {
	valid := testConfig("https://example.com/api/")
	embedder, err := NewEmbedder(valid)
	if err != nil {
		t.Fatal(err)
	}
	if embedder.endpoint != "https://example.com/api/embeddings" {
		t.Fatalf("endpoint = %q", embedder.endpoint)
	}
	if embedder.batchSize != maxBatchSize || embedder.maxRetries != defaultMaxRetries {
		t.Fatalf("batch size/retries = %d/%d", embedder.batchSize, embedder.maxRetries)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "base URL required", mutate: func(config *Config) { config.BaseURL = "" }},
		{name: "base URL valid", mutate: func(config *Config) { config.BaseURL = "://invalid" }},
		{name: "API key required", mutate: func(config *Config) { config.APIKey = "" }},
		{name: "fixed model", mutate: func(config *Config) { config.Model = "other" }},
		{name: "fixed dimensions", mutate: func(config *Config) { config.Dimensions = 512 }},
		{name: "batch below range", mutate: func(config *Config) { config.BatchSize = 0 }},
		{name: "batch above range", mutate: func(config *Config) { config.BatchSize = 65 }},
		{name: "timeout positive", mutate: func(config *Config) { config.Timeout = 0 }},
		{name: "retries nonnegative", mutate: func(config *Config) { config.MaxRetries = -1 }},
		{name: "retries capped", mutate: func(config *Config) { config.MaxRetries = 3 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig("https://example.com/api")
			test.mutate(&config)
			if _, err := NewEmbedder(config); err == nil {
				t.Fatal("NewEmbedder() accepted invalid config")
			}
		})
	}
}

func newTestEmbedder(t *testing.T, baseURL string, mutate func(*Config)) *Embedder {
	t.Helper()
	config := testConfig(baseURL)
	if mutate != nil {
		mutate(&config)
	}
	embedder, err := NewEmbedder(config)
	if err != nil {
		t.Fatal(err)
	}
	return embedder
}

func testConfig(baseURL string) Config {
	return Config{
		BaseURL:    baseURL,
		APIKey:     testAPIKey,
		Model:      embeddingModel,
		Dimensions: embeddingDims,
		BatchSize:  maxBatchSize,
		Timeout:    time.Second,
		MaxRetries: defaultMaxRetries,
	}
}

func vector(value float64) []float64 {
	result := make([]float64, embeddingDims)
	for index := range result {
		result[index] = value
	}
	return result
}

func writeResponse(t *testing.T, writer http.ResponseWriter, items []responseItem) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"object": "list",
		"data":   items,
		"usage": map[string]int{
			"prompt_tokens":     len(items),
			"completion_tokens": 0,
			"total_tokens":      len(items),
		},
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func responseJSON(t *testing.T, items []responseItem) string {
	t.Helper()
	var buffer bytes.Buffer
	writeResponse(t, &bufferResponseWriter{buffer: &buffer, header: make(http.Header)}, items)
	return buffer.String()
}

type bufferResponseWriter struct {
	buffer *bytes.Buffer
	header http.Header
}

func (writer *bufferResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *bufferResponseWriter) Write(value []byte) (int, error) {
	return writer.buffer.Write(value)
}

func (*bufferResponseWriter) WriteHeader(int) {}

func immediateSleep(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if !domain.IsCode(err, code) {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func batchLength(batches []embeddingRequest, index int) int {
	if index >= len(batches) {
		return -1
	}
	return len(batches[index].Input)
}
