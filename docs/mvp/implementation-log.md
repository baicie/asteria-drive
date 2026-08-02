# MVP 实施与验收记录

> 本页记录已经由代码和命令证明的事实。规划目标仍以 [scope.md](scope.md) 和
> [acceptance.md](acceptance.md) 为准；未列为通过的条目不能视为完成。

## 2026-08-02 - 里程碑分支链

按 [branch-workflow.md](../process/branch-workflow.md) 建立本地分支链（尚未推送远端）：

```text
docs/product-vision
  → mvp/p0-baseline
  → mvp/p1-namespace
  → mvp/p2-upload
  → mvp/p3-download-recycle
  → mvp/integrate
  → mvp/p4-integration  (当前工作分支)
```

- 产品蓝图归档于 `docs/vision/`，不扩大 MVP MUST。
- P0 设计门禁与 P0–P3 垂直切片候选以独立 commit 合入；P1/P2/P3 以分支归属记录确认（见 `BRANCH-p1.md` 等）。
- 历史过渡分支 `codex/mvp` 不再承接新工作。

## 2026-08-02 - 设计基线

- 建立 `codex/mvp`，基于远端 `main` 初始化提交保留现有工作区骨架。
- 归档 MVP scope、P0-P4 路线、验收矩阵、架构/API/数据/测试设计和 ADR-0001 至 ADR-0007。
- 统一本轮边界：trusted-dev 隔离环境；PostgreSQL + S3/SeaweedFS；OIDC、分享、配额、Outbox 为 M2。
- 修正 Go module 为 `github.com/baicie/asteria-drive`。

## 2026-08-02 - 内存垂直切片

实现：

- 领域模型、错误码、名称 NFC 规格化、HMAC Cursor、Repository/StorageProvider port。
- 确定性内存 Repository 和对象存储 fake。
- trusted-dev Bearer Token、请求 ID、统一 JSON 错误、严格 JSON 和 HTTP 超时骨架。
- 目录、文件、Multipart 会话、分片签名、幂等完成、下载授权、回收和恢复 API。
- PostgreSQL 迁移/Repository adapter 与 S3-compatible adapter 的首版实现。

已执行：

```text
go test ./...
```

结果：内存垂直切片、认证、Cursor、名称规则与 HTTP API 测试通过。该次记录没有提供真实
PostgreSQL/SeaweedFS、竞态、SLO 或故障注入证据。

## 2026-08-02 - 候选实现、文档与静态验证

实现/归档：

- 新增认证租户发现 `GET /api/v1/tenant`，由 Token 上下文确定租户并返回根目录 ID。
- 归档与 18 个注册 HTTP 操作一一对应的 `docs/openapi.yaml`（OpenAPI 3.1）。
- 重写顶层 README，归档隔离环境启动、配置、迁移、测试门禁与非生产限制。
- ADR-0008 记录创建 `Idempotency-Key` 和自动维护任务作为 SHOULD 的分阶段边界；上传完成幂等仍为 MUST。

在未设置 `ASTERIA_TEST_DATABASE_URL` 和 `ASTERIA_TEST_S3_ENDPOINT` 的进程中执行：

```text
go test ./...
go vet ./...
go build ./...
git diff --check
npx --yes @redocly/cli@latest lint docs/openapi.yaml
```

结果：

- `go test ./...`、`go vet ./...`、`go build ./...` 和 `git diff --check` 退出码均为 0。
- PostgreSQL 与 SeaweedFS 集成测试因门禁变量未设置而跳过；本次 `go test` 结果不作为 AC-060 至 AC-062 的真实依赖证据。
- OpenAPI YAML 解析成功；注册路由与契约脚本比较得到 18 个操作、无缺失、无多余。
- Redocly recommended lint 判定契约有效；保留 3 个非契约警告：仓库尚未声明 license，以及两个探针按实际行为不虚构 `4xx` 响应。

## 2026-08-02 - PostgreSQL 与 SeaweedFS adapter 契约

环境：

- PostgreSQL `17.5-alpine`，本机 Compose 端口 `15432`，health 为 healthy。
- SeaweedFS `3.85` S3 Gateway，本机 Compose 端口 `18333`，health 为 healthy。
- 测试凭据通过进程环境注入，未写入本记录；PostgreSQL 使用临时 Schema，S3 使用专用测试 Bucket/对象并清理。

设置 `ASTERIA_TEST_DATABASE_URL`、`ASTERIA_TEST_S3_ENDPOINT`、`ASTERIA_TEST_S3_ACCESS_KEY`、
`ASTERIA_TEST_S3_SECRET_KEY`、region 和测试 Bucket 后执行：

```text
go test -v ./internal/postgres ./internal/s3store
```

结果：

- `TestMigrateIsIdempotent`：通过；迁移重复执行，目标 Schema 中迁移记录和 6 张 MVP 表符合预期。
- `TestRepositoryNamespaceAndUploadContract`：通过；覆盖租户 Namespace、冲突和上传持久化契约。
- `TestRepositoryCommitRollbackAndPurgeContract`：通过；覆盖提交回滚、名称冲突无残留和永久清理计划。
- `TestProviderSeaweedFSContract`：通过；真实执行 Multipart 分片 PUT/Complete、Head、签名全量下载、
  `206 Range` 校验和对象清理。
- `TestCustomEndpointPresignedDownloadOmitsOptionalChecksumMode`：通过。

这些结果为 AC-041 和 AC-060/061 的一部分提供真实协议证据，但尚未单独关闭 AC-060/061/062：仍需
从 HTTP API 到 PostgreSQL + SeaweedFS 的完整主路径、服务重启读取、跨租户和关键失败路径证据。

仍待验证：完整服务端端到端、重启持久性、竞态、跨系统故障注入、日志 Secret 扫描、生产模式负向
启动、SLO、100 并发会话和完整 Definition of Done。

## 2026-08-02 - P4 真实依赖与发布门槛

实现/加固：

- HTTP live e2e（已有）覆盖上传直传、下载/Range、重启持久性、回收/恢复冲突/清理、跨租户。
- 新增 `TestHealthStaysUpWhenDependenciesFail`、`TestGracefulShutdownStopsListener`、
  `TestControlPlaneLatencySmokeBaseline`、`TestRequestBodyLimitsAndMediaType`。
- ADR-0010 明确 MVP MUST 性能子集与百万节点 SHOULD 延期边界。
- 证据归档：[evidence/p4-live-baseline.md](./evidence/p4-live-baseline.md)；验收矩阵更新为通过。

环境：PostgreSQL 17.5-alpine + SeaweedFS 3.85（Compose healthy）；Go 1.23.6。

```text
go test ./...
go vet ./...
go build ./...
go test -v -count=1 ./internal/postgres ./internal/s3store ./internal/server -timeout 10m
```

结果：

- 单元/组件测试全部通过。
- `TestLiveHTTPPostgresSeaweedFSEndToEnd`：通过（关闭 AC-062 及重启持久性）。
- `TestLiveHTTPConcurrentUploadSessionBaseline`：通过；100 会话 create p95≈257.5ms（AC-081）。
- adapter 契约与迁移幂等再次通过（AC-060/061/041）。
- 范围回归：运行依赖仍为 PostgreSQL + S3 兼容端点；无 Redis/Kafka/OpenSearch（AC-091）。
- Definition of Done 工程项已勾选；合入 `main` 前建议人工 PR 签核。

SHOULD 延期见 [acceptance.md](./acceptance.md) 第 7 节。
