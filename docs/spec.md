# Asteria Drive Initialization Specification

> 状态：Superseded（历史归档）。本文只记录仓库最初的 HTTP 骨架目标，其中 SQLite 方向、目录结构和
> Open Questions 已不再代表当前决策。当前权威范围见 [MVP scope](mvp/scope.md)，架构决策见
> [ADR 索引](adr/README.md)，实现证据见 [implementation log](mvp/implementation-log.md)。

## Objective

建立 Asteria Drive 的可运行服务端基础，为后续文件管理、对象存储、用户认证和多端同步提供稳定入口。

本阶段成功标准：开发者可以启动服务，健康检查返回明确的 JSON 状态，并通过自动化测试验证该行为。

## Tech Stack

- Go 1.25.13+
- Go 标准库 HTTP 服务
- SQLite 仅作为后续本地开发存储方向，本阶段不引入驱动

## Commands

```text
go run ./cmd/asteria-server
go test ./...
go build ./...
go vet ./...
```

## Project Structure

```text
cmd/asteria-server/  服务启动入口
internal/server/     HTTP 服务与路由
internal/health/     健康检查领域逻辑预留目录
docs/                规范和架构设计
testdata/            测试数据预留目录
```

## Code Style

使用 Go 官方格式化工具，包名简短且小写，导出标识符使用 Go 文档约定。HTTP handler 优先返回明确的状态码和机器可读 JSON。

```go
func healthHandler(w http.ResponseWriter, _ *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
}
```

## Testing Strategy

本阶段使用标准库 `testing`，为健康检查接口提供 HTTP handler 测试。后续增加业务逻辑时优先补充小型单元测试，跨存储或 API 边界使用集成测试，关键用户流程再增加端到端测试。

## Boundaries

- Always: 运行 `gofmt`、`go test ./...` 和 `go vet ./...`；不提交密钥；接口输入需显式验证。
- Ask first: 引入第三方依赖、修改数据库 schema、增加认证协议、调整 CI 或部署配置。
- Never: 把真实用户数据、凭据或本地数据库文件提交到仓库；删除失败测试以绕过验证。

## Success Criteria

- 项目可以通过 `go test ./...`。
- 项目可以通过 `go build ./...`。
- `GET /healthz` 返回 HTTP 200、JSON content type 和 `status: ok`。
- 服务监听地址可通过 `ASTERIA_SERVER_ADDRESS` 配置。
- 仓库包含清晰的 README 和本规范。

## Open Questions

- 生产数据库使用 PostgreSQL 还是兼容 S3 的元数据与对象存储组合？
- 首版认证采用自托管账号、OIDC，还是两者并行？
- 是否先做 Web 管理界面，还是优先实现桌面同步客户端？
