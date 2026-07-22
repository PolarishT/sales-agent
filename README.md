# AI 电商导购后端

这是一个使用 Hertz、Eino Graph、Neon Postgres 和 pgvector 的 AI 电商导购后端骨架。HTTP 契约由 Thrift IDL 定义，项目脚手架和路由由 `hz` 生成。

当前只包含 `hz` 默认项目结构、存活与就绪检查、最小 Eino Graph、Neon 连接池和 pgvector 类型注册。不包含数据库迁移、LLM、Embedding、商品导入、完整 RAG、SSE、购物车或多模态逻辑。

## 生成代码

首次生成使用 hz v0.9.7：

```bash
hz new --idl idl/health.thrift --module github.com/PolarishT/sales-agent --service sales-agent --sort_router
```

仓库已经包含生成结果。修改 `idl/health.thrift` 后运行：

```bash
make generate
```

`biz/model`、`biz/router` 和 `router_gen.go` 是生成物，不要手工修改。项目使用 hz 默认目录，不使用自定义 layout。

## 本地运行

需要 Go 1.25、hz v0.9.7，以及已经准备好 pgvector 扩展和业务表的 Neon 数据库。本仓库不执行数据库迁移。

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

`live` 只检查 HTTP 进程；`ready` 在受限超时内检查 Neon。数据库不可用不会阻止 HTTP 服务启动，但 `ready` 会返回 HTTP 503。

## 环境变量

`DATABASE_URL` 是唯一必需的运行时变量，必须使用 Neon 池化地址。监听地址优先级为 `PORT`、`HTTP_ADDR`、`:3000`；环境优先级为 `APP_ENV`、`VERCEL_ENV`、`development`。

连接池和超时可通过 `DB_MAX_CONNS`、`DB_MIN_CONNS`、`DB_MAX_CONN_IDLE_TIME`、`DB_CONNECT_TIMEOUT`、`REQUEST_TIMEOUT`、`GRAPH_TIMEOUT`、`SHUTDOWN_TIMEOUT` 和 `LOG_LEVEL` 调整。`PORT` 和 `VERCEL_*` 由 Vercel 注入，不写入 `.env.example`。

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
