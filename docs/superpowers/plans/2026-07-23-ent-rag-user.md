# Ent `rag_users` Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 删除自定义 `pgxpool` 基础设施，按 Ent 官方快速入门生成 `RagUser` 模型，并由根入口使用 `DATABASE_URL` 打开 PostgreSQL Ent Client。

**Architecture:** `ent/schema/raguser.go` 是既有 `rag_users` 表的强类型映射，生成的 `ent.Client` 由 `main.go` 创建并在进程退出时关闭。HTTP readiness 通过一个小型适配器执行 `RagUser.Query().Exist(ctx)`，Handler 继续只依赖 `Ping(context.Context) error` 接口。

**Tech Stack:** Go 1.25.5、Ent v0.14.6、lib/pq v1.12.3、PostgreSQL/Neon、Hertz v0.10.5、Eino v0.9.12。

## Global Constraints

- 所有工作直接在用户指定的 `main` 分支执行，不创建 worktree。
- 真实连接串不得写入代码、测试、文档、命令或 Git 历史；只读取 `DATABASE_URL`。
- 使用 Ent 教程的 `ent.Open("postgres", settings.DatabaseURL)` 与 `github.com/lib/pq` 驱动。
- `lib/pq` 不支持 `channel_binding=require`；运行时 `DATABASE_URL` 必须省略这个参数并保留 `sslmode=require`。
- `rag_users` 已存在；不得调用 `client.Schema.Create`，不得连接或修改远程 Neon 数据库。
- 不新增根 `migrations` 目录；Ent 自动生成的 `ent/migrate` 仅是客户端生成物，不在启动路径调用。
- `biz/model`、`biz/router`、`router_gen.go` 是 hz 生成物，不手工修改。
- 生产代码必须在对应失败测试出现后实现；Ent 自动生成文件除外。

---

## File Map

- Create `ent/schema/raguser.go`: `rag_users` 表的唯一 Ent Schema 源。
- Create `ent/schema/rag_user_test.go`: 验证表名和五个字段的数据库映射。
- Generate `ent/*.go`, `ent/raguser/*.go`, `ent/migrate/*.go`: Ent CLI 生成物，禁止手工修改。
- Create `internal/platform/database/readiness.go`: 将 Ent 查询适配为现有健康检查接口。
- Create `internal/platform/database/readiness_test.go`: 验证空表、错误和缺失依赖。
- Modify `main.go`: 直接 `ent.Open`、关闭 Client 并注入 readiness。
- Modify `internal/config/config.go`: 删除连接池字段和环境变量解析。
- Modify `internal/config/config_test.go`: 验证连接池配置已经退出公共配置。
- Delete `internal/platform/postgres/pool.go` and `pool_test.go`: 删除 `NewPool` 全部实现。
- Modify `go.mod`, `go.sum`: 增加 Ent/libpq，移除 pgx/pgvector。
- Modify `Makefile`: 同时运行 hz 与 Ent 生成。
- Modify `.env.example`, `README.md`, `AGENTS.md`: 同步运行方式和工程边界。

---

### Task 1: Generate and Validate the `RagUser` Schema

**Files:**
- Create: `ent/schema/rag_user_test.go`
- Create with Ent CLI, then modify: `ent/schema/raguser.go`
- Generate: `ent/*.go`, `ent/raguser/*.go`, `ent/migrate/*.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: screenshot-defined PostgreSQL table `rag_users`.
- Produces: generated `*ent.Client` with `RagUser` API and `ent.Open(driverName, dataSourceName string, options ...ent.Option) (*ent.Client, error)`.

- [ ] **Step 1: Add pinned Ent and PostgreSQL driver dependencies**

Run:

```bash
go get entgo.io/ent@v0.14.6 github.com/lib/pq@v1.12.3
```

Expected: `go.mod` contains direct requirements for Ent and lib/pq; no production behavior changes yet.

- [ ] **Step 2: Write the failing Schema test**

Create `ent/schema/rag_user_test.go`:

```go
package schema

import (
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
)

func TestRagUserTableName(t *testing.T) {
	annotations := (RagUser{}).Annotations()
	if len(annotations) != 1 {
		t.Fatalf("len(Annotations()) = %d, want 1", len(annotations))
	}
	annotation, ok := annotations[0].(entsql.Annotation)
	if !ok || annotation.Table != "rag_users" {
		t.Fatalf("table annotation = %#v, want rag_users", annotations[0])
	}
}

func TestRagUserFieldsMatchExistingTable(t *testing.T) {
	fields := make(map[string]*field.Descriptor)
	for _, configuredField := range (RagUser{}).Fields() {
		descriptor := configuredField.Descriptor()
		if descriptor.Err != nil {
			t.Fatalf("field %q: %v", descriptor.Name, descriptor.Err)
		}
		fields[descriptor.Name] = descriptor
	}
	if len(fields) != 5 {
		t.Fatalf("field count = %d, want 5", len(fields))
	}
	if fields["id"].Info.Type != field.TypeInt64 {
		t.Fatalf("id type = %v, want int64", fields["id"].Info.Type)
	}
	if userID := fields["user_id"]; userID.Info.Type != field.TypeString || userID.Size != 128 || !userID.Unique || userID.Optional {
		t.Fatalf("user_id descriptor = %#v", userID)
	}
	metadata := fields["metadata"]
	if metadata.Info.Type != field.TypeJSON || metadata.SchemaType[dialect.Postgres] != "jsonb" || metadata.Default == nil || metadata.Optional {
		t.Fatalf("metadata descriptor = %#v", metadata)
	}
	for _, name := range []string{"first_seen_at", "last_seen_at"} {
		timestamp := fields[name]
		if timestamp.Info.Type != field.TypeTime || timestamp.SchemaType[dialect.Postgres] != "timestamptz" || timestamp.Default == nil || timestamp.Optional {
			t.Fatalf("%s descriptor = %#v", name, timestamp)
		}
		if timestamp.Immutable || timestamp.UpdateDefault != nil {
			t.Fatalf("%s adds update semantics not present in the database", name)
		}
	}
}
```

- [ ] **Step 3: Run the Schema test and verify RED**

Run:

```bash
go test ./ent/schema
```

Expected: FAIL to compile with `undefined: RagUser`, proving the model is absent.

- [ ] **Step 4: Create the Schema skeleton with the official Ent command**

Run:

```bash
go run -mod=mod entgo.io/ent/cmd/ent new RagUser
```

Expected: Ent creates `ent/schema/rag_user.go` and `ent/generate.go`.

- [ ] **Step 5: Implement the exact existing-table mapping**

Replace `ent/schema/raguser.go` with:

```go
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// RagUser holds the schema definition for the RagUser entity.
type RagUser struct {
	ent.Schema
}

// Fields of the RagUser.
func (RagUser) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("id"),
		field.String("user_id").MaxLen(128).Unique(),
		field.JSON("metadata", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Time("first_seen_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_seen_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

// Edges of the RagUser.
func (RagUser) Edges() []ent.Edge {
	return nil
}

// Annotations fixes the entity to the existing PostgreSQL table name.
func (RagUser) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "rag_users"}}
}
```

- [ ] **Step 6: Run the Schema test and verify GREEN**

Run:

```bash
go test ./ent/schema
```

Expected: PASS.

- [ ] **Step 7: Generate Ent code and run its package tests**

Run:

```bash
go generate ./ent
go test ./ent/... 
```

Expected: Ent Client, entity builders, predicates and generated migration metadata compile; all tests PASS.

- [ ] **Step 8: Commit the Schema slice**

```bash
git add ent go.mod go.sum
git commit -m "feat: 增加 RagUser Ent 模型"
```

---

### Task 2: Adapt Ent to the Existing Readiness Contract

**Files:**
- Create: `internal/platform/database/readiness_test.go`
- Create: `internal/platform/database/readiness.go`

**Interfaces:**
- Consumes: `*ent.Client` and generated `client.RagUser.Query().Exist(context.Context) (bool, error)`.
- Produces: `database.NewReadiness(*ent.Client) *database.Readiness` implementing `Ping(context.Context) error`.

- [ ] **Step 1: Write failing readiness tests**

Create `internal/platform/database/readiness_test.go`:

```go
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
```

- [ ] **Step 2: Run the readiness tests and verify RED**

Run:

```bash
go test ./internal/platform/database
```

Expected: FAIL to compile with `undefined: Readiness` and `undefined: NewReadiness`.

- [ ] **Step 3: Implement the minimal Ent readiness adapter**

Create `internal/platform/database/readiness.go`:

```go
package database

import (
	"context"
	"errors"

	"github.com/PolarishT/sales-agent/ent"
)

type existsFunc func(context.Context) (bool, error)

// Readiness checks that the existing rag_users table is queryable through Ent.
type Readiness struct {
	exists existsFunc
}

// NewReadiness creates a readiness checker backed by an Ent Client.
func NewReadiness(client *ent.Client) *Readiness {
	if client == nil {
		return &Readiness{}
	}
	return &Readiness{exists: func(ctx context.Context) (bool, error) {
		return client.RagUser.Query().Exist(ctx)
	}}
}

// Ping reports whether Ent can query the existing rag_users table.
func (readiness *Readiness) Ping(ctx context.Context) error {
	if readiness == nil || readiness.exists == nil {
		return errors.New("缺少 Ent 数据库客户端")
	}
	_, err := readiness.exists(ctx)
	return err
}
```

- [ ] **Step 4: Run readiness and HTTP tests and verify GREEN**

Run:

```bash
go test ./internal/platform/database ./internal/http ./biz/handler/health
```

Expected: all selected packages PASS; `Readiness` satisfies the existing `HealthChecker` interface structurally.

- [ ] **Step 5: Commit the readiness slice**

```bash
git add internal/platform/database
git commit -m "feat: 使用 Ent 检查数据库就绪状态"
```

---

### Task 3: Remove Pool Configuration and Wire `ent.Open`

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `main.go`
- Delete: `internal/platform/postgres/pool.go`
- Delete: `internal/platform/postgres/pool_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: `config.Config.DatabaseURL`, `ent.Open`, and `database.NewReadiness`.
- Produces: one process-level `*ent.Client` closed by `defer client.Close()`; no `NewPool` or pool tuning fields.

- [ ] **Step 1: Replace pool-config assertions with failing removal tests**

Update `internal/config/config_test.go` so safe defaults only assert address/environment, and add:

```go
func TestLoadIgnoresRemovedPoolTuning(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapGetenv(map[string]string{
		"DATABASE_URL":             "postgres://runtime",
		"DB_MAX_CONNS":             "invalid",
		"DB_MIN_CONNS":             "invalid",
		"DB_MAX_CONN_IDLE_TIME":     "invalid",
		"DB_CONNECT_TIMEOUT":        "7s",
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
```

Add `reflect` to the test imports. Remove the pool-size cases from `TestLoadRejectsInvalidRuntimeTuning`; keep the invalid duration case. Update `TestLoadParsesRuntimeTuning` to stop setting and asserting pool values.

- [ ] **Step 2: Run config tests and verify RED**

Run:

```bash
go test ./internal/config
```

Expected: FAIL because invalid pool environment variables are still parsed or removed fields still exist.

- [ ] **Step 3: Delete pool parsing from configuration**

In `internal/config/config.go`:

- remove `defaultDatabaseMaxConns`, `defaultDatabaseMinConns`, and `defaultDatabaseMaxConnIdleTime`;
- remove `DatabaseMaxConns`, `DatabaseMinConns`, and `DatabaseMaxConnIdleTime` from `Config`;
- remove parsing and validation for `DB_MAX_CONNS`, `DB_MIN_CONNS`, and `DB_MAX_CONN_IDLE_TIME`;
- remove now-unused `positiveInt32Value`, `nonNegativeInt32Value`, and `int32Value` helpers;
- retain `DatabaseConnectTimeout` and its existing positive-duration validation.

The returned value becomes:

```go
return Config{
	Environment: environment,
	Address: address,
	DatabaseURL: databaseURL,
	DatabaseConnectTimeout: connectTimeout,
	RequestTimeout: requestTimeout,
	GraphTimeout: graphTimeout,
	ShutdownTimeout: shutdownTimeout,
	LogLevel: valueOr(getenv("LOG_LEVEL"), defaultLogLevel),
}, nil
```

- [ ] **Step 4: Run config tests and verify GREEN**

Run:

```bash
go test ./internal/config
```

Expected: PASS.

- [ ] **Step 5: Replace `NewPool` with the tutorial-style Ent Client**

In `main.go`:

- import `github.com/PolarishT/sales-agent/ent`;
- import `databaseplatform "github.com/PolarishT/sales-agent/internal/platform/database"`;
- add `_ "github.com/lib/pq"`;
- remove the `internal/platform/postgres` import;
- replace the `NewPool` block with:

```go
client, err := ent.Open("postgres", settings.DatabaseURL)
if err != nil {
	return fmt.Errorf("打开 PostgreSQL Ent 客户端: %w", err)
}
defer client.Close()
```

- inject `HealthChecker: databaseplatform.NewReadiness(client)`.

Do not call `client.Schema.Create`.

- [ ] **Step 6: Delete the old pool package and tidy dependencies**

Delete with `apply_patch`:

```text
internal/platform/postgres/pool.go
internal/platform/postgres/pool_test.go
```

Then run:

```bash
go mod tidy
```

Expected: `github.com/jackc/pgx/v5` and `github.com/pgvector/pgvector-go` disappear when no generated or handwritten code imports them; Ent and lib/pq remain direct requirements.

- [ ] **Step 7: Run focused integration checks**

Run:

```bash
go test ./internal/config ./internal/platform/database ./...
go build .
```

Expected: PASS without establishing a remote database connection; `ent.Open` is lazy and the test suite does not invoke `run()`.

- [ ] **Step 8: Commit the runtime replacement**

```bash
git add main.go internal/config internal/platform go.mod go.sum
git commit -m "refactor: 使用 Ent 替换自定义数据库连接池"
```

---

### Task 4: Make Generation Reproducible and Update Documentation

**Files:**
- Modify: `Makefile`
- Modify: `.env.example`
- Modify: `README.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: generated `ent/generate.go` and existing hz IDL.
- Produces: `make generate` regenerates both hz and Ent outputs; documentation matches runtime behavior.

- [ ] **Step 1: Extend the generation command**

Change `Makefile`:

```make
generate:
	$(HZ) update --idl idl/health.thrift --sort_router
	go generate ./ent
```

- [ ] **Step 2: Remove pool-only environment examples**

Delete these lines from `.env.example`:

```dotenv
DB_MAX_CONNS=4
DB_MIN_CONNS=0
DB_MAX_CONN_IDLE_TIME=30s
```

Keep the placeholder URL free of real credentials and omit `channel_binding=require` for lib/pq compatibility:

```dotenv
DATABASE_URL=postgresql://USER:PASSWORD@HOST-pooler.REGION.aws.neon.tech/DATABASE?sslmode=require
```

- [ ] **Step 3: Update user and agent documentation**

Update `README.md` and `AGENTS.md` so they state:

- Ent generated from `ent/schema` maps the existing `rag_users` table;
- startup uses `ent.Open("postgres", DATABASE_URL)` and does not run `Schema.Create`;
- no custom `pgxpool`, pgvector registration, pool tuning variables, or repository migrations remain;
- `DB_CONNECT_TIMEOUT` only bounds readiness checks;
- `make generate` runs both hz and Ent generation;
- the Neon URL uses the `-pooler` endpoint with `sslmode=require`, and lib/pq URLs omit `channel_binding=require`.

Retain the overall phase restriction against LLM, Embedding, product import, full RAG, SSE, cart and multimodal work.

- [ ] **Step 4: Regenerate and inspect generated-only changes**

Run:

```bash
make generate
git status --short
git diff --check
```

Expected: generation succeeds; only intended hz/Ent generation, documentation and configuration changes appear; no secret appears in the diff.

- [ ] **Step 5: Commit generation and docs**

```bash
git add Makefile .env.example README.md AGENTS.md ent
git commit -m "docs: 同步 Ent 生成与数据库配置"
```

---

### Task 5: Final Verification and Security Audit

**Files:**
- Verify all modified and generated files.

**Interfaces:**
- Consumes: completed Ent integration.
- Produces: evidence that code generation, formatting, tests, vet, build and credential hygiene pass.

- [ ] **Step 1: Run required verification commands**

Run independently and record exit status:

```bash
make generate
make fmt
git diff --check
make test
make vet
make build
```

Expected: every command exits 0.

- [ ] **Step 2: Confirm old pool code and dependencies are gone**

Run:

```bash
rg -n "NewPool|pgxpool|DB_MAX_CONNS|DB_MIN_CONNS|DB_MAX_CONN_IDLE_TIME|pgvector-go/pgx" . --glob '!docs/superpowers/plans/2026-07-22-*' --glob '!docs/superpowers/specs/2026-07-22-*'
go list -m all | rg "entgo.io/ent|github.com/lib/pq|github.com/jackc/pgx|github.com/pgvector"
```

Expected: the first command finds no current code/config references; the module list contains Ent and lib/pq, not pgx or pgvector.

- [ ] **Step 3: Audit for the exposed credential and migration calls**

Run without placing the original secret in shell history:

```bash
rg -n "neondb_owner|npg_|Schema\.Create|channel_binding=require" . --hidden --glob '!.git/**'
```

Expected: no credential, automatic migration call, or incompatible channel-binding parameter is present.

- [ ] **Step 4: Review the final diff and repository status**

Run:

```bash
git status --short --branch
git diff HEAD~3 --stat
git log -4 --oneline
```

Expected: `main` contains the design plus focused Schema, runtime and documentation commits; no unrelated files are changed.
