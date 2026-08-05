# Asteria Drive

Asteria Drive 是一个面向个人网盘和企业文件平台的开源控制面。项目负责身份边界、租户 Namespace、
上传协调、下载授权、内部文件版本和回收站，不自行实现底层分布式对象存储。文件正文通过短期签名 URL
在客户端与 S3-compatible 数据面之间直接传输。

> 当前状态：后端 MVP 工程验收基线（`main`）上已实现 M2-1 和 M2-2。系统已包含 OIDC/OAuth2 Resource
> Server、内部主体、租户成员、基础 RBAC，以及成员角色/状态管理；`trusted-dev` 仍只允许 development，
> production 必须使用 OIDC + PostgreSQL + S3。生产部署仍需完成邀请、正式 ACL、审计和安全评审，
> 不能仅凭本阶段描述为完整生产就绪。

## MVP 能力

- 服务端配置的 Bearer Token 固定映射到租户和主体，不接受客户端切换租户。
- PostgreSQL 元数据适配器，以及无需外部依赖的确定性内存 fake。
- 目录创建、读取、稳定 Cursor 分页、重命名和移动。
- S3 Multipart 会话、分片签名、幂等完成和 S3-compatible 存储适配器。
- 已提交文件查询、短期下载授权、回收、恢复和永久清理。
- 统一 JSON 错误、请求 ID、health/readiness、版本化迁移和优雅停止。

M2-1 已加入 OIDC/OAuth2 Resource Server、内部主体、租户成员和基础 RBAC；M2-2 已加入成员列表、角色与
状态管理。邀请、正式 ACL、分享、配额、桌面同步、预览、搜索、Outbox 和独立 Worker 属于后续阶段。
完整范围与完成条件见 [MVP 文档](docs/mvp/README.md)。

## 本地启动

### 依赖

- Go 1.25.12 或更高版本
- Docker Engine 与 Docker Compose v2
- PowerShell 7 或 Windows PowerShell 5.1（以下命令使用 PowerShell）
- Node.js 24.16.0 与 npm 11（Actions schema 与 OpenAPI 契约校验需要）

### 1. 启动依赖

```powershell
docker compose up -d
docker compose ps
```

Compose 会启动 PostgreSQL `17.5` 和 SeaweedFS `3.85`，仅绑定本机端口。镜像分别固定为
`postgres:17.5-alpine@sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa`
和 `chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a`；
命名卷会保留本地数据。

### 2. 导入本地配置

[`.env.example`](.env.example) 中的值仅供隔离本机开发。下面的命令把它们导入当前 PowerShell
进程，不会修改用户级或系统级环境变量：

```powershell
Get-Content .env.example |
  Where-Object { $_ -match '^[A-Z0-9_]+=' } |
  ForEach-Object {
    $name, $value = $_ -split '=', 2
    Set-Item -Path "Env:$name" -Value $value
  }
```

不要在共享环境复用示例 Token、数据库密码、S3 凭据或 Cursor HMAC Key。

### 3. 迁移并启动服务

```powershell
go run ./cmd/asteria-migrate
go run ./cmd/asteria-server
```

服务默认监听 `http://127.0.0.1:8080`。`ASTERIA_AUTO_MIGRATE=true` 也可在服务启动时执行迁移，
但部署流程建议显式运行 `asteria-migrate`，并把服务实例的自动迁移关闭。

### 4. 验证控制面

另开一个 PowerShell 终端：

```powershell
Invoke-RestMethod http://127.0.0.1:8080/healthz

$headers = @{
  Authorization = 'Bearer local-development-token-change-me-000000000000'
}
$tenant = Invoke-RestMethod -Headers $headers http://127.0.0.1:8080/api/v1/tenant
$tenant.data
```

`root_directory_id` 是后续创建顶层目录时使用的 `parent_id`。完整 HTTP 契约见
[OpenAPI 3.1](docs/openapi.yaml) 和 [API 设计](docs/design/api.md)。

### 仅内存模式

开发 HTTP 层时可以把 `ASTERIA_METADATA_DRIVER` 与 `ASTERIA_STORAGE_DRIVER` 都设为 `memory`，
无需 Docker。内存模式会在重启后丢失数据，且不构成 PostgreSQL/S3 兼容性证据。

## 配置

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ASTERIA_ENV` | `development` | `development` 或 `production`；当前 `production` 会拒绝 trusted-dev |
| `ASTERIA_SERVER_ADDRESS` | `127.0.0.1:8080` | HTTP 监听地址；MVP 应只绑定回环或受控网络 |
| `ASTERIA_AUTH_MODE` | `trusted-dev` | `trusted-dev` 仅允许 development；production 必须使用 `oidc` |
| `ASTERIA_TRUSTED_TOKENS_JSON` | 无 | `trusted-dev` 时必填；Token 到 UUID tenant/principal 和租户显示名的 JSON 映射 |
| `ASTERIA_OIDC_ISSUER` | 无 | OIDC issuer URL；production 必须使用 HTTPS |
| `ASTERIA_OIDC_CLIENT_ID` | 无 | API access-token 的 audience/client ID |
| `ASTERIA_OIDC_BOOTSTRAP_JSON` | 无 | 预置 issuer/subject、内部 principal、tenant member 和 role 的 JSON 数组 |
| `ASTERIA_CURSOR_HMAC_KEY` | 无 | 必填；至少 32 bytes，不得复用示例值 |
| `ASTERIA_METADATA_DRIVER` | `memory` | `memory` 或 `postgres` |
| `ASTERIA_DATABASE_URL` | 无 | PostgreSQL DSN；使用 `postgres` 时必填 |
| `ASTERIA_AUTO_MIGRATE` | `false` | 服务启动时自动前进迁移；正式部署建议使用独立迁移步骤 |
| `ASTERIA_STORAGE_DRIVER` | `memory` | `memory` 或 `s3` |
| `ASTERIA_S3_ENDPOINT` | 无 | S3-compatible endpoint；使用 `s3` 时必填 |
| `ASTERIA_S3_REGION` | `us-east-1` | S3 region |
| `ASTERIA_S3_BUCKET` | `asteria-drive` | 私有对象 Bucket |
| `ASTERIA_S3_ACCESS_KEY_ID` | 无 | S3 Access Key，通过 Secret 注入 |
| `ASTERIA_S3_SECRET_ACCESS_KEY` | 无 | S3 Secret Key，通过 Secret 注入 |
| `ASTERIA_S3_PATH_STYLE` | `true` | 是否使用 path-style addressing |
| `ASTERIA_S3_AUTO_CREATE_BUCKET` | `false` | 仅建议本地开发启用 |
| `ASTERIA_S3_CHECKSUM_HEADERS` | `false` | 后端明确兼容 S3 checksum request headers 时启用；SeaweedFS 3.85 保持关闭 |
| `ASTERIA_MAX_FILE_SIZE` | `53687091200` | 单文件上限，bytes；默认 50 GiB |
| `ASTERIA_PART_SIZE` | `8388608` | 建议分片大小，bytes；必须在 5 MiB 至 5 GiB 之间 |
| `ASTERIA_UPLOAD_TTL` | `24h` | 上传会话有效期 |
| `ASTERIA_UPLOAD_SIGN_TTL` | `15m` | 分片签名有效期，最大 1 小时 |
| `ASTERIA_DOWNLOAD_SIGN_TTL` | `15m` | 下载签名有效期，最大 1 小时 |
| `ASTERIA_READ_HEADER_TIMEOUT` | `5s` | HTTP Header 读取超时 |
| `ASTERIA_READ_TIMEOUT` | `15s` | HTTP 请求读取超时 |
| `ASTERIA_WRITE_TIMEOUT` | `30s` | HTTP 响应写入超时 |
| `ASTERIA_IDLE_TIMEOUT` | `60s` | HTTP keep-alive 空闲超时 |

持续时间使用 Go duration 格式，例如 `30s`、`15m`、`24h`。JSON 请求体上限为 1 MiB；目录列表
默认 50 项、最多 200 项；Multipart 最多 10,000 个分片。

## 迁移

迁移命令只读取 `ASTERIA_DATABASE_URL`：

```powershell
go run ./cmd/asteria-migrate
```

迁移是前进式的；当前仓库不提供自动向下回滚。升级前应先备份 PostgreSQL，并在与目标版本相同的
候选环境执行迁移和重启读取验证。迁移实现与 Schema 位于 `internal/postgres`。

## 验证

```powershell
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
npm ci --ignore-scripts
npm run lint:actions
npm run lint:openapi
go test ./internal/server -run '^TestOpenAPIOperationsMatchRegisteredRoutes$' -count=1
```

Linux race 也已在 Debian 13 容器中用 Go 1.24.4 + GCC 验证；完整结果、真实依赖地址和 OpenAPI lint
见 [P4 证据页](docs/mvp/evidence/p4-live-baseline.md)。GitHub Actions 使用固定版本的工具和 Action 提交，
已实现 `CI / quality`、`CI / race`、`CI / api-contract` 和 `CI / integration` 四项无 GitHub Secrets
检查，详见 [CI 系统设计](docs/process/ci-system.md)。其中集成检查以固定 digest 的 PostgreSQL 与
SeaweedFS 运行 17 个必需测试（14 个 PostgreSQL、1 个 SeaweedFS、2 个 live HTTP），结构化验证每个
package 和测试均为 `pass`，任意 `skip` 都会失败；`always()` 路径会脱敏证据、保留 7 天并清理 Compose
资源。Rollout step 5 已完成，四项检查均为受保护 `main` 分支的 required gates。

安全检查 `Security / govulncheck`、`Security / dependency-review` 和 `Security / codeql` 已加入并同样
设为 required gates；每周 Dependabot 更新覆盖 Go、npm、GitHub Actions 和 Compose/Docker 依赖。

不带外部测试环境变量时，快速测试使用内存 fake。真实 PostgreSQL Repository、SeaweedFS Multipart、
签名下载和 Range 使用显式环境门禁：

```powershell
$env:ASTERIA_TEST_DATABASE_URL = $env:ASTERIA_DATABASE_URL
$env:ASTERIA_TEST_S3_ENDPOINT = $env:ASTERIA_S3_ENDPOINT
$env:ASTERIA_TEST_S3_ACCESS_KEY = $env:ASTERIA_S3_ACCESS_KEY_ID
$env:ASTERIA_TEST_S3_SECRET_KEY = $env:ASTERIA_S3_SECRET_ACCESS_KEY
$env:ASTERIA_TEST_S3_REGION = $env:ASTERIA_S3_REGION
$env:ASTERIA_TEST_S3_BUCKET = 'asteria-drive-contract'

go test -v -p=1 -count=1 -timeout=15m ./internal/postgres ./internal/s3store ./internal/server
```

PostgreSQL 测试会在该连接指向的数据库内创建并清理隔离 Schema；S3 测试会创建并清理专用对象。
只可对隔离测试依赖运行。未设置门禁变量时相应集成测试会跳过，不构成真实适配器验收证据。当前证据
和剩余门槛见 [实施与验收记录](docs/mvp/implementation-log.md)。

## 文档

- [文档索引](docs/README.md)
- [MVP 范围、路线与验收](docs/mvp/README.md)
- [架构与详细设计](docs/design/architecture.md)
- [架构决策记录](docs/adr/README.md)
- [OpenAPI 3.1 契约](docs/openapi.yaml)

## 仓库布局

```text
cmd/asteria-server/   HTTP 服务入口
cmd/asteria-migrate/  显式数据库迁移入口
internal/drive/       领域类型、应用服务与 ports
internal/server/      HTTP 路由与协议适配
internal/postgres/    PostgreSQL Repository 与迁移
internal/s3store/     S3-compatible StorageProvider
internal/memory/      确定性开发/测试 fake
docs/mvp/             范围、路线、验收与实施记录
docs/design/          详细设计
docs/adr/             Architecture Decision Records
```

品牌名为 **Asteria**，产品名为 **Asteria Drive**。当前服务端命令为 `asteria-server`；后续同步引擎
和客户端在进入相应产品阶段后再分别使用 `asteria-sync` 与 `asteria-desktop` 命名。
