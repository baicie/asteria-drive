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
  → mvp/p4-integration  (历史 P4 最终里程碑分支；已合入 main)
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

The committed integration manifest requires 19 tests: 16 PostgreSQL adapter
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
verified 19 required integration tests across 3 packages
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
the integration verifier reported `verified 19 required integration tests
across 3 packages`, and the sanitized evidence scan found no credentials.
Redocly retained the three previously documented non-blocking warnings. This
local record does not claim that a hosted runner has already produced a security
baseline; remote workflow evidence is observed on the rollout step 4 pull
request.

## 2026-08-05 - CI rollout step 5

The step 4 pull request was merged to `main` as
`72346294e2960e75874e01e5272db57bee084f34`. The stable CI and security checks
were then applied as the protected `main` branch merge gate:

- `CI / quality`;
- `CI / race`;
- `CI / api-contract`;
- `CI / integration`;
- `Security / govulncheck`;
- `Security / dependency-review`;
- `Security / codeql`.

The protection policy requires strict up-to-date checks, one approving review,
stale-review dismissal, approval after the last push, and resolved review
conversations. Administrator enforcement and required CODEOWNERS review are
enabled, while force pushes and branch deletion are disabled. Linear-history
enforcement remains a separate follow-up decision.

Repository settings also enable the dependency graph, vulnerability alerts,
Dependabot security updates, secret scanning, and push protection. Earlier
rollout notes treated base-document OpenAPI compatibility as a step 5
prerequisite; step 5 explicitly decouples it as later API hardening. Route and
operation inventory equality remains enforced by the required
`CI / api-contract` check.

GitHub API verification returned seven required contexts with `strict: true`,
one required approval, code-owner review enabled,
`require_last_push_approval: true`, conversation resolution enabled,
administrator enforcement enabled, and both destructive branch operations
disabled. The branch protection endpoint returned 404 before this change and
the configured policy after it. No release workflow or release artifact policy
was introduced in this step.

## 2026-08-05 - CI rollout step 6 implementation

ADR-0018 defines the release artifact and provenance boundary. The repository now
contains a deterministic package tool and release workflow that builds
`asteria-server` and `asteria-migrate` for Linux `amd64` and `arm64`, embeds the
version/source commit/build date, writes `release-manifest.json`, generates an
SPDX JSON SBOM, and writes sorted SHA-256 checksums for the complete bundle.

The workflow validates that a `vMAJOR.MINOR.PATCH` tag resolves to a commit
reachable from protected `main`. Build permissions remain read-only; a separate
publish job is gated by the `release` environment and is the only job with
contents, OIDC, and attestation write permissions. Pull requests cannot publish
or attest releases. The API-contract job also compares pull-request OpenAPI
documents with their base using the repository-owned compatibility checker and
requires an accompanying ADR for OpenAPI changes.

Local verification built both target archives, inspected their deterministic
contents, and passed the release package, checksum, build metadata, CI workflow
contract, and existing action-validator tests. GitHub API verification confirmed
the `release` environment with one custom `tag` deployment policy named `v*`;
the workflow separately requires each tag commit to be reachable from protected
`main`.

## 2026-08-05 - Phase 1 production-readiness evidence closeout

The production-readiness increment was implemented on `codex/production-readiness` and
merged into protected `main` through PR #21.
The repository now includes invitation issue/accept/revoke lifecycle with one-time
issuer and subject binding, tenant-local inherited allow-only ACLs, append-only
security audit events with bounded NDJSON export, request idempotency leases and
replay protection, automatic maintenance claims, bounded Prometheus metrics,
fuzz/property coverage for protocol boundaries, production-negative configuration
checks, and hardened Kubernetes/Docker deployment contracts. PostgreSQL and memory
implementations share the governance and idempotency contracts; HTTP and PostgreSQL
integration contracts cover cross-tenant access, inherited ACL elevation, invitation
replay, audit pagination, and append-only enforcement.

Secret-free repository evidence added on this date:

- `docs/operations/evidence/million-node-integrity-20260805.json` verifies a
  1,000,000-node fixture plus its root with zero orphans and zero duplicate active
  names; the load and replay timings are in the adjacent JSON artifacts.
- `docs/operations/evidence/slo-control-plane-20260805.json` records the required
  five-minute warmup and ten-minute, 50-RPS read-only sampler. It observed 29,998
  requests, zero drops, one transient server error, and a server-error rate of
  `0.003334%`; every sampled endpoint stayed below the initial latency thresholds.
- `docs/operations/evidence/recovery-drill-20260805.md` records a seeded isolated
  PostgreSQL/SeaweedFS restore, checksum verification, storage integrity check,
  application smoke checks, and measured elapsed times. It explicitly leaves
  production PITR/WAL, immutable object retention, candidate binding, and
  independent approval open.

These results close the repository implementation and local evidence portions of
Phase 1/M2. They do not claim a production deployment, public SLA, immutable OCI
release digest, platform-owned recovery controls, or independent security approval.

## 2026-08-05 - Release/API and dependency-pin closeout

PR #22 (`20568f863e1005edb310b4a2600c5678b2c9a90a`) merged the remaining release/API
hardening and dependency-pin consistency work into `main`. The workflow pins for
checkout, upload-artifact, setup-node, and CodeQL now match the repository-owned
contract test expectations. The merge PR passed all seven required checks plus the
CodeQL workflow check, and the remote `main` branch is protected at that commit.

The repository currently has no release tag or GitHub Release. The `release`
environment accepts `v*` tags, but no tag was created during this closeout; an
authorized release operation must still establish the immutable OCI digest and
platform-owned production evidence before the project can claim public production
readiness.

## 2026-08-05 - v0.1.0 release and provenance closeout

The annotated `v0.1.0` tag points to protected `main` commit
`8878d9eaaf88973c522a4f4742ea960acd63d503`. Release workflow run
`31015266832` completed successfully: deterministic `linux/amd64` and
`linux/arm64` archives, SPDX SBOM, release manifest, and checksums were built;
the publish job pushed both release tags and created the immutable GitHub Release
at <https://github.com/baicie/asteria-drive/releases/tag/v0.1.0>.

The published OCI manifest digest is
`sha256:f5da244cba2055764a8caae7b9e9a752cc8f07356c0d7ae6397a6a7992e0cccc`.
`gh attestation verify` validated the release manifest attestation against the
`Release` workflow, `refs/tags/v0.1.0`, source commit `8878d9e`, and a Rekor
transparency-log timestamp; the attestation subject set matched the four
published release files. The same workflow recorded OCI provenance for the
published manifest digest.

This closes the repository release gate. It does not claim public production
readiness: production PITR/WAL, immutable object retention, platform-owned
deployment evidence, and independent security approval remain required.

## 2026-08-08 - v0.1.0 staging deployment and capacity closeout

Two credential-bearing server inventory CSV files were evaluated without
printing or committing credentials. The lower-load candidate was selected from
live measurements: 4 vCPU, load1 `0.00`, about 24% memory use, and three existing
containers, compared with the other candidate's load1 `0.01`, about 51% memory
use, and thirteen containers. The selected host key was pinned before every SSH
operation.

Initial deployment run
[31249531451](https://github.com/baicie/asteria-drive/actions/runs/31249531451)
proved the release path but exposed root-disk growth from 81% to 100%. The growth
was isolated to empty SeaweedFS staging volumes created with large allocation
defaults. Only the Asteria staging PostgreSQL and SeaweedFS data volumes were
removed; all server-managed secret volumes and unrelated workloads were
preserved, recovering the root filesystem to 63% use.

PR [#29](https://github.com/baicie/asteria-drive/pull/29) added bounded SeaweedFS
volume settings plus root, Docker-data, and data-volume capacity evidence and
85%/5 GiB gates. Its first rerun stopped before mutation because Docker renders a
default local volume's nil `.Options` map as `null`. PR
[#30](https://github.com/baicie/asteria-drive/pull/30) strictly allowed only
`null` or `{}` after requiring the `local` driver, continued to reject every
non-empty custom option object, updated the forced-command script digest, and
passed all seven protected checks plus the extra CodeQL check.

The staging deploy key was rotated without writing the private key to disk. The
new key was validated by OpenSSH, the host dispatcher was bootstrapped with only
the public key, `prepare`/`cleanup` were exercised, and the GitHub `staging`
Environment secret was updated through the sealed-box API. Deployment workflow
[31260549517](https://github.com/baicie/asteria-drive/actions/runs/31260549517)
attempt 2 then succeeded from protected-main workflow commit
`ddc9f126945a97fb152b8a805929abca18477654`.

The retained evidence artifact is `9022865120`, digest
`sha256:d1cc889fd4386594d1d26d728bdb868ed107b597f8a6db091913f15f1055bf03`.
It binds the deployment to:

- release tag `v0.1.0`, source commit
  `8878d9eaaf88973c522a4f4742ea960acd63d503`, and OCI digest
  `sha256:f5da244cba2055764a8caae7b9e9a752cc8f07356c0d7ae6397a6a7992e0cccc`;
- Compose digest
  `d7d39a2e965849f364ceb25ab4106efd575f9a6d924e8ebfd9d508a594adc5dc`;
- schema migration `0 -> 3` and successful health, readiness, authenticated
  smoke, multipart upload/download equality, metrics scrape, binary identity,
  loopback binding, capacity guard, and data-volume filesystem checks;
- root and Docker-data use holding at 62%, with about 14.3 GiB available before
  and after deployment; PostgreSQL and SeaweedFS data volumes remained on the
  same measured filesystem and above the reserve;
- one checked storage object, one healthy object, `verified: true`, no findings,
  and no truncated findings.

Post-run host inspection confirmed all three staging containers running, exact
API image identity, only loopback host bindings, live `/healthz` and `/readyz`,
the reviewed active Compose digest, successful remote temporary-file cleanup,
about 47 MiB of PostgreSQL data, and only 256 KiB of SeaweedFS data. This evidence
is explicitly `staging-not-production`.

Public production readiness remains open for HTTPS OIDC and IdP lifecycle,
PostgreSQL `verify-full` and PITR/WAL, object lock/versioning, KMS or
secret-controller rotation, public TLS ingress, monitoring/SIEM, host-level HA,
an externally valid presign endpoint, platform-owned capacity planning and
external alerts, and independent security/platform approval.

## 2026-08-08 - staging continuous monitoring closeout

PR [#32](https://github.com/baicie/asteria-drive/pull/32) added the hourly and
manually dispatchable `Monitor staging` workflow. It runs only from protected
`main`, serializes with deployments, uses the same five environment-scoped SSH
secrets and strict host-key verification, and retains sanitized JSON for 30 days.
The forced-command dispatcher accepts only a validated `status RUN_ID ATTEMPT
WORKFLOW_SHA` request and executes a root-owned `0755` probe after checking its
SHA-256 on every invocation. The pinned probe digest is
`8c7e3526c091e0c73b5300f8ec105c027bf9fbee750554f716f15772c6d4e134`.

The probe rejects non-default local-volume options and verifies the active
Compose digest, exact API and dependency image identities, Compose project and
service labels, container health, the exact loopback binding set, health,
readiness, metrics, and root/Docker/data-volume 85%/5 GiB capacity thresholds.
PR #32 passed all seven protected checks plus the extra CodeQL check and merged
as `51fdd808fdec0708ef8408fb8d6944991bdb7c29`.

After merge, the host dispatcher and monitor were bootstrapped with a newly
generated ED25519 key. The private key remained in process memory, the host
received only its public key, `prepare`/`cleanup`/`status` succeeded, an arbitrary
shell command was rejected, and the GitHub `staging` Environment secret was
updated through the sealed-box API. Successful workflow run
[31265767434](https://github.com/baicie/asteria-drive/actions/runs/31265767434)
attempt 1 additionally proved OpenSSH private-key parsing on the hosted runner,
strict host trust, the forced status path, and artifact upload from protected-main
commit `51fdd808fdec0708ef8408fb8d6944991bdb7c29`.

Artifact `9024088965`, digest
`sha256:349d2e98bbe548a00a49180403d085eea173346ce9b259b44d4a467e822deab3`,
was downloaded directly from the GitHub API into memory and verified independently
of the workflow. The archive digest matched, it contained exactly one
`monitor-evidence.json`, its field set matched the allowlist, all required proofs
were true, and no forbidden secret marker was present. The evidence reported
load1 `0.00`, memory use `27.39%`, root and Docker/data-volume use `62%`, and
`15332425728` bytes available, above the required reserve.

This closes periodic staging capacity and health signaling only. The claim remains
`staging-not-production`; platform-owned production monitoring and alert routing,
SIEM retention, capacity planning, HTTPS OIDC/IdP lifecycle, PostgreSQL
`verify-full` and PITR/WAL, object lock/versioning, KMS or secret-controller
rotation, public TLS ingress, host-level HA, an externally valid presign endpoint,
and independent security/platform approval remain open.

## 2026-08-09 - staging recovery automation and evidence closeout

PR [#34](https://github.com/baicie/asteria-drive/pull/34) added the weekly and
manually dispatchable `Drill staging recovery` workflow. It runs only from
protected `main`, shares the staging deployment/monitor concurrency group, checks
the reviewed recovery script digest before transport, and invokes only a
run-bound forced SSH command. The remote root-owned script creates a bounded
logical PostgreSQL archive, restores it into isolated internal-network resources,
compares schema/table/row checkpoints, runs `asteria-verify-storage`, exercises
recovered application probes, enforces the 85%/5 GiB capacity guard, and performs
idempotent cleanup. It never mutates or deletes the source staging data or its
server-managed secret volumes.

The merged recovery script SHA-256 is
`0e03a8e03ee2a98c277818e172822a401614f778f58f8c3e85130a83467402a6`.
The host dispatcher requires that digest in the forced command and rechecks the
root-owned installed file. Host bootstrap and negative-path tests verified the
script and parent-directory ownership, accepted `prepare`, `cleanup`, `status`,
and the exact recovery command, and rejected arbitrary shell, port forwarding,
the legacy four-argument recovery command, and a wrong script digest. The staging
ED25519 key was rotated with the private key held only in process memory; GitHub
received it through the sealed-box secret API and the host received only the
public key.

PR #34 passed all seven protected checks plus the additional CodeQL check and
merged as `0a7a5360dbb0fd0ad6f46e262929a7a2e318cb18`. No review approval is recorded,
so independent approval remains open. Workflow run
[31292745979](https://github.com/baicie/asteria-drive/actions/runs/31292745979)
attempt 1 then completed successfully from that exact protected-main commit.
Artifact `9031929907` is retained for 90 days and has digest
`sha256:e1edc196bf2b33d08f2567509136bdd94b23a73ddde01a6a28bdc4e87d9accad`.
Independent in-memory verification found exactly one member,
`recovery-evidence.json`, whose digest is
`sha256:4d1f55d5ac166cae12b87c35046a456e78710217e24762b9b78de29a5b5034c7`;
no raw remote stderr was archived.

The evidence reports `success` and final phase `complete`: the 51,454-byte archive
catalog was valid, schema stayed `3 -> 3`, all 15 tables were checked, total rows
matched `12 -> 12`, and the storage verifier reported one checked and healthy
object with no findings. Backup and restore took 1 and 8 seconds, recovered
health/readiness/authenticated-read/metrics probes passed, capacity remained at
62% disk use, and cleanup left no drill resources. The checked-in detail is
`docs/operations/evidence/staging-recovery-20260809.md`.

This closes staging logical-recovery automation and retained evidence only. The
report explicitly records `object_versions_restored=false`,
`pitr_wal_replayed=false`, and `staging-recovery-not-production`; encrypted
production PITR/WAL, object versioning/Object Lock, accepted production RPO/RTO,
public TLS and HTTPS OIDC lifecycle, `verify-full`, KMS-backed rotation,
production monitoring/SIEM and alerts, host-level HA, external presign routing,
capacity planning, and independent security/platform approval remain open.

## 2026-08-09 - S3 control/public endpoint separation

The S3 adapter now accepts a distinct client-visible presign endpoint while
retaining the existing endpoint for API control calls, readiness, maintenance, and
recovery verification. `ASTERIA_S3_PUBLIC_ENDPOINT` defaults to
`ASTERIA_S3_ENDPOINT`, so existing single-endpoint deployments remain compatible.
When the values differ, upload-part and download URLs are signed directly against
the public scheme and host; the application does not rewrite a signed URL and does
not contact the public endpoint during presigning.

Production validation requires both endpoints to be HTTPS URLs without embedded
credentials, query strings, or fragments. The Kubernetes base exposes separate
placeholders, ADR-0023 records the routing and Signature V4 invariants, and the
deployment runbook requires external DNS/TLS, CORS, signed-host preservation, and
real client transfer evidence. Unit coverage proves that readiness reaches only the
private control endpoint while both upload and download signatures carry the public
host and a Signature V4 value.

This closes the repository capability gap only. No user-owned public hostname or
certificate has been supplied, and `v0.1.0` predates this change. A later immutable
release plus target-platform evidence is required before the external-presign or
public-production gates can be marked complete.

## 2026-08-09 - PostgreSQL production TLS guardrails

Production database validation now requires the entire DSN to come from
`ASTERIA_DATABASE_URL_FILE`, rather than checking only a password in URL userinfo.
This closes the alternate `?password=` path and also rejects inline passwordless
DSNs, which could otherwise gain credentials later without changing the source
boundary. The query must contain exactly one `sslmode=verify-full`; missing, weak,
or duplicated modes are rejected before a connection attempt.

The portable backup and isolated-restore scripts now append
`sslmode=verify-full` to their password-free `service=<name>` conninfo, overriding
weaker service-file or environment settings while keeping credentials off the
command line. Contract tests fix that invariant. The Kubernetes migration Job also
sets `fsGroup: 65532`, matching the application Deployment so its `0400` projected
database URL and CA material are readable by the non-root runtime group.

Negative tests cover inline userinfo and query passwords, inline passwordless DSNs,
missing/weak/duplicate TLS modes, and valid file-sourced `verify-full`. These changes
close a repository configuration bypass only. Current staging still uses a non-TLS
development PostgreSQL link, and `v0.1.0` predates the fix; a new immutable release,
real CA/host-name validation, measured `pg_stat_ssl` evidence, and target-platform
certificate rotation remain required.

## 2026-08-10 - v0.1.1 release and provenance closeout

PostgreSQL production TLS guardrails merged into protected `main` through PR
[#37](https://github.com/baicie/asteria-drive/pull/37) at
`e8d26ded6e9138bbfdeac60f2487c5f835ab61a5`. Release workflow
[31298871230](https://github.com/baicie/asteria-drive/actions/runs/31298871230)
published immutable
[`v0.1.1`](https://github.com/baicie/asteria-drive/releases/tag/v0.1.1) artifacts.
Checksums, the SPDX 2.3 SBOM, all four Release subject provenance statements, and
OCI provenance were independently verified.

The published multi-architecture OCI digest is
`sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842`;
its Linux `amd64` and `arm64` manifests are respectively
`sha256:fa1d574cabfe0ca38b88e07bec5e4524bdfd2528757555850fda55472d397c1c`
and
`sha256:9a31d5c8f9b73dd44fdb181a052110bcd6792bf9160ab1fcff2185a0e5f4ca5b`.
This closes the immutable release prerequisite for the staging TLS rollout. It does
not prove that the staging host is running `v0.1.1`, and it does not close any
platform-owned public-production gate.

## 2026-08-10 - staging PostgreSQL private PKI implementation

[ADR-0024](../adr/0024-staging-postgresql-private-pki.md) replaces only the
single-host staging decision to use plaintext PostgreSQL. Explicit root bootstrap
now creates a 3072-bit private CA and a `serverAuth` leaf whose SAN is exactly
`DNS:postgres`. The CA private key is root-owned mode `0400` and remains only at
`/etc/asteria-drive/staging-postgres-pki/issuer/ca.key`; Actions, Docker volumes, and
evidence artifacts never receive it. Bootstrap refuses a partial PKI and does not
implicitly repair or rotate certificates.

The candidate Compose target enables PostgreSQL TLS with a TLS 1.2 minimum, rejects
plaintext TCP, and requires SCRAM-SHA-256 on TLS TCP connections. The API stays
inside the `development`/trusted-development staging boundary, while migration runs
with `ASTERIA_ENV=production`; both consume the file-mounted `database-url-tls` with
`sslmode=verify-full`. The old `database-url` remains only for automatic rollback to
the previous active `v0.1.0` Compose definition.

Deployment and monitor verification cover file ownership, chain and host-name
validation, certificate validity, HBA and TLS settings, SCRAM storage, API sessions
in `pg_stat_ssl`, negotiated TLS version/cipher/bits, and rejection of an explicit
plaintext connection. The negative probe deletes raw stderr after retaining only a
presence flag and SHA-256. Candidate failures after a runtime change restore the
previous PostgreSQL, SeaweedFS, and API offline from local images and recheck
readiness, the fixed image reference, and the matching local config ID. A failed
first deployment removes only candidate containers and networks, preserving every
persistent data and secret volume.

Deployment, monitor, and recovery evidence advance to v2 schemas with duplicate-key
rejection and exact field allowlists. Recovery v2 records the source API's TLS
connection/version/cipher/bits before the logical backup; its temporary restore
database remains an isolated staging resource and is not production TLS or PITR
evidence.

At this checkpoint the shell syntax checks, rendered staging Compose validation, and
Actions policy lint pass locally. Runtime closure is intentionally still open until
the change passes protected checks, merges to `main`, the selected host is explicitly
re-bootstrapped without replacing data or credentials, `v0.1.1` is deployed, and the
deployment/monitor/recovery v2 artifacts are independently verified. The private CA
does not close public TLS, managed database PKI/rotation, encrypted production
PITR/WAL, object immutability, KMS, HA, SIEM/alerts, or independent approval.

## 2026-08-16 - staging TLS runtime and recovery closeout

The rollout sequence completed through PRs #38, #39, #52, and #53. The final Secret
owner probe fix merged into protected `main` as
`7c6565bfb16d63c8a9ef7fad54df7e91276a504b` after all seven required checks and the
additional CodeQL check passed. Its contract tests bind one global `readonly` helper
image, exact application and PostgreSQL runtime users, separate read-only Secret
mounts, and continuous summary parse/validation/comparison blocks; negative mutations
cover helper reassignment and summary or digest overwrites.

The two CSV inventory records were parsed without emitting credentials, and the
lower-load target was selected behind a fixed ED25519 host fingerprint. GitHub's
`staging` Environment host, port, user, and known-host values were aligned to that
target without replacing the existing private deploy key. A SHA-256-checked on-disk
launcher then ran the merged root bootstrap without deleting or rotating any data or
Secret volume. Independent host checks proved the reviewed monitor and recovery
digests, root ownership and modes, dispatcher configuration, single forced-command
key, removal of upload files, and absence of diagnostic wrappers.

Deployment run
[31713181762](https://github.com/baicie/asteria-drive/actions/runs/31713181762)
had already installed the immutable `v0.1.1` OCI digest. Its artifact `9186277164`
has digest
`sha256:443a1f734809e6d616558d45eb874e689ac444838dbaf285799d61fd20076a49`
and proves migration `3 -> 3`, storage verifier `2/2`, TLS 1.3, SCRAM/HBA/plaintext
rejection, application smoke checks, capacity, loopback exposure, and no rollback.

After final bootstrap, protected-main monitor run
[31953366435](https://github.com/baicie/asteria-drive/actions/runs/31953366435)
and recovery run
[31953850132](https://github.com/baicie/asteria-drive/actions/runs/31953850132)
both succeeded from `7c6565bfb16d63c8a9ef7fad54df7e91276a504b`. Their artifact
digests are respectively
`sha256:65cc421a86b5fee858b1141b98cacaaaf9eab13e32f3f0b045e6f3d5166685bb`
and
`sha256:a5944afd9514755a2469e1c26eacb8836ebdec70549c0bcab5278c5c2477f619`.
Independent duplicate-key and exact-allowlist checks bound both v2 reports to their
run IDs, attempt, workflow SHA, script digests, Compose digest, and OCI digest. The
monitor observed root-disk use at 65% with more than 13 GiB free and all runtime/TLS
proofs true. Recovery matched schema `3 -> 3`, all 15 tables, 17 source and restored
rows, storage verifier `2/2`, recovered application checks, and zero drill residue.

The checked-in evidence detail is
`docs/operations/evidence/staging-tls-rollout-20260816.md`. This closes the staging
TLS deployment, monitoring, and logical-recovery evidence gap. It does not close
public DNS/TLS ingress, production OIDC lifecycle, managed database trust or
PITR/WAL, object versioning/Object Lock, KMS-backed rotation, HA, production
monitoring/SIEM and external alerts, capacity planning, external presign transfer
evidence, or independent security/platform approval.

## 2026-08-22 - current production-gate and release-state correction

The repository-side release and CI hardening described above is now superseded by
the checked-in workflow contracts and [production closure checklist](../operations/production-readiness.md).
The historical `v0.1.0` and `v0.1.1` Release records are retained as provenance and
artifact evidence, but a current GitHub API review returned `isImmutable=false` for
both releases while `immutable-releases.enabled=false`. Therefore those historical
Releases must not be described as immutable production candidates; a new release is
blocked until the platform setting is enabled and the post-publish immutability
check succeeds.

The same review found seven required status checks and strict administrator
enforcement on `main`, but no required pull-request review object, no repository
rulesets, and no required reviewer on the `release` Environment. The repository
documents these as open platform gates rather than inferring them from workflow
presence. The current closure checklist is the authoritative Definition of Done;
staging evidence remains explicitly non-production.
