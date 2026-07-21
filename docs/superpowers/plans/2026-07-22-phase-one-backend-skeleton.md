# Phase One Backend Skeleton Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Gin starter with a runnable, testable Go backend skeleton using Hertz, Eino Graph, and Neon Postgres with pgvector support.

**Architecture:** Hertz owns transport and lifecycle, Eino Graph owns typed request orchestration, and `pgxpool` owns PostgreSQL connectivity behind small interfaces. The skeleton exposes liveness/readiness endpoints, compiles and invokes a minimal Eino graph, supplies direct-connection SQL migrations for Neon, and leaves model/retrieval behavior for the next Phase One increment.

**Tech Stack:** Go 1.23+, Hertz v0.10.5, Eino v0.9.12, pgx/v5 v5.7.4, pgvector-go v0.4.0, Neon Postgres with the `vector` extension.

## Global Constraints

- Backend only; do not implement iOS, Android, or a replacement web client.
- Replace Gin with Hertz and do not keep a Gin compatibility layer.
- Use Eino Graph for all Agent orchestration, including the minimal skeleton path.
- Use `DATABASE_URL` for pooled runtime traffic and `DATABASE_MIGRATION_URL` for direct migration traffic.
- Never commit credentials; `.env` remains ignored and `.env.example` contains placeholders only.
- For the initial 50–100 item dataset, keep exact pgvector cosine search available and defer HNSW creation until the embedding model and dimension are fixed.
- Production packages must not depend on test-only database or HTTP implementations.

---

## File Structure

- `AGENTS.md`: repository-wide implementation constraints and phase gates.
- `cmd/server/main.go`: process entry, signal handling, dependency construction, and Hertz startup.
- `internal/config/config.go`: environment parsing and validation.
- `internal/config/config_test.go`: configuration behavior.
- `internal/platform/postgres/pool.go`: Neon-compatible `pgxpool` construction, pgvector type registration, ping, and close.
- `internal/platform/postgres/pool_test.go`: invalid configuration and connection-hook tests that do not require external credentials.
- `internal/agent/graph.go`: typed Eino Graph compilation and invocation.
- `internal/agent/graph_test.go`: graph compilation, normalization, and validation tests.
- `internal/http/app.go`: Hertz engine construction and route registration.
- `internal/http/health.go`: liveness/readiness handlers behind a `HealthChecker` interface.
- `internal/http/health_test.go`: Hertz route behavior with a fake checker.
- `migrations/000001_init.up.sql`: enable pgvector and create catalog/chunk schema.
- `migrations/000001_init.down.sql`: remove Phase One tables without removing the shared extension.
- `.env.example`: safe local configuration contract.
- `.gitignore`: prevent credential and build artifacts from being committed.
- `Makefile`: deterministic format, test, vet, build, and run commands.
- `README.md`: setup, Neon connection modes, migration, run, and verification instructions.

---

### Task 1: Repository Contract and Configuration

**Files:**
- Create: `AGENTS.md`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `.env.example`
- Create: `.gitignore`
- Modify: `go.mod`

**Interfaces:**
- Produces: `config.Config`, `config.Load(getenv func(string) string) (Config, error)`.
- `Config` contains `Environment string`, `Address string`, `DatabaseURL string`, `DatabaseMigrationURL string`, and `ShutdownTimeout time.Duration`.

- [ ] **Step 1: Write the failing configuration tests**

```go
func TestLoadUsesSafeDefaults(t *testing.T) {
	getenv := func(key string) string {
		if key == "DATABASE_URL" { return "postgres://runtime" }
		if key == "DATABASE_MIGRATION_URL" { return "postgres://migration" }
		return ""
	}
	cfg, err := Load(getenv)
	if err != nil { t.Fatal(err) }
	if cfg.Address != ":3000" { t.Fatalf("Address = %q", cfg.Address) }
	if cfg.Environment != "development" { t.Fatalf("Environment = %q", cfg.Environment) }
}

func TestLoadRequiresDatabaseURLs(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil { t.Fatal("expected missing database configuration error") }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/config -run TestLoad -v`

Expected: FAIL because package `internal/config` and `Load` do not exist.

- [ ] **Step 3: Implement minimal environment parsing**

```go
type Config struct {
	Environment          string
	Address              string
	DatabaseURL          string
	DatabaseMigrationURL string
	ShutdownTimeout      time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Environment: valueOr(getenv("APP_ENV"), "development"),
		Address: valueOr(getenv("HTTP_ADDR"), ":3000"),
		DatabaseURL: strings.TrimSpace(getenv("DATABASE_URL")),
		DatabaseMigrationURL: strings.TrimSpace(getenv("DATABASE_MIGRATION_URL")),
		ShutdownTimeout: 10 * time.Second,
	}
	if cfg.DatabaseURL == "" { return Config{}, errors.New("DATABASE_URL is required") }
	if cfg.DatabaseMigrationURL == "" { return Config{}, errors.New("DATABASE_MIGRATION_URL is required") }
	return cfg, nil
}
```

Add `.env.example` with only:

```dotenv
APP_ENV=development
HTTP_ADDR=:3000
DATABASE_URL=postgresql://USER:PASSWORD@HOST-pooler.REGION.aws.neon.tech/DATABASE?sslmode=require
DATABASE_MIGRATION_URL=postgresql://USER:PASSWORD@HOST.REGION.aws.neon.tech/DATABASE?sslmode=require
```

Create `AGENTS.md` from the approved design at `docs/superpowers/specs/2026-07-22-backend-agents-design.md`, keeping its five phase gates and limiting this increment to the skeleton.

Change the module path to `github.com/PolarishT/sales-agent`, retain `go 1.23`, remove Gin, and add the pinned direct dependencies from the global constraints.

- [ ] **Step 4: Run tests and verify GREEN**

Run: `go test ./internal/config -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add AGENTS.md .env.example .gitignore go.mod go.sum internal/config
git commit -m "chore: establish backend project contract"
```

---

### Task 2: Neon Postgres and pgvector Foundation

**Files:**
- Create: `internal/platform/postgres/pool.go`
- Create: `internal/platform/postgres/pool_test.go`
- Create: `migrations/000001_init.up.sql`
- Create: `migrations/000001_init.down.sql`

**Interfaces:**
- Consumes: `Config.DatabaseURL` for runtime traffic.
- Produces: `postgres.NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error)`.
- The returned pool implements `Ping(context.Context) error` and `Close()` for server lifecycle use.

- [ ] **Step 1: Write the failing pool configuration tests**

```go
func TestNewPoolRejectsEmptyURL(t *testing.T) {
	pool, err := NewPool(context.Background(), "")
	if err == nil { t.Fatal("expected error") }
	if pool != nil { t.Fatal("expected nil pool") }
}

func TestParseConfigInstallsVectorRegistration(t *testing.T) {
	cfg, err := parseConfig("postgres://user:pass@localhost:5432/app?sslmode=disable")
	if err != nil { t.Fatal(err) }
	if cfg.AfterConnect == nil { t.Fatal("AfterConnect must register pgvector types") }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/platform/postgres -v`

Expected: FAIL because `NewPool` and `parseConfig` do not exist.

- [ ] **Step 3: Implement the pgxpool constructor**

```go
func parseConfig(databaseURL string) (*pgxpool.Config, error) {
	if strings.TrimSpace(databaseURL) == "" { return nil, errors.New("database URL is required") }
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil { return nil, fmt.Errorf("parse database URL: %w", err) }
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if err := pgxvec.RegisterTypes(ctx, conn); err != nil {
			return fmt.Errorf("register pgvector types: %w", err)
		}
		return nil
	}
	return cfg, nil
}

func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := parseConfig(databaseURL)
	if err != nil { return nil, err }
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil { return nil, fmt.Errorf("create postgres pool: %w", err) }
	return pool, nil
}
```

- [ ] **Step 4: Add reversible schema migrations**

`000001_init.up.sql` must contain executable SQL with no template variables:

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

`000001_init.down.sql` must drop `product_chunks` before `products` and must not drop the shared `vector` extension.

- [ ] **Step 5: Run tests and verify GREEN**

Run: `go test ./internal/platform/postgres -v`

Expected: PASS without requiring a live Neon connection.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/postgres migrations
git commit -m "feat: add Neon pgvector foundation"
```

---

### Task 3: Minimal Eino Graph

**Files:**
- Create: `internal/agent/graph.go`
- Create: `internal/agent/graph_test.go`

**Interfaces:**
- Produces: `agent.Request{Query string}`, `agent.Response{Query string, Stage string}`.
- Produces: `agent.Runner` interface with `Invoke(context.Context, Request) (Response, error)`.
- Produces: `agent.NewGraph(ctx context.Context) (Runner, error)`.

- [ ] **Step 1: Write the failing graph tests**

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

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/agent -v`

Expected: FAIL because `NewGraph`, `Request`, and `Response` do not exist.

- [ ] **Step 3: Compile a one-node typed Eino Graph**

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

- [ ] **Step 4: Run tests and verify GREEN**

Run: `go test ./internal/agent -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent
git commit -m "feat: add Eino graph skeleton"
```

---

### Task 4: Hertz Application and Health Contract

**Files:**
- Create: `internal/http/health.go`
- Create: `internal/http/app.go`
- Create: `internal/http/health_test.go`
- Create: `cmd/server/main.go`
- Delete: `main.go`
- Delete: `public/index.html`
- Delete: `public/favicon.ico`

**Interfaces:**
- Consumes: a database value satisfying `HealthChecker { Ping(context.Context) error }`.
- Consumes: `agent.Runner` for future `/api/v1/chat/stream` work but does not expose a placeholder chat endpoint.
- Produces: `http.NewApp(address string, checker HealthChecker) *server.Hertz`.
- Produces: `GET /api/v1/health/live` and `GET /api/v1/health/ready`.

- [ ] **Step 1: Write the failing handler tests**

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

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/http -v`

Expected: FAIL because `NewApp` and health handlers do not exist.

- [ ] **Step 3: Implement versioned health routes**

Use Hertz `server.New(server.WithHostPorts(address))`. Register `/api/v1/health/live` to return `{"status":"ok"}` and `/api/v1/health/ready` to call `checker.Ping` with a two-second timeout. A failed ping returns HTTP 503 with a stable body `{"status":"unavailable","code":"DATABASE_UNAVAILABLE"}`; do not include the raw database error.

- [ ] **Step 4: Add process startup and graceful shutdown**

`cmd/server/main.go` must:

1. load and validate configuration;
2. create the Neon runtime pool;
3. ping the pool during startup with a five-second timeout;
4. compile the Eino Graph so invalid graph configuration fails fast;
5. construct the Hertz app;
6. start Hertz and close it on `SIGINT` or `SIGTERM` using the configured shutdown timeout;
7. close the database pool on exit;
8. never log a connection string.

Remove the Gin entry and starter static assets after the Hertz tests pass.

- [ ] **Step 5: Run tests and verify GREEN**

Run: `go test ./internal/http ./cmd/server -v`

Expected: PASS; `cmd/server` may report `[no test files]`.

- [ ] **Step 6: Commit**

```bash
git add cmd internal/http main.go public
git commit -m "feat: serve backend skeleton with Hertz"
```

---

### Task 5: Developer Workflow and Documentation

**Files:**
- Create: `Makefile`
- Rewrite: `README.md`

**Interfaces:**
- Produces: `make fmt`, `make test`, `make vet`, `make build`, and `make run`.
- Documents: pooled runtime URL, direct migration URL, vector extension, exact-search rationale, and no-secret policy.

- [ ] **Step 1: Add workflow commands**

```make
.PHONY: fmt test vet build run

fmt:
	gofmt -w $$(find cmd internal -name '*.go')

test:
	go test ./...

vet:
	go vet ./...

build:
	go build ./cmd/server

run:
	go run ./cmd/server
```

- [ ] **Step 2: Rewrite README for the new skeleton**

Document:

- architecture and directory map;
- Go 1.23+ prerequisite;
- `cp .env.example .env` without showing real credentials;
- Neon pooled URL for runtime and direct URL for migrations;
- applying `migrations/000001_init.up.sql` with `psql "$DATABASE_MIGRATION_URL" -f ...`;
- `make run`, liveness, and readiness URLs;
- the Eino Graph skeleton behavior;
- why exact cosine search is retained for 50–100 records and HNSW is deferred until embedding dimensions are fixed;
- verification commands and the next Phase One increment.

- [ ] **Step 3: Run full verification**

Run:

```bash
make fmt
git diff --check
make test
make vet
make build
```

Expected: every command exits 0 and the repository contains no generated `server` binary.

- [ ] **Step 4: Scan for secrets and stale Gin references**

Run:

```bash
rg -n 'ark-[A-Za-z0-9-]+|postgres(ql)?://[^[:space:]]+:[^[:space:]]+@' --glob '!go.sum' --glob '!.env.example' .
rg -n 'gin-gonic|gin\.Context|Vercel \+ Gin' .
```

Expected: both commands return no matches.

- [ ] **Step 5: Commit**

```bash
git add Makefile README.md
git commit -m "docs: explain backend skeleton workflow"
```

---

## Plan Self-Review

- Spec coverage: the plan covers Hertz replacement, Eino Graph compilation, Neon runtime connectivity, pgvector schema, versioned health endpoints, safe configuration, phase instructions, tests, and documentation.
- Deliberate deferrals: no LLM, embedding provider, product ingestion, retrieval repository, SSE chat endpoint, or HNSW index is implemented in this skeleton increment.
- Type consistency: `HealthChecker.Ping`, `agent.Runner.Invoke`, `config.Config`, and `postgres.NewPool` signatures are consistent across producers and consumers.
- Security: no real database URL, password, API key, model ID, or document credential appears in the plan.
