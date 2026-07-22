# Ent `rag_users` 接入设计

## 目标

删除当前自定义 Neon `pgxpool` 与 pgvector 类型注册，按照 Ent 官方快速入门的代码生成与 PostgreSQL 客户端打开方式，将已经存在的 `rag_users` 表映射为强类型 Ent 实体。

应用只从 `DATABASE_URL` 读取连接串，不在仓库中保存真实数据库凭据。数据库表已经由仓库外流程创建，因此应用启动时不执行自动迁移，也不新增 `migrations` 目录。

## Ent Schema

使用 Ent CLI 创建 `ent/schema/raguser.go` 中的 `RagUser` Schema，并由 `go generate ./ent` 生成客户端代码。通过表注解将实体明确映射到 `rag_users`，避免依赖默认复数命名行为。

字段映射如下：

| PostgreSQL | Ent |
| --- | --- |
| `id BIGSERIAL PRIMARY KEY` | `field.Int64("id")`，使用数据库生成的主键 |
| `user_id VARCHAR(128) UNIQUE NOT NULL` | `field.String("user_id").MaxLen(128).Unique()` |
| `metadata JSONB NOT NULL DEFAULT '{}'` | `field.JSON("metadata", map[string]any{}).Default(...)`，PostgreSQL 类型固定为 `jsonb` |
| `first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()` | `field.Time("first_seen_at").Default(time.Now)` |
| `last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()` | `field.Time("last_seen_at").Default(time.Now)` |

截图没有声明 `first_seen_at` 不可更新，也没有声明 `last_seen_at` 在每次更新时自动刷新，因此 Schema 不额外增加 `Immutable` 或 `UpdateDefault` 语义。

## 数据库客户端与生命周期

根目录 `main.go` 按 Ent PostgreSQL 快速入门方式初始化进程级客户端：

```go
client, err := ent.Open("postgres", settings.DatabaseURL)
if err != nil {
	return fmt.Errorf("打开 PostgreSQL Ent 客户端: %w", err)
}
defer client.Close()
```

PostgreSQL 驱动使用教程对应的 `github.com/lib/pq` 匿名导入。连接串只来自配置中的 `DATABASE_URL`，不会以常量、测试数据或文档示例的形式写入真实凭据。

由于 `rag_users` 已经存在，启动流程不得调用 `client.Schema.Create`。本仓库继续不创建、不生成和不应用数据库迁移。

## 就绪检查

Ent Client 本身不提供 `Ping(context.Context)`。新增一个只依赖 Ent Client 的就绪检查适配器，通过带请求上下文的轻量 `rag_users` 存在性查询验证：

- 数据库连接可以建立；
- `rag_users` 表存在且当前凭据可以读取；
- 查询超时继续由现有 HTTP readiness 层控制。

表为空时查询返回 `false, nil`，仍视为就绪；只有连接、权限、表结构或上下文错误才返回不可用。Handler 继续只调用 `HealthChecker` 接口，不直接依赖 Ent 或数据库类型。

## 删除范围

删除以下自定义连接池能力：

- `internal/platform/postgres/pool.go`；
- `internal/platform/postgres/pool_test.go`；
- `pgx/v5` 与 `pgvector-go/pgx` 直接依赖；
- `DB_MAX_CONNS`、`DB_MIN_CONNS`、`DB_MAX_CONN_IDLE_TIME` 配置、解析、测试和示例；
- README 与开发约定中对 `pgxpool`、连接池参数和连接时 pgvector 注册的描述。

保留 `DB_CONNECT_TIMEOUT`，它用于 HTTP readiness 查询的超时控制，不属于连接池配置。

## 生成与测试

`make generate` 先执行现有 `hz update`，再执行 `go generate ./ent`，保证 HTTP 契约生成物和 Ent 模型都可重复生成。

测试覆盖：

- `RagUser` Schema 的表名、字段类型、长度、唯一性、JSONB 类型和默认值；
- 配置只要求 `DATABASE_URL`，并不再解析连接池变量；
- Ent 就绪检查在表为空、存在记录和查询失败时的行为；
- 既有 Hertz 路由、Eino Graph、配置与服务生命周期测试保持通过。

完成前运行：

```bash
make generate
make fmt
git diff --check
make test
make vet
make build
```

验证过程中不使用用户提供的真实数据库连接串，也不连接或修改远程 Neon 数据库。
