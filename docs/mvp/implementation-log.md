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

## 2026-08-02 - P4 真实依赖与发布门槛（历史中间快照）

> 本节保留当日测量和当时的待验证状态；后续收尾结果以 2026-08-03 章节为准。

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

## 2026-08-03 - P4 收尾验收与 ADR-0011

本节是前述“仍待验证”清单的关闭记录；旧章节保留为当时的历史状态。

- Go `1.23.12 windows/amd64`：`go test -count=1 ./... -timeout 10m`、`go vet ./...`、
  `go build ./...` 通过；`git diff --check` 和 `docker compose config --quiet` 通过。
- Linux race：在 Debian 13 容器中临时安装 Go `1.24.4` 与 GCC 后执行 `go test -race ./...`，全部通过。
  官方 `golang:1.23` 镜像因本机 Docker Hub 网络不可达未使用，race 仍在 Linux 工具链上完成。
- OpenAPI：`pnpm --package='@redocly/cli@latest' dlx redocly lint docs/openapi.yaml` 通过，保留 3 个既有 warning
  （license 缺失、`healthz`/`readyz` 未添加虚构 4xx）。
- 真实依赖：PostgreSQL `17.5-alpine` 与 SeaweedFS `3.85` healthy；`TestLiveHTTPPostgresSeaweedFSEndToEnd`、
  Range、重启持久性、跨租户、恢复冲突、清理和 100 会话基线全部通过。
- 100 会话 create 基线：`p50=101.7472ms p95=122.3139ms p99=123.5448ms max=123.857ms`。
- ADR-0011 落地：非法 Part 清单在状态变化前拒绝；确定性存储拒绝、大小不符和 verified checksum 不符分别持久化
  `FAILED` 与 failure code，并进行精确对象/Multipart 清理；超时、连接错误和未知结果继续保留 `COMPLETING`。
- 新增内存/PostgreSQL `FailUploadCompletion` 契约、并发同名创建、目录子树回收/恢复、恢复冲突和真实在途请求优雅停止测试。

仍延期的 SHOULD：`Idempotency-Key`、自动维护/孤儿对账、Prometheus、fuzz/property suite，以及百万节点长时 SLO；
原因、风险和后续路线见 [acceptance.md](./acceptance.md) 第 7 节。

## 2026-08-03 - PR #2 审查问题修复

分支：`codex/fix-p4-review-findings`。设计与问题映射见
[ADR-0012](../adr/0012-tenant-namespace-mutation-serialization.md)、
[ADR-0013](../adr/0013-completed-upload-reconciliation.md) 和
[review-remediation.md](./review-remediation.md)。

实现与回归：

- tenant 行作为 MVP Namespace 事务互斥点，固定 tenant → upload session → node/parent → Blob 锁顺序；
  目录创建/移动、上传提交、回收、恢复和清理准备在锁内复查状态。
- 递归移动/回收使用可终止的 `UNION` 和防御性 active/unowned 条件；并发互相移动最多一个成功，父回收
  与创建/移动/上传提交/恢复不会产生活动孤儿，父子回收保持独立根。
- Blob 清理忽略 `purging` owner；两个文件顺序清理共享 Blob 时，最后一个有效引用会安排删除。
- 父目录不可用和名称冲突在上传提交事务内持久化为 `FAILED`，对象按精确 Key 补偿删除；删除失败可通过
  上传 DELETE 重试，且取消依据仓储原子转换返回的最新状态选择 Delete 或 Abort Multipart。
- committed 完成重放不再依赖活动下载可见性；文件回收和永久清理后仍返回同一文件 ID。
- `NoSuchUpload + HEAD 404` 保持 `COMPLETING` 并返回可重试依赖错误；`NoSuchBucket` 映射为可重试
  `dependency_unavailable`，`NoSuchKey`/`NoSuchUpload` 保持 `not_found`。
- 非法 UUID 在 Service 边界返回 `400 invalid_request`，PostgreSQL `22P02` 保留防御性映射；恢复名称
  唯一冲突稳定返回 `restore_conflict`。

验证环境：Windows `amd64` / Go `1.26.5`；Compose PostgreSQL `17.5-alpine` 与 SeaweedFS `3.85`
均为 healthy。真实依赖测试使用隔离 PostgreSQL Schema 和专用测试 Bucket。

```text
go test ./... -count=1
go vet ./...
go build ./...
git diff --check

# 注入仓库本地 Compose 测试变量
go test -v ./internal/postgres -count=1
go test -v ./internal/s3store -count=1
go test -v ./internal/server -count=1

docker run --rm --mount type=bind,source=<workspace>,target=/src -w /src \
  golang:1.26.5-bookworm go test -race ./... -count=1
```

结果：

- 全仓测试、`go vet`、`go build`、`git diff --check` 与 Linux race 全部退出码 0。
- PostgreSQL 真实测试通过：并发互相移动、父回收四类竞态、并发嵌套回收、共享 Blob 顺序清理、迁移和
  Repository 契约全部通过。
- SeaweedFS 真实 `TestProviderSeaweedFSContract` 通过；S3 缺失资源分类单元测试全部通过。
- `TestLiveHTTPPostgresSeaweedFSEndToEnd` 和 100 会话并发基线通过；本次基线
  `p50=127.9025ms p95=155.9785ms p99=156.4998ms max=156.4998ms`。
- `TestMalformedPathIdentifierReturnsBadRequest`、上传未知完成、Namespace 接纳失败、对象删除重试、
  committed 回放和取消状态竞态回归全部通过。

## 2026-08-03 - M2-1 生产身份与权限基础

分支：`codex/m2-identity-access`。设计与边界见 [ADR-0014](../adr/0014-oidc-resource-server-and-tenant-rbac.md)
和 [M2-1 身份与权限设计](../m2/identity-access.md)。

实现与证据：

- API 作为 OIDC Resource Server，完成 discovery/JWKS、RS256/384/512 与 ES256/384/512 签名校验，
  issuer、audience、`azp`、`exp`、`nbf`、`kid`、算法和 JWT JSON 完整性检查；provider 故障映射为
  `dependency_unavailable`，不记录 token 或 JWT 内容。
- PostgreSQL 迁移 `0002_identity_access.sql` 增加 `principal` 与 `tenant_member`，以 `(issuer, subject)`
  映射内部 UUID；内存和 PostgreSQL adapter 覆盖幂等 bootstrap、多租户成员、suspended 状态和冲突约束。
- HTTP 路由按 `tenant:read`、`files:read`、`files:write`、`files:delete` 执行 owner/admin/editor/viewer
  RBAC；OIDC 要求 `X-Tenant-ID`，认证失败为 `401`，选择器错误为 `400`，成员/权限不足为 `403`。
- `trusted-dev` 仅允许 development；production 强制 OIDC、PostgreSQL 和 S3。README、OpenAPI、API 设计、
  运维测试设计和文档索引已同步配置变量与错误契约。

M2-1 验证命令：

```text
gofmt -w internal/auth/oidc.go internal/auth/oidc_test.go internal/config/config.go internal/config/config_test.go internal/drive/ports.go internal/drive/types.go internal/memory/repository.go internal/memory/identity_test.go internal/postgres/identity.go internal/postgres/repository_integration_test.go internal/server/server.go internal/server/server_test.go cmd/asteria-server/main.go
go test ./internal/auth ./internal/config ./internal/drive ./internal/memory ./internal/postgres ./internal/server -count=1
go test ./... -count=1
go vet ./...
go build ./...
git diff --check
```

## 2026-08-03 - M2-2 tenant member lifecycle management

Design is archived in [ADR-0015](../adr/0015-tenant-member-lifecycle.md) and
[member-management.md](../m2/member-management.md). The implementation adds
tenant member listing plus owner/admin role and active/suspended status updates.
PostgreSQL and memory repositories share an atomic update contract that preserves
at least one active owner under concurrent changes. Invitations, self-registration,
member deletion, fine-grained ACLs, and audit export remain deferred.

Verification for this increment:

```text
go test ./... -count=1
go vet ./...
go build ./...
git diff --check
pnpm --package='@redocly/cli@latest' dlx redocly lint docs/openapi.yaml

# With local Compose PostgreSQL on 127.0.0.1:15432:
$env:ASTERIA_TEST_DATABASE_URL='postgres://asteria:local-asteria-password@127.0.0.1:15432/asteria?sslmode=disable'
go test ./internal/postgres -count=1
```

## 2026-08-04 - CI rollout step 2

ADR-0016 is accepted and the first secret-free GitHub Actions candidate checks are
implemented in `.github/workflows/ci.yml`:

- `CI / quality` verifies modules and formatting, runs the complete deterministic
  test suite, vet and build, checks whitespace against the event's actual commit
  range, and retains JSON/coverage evidence for seven days;
- `CI / race` runs the full deterministic suite with the Linux race detector;
- `CI / api-contract` installs Action Validator 0.6.0 and Redocly CLI 2.43.3
  from `package-lock.json`, validates the workflow schema, lints
  `docs/openapi.yaml`, and verifies exact equality between documented operations
  and the route inventory used by the server;
- all Actions use full commit SHAs, workflow permissions are `contents: read`,
  checkout credentials are discarded, and no repository secret is referenced;
- `internal/cicheck` parses the workflow and guards the stable job names, events,
  permissions, credential handling, and immutable Action references.

Quality and API checks use the minimum Go 1.23.12, while race uses the current
Go 1.26.5. Node.js is fixed at 24.16.0 and every Go job uses `GOTOOLCHAIN=local`.
Compose-backed integration and structured skip detection are explicitly rollout
step 3. Repository rules that make all four checks required are rollout step 5;
they are not represented as configured here.

Local verification used Go 1.26.5 on Windows, Node.js 24.16.0/npm 11.13.0, and a
read-only Linux container for race detection:

```text
go mod verify
go test ./... -count=1
go vet ./...
go build ./...
git diff --check
npm ci --ignore-scripts
npm run lint:actions
npm run lint:openapi
go test ./internal/cicheck ./internal/server -count=1

docker run --rm --mount type=bind,source=<workspace>,target=/src,readonly \
  --mount type=bind,source=<go-module-cache>,target=/go/pkg/mod,readonly \
  -w /src -e GOTOOLCHAIN=local \
  golang@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 \
  go test -race ./... -count=1
```

All commands exited 0. Redocly retained the three previously documented warnings
(missing license metadata and no invented `4xx` responses for health probes);
the OpenAPI document remained valid.

## 2026-08-04 - CI rollout step 3

The fourth secret-free candidate check, `CI / integration`, is implemented in
`.github/workflows/ci.yml`. It runs on Ubuntu 24.04 with Go 1.26.5 and Docker
Compose v2, using only disposable repository-owned test values rather than
GitHub Secrets. `compose.yaml` pins PostgreSQL and SeaweedFS to these digests:

```text
postgres:17.5-alpine@sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa
chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a
```

The committed integration manifest requires 17 tests: 14 PostgreSQL adapter
tests, one SeaweedFS provider test, and two live HTTP tests. The workflow runs
the three packages serially with `go test -json -p=1 -count=1 -timeout=15m`.
Its repository-owned verifier requires every package and named test to finish
with `pass` and rejects any `skip`, missing result, failed package, or malformed
report.

Uploaded failure evidence does not include the raw report. Under `always()`, the
workflow sanitizes the JSON report and Compose logs, removes the raw JSON, tears
down containers and volumes with `docker compose down -v --remove-orphans`, and
uploads only sanitized evidence with seven-day retention. Sanitizer tests cover
database URLs and passwords, explicit S3 credentials, bearer authorization, and
signed URLs.

The local completion run used the same disposable Compose dependencies and JSON
report verifier as CI:

```text
docker compose up -d --wait --wait-timeout 180
go test -json -p=1 -count=1 -timeout=15m \
  ./internal/postgres ./internal/s3store ./internal/server \
  > integration-test.raw.json
go run ./tools/verify-integration-report \
  -manifest .github/integration-tests.json \
  -input integration-test.raw.json
go run ./tools/sanitize-ci-log \
  < integration-test.raw.json > integration-test.json
docker compose down -v --remove-orphans
```

The verifier exited 0 with this exact result:

```text
verified 17 required integration tests across 3 packages
```

This is local real-dependency evidence, not a claim that a GitHub-hosted runner
has executed the candidate check. The four stable checks remain non-required
candidates until rollout step 5 configures branch protection. API compatibility
comparison remains later hardening to complete before that step.

## 2026-08-04 - CI rollout step 4

Security automation and dependency update proposals are implemented without
repository secrets:

The first govulncheck baseline found reachable fixes requiring newer toolchain
support, so ADR-0017 retains the Go 1.25.0 module directive and raises the
supported compiler floor to patched Go 1.25.12. A follow-up scan found
GO-2026-4341 reachable in the Go 1.25.0 standard library; the minimum lane now
includes its Go 1.25.6-or-later fix. The historical rollout step 2 verification
remains recorded as run with the then-current Go 1.23.12 policy. The dependency
findings were GO-2026-5970 (`x/text`), GO-2026-5764 (AWS event-stream/S3), and
GO-2026-5004 (`pgx`). The dependency graph now uses `x/text` v0.39.0, AWS
event-stream v1.7.8, AWS S3 v1.97.3, and `pgx/v5` v5.9.2 or later fixed
companion versions.

- `Security / govulncheck` runs the pinned
  `golang.org/x/vuln/cmd/govulncheck@v1.1.4` tool against both Go 1.25.12 and
  Go 1.26.5;
- `Security / dependency-review` uses
  `actions/dependency-review-action@a1d282b36b6f3519aa1f3fc636f609c47dddb294`
  (v5.0.0) for pull requests and blocks newly introduced high-severity issues;
- `Security / codeql` uses
  `github/codeql-action/*@e60ea984bd3baa95954f2856bcf24f9eaba46637` (v3.37.5)
  for Go, with `security-events: write` scoped only to that job;
- `.github/dependabot.yml` checks Go modules, npm, GitHub Actions, and
  Compose/Docker references weekly, with five open update PRs per ecosystem.

`internal/cicheck` contract tests verify the security workflow triggers,
permissions, conditions, Action pins, CodeQL inputs, govulncheck version, and
Dependabot coverage. The security checks remain candidate checks while the first
baseline is reviewed; branch protection remains rollout step 5.

Validation evidence for this step:

```text
go mod verify
go test ./... -count=1
go vet ./...
go build ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
npm run lint:actions
npm run lint:openapi
docker compose config --quiet

# Go 1.25.12 clean container:
go test ./... -count=1

# Go 1.25.12 and Go 1.26.5 govulncheck lanes:
go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...

# Go 1.26.5 Linux container:
go test -race ./... -count=1

# Digest-pinned local Compose dependencies:
go test -json -p=1 -count=1 -timeout=15m \
  ./internal/postgres ./internal/s3store ./internal/server
go run ./tools/verify-integration-report \
  -manifest .github/integration-tests.json \
  -input integration-test.raw.json
```

All local commands exited 0. Govulncheck reported `No vulnerabilities found`,
the integration verifier reported `verified 17 required integration tests
across 3 packages`, and the sanitized evidence scan found no credentials.
Redocly retained the three previously documented non-blocking warnings. This
local record does not claim that a hosted runner has already produced a security
baseline; remote workflow evidence is observed on the rollout step 4 pull
request.
