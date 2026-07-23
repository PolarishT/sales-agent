package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/PolarishT/sales-agent/internal/agent"
	httpapi "github.com/PolarishT/sales-agent/internal/http"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

type fakeHealthChecker struct {
	err   error
	calls int
}

func (f *fakeHealthChecker) Ping(context.Context) error {
	f.calls++
	return f.err
}

type fakeAgentRunner struct{}

func (fakeAgentRunner) Invoke(_ context.Context, request agent.Request) (agent.Response, error) {
	return agent.Response{Query: request.Query, Stage: "fake"}, nil
}

func TestGeneratedLiveRouteDoesNotCheckDatabase(t *testing.T) {
	checker := &fakeHealthChecker{err: errors.New("database is unavailable")}
	h := newTestServer(checker)
	response := ut.PerformRequest(h.Engine, "GET", "/api/v1/health/live", nil)
	if response.Code != 200 {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if checker.calls != 0 {
		t.Fatalf("Ping() calls = %d, want 0", checker.calls)
	}
	assertJSONField(t, response.Body.Bytes(), "status", "ok")
	assertJSONField(t, response.Body.Bytes(), "code", "LIVE")
}

func TestGeneratedReadyRoute(t *testing.T) {
	tests := []struct {
		name       string
		pingError  error
		wantStatus int
		wantCode   string
	}{
		{name: "database is ready", wantStatus: 200, wantCode: "READY"},
		{name: "database is unavailable", pingError: errors.New("offline"), wantStatus: 503, wantCode: "DATABASE_UNAVAILABLE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := &fakeHealthChecker{err: tc.pingError}
			h := newTestServer(checker)
			response := ut.PerformRequest(h.Engine, "GET", "/api/v1/health/ready", nil)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tc.wantStatus)
			}
			if checker.calls != 1 {
				t.Fatalf("Ping() calls = %d, want 1", checker.calls)
			}
			assertJSONField(t, response.Body.Bytes(), "code", tc.wantCode)
		})
	}
}

func TestGeneratedRoutesExcludePing(t *testing.T) {
	h := newTestServer(&fakeHealthChecker{})
	response := ut.PerformRequest(h.Engine, "GET", "/ping", nil)
	if response.Code != 404 {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestRAGIngestionRoutesAreGenerated(t *testing.T) {
	h := newTestServer(&fakeHealthChecker{})

	create := ut.PerformRequest(h.Engine, "POST", "/api/v1/rag/ingestions", nil)
	if create.Code == 404 {
		t.Fatal("POST /api/v1/rag/ingestions is not registered")
	}

	get := ut.PerformRequest(h.Engine, "GET", "/api/v1/rag/ingestions/550e8400-e29b-41d4-a716-446655440000", nil)
	if get.Code == 404 {
		t.Fatal("GET /api/v1/rag/ingestions/:ingestion_id is not registered")
	}
}

func TestStableTransportErrors(t *testing.T) {
	h := newTestServer(&fakeHealthChecker{})
	notFound := ut.PerformRequest(h.Engine, "GET", "/missing", nil)
	if notFound.Code != 404 {
		t.Fatalf("404 status = %d, want 404", notFound.Code)
	}
	requestID := string(notFound.Result().Header.Peek("X-Request-ID"))
	if requestID == "" {
		t.Fatal("X-Request-ID header is empty")
	}
	assertJSONField(t, notFound.Body.Bytes(), "code", "NOT_FOUND")
	assertJSONField(t, notFound.Body.Bytes(), "request_id", requestID)

	wrongMethod := ut.PerformRequest(h.Engine, "POST", "/api/v1/health/live", nil)
	if wrongMethod.Code != 405 {
		t.Fatalf("405 status = %d, want 405", wrongMethod.Code)
	}
	assertJSONField(t, wrongMethod.Body.Bytes(), "code", "METHOD_NOT_ALLOWED")
}

func TestRecoveryDoesNotExposePanic(t *testing.T) {
	h := newTestServer(&fakeHealthChecker{})
	h.GET("/panic", func(_ context.Context, _ *app.RequestContext) {
		panic("sensitive internal detail")
	})
	response := ut.PerformRequest(h.Engine, "GET", "/panic", nil)
	if response.Code != 500 {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	assertJSONField(t, response.Body.Bytes(), "code", "INTERNAL_ERROR")
	if strings.Contains(string(response.Body.Bytes()), "sensitive internal detail") {
		t.Fatal("response exposed panic detail")
	}
}

func TestReadyWithoutDependenciesUsesStableError(t *testing.T) {
	h := httpapi.NewServer(httpapi.Options{Address: ":0"})
	register(h)
	response := ut.PerformRequest(h.Engine, "GET", "/api/v1/health/ready", nil)
	if response.Code != 503 {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	assertJSONField(t, response.Body.Bytes(), "code", "DATABASE_UNAVAILABLE")
}

func newTestServer(checker httpapi.HealthChecker) *server.Hertz {
	h := httpapi.NewServer(httpapi.Options{
		Address: ":0",
		Dependencies: httpapi.Dependencies{
			HealthChecker:    checker,
			AgentRunner:      fakeAgentRunner{},
			ReadinessTimeout: time.Second,
		},
	})
	register(h)
	return h
}

func assertJSONField(t *testing.T, body []byte, field, want string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if got := payload[field]; got != want {
		t.Fatalf("%s = %v, want %q", field, got, want)
	}
}
