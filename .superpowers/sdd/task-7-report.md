# Task 7 实施报告：Ent 导入仓储与原子激活

## 实施范围

- 新增 `internal/rag/ingestion/contracts.go`，定义 Pipeline 所需的窄接口、仓储接口与提交判定。
- 新增 `internal/platform/database/ingestion_repository.go`，实现 Ent 导入仓储。
- 新增 `internal/platform/database/ingestion_repository_test.go`，使用 `sqlmock` 验证查询、并发重检、事务、回滚和稳定错误映射。
- 未修改 Ent 生成物、Schema、DDL、运行时装配或 `main.go`。

## TDD 证据

首次执行：

```text
go test ./internal/platform/database -run Ingestion
```

测试因 `internal/rag/ingestion` 契约不存在而 RED；补齐 contracts 后再次执行，测试因 `NewIngestionRepository` 和 `IngestionRepository` 不存在而 RED。实现最小仓储后，聚焦测试转为 GREEN。

## 已实现行为

- `InspectSubmission`：
  - 不存在的 `document_key` 返回 `SubmissionCreate`；
  - 当前成功版本或排队/运行版本哈希相同返回 `SubmissionReuse`；
  - 不同哈希的排队/运行版本返回 `SubmissionConflict`；
  - 当前版本同哈希的复用优先于运行版本冲突。
- `CreateQueued`：
  - 在事务中通过 Ent `OnConflictColumns(...).Ignore()` 处理并发首次创建；
  - 使用 `FOR UPDATE` 锁定稳定文档行并重复去重/冲突判断；
  - 版本号为 `max(current_version, 已存版本号) + 1`；
  - 插入失败回滚，提交失败映射为 `DOCUMENT_STORE_FAILED`。
- 查询与状态：
  - `GetTask`、`LoadSource`、`UpdateStage`、`UpdateProgress`、`MarkFailed` 均使用稳定领域类型；
  - 缺失任务映射为 `INGESTION_NOT_FOUND`；
  - `MarkFailed` 只写入稳定 code/message，不接受或持久化内部错误。
- `StoreAndActivate`：
  - 在开启事务前校验所有向量恰好 1024 维；
  - 将 `[]float64` 转换为 `[]float32` 和 `pgvector.Vector`；
  - 事务内锁定目标版本、写入全部 Chunk、写入精确计数和成功状态、切换 `current_version`、删除所有旧版本后提交；
  - Chunk 插入或激活更新失败均回滚。

## 验证结果

```text
PATH=/Users/polaris/go/bin:$PATH make generate  PASS
make fmt                                      PASS
go test ./internal/platform/database -run Ingestion
                                               PASS
go test -race ./internal/platform/database    PASS
make test                                     PASS
make vet                                      PASS
make build                                    PASS
git diff --check                              PASS
```

直接运行 `make generate` 时当前 shell 的 `PATH` 不含 Go bin，先后提示找不到 `hz` 和 `thriftgo`；两个固定工具均已存在于 `/Users/polaris/go/bin`，补入 PATH 后生成命令完整通过且未产生额外生成物差异。

## 限制

- 按任务约束未连接真实 Neon、未执行迁移、未进行网络调用。
- 并发语义通过 PostgreSQL upsert SQL、事务内 `FOR UPDATE` 和二次判定的 `sqlmock` 契约测试验证；未在本任务中启动真实 PostgreSQL 做并发集成测试。
