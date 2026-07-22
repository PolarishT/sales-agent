# hz + Thrift 后端脚手架重建设计

## 1. 目标

使用本机已安装的 `hz v0.9.7` 和 Thrift IDL，从 `hz new` 重新生成 AI 电商导购后端的 Hertz 项目脚手架。生成完成后，按照 `hz` 默认目录边界重新接入当前阶段已有的 Eino Graph、Neon `pgxpool`、pgvector 类型注册、配置、健康检查、Vercel 与本地运行支持。

本次重建删除手写 Hertz 脚手架、默认 `/ping` 示例和数据库迁移能力。最终正式 HTTP 路由必须由 Thrift IDL 驱动。

## 2. 范围

### 2.1 包含

- 新建 `idl/health.thrift`，作为健康检查 HTTP 契约的唯一来源。
- 使用 `hz new` 默认布局生成根目录入口、路由、Handler 和 Thrift 模型。
- 提供 `GET /api/v1/health/live` 与 `GET /api/v1/health/ready`。
- 重新接入 Eino Graph 最小强类型编排。
- 重新接入 Neon `pgxpool` 与 pgvector 类型注册。
- 保留 Vercel `PORT` 与本地 `HTTP_ADDR` 的监听配置规则。
- 保留稳定错误结构、请求 ID、404、405 和 panic recovery。
- 更新 Makefile、README、`.env.example` 和 `AGENTS.md`。

### 2.2 不包含

- 数据库迁移、建表或回滚命令。
- `DATABASE_MIGRATION_URL` 配置。
- 自定义 `hz` layout、handler、model 或 router 目录。
- LLM、Embedding、商品导入、完整 RAG、SSE 对话、购物车、多模态或交易能力。
- 非 IDL 的 `/ping` 示例接口。

## 3. 方案选择

采用 `hz` 默认目录布局。相比自定义生成布局，该方案无需维护模板，并与 `hz update` 的默认行为一致；相比只复制部分生成物，该方案确保仓库确实以 `hz new` 结果为脚手架基线。

首次生成前移除现有 Go 脚手架、`go.mod`、`go.sum`、`.gitignore` 和 `migrations/`。Git 历史作为可恢复基线。创建 IDL 后，在仓库根目录执行：

```bash
/Users/polaris/go/bin/hz new \
  --idl idl/health.thrift \
  --module github.com/PolarishT/sales-agent \
  --service sales-agent \
  --sort_router
```

命令不使用 `--force`、`--handler_dir`、`--model_dir`、`--router_dir` 或 `--customize_layout`。

## 4. 目录与职责

```text
.
├── idl/
│   └── health.thrift
├── biz/
│   ├── handler/
│   ├── model/
│   ├── router/
│   ├── dal/
│   └── service/
├── internal/
│   ├── agent/
│   ├── config/
│   ├── http/
│   └── platform/postgres/
├── main.go
├── router.go
└── router_gen.go
```

- `idl/health.thrift`：HTTP 方法、路径、请求和响应结构的契约源。
- `biz/model`：Thrift 生成模型，不手工修改。
- `biz/router`：IDL 生成路由，不手工修改。
- `router_gen.go`：根路由生成入口，不手工修改。
- `biz/handler`：`hz` 创建的协议适配器。只绑定请求、读取已装配依赖并写入响应。
- `main.go`：配置加载、日志、数据库池、Graph、Hertz 服务和生命周期装配。
- `router.go`：自定义路由入口；本阶段不注册 `/ping` 或其他正式接口。
- `internal/agent`：与 HTTP 类型无关的 Eino Graph。
- `internal/config`：环境变量解析与校验。
- `internal/http`：Hertz 服务选项、请求依赖注入、中间件、错误映射和健康检查辅助逻辑。
- `internal/platform/postgres`：进程级唯一 `pgxpool` 创建与 pgvector 类型注册。

`biz/handler` 不直接创建连接池、不执行 SQL、不编排 Graph。领域与 Agent 包不得依赖 Hertz 类型。

## 5. Thrift HTTP 契约

`idl/health.thrift` 定义以下类型：

- `HealthRequest`：当前为空，供两种健康检查方法使用。
- `HealthResponse`：包含 `status` 与 `code`。
- `ErrorResponse`：包含 `status`、`code`、`message` 与 `request_id`。

`HealthService` 定义：

- `Live(HealthRequest)`，注解为 `api.get="/api/v1/health/live"`。
- `Ready(HealthRequest)`，注解为 `api.get="/api/v1/health/ready"`。

成功响应使用 Thrift 生成的 `HealthResponse`。协议级错误使用与 `ErrorResponse` 字段一致的统一 JSON 结构。

## 6. 运行时装配与数据流

启动顺序为：

1. 加载并校验当前功能所需配置。
2. 配置结构化日志。
3. 创建进程级唯一 Neon `pgxpool`，并为新连接注册 pgvector 类型。
4. 编译 Eino Graph。
5. 创建 Hertz 服务并配置监听器、超时和中间件。
6. 将健康检查器和 Agent Runner 作为显式应用依赖装配到请求上下文。
7. 调用 `router_gen.go` 的注册入口挂载 IDL 生成路由。
8. 启动服务并处理优雅关闭。

请求路径为：

```text
请求
→ request ID / recovery 中间件
→ hz 生成路由
→ biz/handler 薄 Handler
→ 请求上下文中的应用依赖
→ 健康检查逻辑
→ Thrift 生成响应模型
```

依赖不保存在包级可变单例中。Handler 通过 `internal/http` 提供的类型化辅助函数从当前请求上下文读取依赖。长期状态仍由独立存储负责。

## 7. 健康检查与错误处理

### 7.1 存活检查

`GET /api/v1/health/live` 返回 HTTP 200、`status=ok`、`code=LIVE`。该接口不访问数据库。

### 7.2 就绪检查

`GET /api/v1/health/ready` 在受限超时内调用 `pgxpool.Ping`：

- 成功时返回 HTTP 200、`status=ok`、`code=READY`。
- 失败时返回 HTTP 503、`status=unavailable`、`code=DATABASE_UNAVAILABLE`。

服务启动阶段不强制执行数据库 `Ping`。数据库暂时不可用时，Hertz 仍能启动，由就绪检查表达不可用状态。

### 7.3 通用错误

- 每个请求生成请求 ID，并写入 `X-Request-ID` 响应头。
- 404 返回稳定错误码 `NOT_FOUND`。
- 405 返回稳定错误码 `METHOD_NOT_ALLOWED`。
- panic recovery 返回 `INTERNAL_ERROR`。
- 缺少运行时依赖时返回稳定服务错误，不暴露装配细节。
- 响应不得包含数据库原始错误、内部堆栈、Prompt 或供应商原始响应。

## 8. 生成物维护

`.hz` 提交到版本库，用于保存项目生成配置。后续 IDL 变更执行：

```bash
hz update --idl idl/health.thrift --sort_router
```

Makefile 提供 `generate` 目标执行上述更新。`hz` 必须通过 `PATH` 可用；本次 Codex 环境执行时可显式使用 `/Users/polaris/go/bin/hz`，仓库文件不硬编码用户主目录。

生成文件的维护边界：

- `biz/model`、`biz/router` 和 `router_gen.go` 不手工修改。
- `biz/handler`、`main.go` 和 `router.go` 可在首次生成后补充项目逻辑。
- `router.go` 删除生成的 `/ping` 注册，`biz/handler/ping.go` 同步删除。

## 9. 配置与数据库边界

运行时继续要求 `DATABASE_URL`，并保留以下配置能力：

- `APP_ENV`、`VERCEL_ENV`。
- `PORT`、`HTTP_ADDR`。
- `DB_MAX_CONNS`、`DB_MIN_CONNS`、`DB_MAX_CONN_IDLE_TIME`。
- `DB_CONNECT_TIMEOUT`、`REQUEST_TIMEOUT`、`GRAPH_TIMEOUT`、`SHUTDOWN_TIMEOUT`。
- `LOG_LEVEL`。

删除 `DATABASE_MIGRATION_URL` 以及所有迁移命令和文档。本仓库仅连接已经准备好的 Neon 数据库，不负责创建 pgvector 扩展或业务表。pgvector Go 类型仍在连接建立时注册。

## 10. 测试与验证

测试必须覆盖：

- IDL 生成路由注册两个健康检查端点。
- `/ping` 不存在。
- `live` 返回 200 且不调用数据库。
- `ready` 的成功和数据库不可用分支。
- 请求 ID、404、405、panic recovery 和稳定错误结构。
- 配置优先级、缺失必需变量和非法配置。
- Eino Graph 编译、输入规范化和校验。
- `pgxpool` 配置与 pgvector 注册钩子。
- 服务生命周期相关的可测试辅助逻辑。

生成代码本身属于测试驱动开发的生成物例外；所有手写行为在实现前先增加失败测试并确认失败原因正确。

完成前运行：

```bash
make generate
make fmt
git diff --check
make test
make vet
make build
```

`make fmt` 覆盖根目录、`biz` 和 `internal` 中的 Go 文件。`make generate` 后必须检查差异，确保 IDL 与提交的生成物一致。

## 11. 文档更新

- README 说明安装 `hz`、首次 `hz new` 的准确命令、后续 `hz update`、本地启动、Vercel 配置和健康检查。
- `.env.example` 不再包含 `DATABASE_MIGRATION_URL`。
- Makefile 不再包含 `migrate-up` 或 `migrate-down`。
- `AGENTS.md` 明确默认 `hz` 目录职责、IDL 契约源和生成物不可手改边界，并移除初始迁移交付要求。
- 原设计和实施计划作为历史记录保留；本设计与后续新计划覆盖其中关于手写 HTTP 脚手架和数据库迁移的内容。

## 12. 完成标准

- 仓库由 `hz new` 默认布局生成，不存在自定义 layout 配置。
- `.hz`、Thrift IDL 和对应生成物已提交。
- 正式路由仅包含 IDL 驱动的版本化健康检查；`/ping` 返回 404。
- Eino Graph、Neon `pgxpool`、pgvector 类型注册、Vercel 和本地配置已重新接入。
- 不存在 `migrations/`、迁移 Makefile 目标或 `DATABASE_MIGRATION_URL` 文档。
- 统一错误结构和请求 ID 行为通过测试。
- 全部规定验证命令成功，且完成报告列出仍存在的阶段限制。
