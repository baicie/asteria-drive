# P4 真实依赖与性能基线证据

> 日期：2026-08-03
> 分支：`mvp/p4-integration`  
> 不包含 Secret、Token 或签名 URL。

## 环境

| 组件 | 版本 / 配置 |
| --- | --- |
| OS | Windows 10 (build 26200) |
| Go | 1.23.12 windows/amd64（便携安装） |
| PostgreSQL | 17.5-alpine（Compose，端口 15432，healthy） |
| SeaweedFS | 3.85 S3 Gateway（Compose，端口 18333，healthy） |
| 测试隔离 | PostgreSQL 临时 Schema；S3 专用测试 Bucket |

## 命令

```powershell
$env:ASTERIA_TEST_DATABASE_URL = 'postgres://asteria:***@127.0.0.1:15432/asteria?sslmode=disable'
$env:ASTERIA_TEST_S3_ENDPOINT = 'http://127.0.0.1:18333'
$env:ASTERIA_TEST_S3_ACCESS_KEY = '***'
$env:ASTERIA_TEST_S3_SECRET_KEY = '***'
$env:ASTERIA_TEST_S3_REGION = 'us-east-1'
$env:ASTERIA_TEST_S3_BUCKET = 'asteria-http-e2e'
go test -count=1 ./... -timeout 10m
go vet ./...
go build ./...
```

另执行 Linux race 门禁（Debian 13 容器临时安装 Go 1.24.4 + GCC）：
`docker run --rm -v "${PWD}:/src" -w /src infra-aiops-agent:latest sh -lc
'apt-get update -qq && apt-get install -y -qq golang-go gcc && go test -race ./...'`（通过）。

## 结果摘要

| 测试 | 结果 |
| --- | --- |
| `TestMigrateIsIdempotent` | PASS |
| `TestRepositoryNamespaceAndUploadContract` | PASS |
| `TestRepositoryCommitRollbackAndPurgeContract` | PASS |
| `TestRepositoryNestedRecycleRootsRemainIndependent` | PASS |
| `TestRepositoryFailUploadCompletionContract`（memory + PostgreSQL） | PASS |
| `TestRepositoryConcurrentNormalizedNameCreateHasSingleWinner`（memory + PostgreSQL） | PASS |
| `TestRepositoryDirectorySubtreeRecycleRestoreContract`（含子目录、文件、下载） | PASS |
| `TestRepositoryRestoreChildConflictsWhileOriginalParentIsTrashed` | PASS |
| `TestProviderSeaweedFSContract`（含 Range） | PASS |
| `TestLiveHTTPPostgresSeaweedFSEndToEnd`（上传/下载/重启/回收/恢复/清理/跨租户） | PASS |
| `TestLiveHTTPConcurrentUploadSessionBaseline`（100 会话） | PASS |
| `TestHealthStaysUpWhenDependenciesFail` | PASS |
| `TestGracefulShutdownStopsListener` + `TestGracefulShutdownDrainsInFlightRequest` | PASS |
| `TestControlPlaneLatencySmokeBaseline`（内存烟雾） | PASS |
| `TestStructuredRequestLogIsCorrelatedAndRedacted` | PASS |
| `TestLoadRejectsUnsafeOrIncompleteConfiguration`（含 production + trusted-dev） | PASS |

### 100 并发会话（AC-081）

```text
100-session create baseline: p50=101.7472ms p95=122.3139ms p99=123.5448ms max=123.857ms
```

无重复 upload ID；未提交文件未进入 Namespace；跨租户查询返回 404。

### 控制面烟雾（ADR-0010 MUST）

```text
healthz p50=0s p95=0s max=580.5µs
get_tenant p50=0s p95=0s max=660.9µs
list_children p50=0s p95=0s max=563.4µs
sign_part p50=0s p95=566.3µs max=572.8µs
```

### 未执行（SHOULD，见 ADR-0010）

- Namespace 100 万活动节点装载
- 持续 ≥10 分钟多端点负载与可用性采样
