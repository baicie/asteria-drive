# P4 真实依赖与性能基线证据

> 日期：2026-08-02  
> 分支：`mvp/p4-integration`  
> 不包含 Secret、Token 或签名 URL。

## 环境

| 组件 | 版本 / 配置 |
| --- | --- |
| OS | Windows 10 (build 26200) |
| Go | 1.23.6 windows/amd64（便携安装） |
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
go test -v -count=1 ./internal/postgres ./internal/s3store ./internal/server -timeout 10m
```

另在无门禁变量时执行：`go test ./...`、`go vet ./...`、`go build ./...`（全部退出码 0）。

## 结果摘要

| 测试 | 结果 |
| --- | --- |
| `TestMigrateIsIdempotent` | PASS |
| `TestRepositoryNamespaceAndUploadContract` | PASS |
| `TestRepositoryCommitRollbackAndPurgeContract` | PASS |
| `TestRepositoryNestedRecycleRootsRemainIndependent` | PASS |
| `TestProviderSeaweedFSContract`（含 Range） | PASS |
| `TestLiveHTTPPostgresSeaweedFSEndToEnd`（上传/下载/重启/回收/恢复/清理/跨租户） | PASS |
| `TestLiveHTTPConcurrentUploadSessionBaseline`（100 会话） | PASS |
| `TestHealthStaysUpWhenDependenciesFail` | PASS |
| `TestGracefulShutdownStopsListener` | PASS |
| `TestControlPlaneLatencySmokeBaseline`（内存烟雾） | PASS |
| `TestStructuredRequestLogIsCorrelatedAndRedacted` | PASS |
| `TestLoadRejectsUnsafeOrIncompleteConfiguration`（含 production + trusted-dev） | PASS |

### 100 并发会话（AC-081）

```text
100-session create baseline: p50=223.5222ms p95=257.5409ms p99=264.4222ms max=264.4222ms
```

无重复 upload ID；未提交文件未进入 Namespace；跨租户查询返回 404。

### 控制面烟雾（ADR-0010 MUST）

```text
healthz p50=0s p95=0s max=520.2µs
get_tenant p50=0s p95=0s max=526µs
list_children p50=0s p95=0s max=545.4µs
sign_part p50=0s p95=538.4µs max=693.2µs
```

### 未执行（SHOULD，见 ADR-0010）

- Namespace 100 万活动节点装载
- 持续 ≥10 分钟多端点负载与可用性采样
