# RAG Markdown Offline Ingestion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 提供基于 Thrift/Hertz 的 Markdown 异步导入 API，将全局商品知识解析、过滤、结构化切分、调用智谱 `embedding-3` 后原子写入 Neon PostgreSQL 原文与 `vector(1024)`。

**Architecture:** HTTP Handler 只处理 multipart 协议并调用 `internal/rag/ingestion.Service`；Service 使用有界进程内 worker 队列提交 `ingestion_id`，普通 Go `Pipeline` 顺序调用 Goldmark Parser、Normalizer、结构感知 Splitter、智谱 Embedder 和 Ent Repository。新版本在全部向量生成后通过短事务激活，并级联删除同一 `document_key` 的旧版本。

**Tech Stack:** Go 1.25.5、hz v0.9.7、Hertz v0.10.5、Thrift、Ent v0.14.6、lib/pq v1.12.3、Goldmark v1.8.4、Eino v0.9.12 `embedding.Embedder` 接口、pgvector-go v0.4.0、Neon PostgreSQL、智谱 `embedding-3`。

## Global Constraints

- 所有工作在用户指定的 `main` 分支执行，不创建 worktree。
- 开始实施前记录并保护用户现有未提交的 `main.go` 大小写修改；不得覆盖、还原或混入其他任务提交。
- `idl` 是 HTTP 路由、普通表单字段、请求和响应的契约源；multipart 文件字段通过 IDL 注释和 Handler 的 `FormFile`/`MultipartForm` 协议适配共同落实。
- `biz/model`、`biz/router`、`router_gen.go` 是 hz 生成物，禁止手工修改。
- `ent` 中除 `ent/schema` 和 `ent/generate.go` 外均为生成物，禁止手工修改。
- 后续 IDL 变化只使用 `hz update`，不重新执行 `hz new`。
- 离线导入链路使用普通 Go Pipeline，禁止构造 Eino Graph；在线 `internal/agent` 保持不变。
- 上传仅接受一个 `.md` 或 `.markdown` 文件，规范化前读取硬上限为 5,242,880 字节。
- 队列默认 1 个 worker、8 个等待任务；队列满必须非阻塞返回 `INGESTION_QUEUE_FULL`。
- 队列只保存 `ingestion_id`，原始 Markdown 在入队前保存到 PostgreSQL。
- Embedding 固定使用 `https://open.bigmodel.cn/api/paas/v4/embeddings`、模型 `embedding-3`、维度 1024、批量 32。
- PostgreSQL 列必须是 `vector(1024)`；使用 pgvector-go Scanner/Valuer。
- 新版本成功后立即删除该 `document_key` 的全部旧版本；失败时旧成功版本继续生效。
- 不新增根 `migrations` 目录，不在启动时调用 `client.Schema.Create`；DDL 放在 `docs/database` 供仓库外受控执行。
- 不创建 HNSW；本增量只准备精确向量检索所需数据。
- 真实 `DATABASE_URL`、`ZHIPU_API_KEY`、Markdown 内容、向量和供应商原始错误体不得进入 Git 历史或普通日志。
- 每项生产行为必须先有能因缺少目标行为而失败的测试；不得降低断言强度换取通过。

---

## File Map

### Contract and generated adapters

- Create `idl/rag_ingestion.thrift`: 创建和查询导入任务的唯一 Thrift 契约。
- Modify `Makefile`: 依次更新 health 与 rag ingestion IDL，再生成 Ent。
- Generate `biz/model/rag_ingestion/rag_ingestion.go`: hz 生成请求与响应模型。
- Generate `biz/router/rag_ingestion/*.go`: hz 生成路由。
- Generate `biz/handler/rag_ingestion/rag_ingestion_service.go`: hz 创建 Handler 文件，随后仅在该协议适配层实现业务调用。
- Generate `biz/router/register.go`, `router_gen.go`: hz 更新路由注册。

### Domain and deterministic document pipeline

- Create `internal/rag/domain/task.go`: 状态、阶段、任务、提交结果和稳定错误。
- Create `internal/rag/domain/document.go`: Upload、MarkdownBlock、NormalizedDocument、Chunk、EmbeddedChunk。
- Create `internal/rag/markdown/parser.go`: Goldmark AST 解析和行号映射。
- Create `internal/rag/markdown/filter.go`: 确定性过滤。
- Create `internal/rag/markdown/normalizer.go`: 将过滤结果转换为统一文本格式。
- Create `internal/rag/markdown/parser_test.go`: Markdown AST 结构与行号测试。
- Create `internal/rag/markdown/filter_test.go`: HTML、图片和空内容过滤测试。
- Create `internal/rag/markdown/normalizer_test.go`: 列表、表格、链接、引用和代码规范化测试。
- Create `internal/rag/splitter/estimator.go`: 可替换的本地 Token 估算。
- Create `internal/rag/splitter/splitter.go`: 结构感知切分和滑窗兜底。
- Create `internal/rag/splitter/splitter_test.go`: Chunk 边界、重叠、表格、列表、代码块和 Unicode 测试。

### External capability adapters

- Create `internal/platform/bigmodel/embedder.go`: 智谱 Embedding HTTP 客户端，实现 Eino `embedding.Embedder`。
- Create `internal/platform/bigmodel/embedder_test.go`: `httptest.Server` 契约、校验和重试测试。
- Create `ent/schema/ragdocument.go`: `rag_documents` Ent 映射。
- Create `ent/schema/ragdocumentversion.go`: `rag_document_versions` Ent 映射。
- Create `ent/schema/ragchunk.go`: `rag_chunks` 与 `vector(1024)` Ent 映射。
- Create `ent/schema/rag_ingestion_test.go`: Schema 描述符测试。
- Modify `ent/generate.go`: 启用 `sql/lock` 和 `sql/upsert` 生成能力。
- Generate `ent/*`: 新实体、查询、事务、锁和迁移元数据。
- Create `internal/platform/database/ingestion_repository.go`: Ent 查询、行锁和激活事务。
- Create `internal/platform/database/ingestion_repository_test.go`: SQL mock 与事务行为测试。
- Create `docs/database/rag_ingestion.sql`: 受控外部建表 DDL。

### Application orchestration

- Create `internal/rag/ingestion/contracts.go`: Repository、Parser、Splitter、Embedder、Queue 接口。
- Create `internal/rag/ingestion/pipeline.go`: 普通 Go 顺序 Pipeline。
- Create `internal/rag/ingestion/pipeline_test.go`: 阶段顺序与错误分类测试。
- Create `internal/rag/ingestion/queue.go`: 有界 reservation 队列。
- Create `internal/rag/ingestion/queue_test.go`: 容量、释放、关闭和 panic 隔离测试。
- Create `internal/rag/ingestion/service.go`: 上传规范化、去重、冲突、任务创建与状态查询。
- Create `internal/rag/ingestion/service_test.go`: Submit/Get 行为测试。
- Create `internal/rag/ingestion/worker.go`: worker 生命周期、超时与失败落库。
- Create `internal/rag/ingestion/worker_test.go`: 串行执行、超时和优雅关闭测试。

### Runtime integration and documentation

- Modify `internal/config/config.go`: 加载并校验导入和智谱配置。
- Modify `internal/config/config_test.go`: 默认值、必填项、整数和固定模型/维度测试。
- Modify `internal/http/dependencies.go`: 注入导入 API。
- Modify `biz/handler/rag_ingestion/rag_ingestion_service.go`: multipart 读取、错误映射和响应转换。
- Modify `router_test.go`: 路由与 multipart API 集成测试。
- Modify `main.go`: 创建 Embedder、Repository、Pipeline、Service、Worker，按顺序启动和关闭。
- Modify `main_test.go`: worker shutdown hook 与已有 listener 行为测试。
- Modify `.env.example`: 只增加无秘密的配置占位。
- Modify `README.md`: DDL、环境变量、启动和 curl 示例。
- Modify `AGENTS.md`: 将当前阶段更新为已验收基础骨架后的基础 RAG 离线增量。
- Modify `go.mod`, `go.sum`: 固定 Goldmark、pgvector-go 和 SQL mock 依赖。

## Spec Coverage Index

| Design requirement | Implemented by |
| --- | --- |
| Thrift routes and stable JSON contract | Tasks 1 and 9 |
| Multipart, 5 MiB, UTF-8, BOM/newline normalization and SHA-256 | Tasks 2 and 9 |
| Global `document_key`, deduplication, conflict and replacement | Tasks 7 and 8 |
| Goldmark AST parsing | Task 4 |
| Deterministic filtering and unified format | Task 4 |
| Structure-aware 512/64 Chunking and sliding fallback | Task 5 |
| Zhipu `embedding-3`, 1024 dimensions and batch 32 | Task 6 |
| Ent mapping, pgvector and external DDL | Task 3 |
| Atomic activation and immediate old-version deletion | Task 7 |
| Plain Go Pipeline without Eino Graph | Task 8 |
| 1 worker, 8 waiters, non-blocking full queue and shutdown | Tasks 8 and 9 |
| Runtime configuration, secret handling and structured logs | Tasks 2, 8 and 10 |
| Vercel/process-local durability limitation | Task 10 |
| Required generation, test, race, vet and build verification | Task 10 |

---

### Task 1: Add the Thrift Contract and Generated Routes

**Files:**
- Create: `idl/rag_ingestion.thrift`
- Modify: `Makefile`
- Generate: `biz/model/rag_ingestion/rag_ingestion.go`
- Generate: `biz/router/rag_ingestion/*.go`
- Generate then modify Handler only: `biz/handler/rag_ingestion/rag_ingestion_service.go`
- Generate: `biz/router/register.go`
- Generate: `router_gen.go`
- Test: `router_test.go`

**Interfaces:**
- Consumes: existing `/api/v1` convention and `httpapi.WriteError`.
- Produces: `POST /api/v1/rag/ingestions`, `GET /api/v1/rag/ingestions/:ingestion_id`, and generated `rag_ingestion` request/response Go types.

- [ ] **Step 1: Add a failing generated-route assertion**

Append to `router_test.go`:

```go
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
```

- [ ] **Step 2: Run the route test and verify RED**

Run:

```bash
go test ./... -run TestRAGIngestionRoutesAreGenerated
```

Expected: FAIL because both routes currently return 404.

- [ ] **Step 3: Create the complete Thrift IDL**

Create `idl/rag_ingestion.thrift`:

```thrift
namespace go rag_ingestion

// CreateIngestion consumes multipart/form-data.
// The form field document_key is declared below.
// The transport must also contain exactly one file field named "file".
// The file must end in .md or .markdown and must not exceed 5 MiB.
struct CreateIngestionRequest {
    1: required string DocumentKey (
        api.form="document_key",
        go.tag="json:\"document_key\""
    )
}

struct CreateIngestionResponse {
    1: required string IngestionID (go.tag="json:\"ingestion_id\"")
    2: required string DocumentKey (go.tag="json:\"document_key\"")
    3: required string Status (go.tag="json:\"status\"")
    4: required string Stage (go.tag="json:\"stage\"")
    5: required bool Deduplicated (go.tag="json:\"deduplicated\"")
    6: required string CreatedAt (go.tag="json:\"created_at\"")
}

struct GetIngestionRequest {
    1: required string IngestionID (
        api.path="ingestion_id",
        go.tag="json:\"ingestion_id\""
    )
}

struct IngestionFailure {
    1: required string Code (go.tag="json:\"code\"")
    2: required string Message (go.tag="json:\"message\"")
}

struct GetIngestionResponse {
    1: required string IngestionID (go.tag="json:\"ingestion_id\"")
    2: required string DocumentKey (go.tag="json:\"document_key\"")
    3: required string Status (go.tag="json:\"status\"")
    4: required string Stage (go.tag="json:\"stage\"")
    5: required i64 SourceBytes (go.tag="json:\"source_bytes\"")
    6: required i32 ChunkCount (go.tag="json:\"chunk_count\"")
    7: required i32 EmbeddedChunkCount (go.tag="json:\"embedded_chunk_count\"")
    8: required string CreatedAt (go.tag="json:\"created_at\"")
    9: required string UpdatedAt (go.tag="json:\"updated_at\"")
    10: optional string CompletedAt (go.tag="json:\"completed_at,omitempty\"")
    11: optional IngestionFailure Failure (go.tag="json:\"failure,omitempty\"")
}

service RAGIngestionService {
    CreateIngestionResponse CreateIngestion(
        1: CreateIngestionRequest request
    ) (api.post="/api/v1/rag/ingestions")

    GetIngestionResponse GetIngestion(
        1: GetIngestionRequest request
    ) (api.get="/api/v1/rag/ingestions/:ingestion_id")
}
```

- [ ] **Step 4: Update the reproducible generation command**

Replace `generate` in `Makefile` with:

```make
generate:
	$(HZ) update --idl idl/health.thrift --sort_router
	$(HZ) update --idl idl/rag_ingestion.thrift --sort_router
	go generate ./ent
```

- [ ] **Step 5: Generate and inspect the output**

Run:

```bash
make generate
rg -n 'rag/ingestions|RAGIngestionService' biz router_gen.go
```

Expected: hz creates the `rag_ingestion` model, Handler and router; generated router contains both POST and GET routes.

- [ ] **Step 6: Replace generated Handler bodies with a stable unavailable response**

Keep hz-generated package names and function signatures, but make both Handler functions call:

```go
httpapi.WriteError(requestContext, consts.StatusServiceUnavailable, "unavailable", "INGESTION_UNAVAILABLE", "文档导入服务暂不可用")
```

This makes the generated routes observable before the application service exists and confines the temporary behavior to the allowed Handler layer.

- [ ] **Step 7: Run route and generation tests and verify GREEN**

Run:

```bash
go test ./... -run TestRAGIngestionRoutesAreGenerated
make generate
git diff --check
```

Expected: route test PASS; the second `make generate` produces no unexpected structural changes; diff check PASS.

- [ ] **Step 8: Commit the contract slice**

```bash
git add idl/rag_ingestion.thrift Makefile biz/model/rag_ingestion biz/router/rag_ingestion biz/handler/rag_ingestion biz/router/register.go router_gen.go router_test.go
git commit -m "feat: 增加 RAG 离线导入 IDL"
```

---

### Task 2: Define Domain Types, Upload Validation, and Runtime Configuration

**Files:**
- Create: `internal/rag/domain/task.go`
- Create: `internal/rag/domain/document.go`
- Create: `internal/rag/domain/upload.go`
- Create: `internal/rag/domain/upload_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `.env.example`

**Interfaces:**
- Consumes: raw uploaded filename, Markdown bytes and environment accessor.
- Produces: `domain.NormalizeUpload(fileName string, raw []byte, maxBytes int64) (domain.Upload, error)`, `domain.ValidateDocumentKey(string) (string, error)`, stable `domain.Task`, `domain.Failure`, and complete runtime config.

- [ ] **Step 1: Write failing domain validation tests**

Create `internal/rag/domain/upload_test.go`:

```go
package domain

import (
	"bytes"
	"testing"
)

func TestNormalizeUploadCanonicalizesBOMAndNewlines(t *testing.T) {
	upload, err := NormalizeUpload("CATALOG.MD", append([]byte{0xef, 0xbb, 0xbf}, []byte("# 商品\r\n颜色：黑色\r")...), 5<<20)
	if err != nil {
		t.Fatal(err)
	}
	if upload.FileName != "CATALOG.MD" {
		t.Fatalf("FileName = %q", upload.FileName)
	}
	if got := string(upload.Markdown); got != "# 商品\n颜色：黑色\n" {
		t.Fatalf("Markdown = %q", got)
	}
	if upload.ContentHash == "" || upload.SourceBytes != int64(len(upload.Markdown)) {
		t.Fatalf("upload metadata = %+v", upload)
	}
}

func TestNormalizeUploadRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name string
		file string
		raw  []byte
		code string
	}{
		{name: "extension", file: "catalog.txt", raw: []byte("text"), code: CodeUnsupportedFileType},
		{name: "empty", file: "catalog.md", raw: []byte(" \n"), code: CodeEmptyDocument},
		{name: "nul", file: "catalog.md", raw: []byte("a\x00b"), code: CodeInvalidMarkdownEncoding},
		{name: "utf8", file: "catalog.md", raw: []byte{0xff}, code: CodeInvalidMarkdownEncoding},
		{name: "large", file: "catalog.md", raw: bytes.Repeat([]byte("x"), 11), code: CodeFileTooLarge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeUpload(tc.file, tc.raw, 10)
			if !IsCode(err, tc.code) {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestValidateDocumentKey(t *testing.T) {
	got, err := ValidateDocumentKey(" 商品/iphone-16_pro ")
	if err != nil || got != "商品/iphone-16_pro" {
		t.Fatalf("ValidateDocumentKey() = %q, %v", got, err)
	}
	for _, key := range []string{"", "has space", "bad!", strings.Repeat("a", 129)} {
		if _, err := ValidateDocumentKey(key); !IsCode(err, CodeInvalidDocumentKey) {
			t.Fatalf("key %q error = %v", key, err)
		}
	}
}
```

- [ ] **Step 2: Write failing config tests**

Extend `internal/config/config_test.go` with:

```go
func TestLoadUsesRAGIngestionDefaults(t *testing.T) {
	cfg, err := Load(mapGetenv(map[string]string{
		"DATABASE_URL": "postgres://runtime",
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
```

Update every existing successful `Load` fixture to include `"ZHIPU_API_KEY": "test-key"` so tests express the newly enabled feature requirement.

- [ ] **Step 3: Run focused tests and verify RED**

Run:

```bash
go test ./internal/rag/domain ./internal/config
```

Expected: FAIL because domain types/functions and new Config fields do not exist.

- [ ] **Step 4: Implement stable domain errors and task types**

Create `internal/rag/domain/task.go` with typed string constants:

```go
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type Status string
type Stage string

const (
	StatusQueued    Status = "QUEUED"
	StatusRunning   Status = "RUNNING"
	StatusSucceeded Status = "SUCCEEDED"
	StatusFailed    Status = "FAILED"

	StageQueued      Stage = "QUEUED"
	StageParsing     Stage = "PARSING"
	StageFiltering   Stage = "FILTERING"
	StageNormalizing Stage = "NORMALIZING"
	StageChunking    Stage = "CHUNKING"
	StageEmbedding   Stage = "EMBEDDING"
	StageStoring     Stage = "STORING"
	StageCompleted   Stage = "COMPLETED"
)

const (
	CodeInvalidDocumentKey       = "INVALID_DOCUMENT_KEY"
	CodeFileRequired             = "FILE_REQUIRED"
	CodeUnsupportedFileType      = "UNSUPPORTED_FILE_TYPE"
	CodeFileTooLarge             = "FILE_TOO_LARGE"
	CodeInvalidMarkdownEncoding  = "INVALID_MARKDOWN_ENCODING"
	CodeEmptyDocument            = "EMPTY_DOCUMENT"
	CodeInvalidIngestionID       = "INVALID_INGESTION_ID"
	CodeIngestionInProgress      = "INGESTION_IN_PROGRESS"
	CodeIngestionNotFound        = "INGESTION_NOT_FOUND"
	CodeIngestionQueueFull       = "INGESTION_QUEUE_FULL"
	CodeIngestionUnavailable     = "INGESTION_UNAVAILABLE"
	CodeMarkdownParseFailed      = "MARKDOWN_PARSE_FAILED"
	CodeNoIndexableContent       = "NO_INDEXABLE_CONTENT"
	CodeDocumentSplitFailed      = "DOCUMENT_SPLIT_FAILED"
	CodeEmbeddingFailed          = "EMBEDDING_FAILED"
	CodeInvalidEmbeddingResponse = "INVALID_EMBEDDING_RESPONSE"
	CodeDocumentStoreFailed      = "DOCUMENT_STORE_FAILED"
	CodeProcessInterrupted       = "PROCESS_INTERRUPTED"
	CodeInternalProcessing       = "INTERNAL_PROCESSING_ERROR"
)

type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

func NewError(code, message string, err error) error {
	return &Error{Code: code, Message: message, Err: err}
}

func IsCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

type Failure struct {
	Code    string
	Message string
}

type Task struct {
	IngestionID       uuid.UUID
	DocumentKey       string
	Status            Status
	Stage             Stage
	SourceBytes       int64
	ChunkCount        int
	EmbeddedChunkCount int
	Failure           *Failure
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}

type Submission struct {
	Task         Task
	Deduplicated bool
}
```

- [ ] **Step 5: Implement document and upload types**

Create `internal/rag/domain/document.go` with:

```go
type BlockType string

const (
	BlockParagraph BlockType = "paragraph"
	BlockList      BlockType = "list"
	BlockTable     BlockType = "table"
	BlockQuote     BlockType = "quote"
	BlockCode      BlockType = "code"
	BlockRawHTML   BlockType = "raw_html"
	BlockImage     BlockType = "image"
)

type MarkdownBlock struct {
	Type        BlockType
	HeadingPath []string
	RawContent  string
	Content     string
	StartLine   int
	EndLine     int
	Ordinal     int
}

type ParsedDocument struct {
	Blocks []MarkdownBlock
}

type NormalizedDocument struct {
	Blocks []MarkdownBlock
}

type ChunkConfig struct {
	ChunkSize    int
	ChunkOverlap int
}

type Chunk struct {
	ChunkIndex       int
	Content          string
	EmbeddingContent string
	HeadingPath      []string
	StartLine        int
	EndLine          int
	EstimatedTokens  int
	ContentHash      string
}

type EmbeddedChunk struct {
	Chunk
	Vector []float64
}
```

Create `internal/rag/domain/upload.go` with:

```go
type Upload struct {
	DocumentKey string
	FileName    string
	Markdown    []byte
	ContentHash string
	SourceBytes int64
}
```

Implement `NormalizeUpload` using `utf8.Valid`, `bytes.TrimPrefix`, `bytes.ReplaceAll`, `sha256.Sum256`, `hex.EncodeToString`, and an exact `len(raw) > maxBytes` comparison. Implement `ValidateDocumentKey` with `utf8.RuneCountInString`, `unicode.IsLetter`, `unicode.IsDigit`, and only `.`, `_`, `/`, `-` as additional accepted runes.

- [ ] **Step 6: Implement complete runtime config**

Add these exact fields to `config.Config`:

```go
RAGIngestionWorkers       int
RAGIngestionQueueCapacity int
RAGMaxUploadBytes         int64
RAGIngestionTimeout       time.Duration
ZhipuAPIKey               string
ZhipuBaseURL              string
ZhipuEmbeddingModel       string
ZhipuEmbeddingDimensions  int
ZhipuEmbeddingBatchSize   int
ZhipuEmbeddingTimeout     time.Duration
```

Use defaults:

```go
1
8
int64(5 << 20)
10 * time.Minute
"https://open.bigmodel.cn/api/paas/v4"
"embedding-3"
1024
32
30 * time.Second
```

Add `positiveIntValue` and `positiveInt64Value` helpers mirroring `durationValue`. Reject missing `ZHIPU_API_KEY`, any model other than `embedding-3`, any dimension other than `1024`, and a batch size outside `1..64`.

- [ ] **Step 7: Update environment example**

Append only placeholders/defaults to `.env.example`:

```env
RAG_INGESTION_WORKERS=1
RAG_INGESTION_QUEUE_CAPACITY=8
RAG_MAX_UPLOAD_BYTES=5242880
RAG_INGESTION_TIMEOUT=10m
ZHIPU_API_KEY=
ZHIPU_BASE_URL=https://open.bigmodel.cn/api/paas/v4
ZHIPU_EMBEDDING_MODEL=embedding-3
ZHIPU_EMBEDDING_DIMENSIONS=1024
ZHIPU_EMBEDDING_BATCH_SIZE=32
ZHIPU_EMBEDDING_TIMEOUT=30s
```

- [ ] **Step 8: Run tests and commit**

Run:

```bash
go test ./internal/rag/domain ./internal/config
```

Expected: PASS.

Commit:

```bash
git add internal/rag/domain internal/config .env.example
git commit -m "feat: 定义 RAG 导入领域与配置"
```

---

### Task 3: Add Ent Schemas, pgvector Mapping, and External DDL

**Files:**
- Modify: `ent/generate.go`
- Create: `ent/schema/ragdocument.go`
- Create: `ent/schema/ragdocumentversion.go`
- Create: `ent/schema/ragchunk.go`
- Create: `ent/schema/rag_ingestion_test.go`
- Generate: `ent/*`
- Create: `docs/database/rag_ingestion.sql`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: confirmed three-table schema and `vector(1024)`.
- Produces: generated `RagDocument`, `RagDocumentVersion`, `RagChunk` Ent APIs, row-lock methods, and externally executable idempotent DDL.

- [ ] **Step 1: Pin dependencies**

Run:

```bash
go get github.com/yuin/goldmark@v1.8.4 github.com/pgvector/pgvector-go@v0.4.0
go get -t github.com/DATA-DOG/go-sqlmock@v1.5.2
```

Expected: Goldmark and pgvector-go are direct requirements; sqlmock is available to tests.

- [ ] **Step 2: Write failing Ent descriptor tests**

Create `ent/schema/rag_ingestion_test.go` and assert:

```go
func TestRagChunkEmbeddingUsesVector1024(t *testing.T) {
	fields := fieldMap((RagChunk{}).Fields())
	embedding := fields["embedding"]
	if embedding.Info.Type != field.TypeOther {
		t.Fatalf("embedding type = %v", embedding.Info.Type)
	}
	if got := embedding.SchemaType[dialect.Postgres]; got != "vector(1024)" {
		t.Fatalf("embedding postgres type = %q", got)
	}
}

func TestRagIngestionTableNames(t *testing.T) {
	assertTable(t, (RagDocument{}).Annotations(), "rag_documents")
	assertTable(t, (RagDocumentVersion{}).Annotations(), "rag_document_versions")
	assertTable(t, (RagChunk{}).Annotations(), "rag_chunks")
}
```

Add these helpers and descriptor assertions in the same test file:

```go
func fieldMap(fields []ent.Field) map[string]*field.Descriptor {
	result := make(map[string]*field.Descriptor, len(fields))
	for _, configured := range fields {
		descriptor := configured.Descriptor()
		if descriptor.Err != nil {
			panic(descriptor.Err)
		}
		result[descriptor.Name] = descriptor
	}
	return result
}

func assertTable(t *testing.T, annotations []entschema.Annotation, want string) {
	t.Helper()
	if len(annotations) != 1 {
		t.Fatalf("annotation count = %d", len(annotations))
	}
	annotation, ok := annotations[0].(entsql.Annotation)
	if !ok || annotation.Table != want {
		t.Fatalf("table annotation = %#v, want %s", annotations[0], want)
	}
}

func TestRagIngestionFieldShapes(t *testing.T) {
	documents := fieldMap((RagDocument{}).Fields())
	if key := documents["document_key"]; key.Size != 128 || !key.Unique || key.Optional {
		t.Fatalf("document_key = %#v", key)
	}
	if current := documents["current_version"]; current.Info.Type != field.TypeInt || current.Default == nil {
		t.Fatalf("current_version = %#v", current)
	}

	versions := fieldMap((RagDocumentVersion{}).Fields())
	if versions["ingestion_id"].Info.Type != field.TypeUUID || !versions["ingestion_id"].Unique {
		t.Fatalf("ingestion_id = %#v", versions["ingestion_id"])
	}
	for _, name := range []string{"failure_code", "failure_message", "completed_at"} {
		if !versions[name].Optional || !versions[name].Nillable {
			t.Fatalf("%s = %#v", name, versions[name])
		}
	}

	chunks := fieldMap((RagChunk{}).Fields())
	if chunks["heading_path"].SchemaType[dialect.Postgres] != "jsonb" || chunks["heading_path"].Default == nil {
		t.Fatalf("heading_path = %#v", chunks["heading_path"])
	}
	if chunks["document_version_id"].Optional {
		t.Fatalf("document_version_id = %#v", chunks["document_version_id"])
	}
}
```

- [ ] **Step 3: Run the Schema tests and verify RED**

Run:

```bash
go test ./ent/schema
```

Expected: FAIL to compile because the three Ent schemas do not exist.

- [ ] **Step 4: Enable Ent row-lock generation**

Replace `ent/generate.go` directive with:

```go
//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate --feature sql/lock,sql/upsert ./schema
```

- [ ] **Step 5: Implement the exact Ent mappings**

Create schemas with explicit table annotations:

```go
entsql.Annotation{Table: "rag_documents"}
entsql.Annotation{Table: "rag_document_versions"}
entsql.Annotation{Table: "rag_chunks"}
```

Use:

```go
field.Other("embedding", pgvector.Vector{}).
	SchemaType(map[string]string{dialect.Postgres: "vector(1024)"})
```

Use `field.JSON("heading_path", []string{}).Default([]string{})` with PostgreSQL `jsonb`, `field.UUID("ingestion_id", uuid.UUID{})`, and these timestamp mappings:

```go
field.Time("created_at").
	Default(time.Now).
	Immutable().
	SchemaType(map[string]string{dialect.Postgres: "timestamptz"})

field.Time("updated_at").
	Default(time.Now).
	UpdateDefault(time.Now).
	SchemaType(map[string]string{dialect.Postgres: "timestamptz"})

field.Time("completed_at").
	Optional().
	Nillable().
	SchemaType(map[string]string{dialect.Postgres: "timestamptz"})
```

Define required field-backed edges:

```go
edge.From("document", RagDocument.Type).
	Ref("versions").
	Field("document_id").
	Unique().
	Required()

edge.From("document_version", RagDocumentVersion.Type).
	Ref("chunks").
	Field("document_version_id").
	Unique().
	Required()
```

Do not add Ent migration calls to runtime code.

- [ ] **Step 6: Generate and verify Ent code**

Run:

```bash
go generate ./ent
go test ./ent/...
rg -n 'ForUpdate|vector\\(1024\\)' ent
```

Expected: all Ent packages compile; `ForUpdate` and `OnConflict` are generated; PostgreSQL migration metadata contains `vector(1024)`.

- [ ] **Step 7: Add controlled external DDL**

Create `docs/database/rag_ingestion.sql` with:

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS rag_documents (
    id BIGSERIAL PRIMARY KEY,
    document_key VARCHAR(128) UNIQUE NOT NULL,
    current_version INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS rag_document_versions (
    id BIGSERIAL PRIMARY KEY,
    ingestion_id UUID UNIQUE NOT NULL,
    document_id BIGINT NOT NULL REFERENCES rag_documents(id) ON DELETE CASCADE,
    version INTEGER NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    content_hash CHAR(64) NOT NULL,
    original_markdown TEXT NOT NULL,
    source_bytes BIGINT NOT NULL,
    status VARCHAR(16) NOT NULL,
    stage VARCHAR(24) NOT NULL,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    embedded_chunk_count INTEGER NOT NULL DEFAULT 0,
    failure_code VARCHAR(64),
    failure_message VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE (document_id, version)
);

CREATE TABLE IF NOT EXISTS rag_chunks (
    id BIGSERIAL PRIMARY KEY,
    document_version_id BIGINT NOT NULL REFERENCES rag_document_versions(id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    content TEXT NOT NULL,
    embedding_content TEXT NOT NULL,
    heading_path JSONB NOT NULL DEFAULT '[]'::jsonb,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    estimated_tokens INTEGER NOT NULL,
    content_hash CHAR(64) NOT NULL,
    embedding VECTOR(1024) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (document_version_id, chunk_index)
);

CREATE INDEX IF NOT EXISTS rag_document_versions_document_id_idx
    ON rag_document_versions (document_id);

CREATE INDEX IF NOT EXISTS rag_chunks_document_version_id_idx
    ON rag_chunks (document_version_id);
```

Do not add a vector ANN index.

- [ ] **Step 8: Commit the persistence model**

```bash
git add ent docs/database/rag_ingestion.sql go.mod go.sum
git commit -m "feat: 增加 RAG 文档 Ent 模型"
```

---

### Task 4: Implement Goldmark Parsing, Filtering, and Normalization

**Files:**
- Create: `internal/rag/markdown/parser.go`
- Create: `internal/rag/markdown/filter.go`
- Create: `internal/rag/markdown/normalizer.go`
- Create: `internal/rag/markdown/parser_test.go`
- Create: `internal/rag/markdown/filter_test.go`
- Create: `internal/rag/markdown/normalizer_test.go`

**Interfaces:**
- Consumes: `[]byte` normalized UTF-8 Markdown.
- Produces: `Parser.Parse(context.Context, []byte) (domain.ParsedDocument, error)`, `Filter.Apply(context.Context, domain.ParsedDocument) (domain.ParsedDocument, error)`, and `Normalizer.Normalize(context.Context, domain.ParsedDocument) (domain.NormalizedDocument, error)`.

- [ ] **Step 1: Write failing structure tests**

Create table-driven tests covering this source:

```markdown
# 手机

短事实：IP68

## 摄像头

- 主摄 4800 万像素
- 支持 5 倍光学变焦

| 规格 | 值 |
| --- | --- |
| 重量 | 199g |

<!-- internal note -->
<script>alert("x")</script>
```

Parser tests assert:

```go
if len(blocks) < 2 {
	t.Fatalf("block count = %d, want at least 2", len(blocks))
}
if got := blocks[0].HeadingPath; !reflect.DeepEqual(got, []string{"手机"}) {
	t.Fatalf("first heading path = %#v", got)
}
if got := blocks[1].HeadingPath; !reflect.DeepEqual(got, []string{"手机", "摄像头"}) {
	t.Fatalf("second heading path = %#v", got)
}
if blocks[0].StartLine <= 0 || blocks[0].EndLine < blocks[0].StartLine {
	t.Fatalf("invalid source range = %d..%d", blocks[0].StartLine, blocks[0].EndLine)
}
```

Filter tests assert Raw HTML and comments disappear, an image without alt text disappears, a short fact remains, and an all-filtered document returns `NO_INDEXABLE_CONTENT`.

Normalizer tests assert Setext headings share the same path semantics as ATX headings, `---` is treated as ordinary Markdown rather than YAML Front Matter, fenced code preserves whitespace, links retain visible text, images retain non-empty alt text, tables become canonical Markdown, and repeated output is deterministic.

- [ ] **Step 2: Run parser tests and verify RED**

Run:

```bash
go test ./internal/rag/markdown
```

Expected: FAIL because `Parser` does not exist.

- [ ] **Step 3: Implement the Goldmark parser**

Construct Goldmark with:

```go
type Parser struct {
	markdown goldmark.Markdown
}

func NewParser() *Parser {
	return &Parser{markdown: goldmark.New(goldmark.WithExtensions(extension.GFM))}
}

func (p *Parser) Parse(ctx context.Context, source []byte) (domain.ParsedDocument, error)
```

Parse with `text.NewReader(source)`, precompute line-start byte offsets, walk block nodes in source order, and maintain a six-element heading stack.
Preserve Raw HTML and images as typed intermediate blocks so Filter behavior remains independently testable. For headings, update the stack and do not emit a standalone content block. Set `RawContent` from the source segment and `Content` from AST-visible text; Parser must not silently apply Filter policy.

- [ ] **Step 4: Implement line mapping, filtering, and normalization**

Parser adds:

```go
func lineNumber(offset int, starts []int) int
func headingText(node ast.Node, source []byte) string
func nodeText(node ast.Node, source []byte) string
func sourceRange(node ast.Node, starts []int) (int, int)
```

Filter removes `BlockRawHTML`, thematic breaks, empty blocks and images with empty alt text. It never removes a non-empty block solely because it is short. Return `domain.NewError(domain.CodeNoIndexableContent, "文档没有可索引内容", nil)` when nothing remains.

Expose:

```go
type Filter struct{}
func NewFilter() *Filter
func (f *Filter) Apply(ctx context.Context, document domain.ParsedDocument) (domain.ParsedDocument, error)
```

Normalizer adds:

```go
func normalizeWhitespace(value string) string
func normalizeTable(raw string) string
func normalizeList(raw string) string
func normalizeCode(raw string) string
```

It emits visible link text, non-empty image alt text, canonical Markdown tables/lists, preserved code lines and normalized paragraph whitespace.

Expose:

```go
type Normalizer struct{}
func NewNormalizer() *Normalizer
func (n *Normalizer) Normalize(ctx context.Context, document domain.ParsedDocument) (domain.NormalizedDocument, error)
```

- [ ] **Step 5: Run focused tests and commit**

Run:

```bash
go test ./internal/rag/markdown
```

Expected: PASS.

Commit:

```bash
git add internal/rag/markdown
git commit -m "feat: 实现 Markdown 结构化解析"
```

---

### Task 5: Implement Structure-Aware Chunking with Sliding Fallback

**Files:**
- Create: `internal/rag/splitter/estimator.go`
- Create: `internal/rag/splitter/splitter.go`
- Create: `internal/rag/splitter/splitter_test.go`

**Interfaces:**
- Consumes: `domain.NormalizedDocument` and `domain.ChunkConfig`.
- Produces: deterministic `[]domain.Chunk` with 0-based index, content, embedding content, heading path, lines, estimated tokens and SHA-256.

- [ ] **Step 1: Write failing estimator and splitter tests**

Tests must assert:

```go
config := domain.ChunkConfig{ChunkSize: 8, ChunkOverlap: 2}
chunks, err := New().Split(context.Background(), document, config)
```

Cover:

- blocks under one heading pack until the target;
- heading changes end the current Chunk;
- short `颜色：黑色` is retained;
- oversized Chinese text breaks after `。`;
- overlap content appears at the beginning of the next Chunk;
- oversized table repeats the header;
- oversized list never splits inside a list item;
- oversized code splits only on line boundaries;
- Emoji remains valid UTF-8;
- `EmbeddingContent` starts with `strings.Join(HeadingPath, " > ")`;
- repeated runs yield identical hashes and order.

- [ ] **Step 2: Run splitter tests and verify RED**

Run:

```bash
go test ./internal/rag/splitter
```

Expected: FAIL because estimator and splitter do not exist.

- [ ] **Step 3: Implement the conservative estimator**

Expose:

```go
type TokenEstimator interface {
	Estimate(string) int
}

type ConservativeEstimator struct{}
```

Count Han runes as 1.5, ASCII letters/digits/punctuation as 0.3, whitespace as zero, and other Unicode/Emoji as 1.5; round up with `math.Ceil`.

- [ ] **Step 4: Implement structure-aware packing**

Create:

```go
type Splitter struct {
	estimator TokenEstimator
}

func New() *Splitter
func NewWithEstimator(estimator TokenEstimator) *Splitter
func (s *Splitter) Split(ctx context.Context, document domain.NormalizedDocument, config domain.ChunkConfig) ([]domain.Chunk, error)
```

Validate `ChunkSize > 0`, `ChunkOverlap >= 0`, and `ChunkOverlap < ChunkSize`. Pack complete blocks while heading paths match. When a block exceeds the target, dispatch by `BlockType` to paragraph, table, list, or code fallback. Use rune slices, never byte indexes, for textual fallback.

- [ ] **Step 5: Implement overlap and finalization**

Overlap must reuse trailing complete semantic units when possible. Only fall back to rune-safe suffix text when one oversized unit was already split. Finalization must:

```go
embeddingContent := strings.TrimSpace(strings.Join(headingPath, " > ") + "\n\n" + content)
sum := sha256.Sum256([]byte(embeddingContent))
```

Assign consecutive 0-based `ChunkIndex`, minimum/maximum source lines, and estimated Token count.

- [ ] **Step 6: Run tests and commit**

Run:

```bash
go test ./internal/rag/splitter
```

Expected: PASS.

Commit:

```bash
git add internal/rag/splitter internal/rag/domain/document.go
git commit -m "feat: 实现 Markdown 结构感知分块"
```

---

### Task 6: Implement the Zhipu `embedding-3` Adapter

**Files:**
- Create: `internal/platform/bigmodel/embedder.go`
- Create: `internal/platform/bigmodel/embedder_test.go`

**Interfaces:**
- Consumes: `[]string` via Eino `embedding.Embedder`.
- Produces: ordered `[][]float64`, model `embedding-3`, dimension 1024, batches no larger than 32.

- [ ] **Step 1: Write failing HTTP contract tests**

Use `httptest.NewServer` and assert decoded requests contain:

```json
{"model":"embedding-3","input":["a","b"],"dimensions":1024}
```

Return deliberately reversed data from the test Handler:

```go
ones := make([]float64, 1024)
twos := make([]float64, 1024)
for index := range ones {
	ones[index] = 1
	twos[index] = 2
}
_ = json.NewEncoder(writer).Encode(map[string]any{
	"object": "list",
	"data": []map[string]any{
		{"index": 1, "object": "embedding", "embedding": twos},
		{"index": 0, "object": "embedding", "embedding": ones},
	},
	"usage": map[string]int{"prompt_tokens": 2, "completion_tokens": 0, "total_tokens": 2},
})
```

Assert output order is restored. Add tests for 33 inputs producing two requests, missing/duplicate/out-of-range indexes, wrong dimensions, non-finite values, 429 then success, 500 exhaustion, 401 without retry, and canceled Context.

- [ ] **Step 2: Run tests and verify RED**

Run:

```bash
go test ./internal/platform/bigmodel
```

Expected: FAIL because `Embedder` and `Config` do not exist.

- [ ] **Step 3: Implement request/response types and constructor**

Create:

```go
type Config struct {
	BaseURL   string
	APIKey    string
	Model     string
	Dimensions int
	BatchSize int
	Timeout   time.Duration
	MaxRetries int
}

type Embedder struct {
	endpoint   string
	apiKey     string
	model      string
	dimensions int
	batchSize  int
	client     *http.Client
	maxRetries int
	sleep      func(context.Context, time.Duration) error
}

func NewEmbedder(config Config) (*Embedder, error)
func (e *Embedder) EmbedStrings(ctx context.Context, texts []string, opts ...embedding.Option) ([][]float64, error)
```

Constructor validation fixes model to `embedding-3`, dimensions to 1024, batch size to `1..64`, trims one trailing slash from BaseURL, and builds endpoint `BaseURL + "/embeddings"`.

- [ ] **Step 4: Implement batching, response validation, and retries**

For each batch:

- marshal JSON;
- create request with Context;
- set `Authorization: Bearer <key>` and `Content-Type: application/json`;
- limit response body to 2 MiB;
- retry only transport errors, `429`, and `5xx`;
- use 100 ms, 200 ms exponential delays plus bounded random jitter;
- reject every other non-2xx response without returning its body;
- validate count, index uniqueness/range, vector length, `math.IsNaN`, and `math.IsInf`.

Wrap public errors with stable domain codes:

```go
domain.CodeEmbeddingFailed
domain.CodeInvalidEmbeddingResponse
```

- [ ] **Step 5: Run tests and commit**

Run:

```bash
go test ./internal/platform/bigmodel
```

Expected: PASS.

Commit:

```bash
git add internal/platform/bigmodel
git commit -m "feat: 接入智谱 Embedding-3"
```

---

### Task 7: Implement the Ent Ingestion Repository and Atomic Activation

**Files:**
- Create: `internal/rag/ingestion/contracts.go`
- Create: `internal/platform/database/ingestion_repository.go`
- Create: `internal/platform/database/ingestion_repository_test.go`

**Interfaces:**
- Consumes: generated Ent entities, `domain.Upload`, `[]domain.EmbeddedChunk`.
- Produces: repository submission inspection, queued task creation, source loading, stage/progress updates, failure persistence, query, and atomic activation.

- [ ] **Step 1: Define repository contracts**

Create `internal/rag/ingestion/contracts.go`:

```go
type SubmissionDecision int

const (
	SubmissionCreate SubmissionDecision = iota
	SubmissionReuse
	SubmissionConflict
)

type Repository interface {
	InspectSubmission(context.Context, string, string) (domain.Task, SubmissionDecision, error)
	CreateQueued(context.Context, domain.Upload) (domain.Task, SubmissionDecision, error)
	GetTask(context.Context, uuid.UUID) (domain.Task, error)
	LoadSource(context.Context, uuid.UUID) (domain.Upload, error)
	UpdateStage(context.Context, uuid.UUID, domain.Status, domain.Stage) error
	UpdateProgress(context.Context, uuid.UUID, int, int) error
	MarkFailed(context.Context, uuid.UUID, domain.Stage, domain.Failure) error
	StoreAndActivate(context.Context, uuid.UUID, []domain.EmbeddedChunk) error
}
```

Also define the exact narrow interfaces used by Pipeline:

```go
type DocumentParser interface {
	Parse(context.Context, []byte) (domain.ParsedDocument, error)
}

type DocumentFilter interface {
	Apply(context.Context, domain.ParsedDocument) (domain.ParsedDocument, error)
}

type DocumentNormalizer interface {
	Normalize(context.Context, domain.ParsedDocument) (domain.NormalizedDocument, error)
}

type ChunkSplitter interface {
	Split(context.Context, domain.NormalizedDocument, domain.ChunkConfig) ([]domain.Chunk, error)
}

type TextEmbedder interface {
	EmbedStrings(context.Context, []string, ...embedding.Option) ([][]float64, error)
}
```

- [ ] **Step 2: Write failing repository transaction tests**

Use `sqlmock.New`, `entsql.OpenDB(dialect.Postgres, db)`, and `ent.NewClient(ent.Driver(driver))`.

Tests must verify:

- nonexistent key returns `SubmissionCreate`;
- same hash in current or running version returns `SubmissionReuse`;
- different hash in queued/running version returns `SubmissionConflict`;
- `CreateQueued` begins a transaction, locks the document row with `FOR UPDATE`, computes `max(current_version, existing versions)+1`, and inserts the version;
- `StoreAndActivate` begins a transaction, inserts all Chunk rows, updates counts/status/stage, updates `current_version`, deletes all other versions, and commits;
- any insert/update failure rolls back;
- `MarkFailed` stores only stable code/message;
- missing task maps to `CodeIngestionNotFound`.

- [ ] **Step 3: Run tests and verify RED**

Run:

```bash
go test ./internal/platform/database -run Ingestion
```

Expected: FAIL because `NewIngestionRepository` and methods do not exist.

- [ ] **Step 4: Implement task mapping and submission inspection**

Create:

```go
type IngestionRepository struct {
	client *ent.Client
	now    func() time.Time
}

func NewIngestionRepository(client *ent.Client) (*IngestionRepository, error)
```

Map Ent rows to domain Task without exposing `original_markdown`. `InspectSubmission` queries the stable document, current version, and any `QUEUED/RUNNING` version. Return reuse before conflict when the active hash matches.

- [ ] **Step 5: Implement transaction-safe task creation**

`CreateQueued` must:

1. begin Ent transaction;
2. insert `rag_documents` with generated `OnConflictColumns(ragdocument.FieldDocumentKey).Ignore()` so concurrent first uploads cannot fail on the unique key;
3. query and lock the resulting document using generated `ForUpdate`;
4. repeat reuse/conflict checks;
5. compute `nextVersion = max(current_version, maximum stored version) + 1`;
6. create the `QUEUED` version with original Markdown;
7. commit;
8. return the final Task and decision.

On commit failure, return `CodeDocumentStoreFailed`.

- [ ] **Step 6: Implement progress, failure, and atomic activation**

Convert each `[]float64` vector to `[]float32` and `pgvector.NewVector`. `StoreAndActivate` must verify every vector length is 1024 before opening the transaction.

Inside the transaction:

```text
load and lock target version
create all chunks
update target version SUCCEEDED/COMPLETED and exact counts
update rag_documents.current_version
delete versions where document_id matches and id differs
commit
```

No network calls may occur inside this method.

- [ ] **Step 7: Run repository tests and commit**

Run:

```bash
go test ./internal/platform/database
```

Expected: PASS, including existing readiness tests.

Commit:

```bash
git add internal/rag/ingestion/contracts.go internal/platform/database
git commit -m "feat: 实现 RAG 导入持久化事务"
```

---

### Task 8: Implement the Plain Go Pipeline, Bounded Queue, Service, and Worker

**Files:**
- Create: `internal/rag/ingestion/pipeline.go`
- Create: `internal/rag/ingestion/pipeline_test.go`
- Create: `internal/rag/ingestion/queue.go`
- Create: `internal/rag/ingestion/queue_test.go`
- Create: `internal/rag/ingestion/service.go`
- Create: `internal/rag/ingestion/service_test.go`
- Create: `internal/rag/ingestion/worker.go`
- Create: `internal/rag/ingestion/worker_test.go`

**Interfaces:**
- Consumes: Repository, Markdown Parser, Splitter, Eino-compatible Embedder and runtime limits.
- Produces: `ingestion.API`, a 1-worker/8-waiter executor, sequential Pipeline and graceful shutdown.

- [ ] **Step 1: Write failing Pipeline tests**

Use fakes that append method names to a slice. Assert one successful call yields:

```go
[]string{
	"load",
	"stage:PARSING",
	"parse",
	"stage:FILTERING",
	"filter",
	"stage:NORMALIZING",
	"normalize",
	"stage:CHUNKING",
	"split",
	"progress:chunks",
	"stage:EMBEDDING",
	"embed",
	"progress:embedded",
	"stage:STORING",
	"activate",
}
```

Assert Parser, Filter, Normalizer, Splitter, Embedder and Repository failures stop subsequent stages and return the matching stable error code. Assert Pipeline source contains no import of `github.com/cloudwego/eino/compose`.

- [ ] **Step 2: Write failing queue, service, and worker tests**

Tests must verify:

- 1 worker never runs two jobs concurrently;
- 8 waiting reservations plus one running reservation are accepted;
- the next reservation fails immediately;
- releasing an uncommitted reservation restores capacity;
- Service checks reuse before reserving;
- Service releases reservation when `CreateQueued` fails or reuses after the second database check;
- Service commits only `ingestion_id`;
- worker applies the 10-minute task timeout supplied to its constructor;
- panic becomes `INTERNAL_PROCESSING_ERROR` and the next job still runs;
- shutdown stops new submissions and waits until Context deadline.

- [ ] **Step 3: Run tests and verify RED**

Run:

```bash
go test ./internal/rag/ingestion
```

Expected: FAIL because Pipeline, Executor, Service and Worker do not exist.

- [ ] **Step 4: Implement the plain Go Pipeline**

Create:

```go
type Pipeline struct {
	repository Repository
	parser     DocumentParser
	filter     DocumentFilter
	normalizer DocumentNormalizer
	splitter   ChunkSplitter
	embedder   TextEmbedder
}

func NewPipeline(repository Repository, parser DocumentParser, filter DocumentFilter, normalizer DocumentNormalizer, splitter ChunkSplitter, embedder TextEmbedder) (*Pipeline, error)
func (p *Pipeline) Run(ctx context.Context, ingestionID uuid.UUID) error
```

Pipeline must call Parser, Filter and Normalizer as three real stages, persisting each stage before its component call. Embed `EmbeddingContent` in batches delegated to the Embedder and attach returned vectors by exact index. Do not import Eino compose.

- [ ] **Step 5: Implement reservation queue**

Create:

```go
type Runner interface {
	Run(context.Context, uuid.UUID) error
}

type Executor struct {
	jobs        chan uuid.UUID
	slots       chan struct{}
	runner      Runner
	repository  Repository
	taskTimeout time.Duration
	workers     int
	mu          sync.RWMutex
	started     bool
	stopping    bool
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func NewExecutor(workers, queueCapacity int, taskTimeout time.Duration, runner Runner, repository Repository) (*Executor, error)
func (e *Executor) Start(ctx context.Context)
func (e *Executor) TryReserve() (*Reservation, bool)
func (e *Executor) Shutdown(ctx context.Context) error
```

The executor's total slot semaphore capacity is `workers + queueCapacity`. A `Reservation` has exactly one terminal action:

```go
Commit(ingestionID uuid.UUID)
Release()
```

Use `sync.Once` to prevent double commit/release. Committed slots are released only after worker execution completes. `TryReserve` and stopped-state checks must be non-blocking. A reservation obtained before shutdown remains committable; shutdown waits for it or cancels it at the supplied deadline.

- [ ] **Step 6: Implement Service**

Expose:

```go
type API interface {
	Submit(context.Context, string, string, []byte) (domain.Submission, error)
	Get(context.Context, uuid.UUID) (domain.Task, error)
}

type Service struct {
	repository Repository
	executor   *Executor
	maxBytes   int64
}

func NewService(repository Repository, executor *Executor, maxBytes int64) (*Service, error)
```

Submit sequence:

```text
ValidateDocumentKey
NormalizeUpload
assign the validated key to Upload.DocumentKey
InspectSubmission
return reuse/conflict when known
TryReserve
CreateQueued with transactional recheck
release on error/reuse/conflict
commit ingestion_id for a new task
return Submission
```

- [ ] **Step 7: Implement Worker error containment**

Executor workers call Pipeline under `context.WithTimeout`. Recover panic at the worker boundary, log only task identifiers and stable code, and call `Repository.MarkFailed`. A canceled service Context maps to `PROCESS_INTERRUPTED`; every other unknown error maps to `INTERNAL_PROCESSING_ERROR`.

Use structured `slog` fields `ingestion_id`, `document_key`, `status`, `stage`, `duration`, `chunk_count`, `embedded_chunk_count`, and `retry_count` when the value is available. Never log Markdown content, API keys, DATABASE_URL, vectors, panic values, or unbounded supplier response bodies.

- [ ] **Step 8: Run tests and commit**

Run:

```bash
go test ./internal/rag/ingestion
```

Expected: PASS with `-race`:

```bash
go test -race ./internal/rag/ingestion
```

Commit:

```bash
git add internal/rag/ingestion
git commit -m "feat: 实现进程内 RAG 导入任务"
```

---

### Task 9: Implement Multipart Handlers and Runtime Composition

**Files:**
- Modify: `internal/http/dependencies.go`
- Modify: `biz/handler/rag_ingestion/rag_ingestion_service.go`
- Modify: `router_test.go`
- Modify: `main.go`
- Modify: `main_test.go`

**Interfaces:**
- Consumes: `ingestion.API`, `httpapi.Dependencies`, generated request/response types.
- Produces: working HTTP API, dependency composition, worker startup and bounded graceful shutdown.

- [ ] **Step 1: Replace route smoke tests with failing multipart behavior tests**

Add a helper:

```go
func multipartUpload(t *testing.T, key, fileName string, content []byte) (*ut.Body, []ut.Header) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("document_key", key); err != nil {
		t.Fatal(err)
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
	return &ut.Body{Body: bytes.NewReader(body.Bytes()), Len: body.Len()}, []ut.Header{{
		Key: "Content-Type", Value: writer.FormDataContentType(),
	}}
}
```

Use a fake ingestion API and assert:

- valid upload calls `Submit` once and returns 202;
- same successful content can return 200 with `deduplicated=true`;
- missing file returns `FILE_REQUIRED`;
- duplicate file field returns `FILE_REQUIRED`;
- oversized content returns 413;
- conflict returns 409;
- queue full returns 503;
- GET parses UUID and returns status fields;
- invalid UUID and missing task return stable 400/404 errors;
- request Context is not retained by the fake after return.

- [ ] **Step 2: Run HTTP tests and verify RED**

Run:

```bash
go test . -run 'RAG|Ingestion|Multipart'
```

Expected: FAIL because Handler still returns `INGESTION_UNAVAILABLE`.

- [ ] **Step 3: Add the HTTP dependency**

Extend `httpapi.Dependencies`:

```go
IngestionService ingestion.API
MaxUploadBytes  int64
```

Keep HealthChecker and AgentRunner unchanged.

- [ ] **Step 4: Implement CreateIngestion Handler**

Handler sequence:

1. obtain dependencies;
2. bind generated `CreateIngestionRequest`;
3. call `requestContext.MultipartForm()` and require `len(form.File["file"]) == 1`;
4. open the file;
5. reject a missing or non-positive `dependencies.MaxUploadBytes` as `INGESTION_UNAVAILABLE`;
6. read with `io.LimitReader(file, dependencies.MaxUploadBytes+1)`;
7. close before calling Service;
8. call `Submit(ctx, documentKey, header.Filename, raw)`;
9. map typed domain errors to exact HTTP codes;
10. return generated response with RFC3339Nano timestamps.

Do not perform a second environment lookup in the Handler.

Use this exact HTTP mapping:

```text
INVALID_DOCUMENT_KEY, FILE_REQUIRED, UNSUPPORTED_FILE_TYPE,
INVALID_MARKDOWN_ENCODING, EMPTY_DOCUMENT, INVALID_INGESTION_ID → 400
FILE_TOO_LARGE                                                    → 413
INGESTION_IN_PROGRESS                                             → 409
INGESTION_NOT_FOUND                                               → 404
INGESTION_QUEUE_FULL, INGESTION_UNAVAILABLE                       → 503
every other synchronous error                                     → 500
```

- [ ] **Step 5: Implement GetIngestion Handler**

Parse generated path value with `uuid.Parse`, call Service.Get, and populate optional failure/completed fields only when present. Invalid UUID returns `INVALID_INGESTION_ID`; missing task returns `INGESTION_NOT_FOUND`.

- [ ] **Step 6: Add runtime composition without overwriting user changes**

Before editing:

```bash
git diff -- main.go
```

Preserve the existing user change:

```go
return errors.New("hertz 服务意外停止")
```

In `run()` create, in order:

```text
Ent client
IngestionRepository
Goldmark Parser
Structure-aware Splitter
BigModel Embedder
Pipeline
Executor/Worker
Ingestion Service
Hertz dependencies
```

Start workers before accepting HTTP. On shutdown:

1. stop HTTP acceptance;
2. create one shutdown deadline;
3. shut down Hertz;
4. stop new queue reservations;
5. drain/cancel workers within remaining deadline;
6. return;
7. defer closes Ent after workers stop.

Do not change `internal/agent.NewGraph`; it remains the online skeleton.

- [ ] **Step 7: Add shutdown lifecycle tests**

Add fakes around a `shutdownComponent` interface and assert HTTP shutdown occurs before worker shutdown, the same deadline Context is propagated, and worker error is returned without hiding an earlier Hertz error.

- [ ] **Step 8: Run HTTP/runtime tests and commit**

Run:

```bash
go test .
go test ./biz/handler/rag_ingestion ./internal/http
```

Expected: PASS.

Stage task files while leaving the pre-existing lowercase `hertz` hunk unstaged:

```bash
git add internal/http biz/handler/rag_ingestion router_test.go main_test.go
git add -p main.go
git diff --cached --check
git diff -- main.go
git commit -m "feat: 接入 RAG 导入 HTTP 服务"
```

In `git add -p`, split hunks when necessary, answer `n` for the pre-existing `Hertz` → `hertz` change, and stage only RAG composition/lifecycle hunks. After commit, `git diff -- main.go` must still show the user's original lowercase change.

---

### Task 10: Synchronize Documentation and Perform Full Verification

**Files:**
- Modify: `README.md`
- Modify: `AGENTS.md`
- Modify: `.env.example` if final variable names changed during reviewed implementation.
- Verify: all generated and handwritten files.

**Interfaces:**
- Consumes: completed implementation and external DDL.
- Produces: reproducible local setup, curl examples, stage documentation and evidence-backed acceptance.

- [ ] **Step 1: Write documentation assertions before prose**

Add a small shell check to the task notes and run it:

```bash
rg -n 'ZHIPU_API_KEY|RAG_INGESTION_WORKERS|rag_ingestion.sql|multipart/form-data|document_key|embedding-3|1024|进程内' README.md AGENTS.md .env.example
```

Expected before docs update: command fails to find several required terms.

- [ ] **Step 2: Update README**

Document:

- run `docs/database/rag_ingestion.sql` through the controlled database process before starting;
- set `DATABASE_URL` and `ZHIPU_API_KEY`;
- `make run`;
- upload:

```bash
curl -i -X POST http://localhost:3000/api/v1/rag/ingestions \
  -F 'document_key=product-catalog/iphone-16-pro' \
  -F 'file=@./iphone-16-pro.md;type=text/markdown'
```

- query using the returned UUID without a literal colon:

```bash
curl -i http://localhost:3000/api/v1/rag/ingestions/550e8400-e29b-41d4-a716-446655440000
```

- explain 5 MiB, 1 worker, queue capacity 8, model/dimension fixed values, old-version deletion, and inability to survive process reclamation.

- [ ] **Step 3: Update AGENTS current phase**

Change the stale “不得加入商品导入/Embedding” current-stage text to state that the foundation milestone has been accepted and this increment covers only the offline half of Basic RAG. Keep online retrieval, reranking, validation, SSE, cart and multimodal work behind later gates.

- [ ] **Step 4: Verify secrets and forbidden architecture**

Run:

```bash
rg -n 'npg_|postgresql://[^U]|Bearer [A-Za-z0-9_-]{12,}' . \
  -g '!go.sum' -g '!.git/**' -g '!.env.local'
rg -n 'eino/compose|compose.NewGraph' internal/rag
find . -maxdepth 2 -type d -name migrations
```

Expected:

- no committed credential matches;
- no Eino Graph imports under `internal/rag`;
- no root `migrations` directory.

- [ ] **Step 5: Run full required verification**

Run in exact order:

```bash
make generate
make fmt
git diff --check
make test
go test -race ./internal/rag/...
make vet
make build
```

Expected:

- generation succeeds and only expected generated differences remain;
- formatting succeeds;
- diff check produces no output;
- all tests PASS;
- race tests PASS;
- vet produces no diagnostics;
- build creates the temporary server binary successfully.

- [ ] **Step 6: Re-run generated route and schema checks**

Run:

```bash
rg -n '/api/v1/rag/ingestions' biz/router router_gen.go
rg -n 'vector\\(1024\\)' ent docs/database/rag_ingestion.sql
git status --short
```

Expected: both routes and all vector mappings are present; status contains only intentional implementation/doc changes.

- [ ] **Step 7: Commit the documentation and final verification slice**

```bash
git add README.md AGENTS.md .env.example
git commit -m "docs: 补充 RAG 离线导入说明"
```

- [ ] **Step 8: Prepare the acceptance report**

Report:

- commit list;
- exact verification command results;
- DDL must be run outside the application;
- process-local queue cannot recover across Vercel instance reclamation;
- old ingestion IDs return 404 after a successful replacement;
- no HNSW or online retrieval is included.
