# AI 电商导购后端

这是一个使用 Hertz、Eino Graph、Ent 和 Neon Postgres 的 AI 电商导购后端骨架。HTTP 契约由 Thrift IDL 定义，项目脚手架和路由由 `hz` 生成。

当前只包含 `hz` 默认项目结构、存活与就绪检查、最小 Eino Graph，以及映射既有 `rag_users` 表的 Ent Client。不包含数据库迁移、LLM、Embedding、商品导入、完整 RAG、SSE、购物车或多模态逻辑。

## 生成代码

首次生成使用 hz v0.9.7：

```bash
hz new --idl idl/health.thrift --module github.com/PolarishT/sales-agent --service sales-agent --sort_router
```

仓库已经包含生成结果。修改 `idl/health.thrift` 或 `ent/schema` 后运行：

```bash
make generate
```

`biz/model`、`biz/router`、`router_gen.go` 和 `ent` 中除 `ent/schema`、`ent/generate.go` 外的 Go 文件都是生成物，不要手工修改。项目使用 hz 默认目录，不使用自定义 layout。`make generate` 会依次运行 hz 更新和 Ent 代码生成。

## 本地运行

需要 Go 1.25、hz v0.9.7，以及已经准备好 `rag_users` 表的 Neon 数据库。本仓库不执行数据库迁移，也不会在启动时调用 `client.Schema.Create`。

```bash
cp .env.example .env.local
# 编辑 .env.local 中的 DATABASE_URL
make run
```

验证接口：

```bash
curl http://localhost:3000/api/v1/health/live
curl http://localhost:3000/api/v1/health/ready
```

`live` 只检查 HTTP 进程；`ready` 在受限超时内通过 Ent 查询 `rag_users`，同时检查 Neon 连接、权限和业务表。数据库不可用不会阻止 HTTP 服务启动，但 `ready` 会返回 HTTP 503。

## 环境变量

`DATABASE_URL` 是唯一必需的运行时变量，必须使用 Neon 的 `-pooler` 池化地址，并保留 `sslmode=require`。当前 PostgreSQL 驱动为 Ent 入门教程使用的 `lib/pq`，连接串不要包含它尚不支持的 `channel_binding=require`。

监听地址优先级为 `PORT`、`HTTP_ADDR`、`:3000`；环境优先级为 `APP_ENV`、`VERCEL_ENV`、`development`。超时和日志级别可通过 `DB_CONNECT_TIMEOUT`、`REQUEST_TIMEOUT`、`GRAPH_TIMEOUT`、`SHUTDOWN_TIMEOUT` 和 `LOG_LEVEL` 调整。`DB_CONNECT_TIMEOUT` 只限制 readiness 查询。`PORT` 和 `VERCEL_*` 由 Vercel 注入，不写入 `.env.example`。

## Vercel

Production、Preview 和 Development 分别配置 `DATABASE_URL`。Preview 必须使用独立 Neon 分支，不能默认连接生产数据库。

```bash
vercel deploy
vercel deploy --prod
```

## 验证命令

```bash
make generate
make fmt
git diff --check
make test
make vet
make build
```
