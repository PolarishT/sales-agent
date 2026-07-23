# AI 电商导购后端

这是一个使用 Hertz、Eino Graph、Ent 和 Neon Postgres 的 AI 电商导购后端。HTTP 契约由 Thrift IDL 定义，项目脚手架和路由由 `hz` 生成。

基础骨架已经验收；当前增量实现基础 RAG 的 Markdown 离线导入半链路：上传、解析、过滤、规范化、结构感知切片、智谱 Embedding 和 pgvector 持久化。在线检索、重排、事实校验、SSE 对话、商品卡片、购物车、订单和多模态逻辑仍不在本增量范围内。

## 生成代码

首次生成使用 hz v0.9.7：

```bash
hz new --idl idl/health.thrift --module github.com/PolarishT/sales-agent --service sales-agent --sort_router
```

仓库已经包含生成结果。`idl` 是 HTTP 请求、响应、方法和路由的唯一契约源；修改任一 Thrift IDL（当前包括 `idl/health.thrift` 和 `idl/rag_ingestion.thrift`）或 `ent/schema` 后都要运行：

```bash
make generate
```

`biz/model`、`biz/router`、`router_gen.go` 和 `ent` 中除 `ent/schema`、`ent/generate.go` 外的 Go 文件都是生成物，不要手工修改。项目使用 hz 默认目录，不使用自定义 layout。`make generate` 会依次运行 hz 更新和 Ent 代码生成。

## 本地运行

需要 Go 1.25、hz v0.9.7，以及已经准备好业务表的 Neon 数据库。本仓库不执行数据库迁移，也不会在启动时调用 `client.Schema.Create`。首次启动前，必须由仓库外的受控数据库流程在目标 Neon 分支执行 [`docs/database/rag_ingestion.sql`](docs/database/rag_ingestion.sql)，以准备 `vector` 扩展和 RAG 导入表；应用不会自动执行这份 DDL。

```bash
cp .env.example .env.local
# 编辑 .env.local，设置真实的 DATABASE_URL 和 ZHIPU_API_KEY
make run
```

验证接口：

```bash
curl http://localhost:3000/api/v1/health/live
curl http://localhost:3000/api/v1/health/ready
```

`live` 只检查 HTTP 进程；`ready` 在受限超时内通过 Ent 查询 `rag_users`，同时检查 Neon 连接、权限和业务表。数据库不可用不会阻止 HTTP 服务启动，但 `ready` 会返回 HTTP 503。

## Markdown 离线导入

创建异步导入任务时使用 `multipart/form-data`，提供稳定的 `document_key` 和恰好一个 `.md` 或 `.markdown` 文件：

```bash
curl -i -X POST http://localhost:3000/api/v1/rag/ingestions \
  -F 'document_key=product-catalog/iphone-16-pro' \
  -F 'file=@./iphone-16-pro.md;type=text/markdown'
```

响应中的 `ingestion_id` 是 UUID。查询任务时直接把 UUID 放进 URL，客户端 URL 不包含 IDL 路径参数语法中的冒号：

```bash
curl -i http://localhost:3000/api/v1/rag/ingestions/550e8400-e29b-41d4-a716-446655440000
```

单文件硬上限默认为 5 MiB（5,242,880 字节）。执行器默认使用 1 个进程内 worker，等待队列容量为 8；队列已满时新任务立即返回服务不可用错误。Embedding 空间固定为智谱 `embedding-3` 和 1024 维，持久化列为 `vector(1024)`，不能通过配置切换到其他模型或维度。

应用层能够识别的超大文件会返回统一 JSON 错误 `FILE_TOO_LARGE`。如果文件名、额外表单字段等 multipart 信封本身先超过 Hertz 的请求体上限，请求会在进入 Handler 前由传输层直接返回纯文本 HTTP 413；这是统一 JSON 错误结构的明确例外。

相同 `document_key` 和规范化后相同内容会复用已有任务。内容变化会创建新版本；只有新版本完整成功后才切换当前版本并删除全部旧版本，此后使用旧 `ingestion_id` 查询会返回 HTTP 404。新版本失败时旧成功版本保持生效。

队列和调度状态只存在于当前进程。优雅关闭会尽力排空或将任务标记为中断，但进程崩溃、Vercel 实例回收、重启或扩缩容时无法跨实例恢复；数据库中遗留而未被当前执行器调度的 stale `RUNNING` 任务不会被自动重放，重复提交会返回导入服务不可用。当前增量不包含持久队列、任务租约、HNSW，也不包含在线向量检索。

## 环境变量

`DATABASE_URL` 和 `ZHIPU_API_KEY` 是必需的运行时变量。`DATABASE_URL` 必须使用 Neon 的 `-pooler` 池化地址，并保留 `sslmode=require`；当前 PostgreSQL 驱动为 Ent 入门教程使用的 `lib/pq`，连接串不要包含它尚不支持的 `channel_binding=require`。不要提交真实数据库连接串或 API Key。

监听地址优先级为 `PORT`、`HTTP_ADDR`、`:3000`；环境优先级为 `APP_ENV`、`VERCEL_ENV`、`development`。导入并发、队列、上传上限和超时可通过 `RAG_INGESTION_WORKERS`、`RAG_INGESTION_QUEUE_CAPACITY`、`RAG_MAX_UPLOAD_BYTES` 和 `RAG_INGESTION_TIMEOUT` 调整。模型与维度配置必须保持为 `ZHIPU_EMBEDDING_MODEL=embedding-3` 和 `ZHIPU_EMBEDDING_DIMENSIONS=1024`。其他超时和日志级别可通过 `.env.example` 中的对应变量调整；`DB_CONNECT_TIMEOUT` 只限制 readiness 查询。`PORT` 和 `VERCEL_*` 由 Vercel 注入，不写入 `.env.example`。

## Vercel

Production、Preview 和 Development 分别配置 `DATABASE_URL` 与 `ZHIPU_API_KEY`。Preview 必须使用独立 Neon 分支，不能默认连接生产数据库，并且每个目标分支都要通过受控流程预先执行 RAG 导入 DDL。

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
