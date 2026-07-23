package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/PolarishT/sales-agent/internal/agent"
	httpapi "github.com/PolarishT/sales-agent/internal/http"
	"github.com/PolarishT/sales-agent/internal/rag/domain"
	"github.com/PolarishT/sales-agent/internal/rag/ingestion"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/google/uuid"
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

type fakeIngestionAPI struct {
	submit func(context.Context, string, string, []byte) (domain.Submission, error)
	get    func(context.Context, uuid.UUID) (domain.Task, error)
}

func (f *fakeIngestionAPI) Submit(
	ctx context.Context,
	documentKey string,
	fileName string,
	raw []byte,
) (domain.Submission, error) {
	if f == nil || f.submit == nil {
		return domain.Submission{}, domain.NewError(
			domain.CodeIngestionUnavailable,
			"文档导入服务暂不可用",
			nil,
		)
	}
	return f.submit(ctx, documentKey, fileName, raw)
}

func (f *fakeIngestionAPI) Get(ctx context.Context, ingestionID uuid.UUID) (domain.Task, error) {
	if f == nil || f.get == nil {
		return domain.Task{}, domain.NewError(
			domain.CodeIngestionUnavailable,
			"文档导入服务暂不可用",
			nil,
		)
	}
	return f.get(ctx, ingestionID)
}

var _ ingestion.API = (*fakeIngestionAPI)(nil)

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

func TestRAGCreateIngestionAcceptsOneMultipartFile(t *testing.T) {
	createdAt := time.Date(2026, 7, 23, 10, 30, 0, 123456789, time.FixedZone("AEST", 10*60*60))
	ingestionID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	submitCalls := 0
	api := &fakeIngestionAPI{submit: func(
		ctx context.Context,
		documentKey string,
		fileName string,
		raw []byte,
	) (domain.Submission, error) {
		submitCalls++
		if ctx == nil {
			t.Fatal("Submit() context = nil")
		}
		if documentKey != "catalog/phone" || fileName != "phone.md" || string(raw) != "# 手机\n" {
			t.Fatalf("Submit() = %q, %q, %q", documentKey, fileName, raw)
		}
		return domain.Submission{Task: domain.Task{
			IngestionID: ingestionID,
			DocumentKey: documentKey,
			Status:      domain.StatusQueued,
			Stage:       domain.StageQueued,
			CreatedAt:   createdAt,
		}}, nil
	}}
	h := newTestServerWithIngestion(&fakeHealthChecker{}, api, 32)
	body, headers := multipartUpload(t, "catalog/phone", "phone.md", []byte("# 手机\n"))

	response := ut.PerformRequest(h.Engine, "POST", "/api/v1/rag/ingestions", body, headers...)

	if response.Code != 202 {
		t.Fatalf("status = %d, want 202; body = %s", response.Code, response.Body.Bytes())
	}
	if submitCalls != 1 {
		t.Fatalf("Submit() calls = %d, want 1", submitCalls)
	}
	assertJSONField(t, response.Body.Bytes(), "ingestion_id", ingestionID.String())
	assertJSONField(t, response.Body.Bytes(), "document_key", "catalog/phone")
	assertJSONField(t, response.Body.Bytes(), "status", string(domain.StatusQueued))
	assertJSONField(t, response.Body.Bytes(), "stage", string(domain.StageQueued))
	assertJSONField(t, response.Body.Bytes(), "created_at", createdAt.Format(time.RFC3339Nano))
	assertJSONBoolField(t, response.Body.Bytes(), "deduplicated", false)
}

func TestRAGCreateIngestionReturnsOKForSucceededDeduplication(t *testing.T) {
	api := &fakeIngestionAPI{submit: func(
		_ context.Context,
		documentKey string,
		_ string,
		_ []byte,
	) (domain.Submission, error) {
		return domain.Submission{
			Task: domain.Task{
				IngestionID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
				DocumentKey: documentKey,
				Status:      domain.StatusSucceeded,
				Stage:       domain.StageCompleted,
				CreatedAt:   time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC),
			},
			Deduplicated: true,
		}, nil
	}}
	h := newTestServerWithIngestion(&fakeHealthChecker{}, api, 32)
	body, headers := multipartUpload(t, "catalog/phone", "phone.md", []byte("# 手机\n"))

	response := ut.PerformRequest(h.Engine, "POST", "/api/v1/rag/ingestions", body, headers...)

	if response.Code != 200 {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.Bytes())
	}
	assertJSONBoolField(t, response.Body.Bytes(), "deduplicated", true)
}

func TestRAGCreateIngestionRequiresExactlyOneFile(t *testing.T) {
	api := &fakeIngestionAPI{submit: func(
		context.Context,
		string,
		string,
		[]byte,
	) (domain.Submission, error) {
		t.Fatal("Submit() must not be called")
		return domain.Submission{}, nil
	}}
	h := newTestServerWithIngestion(&fakeHealthChecker{}, api, 32)

	t.Run("missing", func(t *testing.T) {
		body, headers := multipartWithoutFile(t, "catalog/phone")
		response := ut.PerformRequest(h.Engine, "POST", "/api/v1/rag/ingestions", body, headers...)
		if response.Code != 400 {
			t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.Bytes())
		}
		assertJSONField(t, response.Body.Bytes(), "code", domain.CodeFileRequired)
	})

	t.Run("duplicate", func(t *testing.T) {
		body, headers := multipartWithFiles(t, "catalog/phone", []multipartTestFile{
			{name: "first.md", content: []byte("first")},
			{name: "second.md", content: []byte("second")},
		})
		response := ut.PerformRequest(h.Engine, "POST", "/api/v1/rag/ingestions", body, headers...)
		if response.Code != 400 {
			t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.Bytes())
		}
		assertJSONField(t, response.Body.Bytes(), "code", domain.CodeFileRequired)
	})
}

func TestRAGCreateIngestionReadsAtMostMaximumPlusOne(t *testing.T) {
	const maximum = int64(4)
	api := &fakeIngestionAPI{submit: func(
		_ context.Context,
		_ string,
		_ string,
		raw []byte,
	) (domain.Submission, error) {
		if got := int64(len(raw)); got != maximum+1 {
			t.Fatalf("Submit() bytes = %d, want %d", got, maximum+1)
		}
		return domain.Submission{}, domain.NewError(
			domain.CodeFileTooLarge,
			"Markdown 文件过大",
			nil,
		)
	}}
	h := newTestServerWithIngestion(&fakeHealthChecker{}, api, maximum)
	body, headers := multipartUpload(t, "catalog/phone", "phone.md", []byte("123456789"))

	response := ut.PerformRequest(h.Engine, "POST", "/api/v1/rag/ingestions", body, headers...)

	if response.Code != 413 {
		t.Fatalf("status = %d, want 413; body = %s", response.Code, response.Body.Bytes())
	}
	assertJSONField(t, response.Body.Bytes(), "code", domain.CodeFileTooLarge)
}

func TestRAGMultipartTCPFileBoundaryReachesHandler(t *testing.T) {
	const maximum = int64(5 << 20)
	ingestionID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	submittedSizes := make(chan int, 2)
	api := &fakeIngestionAPI{submit: func(
		_ context.Context,
		documentKey string,
		_ string,
		raw []byte,
	) (domain.Submission, error) {
		submittedSizes <- len(raw)
		if int64(len(raw)) > maximum {
			return domain.Submission{}, domain.NewError(
				domain.CodeFileTooLarge,
				"Markdown 文件不能超过 5 MiB",
				nil,
			)
		}
		return domain.Submission{Task: domain.Task{
			IngestionID: ingestionID,
			DocumentKey: documentKey,
			Status:      domain.StatusQueued,
			Stage:       domain.StageQueued,
			CreatedAt:   time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC),
		}}, nil
	}}
	baseURL := startTCPTestServer(t, api, maximum)

	t.Run("exactly five MiB", func(t *testing.T) {
		response := performMultipartHTTPRequest(
			t,
			baseURL,
			"catalog/exact",
			"exact.md",
			bytes.Repeat([]byte("x"), int(maximum)),
			nil,
		)
		defer response.Body.Close()
		if response.StatusCode != stdhttp.StatusAccepted {
			raw, _ := io.ReadAll(response.Body)
			t.Fatalf("status = %d, want 202; body = %q", response.StatusCode, raw)
		}
		if got := <-submittedSizes; got != int(maximum) {
			t.Fatalf("Submit() bytes = %d, want %d", got, maximum)
		}
	})

	t.Run("five MiB plus one", func(t *testing.T) {
		response := performMultipartHTTPRequest(
			t,
			baseURL,
			"catalog/too-large",
			"too-large.md",
			bytes.Repeat([]byte("x"), int(maximum+1)),
			nil,
		)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != stdhttp.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413; body = %q", response.StatusCode, raw)
		}
		requestID := response.Header.Get("X-Request-ID")
		if requestID == "" {
			t.Fatalf("X-Request-ID is empty; body = %q", raw)
		}
		assertJSONField(t, raw, "code", domain.CodeFileTooLarge)
		assertJSONField(t, raw, "request_id", requestID)
		if got := <-submittedSizes; got != int(maximum+1) {
			t.Fatalf("Submit() bytes = %d, want %d", got, maximum+1)
		}
	})
}

func TestRAGMultipartTCPRejectsOversizedEnvelopeBeforeSubmit(t *testing.T) {
	const maximum = int64(5 << 20)
	requestBodyLimit, err := httpapi.MultipartRequestBodySize(maximum)
	if err != nil {
		t.Fatal(err)
	}
	submitted := make(chan struct{}, 1)
	api := &fakeIngestionAPI{submit: func(
		context.Context,
		string,
		string,
		[]byte,
	) (domain.Submission, error) {
		submitted <- struct{}{}
		return domain.Submission{}, nil
	}}
	baseURL := startTCPTestServer(t, api, maximum)

	tests := []struct {
		name        string
		fileName    string
		extraFields map[string]string
	}{
		{
			name:     "filename",
			fileName: strings.Repeat("f", requestBodyLimit) + ".md",
		},
		{
			name:     "extra field",
			fileName: "small.md",
			extraFields: map[string]string{
				"padding": strings.Repeat("p", requestBodyLimit),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performMultipartHTTPRequest(
				t,
				baseURL,
				"catalog/envelope",
				test.fileName,
				[]byte("small"),
				test.extraFields,
			)
			defer response.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != stdhttp.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413; body = %q", response.StatusCode, raw)
			}
			if len(raw) > 4096 {
				t.Fatalf("error response bytes = %d, want at most 4096", len(raw))
			}
			select {
			case <-submitted:
				t.Fatal("oversized multipart envelope reached Submit")
			default:
			}
		})
	}
}

func TestRAGCreateIngestionMapsTypedErrors(t *testing.T) {
	tests := []struct {
		code       string
		wantStatus int
	}{
		{code: domain.CodeInvalidDocumentKey, wantStatus: 400},
		{code: domain.CodeFileRequired, wantStatus: 400},
		{code: domain.CodeUnsupportedFileType, wantStatus: 400},
		{code: domain.CodeInvalidMarkdownEncoding, wantStatus: 400},
		{code: domain.CodeEmptyDocument, wantStatus: 400},
		{code: domain.CodeInvalidIngestionID, wantStatus: 400},
		{code: domain.CodeFileTooLarge, wantStatus: 413},
		{code: domain.CodeIngestionInProgress, wantStatus: 409},
		{code: domain.CodeIngestionNotFound, wantStatus: 404},
		{code: domain.CodeIngestionQueueFull, wantStatus: 503},
		{code: domain.CodeIngestionUnavailable, wantStatus: 503},
		{code: domain.CodeDocumentStoreFailed, wantStatus: 500},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			api := &fakeIngestionAPI{submit: func(
				context.Context,
				string,
				string,
				[]byte,
			) (domain.Submission, error) {
				return domain.Submission{}, domain.NewError(tc.code, "稳定消息", errors.New("internal"))
			}}
			h := newTestServerWithIngestion(&fakeHealthChecker{}, api, 32)
			body, headers := multipartUpload(t, "catalog/phone", "phone.md", []byte("# 手机\n"))

			response := ut.PerformRequest(h.Engine, "POST", "/api/v1/rag/ingestions", body, headers...)

			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tc.wantStatus, response.Body.Bytes())
			}
			assertJSONField(t, response.Body.Bytes(), "code", tc.code)
			if strings.Contains(string(response.Body.Bytes()), "internal") {
				t.Fatal("response exposed wrapped error")
			}
		})
	}
}

func TestRAGGetIngestionParsesUUIDAndReturnsStatusFields(t *testing.T) {
	ingestionID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	createdAt := time.Date(2026, 7, 23, 10, 30, 0, 111111111, time.UTC)
	updatedAt := createdAt.Add(2*time.Minute + 222*time.Nanosecond)
	completedAt := updatedAt.Add(time.Second)
	api := &fakeIngestionAPI{get: func(_ context.Context, got uuid.UUID) (domain.Task, error) {
		if got != ingestionID {
			t.Fatalf("Get() id = %s, want %s", got, ingestionID)
		}
		return domain.Task{
			IngestionID:        ingestionID,
			DocumentKey:        "catalog/phone",
			Status:             domain.StatusFailed,
			Stage:              domain.StageEmbedding,
			SourceBytes:        99,
			ChunkCount:         3,
			EmbeddedChunkCount: 2,
			CreatedAt:          createdAt,
			UpdatedAt:          updatedAt,
			CompletedAt:        &completedAt,
			Failure: &domain.Failure{
				Code:    domain.CodeEmbeddingFailed,
				Message: "文本向量生成失败",
			},
		}, nil
	}}
	h := newTestServerWithIngestion(&fakeHealthChecker{}, api, 32)

	response := ut.PerformRequest(
		h.Engine,
		"GET",
		"/api/v1/rag/ingestions/"+ingestionID.String(),
		nil,
	)

	if response.Code != 200 {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.Bytes())
	}
	var payload struct {
		IngestionID        string `json:"ingestion_id"`
		DocumentKey        string `json:"document_key"`
		Status             string `json:"status"`
		Stage              string `json:"stage"`
		SourceBytes        int64  `json:"source_bytes"`
		ChunkCount         int32  `json:"chunk_count"`
		EmbeddedChunkCount int32  `json:"embedded_chunk_count"`
		CreatedAt          string `json:"created_at"`
		UpdatedAt          string `json:"updated_at"`
		CompletedAt        string `json:"completed_at"`
		Failure            struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"failure"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.IngestionID != ingestionID.String() ||
		payload.DocumentKey != "catalog/phone" ||
		payload.Status != string(domain.StatusFailed) ||
		payload.Stage != string(domain.StageEmbedding) ||
		payload.SourceBytes != 99 ||
		payload.ChunkCount != 3 ||
		payload.EmbeddedChunkCount != 2 ||
		payload.CreatedAt != createdAt.Format(time.RFC3339Nano) ||
		payload.UpdatedAt != updatedAt.Format(time.RFC3339Nano) ||
		payload.CompletedAt != completedAt.Format(time.RFC3339Nano) ||
		payload.Failure.Code != domain.CodeEmbeddingFailed ||
		payload.Failure.Message != "文本向量生成失败" {
		t.Fatalf("response = %+v", payload)
	}
}

func TestRAGGetIngestionOmitsAbsentOptionalFields(t *testing.T) {
	ingestionID := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	api := &fakeIngestionAPI{get: func(context.Context, uuid.UUID) (domain.Task, error) {
		now := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
		return domain.Task{
			IngestionID: ingestionID,
			DocumentKey: "catalog/phone",
			Status:      domain.StatusQueued,
			Stage:       domain.StageQueued,
			CreatedAt:   now,
			UpdatedAt:   now,
		}, nil
	}}
	h := newTestServerWithIngestion(&fakeHealthChecker{}, api, 32)

	response := ut.PerformRequest(
		h.Engine,
		"GET",
		"/api/v1/rag/ingestions/"+ingestionID.String(),
		nil,
	)

	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["completed_at"]; ok {
		t.Fatal("completed_at must be omitted")
	}
	if _, ok := payload["failure"]; ok {
		t.Fatal("failure must be omitted")
	}
}

func TestRAGGetIngestionMapsInvalidAndMissingIDs(t *testing.T) {
	api := &fakeIngestionAPI{get: func(context.Context, uuid.UUID) (domain.Task, error) {
		return domain.Task{}, domain.NewError(
			domain.CodeIngestionNotFound,
			"导入任务不存在",
			nil,
		)
	}}
	h := newTestServerWithIngestion(&fakeHealthChecker{}, api, 32)

	invalid := ut.PerformRequest(h.Engine, "GET", "/api/v1/rag/ingestions/not-a-uuid", nil)
	if invalid.Code != 400 {
		t.Fatalf("invalid status = %d, want 400", invalid.Code)
	}
	assertJSONField(t, invalid.Body.Bytes(), "code", domain.CodeInvalidIngestionID)

	missing := ut.PerformRequest(
		h.Engine,
		"GET",
		"/api/v1/rag/ingestions/550e8400-e29b-41d4-a716-446655440000",
		nil,
	)
	if missing.Code != 404 {
		t.Fatalf("missing status = %d, want 404", missing.Code)
	}
	assertJSONField(t, missing.Body.Bytes(), "code", domain.CodeIngestionNotFound)
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
	return newTestServerWithIngestion(checker, nil, 0)
}

func newTestServerWithIngestion(
	checker httpapi.HealthChecker,
	api ingestion.API,
	maxUploadBytes int64,
) *server.Hertz {
	h := httpapi.NewServer(httpapi.Options{
		Address: ":0",
		Dependencies: httpapi.Dependencies{
			HealthChecker:    checker,
			AgentRunner:      fakeAgentRunner{},
			ReadinessTimeout: time.Second,
			IngestionService: api,
			MaxUploadBytes:   maxUploadBytes,
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

func assertJSONBoolField(t *testing.T, body []byte, field string, want bool) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if got := payload[field]; got != want {
		t.Fatalf("%s = %v, want %t", field, got, want)
	}
}

type multipartTestFile struct {
	name    string
	content []byte
}

func multipartUpload(
	t *testing.T,
	key string,
	fileName string,
	content []byte,
) (*ut.Body, []ut.Header) {
	t.Helper()
	return multipartWithFiles(t, key, []multipartTestFile{{name: fileName, content: content}})
}

func multipartWithoutFile(t *testing.T, key string) (*ut.Body, []ut.Header) {
	t.Helper()
	return multipartWithFiles(t, key, nil)
}

func multipartWithFiles(
	t *testing.T,
	key string,
	files []multipartTestFile,
) (*ut.Body, []ut.Header) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("document_key", key); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &ut.Body{Body: bytes.NewReader(body.Bytes()), Len: body.Len()}, []ut.Header{{
		Key: "Content-Type", Value: writer.FormDataContentType(),
	}}
}

func startTCPTestServer(
	t *testing.T,
	api ingestion.API,
	maxUploadBytes int64,
) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	maxRequestBodySize, err := httpapi.MultipartRequestBodySize(maxUploadBytes)
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewServer(httpapi.Options{
		Listener:           listener,
		RequestTimeout:     5 * time.Second,
		ShutdownTimeout:    time.Second,
		MaxRequestBodySize: maxRequestBodySize,
		Dependencies: httpapi.Dependencies{
			HealthChecker:    &fakeHealthChecker{},
			AgentRunner:      fakeAgentRunner{},
			IngestionService: api,
			MaxUploadBytes:   maxUploadBytes,
			ReadinessTimeout: time.Second,
		},
	})
	register(h)
	runResult := make(chan error, 1)
	go func() {
		runResult <- h.Run()
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
		_ = listener.Close()
		select {
		case <-runResult:
		case <-time.After(time.Second):
			t.Error("Hertz test server did not stop")
		}
	})
	return "http://" + listener.Addr().String()
}

func performMultipartHTTPRequest(
	t *testing.T,
	baseURL string,
	documentKey string,
	fileName string,
	content []byte,
	extraFields map[string]string,
) *stdhttp.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("document_key", documentKey); err != nil {
		t.Fatal(err)
	}
	for key, value := range extraFields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := stdhttp.NewRequest(
		stdhttp.MethodPost,
		baseURL+"/api/v1/rag/ingestions",
		bytes.NewReader(body.Bytes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Expect", "100-continue")
	client := &stdhttp.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
