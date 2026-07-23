# RAG Markdown 离线导入设计

## 1. 背景与范围

本增量为全局商品知识库提供 Markdown 离线导入能力：

```text
上传 Markdown
→ 解析
→ 确定性过滤
→ 统一格式
→ 结构感知切分
→ Embedding
→ PostgreSQL 保存原文与 vector
```

本设计只覆盖离线索引链路及其异步任务 API，不包含在线检索、重排、事实校验、SSE 对话、商品卡片、购物车或订单能力。

离线链路是确定性的应用流水线，使用普通 Go 服务顺序编排，不使用 Eino Graph。Eino Graph 仍只用于需要图编排的在线 Agent 流程。

前一阶段基础骨架已经由用户验收，本增量开始阶段门禁中的“基础 RAG”离线部分。实施时必须同步更新仍描述为基础骨架阶段的中文项目文档，且不得借此提前实现本设计范围外的在线 RAG 能力。

## 2. 已确认决策

- 输入方式：`multipart/form-data` 直接上传文件。
- 文件格式：仅 Markdown，扩展名为 `.md` 或 `.markdown`。
- 单文件上限：5 MiB，即 5,242,880 字节。
- 执行方式：异步任务。
- 执行器：进程内有界 goroutine worker 队列。
- 默认 worker 数：1。
- 默认排队容量：8。
- 数据范围：全局商品知识库，不按用户隔离。
- 幂等身份：调用方提供稳定 `document_key`。
- 覆盖语义：相同内容复用；内容变化创建新版本，成功后切换。
- 旧版本策略：新版本成功后立即删除该 `document_key` 的全部旧版本。
- Markdown 不包含 YAML Front Matter。
- Markdown 解析：Goldmark AST，启用 GFM 扩展。
- 切分：结构感知优先，滑动窗口处理超大语义单元。
- 默认 Chunk 目标：512 个估算 Token。
- 默认 Chunk 重叠：64 个估算 Token。
- Embedding API：`https://open.bigmodel.cn/api/paas/v4/embeddings`。
- Embedding 模型：`embedding-3`。
- 向量维度：1024。
- PostgreSQL 向量列：`vector(1024)`。

## 3. 约束与非目标

### 3.1 进程内队列限制

进程内队列只保证当前进程存活期间的执行。Vercel 实例回收、进程崩溃、重启或扩缩容可能中断任务，甚至使任务停留在 `QUEUED` 或 `RUNNING`。

第一版不实现：

- 跨实例任务领取；
- 持久队列；
- 任务租约或心跳；
- 崩溃恢复；
- 自动重放；
- “至少一次”或“恰好一次”保证。

优雅关闭时会尽力取消并更新任务；进程被强制终止时不承诺能完成状态更新。

### 3.2 数据库管理

仓库不执行生产数据库迁移。Ent Schema 用于映射预先创建的 PostgreSQL 表及生成类型安全客户端；数据库表、约束、`vector` 扩展由仓库外的受控流程准备。

初始数据量优先使用 pgvector 精确余弦检索，本增量不创建 HNSW。

### 3.3 Multipart 与 IDL

`document_key` 由 Thrift 表单注解绑定。Hertz/Thrift 不能自然生成可用的 `multipart.FileHeader` 字段，因此 `file` 由生成 Handler 中的协议适配代码显式读取。

IDL 必须通过注释声明：

- 请求 Content-Type 为 `multipart/form-data`；
- 文件字段名固定为 `file`；
- 文件字段恰好出现一次；
- 文件只允许 `.md`、`.markdown`；
- 文件最大 5 MiB。

该 Handler 逻辑只属于 HTTP 协议适配，不得解析文档、调用模型或直接执行持久化事务。

## 4. HTTP 与 Thrift 契约

新增 `idl/rag_ingestion.thrift`，后续通过 `hz update` 更新生成物。

### 4.1 创建任务

```http
POST /api/v1/rag/ingestions
Content-Type: multipart/form-data

document_key = product-catalog/iphone-16-pro
file         = iphone-16-pro.md
```

IDL 请求：

```thrift
struct CreateIngestionRequest {
    1: required string DocumentKey (
        api.form="document_key",
        go.tag="json:\"document_key\""
    )
}
```

成功响应：

```thrift
struct CreateIngestionResponse {
    1: required string IngestionID (
        go.tag="json:\"ingestion_id\""
    )
    2: required string DocumentKey (
        go.tag="json:\"document_key\""
    )
    3: required string Status (
        go.tag="json:\"status\""
    )
    4: required string Stage (
        go.tag="json:\"stage\""
    )
    5: required bool Deduplicated (
        go.tag="json:\"deduplicated\""
    )
    6: required string CreatedAt (
        go.tag="json:\"created_at\""
    )
}
```

新任务返回 HTTP `202 Accepted`：

```json
{
  "ingestion_id": "550e8400-e29b-41d4-a716-446655440000",
  "document_key": "product-catalog/iphone-16-pro",
  "status": "QUEUED",
  "stage": "QUEUED",
  "deduplicated": false,
  "created_at": "2026-07-23T10:30:00Z"
}
```

相同 `document_key`、相同内容且任务仍在执行时，返回现有任务、HTTP `202` 和 `deduplicated=true`。相同内容已经成功时，返回现有任务、HTTP `200` 和 `deduplicated=true`。

### 4.2 查询任务

IDL 路由声明使用 Hertz 路径参数语法：

```text
/api/v1/rag/ingestions/:ingestion_id
```

客户端实际 URL 不包含冒号：

```http
GET /api/v1/rag/ingestions/550e8400-e29b-41d4-a716-446655440000
```

IDL 请求与响应：

```thrift
struct GetIngestionRequest {
    1: required string IngestionID (
        api.path="ingestion_id",
        go.tag="json:\"ingestion_id\""
    )
}

struct IngestionFailure {
    1: required string Code (
        go.tag="json:\"code\""
    )
    2: required string Message (
        go.tag="json:\"message\""
    )
}

struct GetIngestionResponse {
    1: required string IngestionID (
        go.tag="json:\"ingestion_id\""
    )
    2: required string DocumentKey (
        go.tag="json:\"document_key\""
    )
    3: required string Status (
        go.tag="json:\"status\""
    )
    4: required string Stage (
        go.tag="json:\"stage\""
    )
    5: required i64 SourceBytes (
        go.tag="json:\"source_bytes\""
    )
    6: required i32 ChunkCount (
        go.tag="json:\"chunk_count\""
    )
    7: required i32 EmbeddedChunkCount (
        go.tag="json:\"embedded_chunk_count\""
    )
    8: required string CreatedAt (
        go.tag="json:\"created_at\""
    )
    9: required string UpdatedAt (
        go.tag="json:\"updated_at\""
    )
    10: optional string CompletedAt (
        go.tag="json:\"completed_at,omitempty\""
    )
    11: optional IngestionFailure Failure (
        go.tag="json:\"failure,omitempty\""
    )
}
```

服务定义：

```thrift
service RAGIngestionService {
    CreateIngestionResponse CreateIngestion(
        1: CreateIngestionRequest request
    ) (api.post="/api/v1/rag/ingestions")

    GetIngestionResponse GetIngestion(
        1: GetIngestionRequest request
    ) (api.get="/api/v1/rag/ingestions/:ingestion_id")
}
```

### 4.3 通用错误

错误继续使用现有稳定 JSON 结构：

```json
{
  "status": "invalid_request",
  "code": "FILE_TOO_LARGE",
  "message": "Markdown 文件不能超过 5 MiB",
  "request_id": "..."
}
```

同步请求错误码：

| HTTP | Code | 含义 |
| --- | --- | --- |
| 400 | `DOCUMENT_KEY_REQUIRED` | 缺少 `document_key` |
| 400 | `INVALID_DOCUMENT_KEY` | `document_key` 格式不合法 |
| 400 | `FILE_REQUIRED` | 缺少 `file` |
| 400 | `UNSUPPORTED_FILE_TYPE` | 不是 `.md` 或 `.markdown` |
| 413 | `FILE_TOO_LARGE` | 文件超过 5 MiB |
| 400 | `INVALID_MARKDOWN_ENCODING` | 不是合法 UTF-8 或包含 NUL |
| 400 | `EMPTY_DOCUMENT` | 规范化后没有正文 |
| 409 | `INGESTION_IN_PROGRESS` | 相同 Key 的不同内容正在处理 |
| 404 | `INGESTION_NOT_FOUND` | 任务不存在或旧版本已删除 |
| 503 | `INGESTION_QUEUE_FULL` | 进程内队列已满 |
| 503 | `INGESTION_UNAVAILABLE` | 导入服务不可用 |

`document_key` 去除首尾空白后必须包含 1–128 个 Unicode 字符，只允许 Unicode 字母、数字以及 `. _ / -`，不允许空白和控制字符。

## 5. 状态模型

整体状态：

```text
QUEUED → RUNNING → SUCCEEDED
                 ↘ FAILED
```

处理阶段：

```text
QUEUED
PARSING
FILTERING
NORMALIZING
CHUNKING
EMBEDDING
STORING
COMPLETED
```

`status` 表示任务整体结果，`stage` 表示当前或失败时所在阶段。API 不返回无法可靠计算的百分比，只返回真实的 Chunk 计数。

异步失败码：

- `MARKDOWN_PARSE_FAILED`
- `NO_INDEXABLE_CONTENT`
- `DOCUMENT_SPLIT_FAILED`
- `EMBEDDING_FAILED`
- `INVALID_EMBEDDING_RESPONSE`
- `DOCUMENT_STORE_FAILED`
- `PROCESS_INTERRUPTED`
- `INTERNAL_PROCESSING_ERROR`

供应商原始响应、数据库错误、堆栈和内部实现细节不得出现在 API 中。

## 6. 上传校验与内容身份

Handler 在返回响应前完成：

1. 解析 multipart 并确认 `file` 恰好一个；
2. 校验扩展名，大小写不敏感；
3. 通过上限为 `5 MiB + 1 byte` 的 Reader 读取，不能只相信 `Content-Length`；
4. 拒绝超过 5 MiB 的内容；
5. 校验 UTF-8；
6. 去除 UTF-8 BOM；
7. 将 `CRLF` 和孤立 `CR` 规范化为 `LF`；
8. 拒绝 NUL；
9. 拒绝去除空白后为空的内容；
10. 对规范化字节计算 SHA-256。

goroutine 不得持有 Hertz `RequestContext`、`multipart.FileHeader` 或上传文件句柄。

相同内容的定义为：

```text
相同 document_key + 相同规范化 Markdown SHA-256
```

纯换行风格或 BOM 差异不会创建新版本；其他内容变化会创建新版本。

## 7. Markdown 解析与统一格式

### 7.1 解析器

使用 `github.com/yuin/goldmark`，开启 GFM 扩展。直接遍历 AST，不先渲染为 HTML，也不使用正则猜测 Markdown 标题。

必须识别：

- ATX H1–H6；
- Setext 标题；
- 段落；
- 有序和无序列表；
- 引用；
- GFM 表格；
- 围栏与缩进代码块；
- 链接和图片；
- 分隔线；
- Raw HTML 和 HTML 注释。

Markdown 不包含 YAML Front Matter，因此 `---` 按普通 Markdown 语义处理。

### 7.2 中间结构

```go
type BlockType string

type MarkdownBlock struct {
    Type        BlockType
    HeadingPath []string
    Content     string
    StartLine   int
    EndLine     int
    Ordinal     int
}
```

Markdown 没有页码，使用标题路径和源文件行号进行引用溯源。

### 7.3 确定性过滤

第一版不调用 LLM 判断“广告”或“废话”，只执行确定性规则：

- 删除空节点和纯空白；
- 删除 HTML 注释；
- Raw HTML 不进入 Embedding 内容；
- 删除没有替代文字的纯图片节点；
- 保留标题、正文、列表、引用、表格和代码块；
- 链接保留可见文字，原始 URL 仍可从原始 Markdown 获取；
- 合并普通文本中的连续空白，不改变代码块内容；
- 不按最短字符数直接删除短商品事实。

标题路径会作为上下文加入 `embedding_content`，例如：

```text
手机 > iPhone 16 Pro > 摄像头

支持 5 倍光学变焦……
```

## 8. Chunk 设计

### 8.1 接口与结果

```go
type ChunkConfig struct {
    ChunkSize    int
    ChunkOverlap int
}

type ChunkResult struct {
    ChunkIndex       int
    Content          string
    EmbeddingContent string
    HeadingPath      []string
    StartLine        int
    EndLine          int
    EstimatedTokens  int
    ContentHash      string
}

type ChunkSplitter interface {
    Split(ctx context.Context, document NormalizedDocument, config ChunkConfig) ([]ChunkResult, error)
}
```

实现：

```text
StructureAwareSplitter   主策略
SlidingWindowSplitter    超大章节或原子块的兜底策略
```

由于输入只允许 Markdown，统一入口总是先执行结构感知切分，不需要按文件格式动态选择策略。

### 8.2 结构感知规则

1. 根据 H1–H6 建立完整 `heading_path`；
2. 将段落、列表、表格、引用和代码块作为原子语义单元；
3. 同一标题路径下相邻原子块按顺序装入 Chunk；
4. 达到约 512 个估算 Token 后在语义边界结束；
5. 相邻 Chunk 保留约 64 个估算 Token 的语义重叠；
6. 短章节优先与同一父标题下的相邻内容合并；
7. 无法安全合并的短商品事实独立保留。

### 8.3 超大单元降级

- 普通段落：段落边界 → 中英文句子边界 → Unicode rune 边界；
- 表格：按数据行切分，每个子块重复表头；
- 列表：按完整列表项切分；
- 代码块：按完整行切分；
- 所有字符级操作必须 Unicode 安全，不能切断 UTF-8 或代理字符语义。

滑动窗口优先在以下位置断开：

```text
段落 > 换行 > 。！？ > ；， > 英文句末标点 > 空白 > rune 边界
```

### 8.4 Token 估算

第一版使用可替换的本地 `TokenEstimator`，不调用外部 Tokenizer：

- 汉字采用保守系数约 1.5 Token；
- 英文及其他非空白字符采用约 0.3 Token；
- 无法分类的符号、Emoji 使用更保守的上界；
- 结果向上取整。

512 是目标而非绝对上限。最终送给智谱前再次校验估算值；由于 `embedding-3` 单条输入上限为 3072 Token，512 目标提供足够安全余量。

## 9. Embedding 适配

实现智谱适配器并满足 Eino `embedding.Embedder` 接口，但离线流水线不使用 Eino Graph。

请求固定为：

```json
{
  "model": "embedding-3",
  "input": ["..."],
  "dimensions": 1024
}
```

默认每批最多 32 个 Chunk，低于供应商的 64 条上限。

响应必须验证：

- 输出数量等于输入数量；
- 根据响应 `index` 恢复输入顺序；
- 每条向量恰好 1024 维；
- 向量值不含 `NaN` 或正负无穷；
- 重复、缺失和越界的 `index` 均视为非法响应。

重试规则：

- 网络临时错误、超时、HTTP `429`、HTTP `5xx`：最多额外重试两次；
- HTTP `400`、`401`、`403` 及其他确定性客户端错误：不重试；
- 重试使用指数退避和抖动，并受任务 Context 约束。

API Key、文档内容、向量和供应商完整错误体不得写入普通日志。

## 10. 普通 Go Pipeline

离线流程使用显式顺序编排：

```text
Worker
→ Pipeline.Run
  → Repository.LoadSource
  → Parser.Parse
  → Filter.Apply
  → Normalizer.Normalize
  → Splitter.Split
  → Embedder.Embed
  → Repository.StoreAndActivate
```

示意接口：

```go
type Pipeline struct {
    repository Repository
    parser     Parser
    filter     Filter
    normalizer Normalizer
    splitter   ChunkSplitter
    embedder   embedding.Embedder
}

func (p *Pipeline) Run(ctx context.Context, ingestionID uuid.UUID) error
```

每个组件都通过构造函数注入，使用强类型输入输出，可独立测试。Pipeline 在每个阶段开始前持久化 `stage`，错误由统一分类器映射为稳定失败码。

不得：

- 构造 Eino Graph；
- 在 Handler 中串联处理步骤；
- 使用隐式全局可变状态；
- 在数据库事务中调用外部 Embedding API。

## 11. Worker 与队列

### 11.1 提交流程

```text
校验上传
→ 规范化并哈希
→ 检查去重和并发冲突
→ 非阻塞预留队列槽位
→ 保存任务与原始 Markdown
→ 向队列提交 ingestion_id
→ 返回 HTTP 响应
```

队列只保存 `ingestion_id`，worker 从 PostgreSQL 加载原文。保存失败时释放预留槽位；队列没有容量时不创建任务，立即返回 `INGESTION_QUEUE_FULL`。

### 11.2 执行流程

```text
取出 ingestion_id
→ 设置 RUNNING/PARSING
→ 在任务超时 Context 中执行 Pipeline
→ 设置 SUCCEEDED/COMPLETED 或 FAILED
→ 释放执行槽位
```

worker 顶层必须恢复 panic，将其转换为 `INTERNAL_PROCESSING_ERROR`，并继续处理后续任务。

### 11.3 关闭

关闭顺序：

1. 停止接受新任务；
2. 关闭提交入口；
3. 在 `SHUTDOWN_TIMEOUT` 内等待当前任务与队列；
4. 超时后取消任务 Context；
5. 尽力记录 `PROCESS_INTERRUPTED`；
6. 再关闭数据库客户端。

## 12. 去重、并发与覆盖

### 12.1 相同内容

- 当前成功版本哈希相同：返回当前任务，`deduplicated=true`，不重新 Embedding；
- 当前执行任务哈希相同：返回执行中的任务，`deduplicated=true`；
- 不重复创建版本。

### 12.2 不同内容

- 没有任务在执行：创建递增版本并排队；
- 已有不同内容处于 `QUEUED/RUNNING`：返回 HTTP `409 INGESTION_IN_PROGRESS`；
- 新版本处理期间，旧成功版本继续作为当前可检索版本；
- 新版本失败，旧版本不变。

创建任务时必须在数据库事务中锁定对应 `rag_documents` 行并重新检查状态，不能只依赖进程内互斥。

### 12.3 成功覆盖

新版本成功后，在同一短事务中：

1. 写入全部新 Chunk；
2. 校验 `chunk_count == embedded_chunk_count`；
3. 将新版本标记为 `SUCCEEDED/COMPLETED`；
4. 更新 `rag_documents.current_version`；
5. 删除该文档除新版本外的全部版本；
6. 依赖外键级联删除旧 Chunk；
7. 提交。

事务失败则全部回滚，旧版本继续生效。

旧版本删除后，对其 `ingestion_id` 的查询返回 `INGESTION_NOT_FOUND`。

## 13. PostgreSQL 与 Ent 模型

### 13.1 `rag_documents`

```text
id               BIGSERIAL PRIMARY KEY
document_key     VARCHAR(128) UNIQUE NOT NULL
current_version  INTEGER NOT NULL DEFAULT 0
created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
```

### 13.2 `rag_document_versions`

```text
id                    BIGSERIAL PRIMARY KEY
ingestion_id          UUID UNIQUE NOT NULL
document_id           BIGINT NOT NULL
version               INTEGER NOT NULL
file_name             VARCHAR(255) NOT NULL
content_hash          CHAR(64) NOT NULL
original_markdown     TEXT NOT NULL
source_bytes          BIGINT NOT NULL
status                VARCHAR(16) NOT NULL
stage                 VARCHAR(24) NOT NULL
chunk_count           INTEGER NOT NULL DEFAULT 0
embedded_chunk_count  INTEGER NOT NULL DEFAULT 0
failure_code          VARCHAR(64)
failure_message       VARCHAR(255)
created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
completed_at          TIMESTAMPTZ

UNIQUE(document_id, version)
FOREIGN KEY(document_id)
    REFERENCES rag_documents(id)
    ON DELETE CASCADE
```

### 13.3 `rag_chunks`

```text
id                   BIGSERIAL PRIMARY KEY
document_version_id  BIGINT NOT NULL
chunk_index          INTEGER NOT NULL
content              TEXT NOT NULL
embedding_content    TEXT NOT NULL
heading_path         JSONB NOT NULL DEFAULT '[]'
start_line           INTEGER NOT NULL
end_line             INTEGER NOT NULL
estimated_tokens     INTEGER NOT NULL
content_hash         CHAR(64) NOT NULL
embedding            VECTOR(1024) NOT NULL
created_at           TIMESTAMPTZ NOT NULL DEFAULT now()

UNIQUE(document_version_id, chunk_index)
FOREIGN KEY(document_version_id)
    REFERENCES rag_document_versions(id)
    ON DELETE CASCADE
```

`embedding` 使用 `pgvector-go` 的 SQL Scanner/Valuer 能力映射，并在 Ent Schema 中显式声明 PostgreSQL 类型 `vector(1024)`。

## 14. 配置

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

启用离线导入路由时，`ZHIPU_API_KEY` 必填。模型和维度必须分别为 `embedding-3` 和 `1024`；不允许请求方覆盖，防止同一个向量列混入不同语义空间。

`.env.example` 只提供占位符，不得提交真实 API Key、DATABASE_URL 或用户数据。

## 15. 可观测性

结构化日志字段：

- `request_id`
- `ingestion_id`
- `document_key`
- `status`
- `stage`
- `duration`
- `chunk_count`
- `embedded_chunk_count`
- `retry_count`

禁止记录：

- Markdown 全文；
- API Key；
- DATABASE_URL；
- 向量数组；
- 供应商未清洗响应体；
- 内部堆栈到客户端。

## 16. 测试策略

### 16.1 Handler 与 API

- 缺少 `document_key`；
- 非法 `document_key`；
- 缺少或重复 `file`；
- 错误扩展名；
- 空文件；
- 非法 UTF-8；
- BOM 与换行规范化；
- 5 MiB 边界；
- 5 MiB + 1 byte；
- 新任务返回 202；
- 相同内容复用；
- 不同内容冲突；
- 状态查询；
- 旧任务删除后返回 404；
- 稳定错误结构和 request ID。

### 16.2 Parser、Filter 与 Normalizer

- ATX H1–H6；
- Setext 标题；
- 中文标题；
- 段落、列表、引用、表格、代码块；
- Raw HTML 和注释；
- 图片替代文字；
- 链接可见文字；
- 行号和标题路径；
- 短商品事实不会丢失。

### 16.3 Splitter

- 512/64 目标；
- 中英文句子断点；
- 同父标题下短节合并；
- 超大段落；
- 超大表格重复表头；
- 超大列表保持列表项；
- 超大代码块按行；
- Emoji 和 Unicode 安全；
- Chunk 顺序、行号、哈希稳定；
- 相同输入产生确定性输出。

### 16.4 Embedding

使用 `httptest.Server` 验证：

- 固定模型和 1024 维；
- 单批最多 32 条；
- 响应乱序恢复；
- 数量不一致；
- 缺失、重复或越界 index；
- 非 1024 维；
- `NaN` 和无穷；
- 429/5xx/超时有限重试；
- 400/401/403 不重试；
- Context 取消。

### 16.5 Queue 与 Pipeline

- 1 worker 串行执行；
- 容量 8；
- 队列满非阻塞失败；
- 数据库保存失败释放预留位；
- panic 隔离；
- 阶段按顺序更新；
- 任务超时；
- 优雅关闭；
- 各阶段错误映射稳定。

### 16.6 存储事务

- 首次成功激活；
- 新版本成功切换；
- 成功后删除全部旧版本和旧 Chunk；
- 新版本失败时旧版本继续生效；
- 写入中途失败完整回滚；
- 并发创建同一 Key 只能有一个不同内容任务；
- `vector(1024)` 长度约束。

## 17. 实施与验收

实施遵循测试先行：

1. 先添加失败测试；
2. 编写 `rag_ingestion.thrift`；
3. 使用 `hz update` 更新生成物；
4. 实现协议适配；
5. 实现 Ent Schema 与仓储；
6. 实现 Goldmark 解析与 Chunk；
7. 实现智谱 Embedding；
8. 实现 Pipeline 与 worker；
9. 完成依赖装配、配置和中文文档；
10. 提供仓库外执行的 PostgreSQL DDL。

完成前至少运行：

```bash
make generate
make fmt
git diff --check
make test
make vet
make build
```

验收时必须明确报告：

- 各验证命令的实际结果；
- 进程内队列无法跨实例恢复的限制；
- 本仓库不会自动执行数据库迁移；
- 旧版本被删除后旧 `ingestion_id` 不再可查询。

## 18. 参考资料

- [Hertz 文件上传与 FormFile](https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/context/request/)
- [Hertz/Hz API 注解](https://www.cloudwego.io/docs/hertz/tutorials/toolkit/annotation/)
- [Goldmark](https://github.com/yuin/goldmark)
- [智谱 Embedding-3](https://docs.bigmodel.cn/cn/guide/models/embedding/embedding-3)
- [智谱文本嵌入 API](https://docs.bigmodel.cn/api-reference/%E6%A8%A1%E5%9E%8B-api/%E6%96%87%E6%9C%AC%E5%B5%8C%E5%85%A5)
- [pgvector-go](https://github.com/pgvector/pgvector-go)
