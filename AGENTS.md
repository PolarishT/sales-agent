# AI 电商导购后端开发约定

## 项目目标

本仓库只实现 AI 电商导购后端。对外使用 Hertz 提供版本化 HTTP/SSE API，内部使用 Eino Graph 编排 Agent，通过 Neon Postgres 保存商品事实数据并使用 pgvector 检索。

客户端、真实支付和真实订单履约不在本仓库范围内。

## 当前阶段

当前处于第一阶段的基础骨架增量，只实现：

- hz v0.9.7 默认脚手架与 Thrift IDL 契约；
- Hertz 服务入口与健康检查；
- Eino Graph 最小可运行编排；
- Neon `pgxpool` 与 pgvector 类型注册；
- Vercel 与本地开发配置；
- 测试、构建和中文文档。

本增量不得提前加入 LLM、Embedding、商品导入、完整 RAG、SSE 对话、购物车或多模态逻辑。

## 阶段门禁

按以下顺序交付，当前阶段完成测试、文档和验收后才能进入下一阶段：

1. 基础 RAG：商品导入、切片、Embedding、检索、重排、事实校验、SSE 与商品卡片。
2. 对话增强：会话存储、上下文摘要、查询改写、混合检索和置信度。
3. 交易 Agent：购物车 CRUD、订单确认、幂等、事务和审计。
4. 多模态：VLM 图片搜货、ASR 和 TTS，失败时降级到文字流程。
5. 工程强化：缓存、限流、熔断、可观测性、评测和性能基线。

## hz、IDL 与目录规则

- `idl`：HTTP 请求、响应、方法和路由的唯一契约源。
- `biz/model`、`biz/router`、`router_gen.go`：hz/Thrift 生成物，禁止手工修改。
- `biz/handler`：hz 创建的协议适配层，只做绑定、依赖调用和响应转换。
- `router.go`：只注册无法由 IDL 表达的路由；当前阶段保持为空。
- `main.go`：Vercel 与本地共用入口，只做配置加载、依赖装配和生命周期管理。
- `internal/http`：Hertz 服务选项、依赖注入、中间件和错误映射。
- `internal/agent`：Eino Graph、强类型状态和子图组合。
- `internal/rag`：切片、Embedding、检索、重排与事实校验。
- `internal/catalog`：商品、价格、库存和商品能力等权威事实。
- `internal/conversation`：会话历史、摘要和上下文窗口。
- `internal/cart`、`internal/order`：交易领域逻辑。
- `internal/multimodal`：VLM、ASR、TTS 适配器。
- `internal/platform`：数据库、向量库、模型客户端、缓存、日志和指标。

首次创建项目使用 `hz new`；后续 IDL 变化使用 `hz update`。项目采用 hz 默认布局，不维护自定义 layout 或生成目录。Handler 不直接访问数据库或模型；领域与 Agent 代码不得依赖 Hertz 类型。

## Hertz 与 API 规则

- 正式接口统一使用 `/api/v1` 前缀。
- 使用稳定错误码、请求 ID 和统一 JSON 错误结构。
- SSE 事件至少包括 `message.delta`、`product.card`、`tool.status`、`error` 和 `done`。
- 异常流必须发送明确的错误与终止事件。
- 不把内部堆栈、Prompt、数据库错误或供应商原始响应暴露给客户端。

## Eino Graph 规则

- Agent 流程必须经 Eino Graph 编排，禁止在 Handler 中手写流程链。
- Node 输入输出必须强类型、可独立测试，并通过依赖注入获取外部能力。
- Graph State 只保存本次执行所需的结构化上下文；长期会话由独立存储负责。
- 禁止隐式全局可变状态。
- 外部调用必须有超时；只读且幂等的操作才允许有限重试。

## Neon 与数据规则

- Go 数据库驱动使用 `pgx/v5`，向量类型使用 `pgvector-go`。
- Vercel 运行时使用 `DATABASE_URL` 的 Neon 池化地址。
- 数据库、pgvector 扩展和业务表由仓库外的受控流程预先准备，本仓库不执行迁移。
- 每个进程只创建一个受限的 `pgxpool`，禁止每请求新建连接池。
- SQL 必须参数化，禁止拼接用户输入。
- 商品价格、库存、优惠和能力只能来自结构化权威数据。
- 向量检索结果只是候选，输出前必须经过事实校验。
- 初始 50–100 条数据优先使用精确检索；Embedding 维度确定并有性能证据后再增加 HNSW。

## Vercel 与环境变量

- 根目录 `main.go` 必须监听 Vercel 注入的 `PORT`；本地才使用 `HTTP_ADDR` 或默认 `:3000`。
- 环境优先级为 `APP_ENV`、`VERCEL_ENV`、`development`。
- `PORT`、`VERCEL_ENV`、`VERCEL_REGION` 等平台变量不写入 `.env.example`。
- Production、Preview、Development 分别配置凭据；Preview 不得默认连接生产 Neon 分支。
- 只校验当前启用功能真正需要的变量。
- 不提交 `.env`、`.env.local`、数据库连接串、API Key、令牌或真实用户数据。

## 开发流程

1. 修改前检查仓库状态、当前阶段和相关文档。
2. 新功能与缺陷修复先写测试并确认测试因目标行为缺失而失败。
3. 只实现使当前测试通过的最小变更，再进行保持绿灯的重构。
4. 保护已有改动，不使用破坏性 Git 命令，不做无关重构。
5. 保持提交小而聚焦；提交信息使用中文描述意图。
6. 修改 IDL、API、配置或 Graph 时同步更新中文文档。

## 完成前验证

至少运行：

```bash
make generate
make fmt
git diff --check
make test
make vet
make build
```

不得通过删除测试、跳过测试或降低断言强度换取通过。完成声明必须附上实际验证结果和仍存在的限制。
