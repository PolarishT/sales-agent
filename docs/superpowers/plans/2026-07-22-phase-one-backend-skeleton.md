# 第一阶段后端基础骨架实施计划

> **供编码代理执行：** 必须使用 `superpowers:subagent-driven-development`（推荐）或 `superpowers:executing-plans`，按任务逐项实施。所有步骤使用复选框跟踪。

**目标：** 将当前 Gin 模板替换为基于 Hertz、Eino Graph 和 Neon Postgres（已启用 pgvector）的可运行、可测试 Go 后端骨架。

**架构：** Hertz 负责传输协议与服务生命周期，Eino Graph 负责强类型请求编排，`pgxpool` 通过小型接口负责 PostgreSQL 连接。根目录 `main.go` 作为 Vercel 零配置 Go 后端与本地开发的共用入口，监听地址优先使用 Vercel 注入的 `PORT`。骨架提供存活与就绪检查、可编译和调用的最小 Eino Graph、适用于 Neon 直连地址的 SQL 迁移，并将模型调用与完整检索能力留到第一阶段的后续增量。

**技术栈：** Go 1.23+、Hertz v0.10.5、Eino v0.9.12、pgx/v5 v5.7.4、pgvector-go v0.4.0、启用 `vector` 扩展的 Neon Postgres。

## 全局约束

- 仅实现后端，不实现 iOS、Android 或替代性的 Web 客户端。
- 使用 Hertz 替换 Gin，不保留 Gin 兼容层。
- 所有 Agent 编排均使用 Eino Graph，最小骨架链路也不能绕过 Graph。
- Vercel 和本地运行时数据库流量使用 `DATABASE_URL` 的池化地址；数据库迁移单独使用 `DATABASE_MIGRATION_URL` 的直连地址。
- 应用启动时只强制校验 `DATABASE_URL`。`DATABASE_MIGRATION_URL` 仅供本地或 CI 迁移命令读取，不能成为 Vercel 运行时启动条件。
- 监听地址优先读取 Vercel 注入的 `PORT`；仅当 `PORT` 为空时才读取本地 `HTTP_ADDR`，再回退到 `:3000`。
- `APP_ENV` 显式配置优先；未配置时使用 Vercel 自动注入的 `VERCEL_ENV`；两者都为空时回退到 `development`。
- Vercel Production、Preview 和 Development 必须分别配置凭据；Preview 环境不得默认使用生产 Neon 分支。
- 每个 Vercel 实例只创建一个进程级 `pgxpool`，并限制单实例连接数，避免自动扩容时耗尽 Neon 连接。
- 严禁提交凭据；`.env` 必须被忽略，`.env.example` 只能包含占位值。
- 初始数据集仅有 50–100 条商品，先保留精确的 pgvector 余弦检索；Embedding 模型和维度确定后再增加 HNSW 索引。
- 生产代码不能依赖仅供测试使用的数据库或 HTTP 实现。

---

## 文件结构

- `AGENTS.md`：仓库级实施约束和阶段门禁。
- `main.go`：Vercel 零配置 Go 后端与本地启动共用的进程入口、信号处理、依赖装配和 Hertz 启动。
- `internal/config/config.go`：环境变量解析与校验。
- `internal/config/config_test.go`：配置行为测试。
- `internal/platform/postgres/pool.go`：兼容 Neon 的 `pgxpool` 创建、pgvector 类型注册、连通性检查和关闭逻辑。
- `internal/platform/postgres/pool_test.go`：无需外部凭据的配置错误与连接钩子测试。
- `internal/agent/graph.go`：Eino Graph 的强类型编译与调用。
- `internal/agent/graph_test.go`：Graph 编译、输入标准化与校验测试。
- `internal/http/app.go`：Hertz 引擎构建和路由注册。
- `internal/http/health.go`：基于 `HealthChecker` 接口的存活与就绪检查。
- `internal/http/health_test.go`：使用伪造检查器验证 Hertz 路由行为。
- `migrations/000001_init.up.sql`：启用 pgvector 并创建商品与分块表。
- `migrations/000001_init.down.sql`：移除第一阶段数据表，但不删除共享扩展。
- `.env.example`：安全的本地配置契约。
- `.gitignore`：防止凭据和构建产物进入版本库。
- `Makefile`：统一格式化、测试、静态检查、构建和启动命令。
- `README.md`：环境配置、Neon 连接方式、迁移、启动和验证说明。

---

### 任务一：仓库契约与配置

**文件：**

- 新建：`AGENTS.md`
- 新建：`internal/config/config.go`
- 新建：`internal/config/config_test.go`
- 新建：`.env.example`
- 新建：`.gitignore`
- 修改：`go.mod`

**接口：**

- 产出 `config.Config`。
- 产出 `config.Load(getenv func(string) string) (Config, error)`。
- `Config` 包含 `Environment string`、`Address string`、`DatabaseURL string`、`DatabaseMaxConns int32`、`DatabaseMinConns int32`、`DatabaseMaxConnIdleTime time.Duration`、`DatabaseConnectTimeout time.Duration`、`RequestTimeout time.Duration`、`GraphTimeout time.Duration`、`ShutdownTimeout time.Duration` 和 `LogLevel string`。

- [ ] **步骤 1：先编写失败的配置测试**

```go
func TestLoadUsesSafeDefaults(t *testing.T) {
	getenv := func(key string) string {
		if key == "DATABASE_URL" { return "postgres://runtime" }
		return ""
	}
	cfg, err := Load(getenv)
	if err != nil { t.Fatal(err) }
	if cfg.Address != ":3000" { t.Fatalf("Address = %q", cfg.Address) }
	if cfg.Environment != "development" { t.Fatalf("Environment = %q", cfg.Environment) }
	if cfg.DatabaseMaxConns != 4 { t.Fatalf("DatabaseMaxConns = %d", cfg.DatabaseMaxConns) }
}

func TestLoadUsesVercelPortAndEnvironment(t *testing.T) {
	getenv := func(key string) string {
		values := map[string]string{
			"DATABASE_URL": "postgres://runtime",
			"PORT": "8080",
			"VERCEL_ENV": "preview",
		}
		return values[key]
	}
	cfg, err := Load(getenv)
	if err != nil { t.Fatal(err) }
	if cfg.Address != ":8080" { t.Fatalf("Address = %q", cfg.Address) }
	if cfg.Environment != "preview" { t.Fatalf("Environment = %q", cfg.Environment) }
}

func TestLoadOnlyRequiresRuntimeDatabaseURL(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil { t.Fatal("expected missing DATABASE_URL error") }
}
```

- [ ] **步骤 2：运行聚焦测试并确认红灯**

运行：`go test ./internal/config -run TestLoad -v`

预期：测试失败，原因是 `internal/config` 包和 `Load` 尚不存在。

- [ ] **步骤 3：实现最小环境变量解析**

```go
type Config struct {
	Environment             string
	Address                 string
	DatabaseURL             string
	DatabaseMaxConns        int32
	DatabaseMinConns        int32
	DatabaseMaxConnIdleTime time.Duration
	DatabaseConnectTimeout  time.Duration
	RequestTimeout          time.Duration
	GraphTimeout            time.Duration
	ShutdownTimeout         time.Duration
	LogLevel                string
}

func Load(getenv func(string) string) (Config, error) {
	environment := valueOr(getenv("APP_ENV"), valueOr(getenv("VERCEL_ENV"), "development"))
	address := strings.TrimSpace(getenv("HTTP_ADDR"))
	if port := strings.TrimSpace(getenv("PORT")); port != "" {
		address = ":" + port
	}
	if address == "" { address = ":3000" }
	databaseMaxConns, err := int32Value(getenv("DB_MAX_CONNS"), 4)
	if err != nil { return Config{}, fmt.Errorf("DB_MAX_CONNS: %w", err) }
	databaseMinConns, err := int32Value(getenv("DB_MIN_CONNS"), 0)
	if err != nil { return Config{}, fmt.Errorf("DB_MIN_CONNS: %w", err) }
	databaseMaxConnIdleTime, err := durationValue(getenv("DB_MAX_CONN_IDLE_TIME"), 30*time.Second)
	if err != nil { return Config{}, fmt.Errorf("DB_MAX_CONN_IDLE_TIME: %w", err) }
	databaseConnectTimeout, err := durationValue(getenv("DB_CONNECT_TIMEOUT"), 5*time.Second)
	if err != nil { return Config{}, fmt.Errorf("DB_CONNECT_TIMEOUT: %w", err) }
	requestTimeout, err := durationValue(getenv("REQUEST_TIMEOUT"), 30*time.Second)
	if err != nil { return Config{}, fmt.Errorf("REQUEST_TIMEOUT: %w", err) }
	graphTimeout, err := durationValue(getenv("GRAPH_TIMEOUT"), 60*time.Second)
	if err != nil { return Config{}, fmt.Errorf("GRAPH_TIMEOUT: %w", err) }
	shutdownTimeout, err := durationValue(getenv("SHUTDOWN_TIMEOUT"), 10*time.Second)
	if err != nil { return Config{}, fmt.Errorf("SHUTDOWN_TIMEOUT: %w", err) }
	cfg := Config{
		Environment: environment,
		Address: address,
		DatabaseURL: strings.TrimSpace(getenv("DATABASE_URL")),
		DatabaseMaxConns: databaseMaxConns,
		DatabaseMinConns: databaseMinConns,
		DatabaseMaxConnIdleTime: databaseMaxConnIdleTime,
		DatabaseConnectTimeout: databaseConnectTimeout,
		RequestTimeout: requestTimeout,
		GraphTimeout: graphTimeout,
		ShutdownTimeout: shutdownTimeout,
		LogLevel: valueOr(getenv("LOG_LEVEL"), "info"),
	}
	if cfg.DatabaseURL == "" { return Config{}, errors.New("DATABASE_URL is required") }
	if cfg.DatabaseMinConns > cfg.DatabaseMaxConns { return Config{}, errors.New("DB_MIN_CONNS must not exceed DB_MAX_CONNS") }
	return cfg, nil
}

func int32Value(raw string, fallback int32) (int32, error) {
	if strings.TrimSpace(raw) == "" { return fallback, nil }
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 0 { return 0, errors.New("must be a non-negative integer") }
	return int32(value), nil
}

func durationValue(raw string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" { return fallback, nil }
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 { return 0, errors.New("must be a positive Go duration") }
	return value, nil
}
```

`.env.example` 只能包含以下安全示例：

```dotenv
APP_ENV=development
HTTP_ADDR=:3000
DATABASE_URL=postgresql://USER:PASSWORD@HOST-pooler.REGION.aws.neon.tech/DATABASE?sslmode=require
DATABASE_MIGRATION_URL=postgresql://USER:PASSWORD@HOST.REGION.aws.neon.tech/DATABASE?sslmode=require
DB_MAX_CONNS=4
DB_MIN_CONNS=0
DB_MAX_CONN_IDLE_TIME=30s
DB_CONNECT_TIMEOUT=5s
REQUEST_TIMEOUT=30s
GRAPH_TIMEOUT=60s
SHUTDOWN_TIMEOUT=10s
LOG_LEVEL=info
```

`PORT`、`VERCEL_ENV` 和其他 `VERCEL_*` 变量由 Vercel 自动注入，不写入 `.env.example`，也不要求用户在 Vercel 控制台重复配置。

本次骨架在 Vercel 控制台只强制要求 `DATABASE_URL`。Production、Preview 和 Development 分别配置对应的 Neon 池化连接串。`DATABASE_MIGRATION_URL` 不配置到 Vercel 运行时，只保存在本地安全环境或 CI 密钥中。

根据已确认的设计文档 `docs/superpowers/specs/2026-07-22-backend-agents-design.md` 创建 `AGENTS.md`，保留五个阶段门禁，并明确本次增量只实现基础骨架。

将模块路径改为 `github.com/PolarishT/sales-agent`，保留 `go 1.23`，移除 Gin，并加入全局约束中指定版本的直接依赖。

- [ ] **步骤 4：运行测试并确认绿灯**

运行：`go test ./internal/config -v`

预期：全部通过。

- [ ] **步骤 5：提交**

```bash
git add AGENTS.md .env.example .gitignore go.mod go.sum internal/config
git commit -m "chore: 建立后端项目契约"
```

---

### 任务二：Neon Postgres 与 pgvector 基础设施

**文件：**

- 新建：`internal/platform/postgres/pool.go`
- 新建：`internal/platform/postgres/pool_test.go`
- 新建：`migrations/000001_init.up.sql`
- 新建：`migrations/000001_init.down.sql`

**接口：**

- 使用 `Config.DatabaseURL` 处理运行时数据库流量，并应用连接数与空闲时间配置。
- 产出 `postgres.Options{MaxConns int32, MinConns int32, MaxConnIdleTime time.Duration}`。
- 产出 `postgres.NewPool(ctx context.Context, databaseURL string, options Options) (*pgxpool.Pool, error)`。
- 返回的连接池必须提供 `Ping(context.Context) error` 和 `Close()`，供服务生命周期管理使用。

- [ ] **步骤 1：先编写失败的连接池配置测试**

```go
func TestNewPoolRejectsEmptyURL(t *testing.T) {
	pool, err := NewPool(context.Background(), "", Options{})
	if err == nil { t.Fatal("expected error") }
	if pool != nil { t.Fatal("expected nil pool") }
}

func TestParseConfigInstallsVectorRegistration(t *testing.T) {
	cfg, err := parseConfig("postgres://user:pass@localhost:5432/app?sslmode=disable", Options{
		MaxConns: 4,
		MinConns: 0,
		MaxConnIdleTime: 30 * time.Second,
	})
	if err != nil { t.Fatal(err) }
	if cfg.AfterConnect == nil { t.Fatal("AfterConnect must register pgvector types") }
	if cfg.MaxConns != 4 { t.Fatalf("MaxConns = %d", cfg.MaxConns) }
}
```

- [ ] **步骤 2：运行聚焦测试并确认红灯**

运行：`go test ./internal/platform/postgres -v`

预期：测试失败，原因是 `NewPool` 和 `parseConfig` 尚不存在。

- [ ] **步骤 3：实现 pgxpool 创建逻辑**

```go
type Options struct {
	MaxConns       int32
	MinConns       int32
	MaxConnIdleTime time.Duration
}

func parseConfig(databaseURL string, options Options) (*pgxpool.Config, error) {
	if strings.TrimSpace(databaseURL) == "" { return nil, errors.New("database URL is required") }
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil { return nil, fmt.Errorf("parse database URL: %w", err) }
	cfg.MaxConns = options.MaxConns
	cfg.MinConns = options.MinConns
	cfg.MaxConnIdleTime = options.MaxConnIdleTime
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := pgxvec.RegisterTypes(ctx, conn); err != nil {
			return fmt.Errorf("register pgvector types: %w", err)
		}
		return nil
	}
	return cfg, nil
}

func NewPool(ctx context.Context, databaseURL string, options Options) (*pgxpool.Pool, error) {
	cfg, err := parseConfig(databaseURL, options)
	if err != nil { return nil, err }
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil { return nil, fmt.Errorf("create postgres pool: %w", err) }
	return pool, nil
}
```

- [ ] **步骤 4：添加可逆数据库迁移**

`000001_init.up.sql` 使用可直接执行且不含模板变量的 SQL：

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE products (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    external_id text NOT NULL UNIQUE,
    name text NOT NULL,
    category text NOT NULL,
    price_amount numeric(12, 2) NOT NULL CHECK (price_amount >= 0),
    currency char(3) NOT NULL,
    description text NOT NULL,
    image_url text,
    inventory_count integer NOT NULL DEFAULT 0 CHECK (inventory_count >= 0),
    attributes jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE product_chunks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id uuid NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    content text NOT NULL,
    chunk_index integer NOT NULL CHECK (chunk_index >= 0),
    embedding vector NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (product_id, chunk_index)
);

CREATE INDEX product_chunks_product_id_idx ON product_chunks(product_id);
CREATE INDEX products_category_idx ON products(category);
```

`000001_init.down.sql` 必须先删除 `product_chunks`，再删除 `products`，且不能删除共享的 `vector` 扩展。

- [ ] **步骤 5：运行测试并确认绿灯**

运行：`go test ./internal/platform/postgres -v`

预期：无需连接真实 Neon 数据库即可全部通过。

- [ ] **步骤 6：提交**

```bash
git add internal/platform/postgres migrations
git commit -m "feat: 增加 Neon pgvector 基础设施"
```

---

### 任务三：最小 Eino Graph

**文件：**

- 新建：`internal/agent/graph.go`
- 新建：`internal/agent/graph_test.go`

**接口：**

- 产出 `agent.Request{Query string}`。
- 产出 `agent.Response{Query string, Stage string}`。
- 产出 `agent.Runner` 接口：`Invoke(context.Context, Request) (Response, error)`。
- 产出 `agent.NewGraph(ctx context.Context) (Runner, error)`。

- [ ] **步骤 1：先编写失败的 Graph 测试**

```go
func TestGraphNormalizesQuery(t *testing.T) {
	runner, err := NewGraph(context.Background())
	if err != nil { t.Fatal(err) }
	got, err := runner.Invoke(context.Background(), Request{Query: "  推荐护肤品  "})
	if err != nil { t.Fatal(err) }
	if got.Query != "推荐护肤品" { t.Fatalf("Query = %q", got.Query) }
	if got.Stage != "skeleton" { t.Fatalf("Stage = %q", got.Stage) }
}

func TestGraphRejectsEmptyQuery(t *testing.T) {
	runner, err := NewGraph(context.Background())
	if err != nil { t.Fatal(err) }
	_, err = runner.Invoke(context.Background(), Request{Query: "  "})
	if err == nil { t.Fatal("expected validation error") }
}
```

- [ ] **步骤 2：运行聚焦测试并确认红灯**

运行：`go test ./internal/agent -v`

预期：测试失败，原因是 `NewGraph`、`Request` 和 `Response` 尚不存在。

- [ ] **步骤 3：编译单节点强类型 Eino Graph**

```go
type Request struct { Query string `json:"query"` }
type Response struct { Query string `json:"query"`; Stage string `json:"stage"` }
type Runner interface { Invoke(context.Context, Request) (Response, error) }

func NewGraph(ctx context.Context) (Runner, error) {
	graph := compose.NewGraph[Request, Response]()
	normalize := compose.InvokableLambda(func(ctx context.Context, in Request) (Response, error) {
		query := strings.TrimSpace(in.Query)
		if query == "" { return Response{}, errors.New("query is required") }
		return Response{Query: query, Stage: "skeleton"}, nil
	})
	if err := graph.AddLambdaNode("normalize_input", normalize); err != nil { return nil, err }
	if err := graph.AddEdge(compose.START, "normalize_input"); err != nil { return nil, err }
	if err := graph.AddEdge("normalize_input", compose.END); err != nil { return nil, err }
	return graph.Compile(ctx, compose.WithGraphName("sales_agent_skeleton"))
}
```

- [ ] **步骤 4：运行测试并确认绿灯**

运行：`go test ./internal/agent -v`

预期：全部通过。

- [ ] **步骤 5：提交**

```bash
git add internal/agent
git commit -m "feat: 增加 Eino Graph 骨架"
```

---

### 任务四：Hertz 应用、Vercel 入口与健康检查契约

**文件：**

- 新建：`internal/http/health.go`
- 新建：`internal/http/app.go`
- 新建：`internal/http/health_test.go`
- 重写：`main.go`
- 删除：`public/index.html`
- 删除：`public/favicon.ico`

**接口：**

- 接收实现 `HealthChecker { Ping(context.Context) error }` 的数据库对象。
- 接收 `agent.Runner`，为后续 `/api/v1/chat/stream` 做好装配准备，但本次不暴露空壳聊天接口。
- 产出 `http.NewApp(address string, checker HealthChecker) *server.Hertz`。
- 产出 `GET /api/v1/health/live` 和 `GET /api/v1/health/ready`。

- [ ] **步骤 1：先编写失败的 Handler 测试**

```go
type fakeChecker struct{ err error }
func (f fakeChecker) Ping(context.Context) error { return f.err }

func TestLivenessReturnsOK(t *testing.T) {
	app := NewApp(":0", fakeChecker{})
	w := ut.PerformRequest(app.Engine, "GET", "/api/v1/health/live", nil)
	if w.Code != 200 { t.Fatalf("status = %d", w.Code) }
}

func TestReadinessReturnsUnavailableWhenDatabaseFails(t *testing.T) {
	app := NewApp(":0", fakeChecker{err: errors.New("database unavailable")})
	w := ut.PerformRequest(app.Engine, "GET", "/api/v1/health/ready", nil)
	if w.Code != 503 { t.Fatalf("status = %d", w.Code) }
}
```

- [ ] **步骤 2：运行聚焦测试并确认红灯**

运行：`go test ./internal/http -v`

预期：测试失败，原因是 `NewApp` 和健康检查 Handler 尚不存在。

- [ ] **步骤 3：实现带版本的健康检查路由**

使用 Hertz 的 `server.New(server.WithHostPorts(address))`。`/api/v1/health/live` 返回 `{"status":"ok"}`；`/api/v1/health/ready` 在两秒超时内调用 `checker.Ping`。数据库检查失败时返回 HTTP 503 和稳定响应 `{"status":"unavailable","code":"DATABASE_UNAVAILABLE"}`，不得泄露原始数据库错误。

- [ ] **步骤 4：添加进程启动与优雅关闭**

根目录 `main.go` 必须按顺序：

1. 加载并校验配置；
2. 创建进程级 Neon 运行时连接池，并应用单实例连接数限制；
3. 使用五秒超时检查数据库连通性；
4. 编译 Eino Graph，确保错误配置在启动时尽早失败；
5. 创建 Hertz 应用；
6. 使用配置解析出的监听地址启动 Hertz：Vercel 上来自 `PORT`，本地默认 `:3000`；
7. 在收到 `SIGINT` 或 `SIGTERM` 时按配置的超时优雅关闭；
8. 退出时关闭数据库连接池；
9. 任何日志均不得包含数据库连接串、模型密钥或 Vercel 系统令牌。

Hertz 测试通过后，再删除 Gin 入口和模板静态资源。

- [ ] **步骤 5：运行测试并确认绿灯**

运行：`go test ./internal/http . -v`

预期：全部通过；根包可以显示 `[no test files]`。

- [ ] **步骤 6：提交**

```bash
git add internal/http main.go public
git commit -m "feat: 使用 Hertz 提供后端骨架"
```

---

### 任务五：开发流程与中文文档

**文件：**

- 新建：`Makefile`
- 重写：`README.md`

**接口：**

- 提供 `make fmt`、`make test`、`make vet`、`make build`、`make run` 和 `make vercel-dev`。
- 说明运行时池化地址、迁移直连地址、vector 扩展、精确检索的选择以及密钥安全规则。

- [ ] **步骤 1：添加统一开发命令**

```make
.PHONY: fmt test vet build run vercel-dev

fmt:
	gofmt -w $$(find . -path './.git' -prune -o -name '*.go' -print)

test:
	go test ./...

vet:
	go vet ./...

build:
	go build .

run:
	go run .

vercel-dev:
	vercel dev
```

- [ ] **步骤 2：用中文重写 README**

README 必须说明：

- 架构与目录职责；
- Go 1.23+ 前置条件；
- 使用 `cp .env.example .env` 创建本地配置，且不得展示真实凭据；
- Neon 池化地址用于运行时，直连地址用于迁移；
- Vercel 会自动注入 `PORT`、`VERCEL_ENV` 和 `VERCEL_REGION`，项目不重复配置这些变量；
- Vercel 控制台中 Production、Preview、Development 三套变量的配置方式；
- 使用 `vercel env pull .env.local` 获取本地开发变量，并确保 `.env.local` 被忽略；
- 使用 `psql "$DATABASE_MIGRATION_URL" -f migrations/000001_init.up.sql` 执行迁移；
- `make run`、`make vercel-dev`、存活检查和就绪检查地址；
- Eino Graph 骨架行为；
- 50–100 条数据先采用精确余弦检索，Embedding 维度确定后再添加 HNSW 的原因；
- 验证命令与第一阶段的下一个增量。

README 必须提供以下 Vercel 变量分类表：

| 分类 | 变量 | 是否必需 | 配置位置 |
| --- | --- | --- | --- |
| 平台注入 | `PORT`、`VERCEL_ENV`、`VERCEL_REGION` | 自动 | 不手工配置 |
| 骨架运行时 | `DATABASE_URL` | 是 | Vercel 三套环境分别配置 |
| 运行时调优 | `DB_MAX_CONNS`、`DB_MIN_CONNS`、`DB_MAX_CONN_IDLE_TIME`、`DB_CONNECT_TIMEOUT`、`REQUEST_TIMEOUT`、`GRAPH_TIMEOUT`、`LOG_LEVEL` | 否 | Vercel 或默认值 |
| 仅迁移 | `DATABASE_MIGRATION_URL` | 执行迁移时必需 | 本地安全环境或 CI Secret，不配置到运行时 |
| 后续阶段 | `LLM_BASE_URL`、`LLM_API_KEY`、`LLM_MODEL`、`EMBEDDING_BASE_URL`、`EMBEDDING_API_KEY`、`EMBEDDING_MODEL`、`EMBEDDING_DIMENSION` | 对应功能启用后 | Vercel 三套环境分别配置 |

- [ ] **步骤 3：运行完整验证**

运行：

```bash
make fmt
git diff --check
make test
make vet
make build
```

预期：所有命令退出码均为 0，仓库中不留下生成的 `server` 二进制文件。

- [ ] **步骤 4：扫描密钥与遗留 Gin 内容**

运行：

```bash
rg -n 'ark-[A-Za-z0-9-]+|postgres(ql)?://[^[:space:]]+:[^[:space:]]+@' --glob '!go.sum' --glob '!.env.example' .
rg -n 'gin-gonic|gin\.Context|Vercel \+ Gin' .
```

预期：两个命令均无匹配结果。

- [ ] **步骤 5：提交**

```bash
git add Makefile README.md
git commit -m "docs: 说明后端骨架开发流程"
```

---

## 计划自检

- 需求覆盖：计划包含 Hertz 替换、Vercel 零配置 Go 后端入口、Eino Graph 编译、Neon 运行时连接、pgvector 表结构、版本化健康检查、分层环境变量、安全配置、阶段指令、测试和文档。
- 明确延期：本次骨架不实现 LLM、Embedding 服务、商品导入、检索仓储、SSE 对话接口或 HNSW 索引。
- 类型一致性：`HealthChecker.Ping`、`agent.Runner.Invoke`、`config.Config` 和 `postgres.NewPool` 在生产者与消费者之间保持一致。
- 安全性：计划中不包含真实数据库地址、密码、API Key、模型 ID 或需求文档中的凭据。
