# 本地 Web 控制台与全栈冒烟证据

日期：2026-09-16

范围：`compose.local.yaml`、`web/`、`scripts/smoke-local.ps1`。本证据只证明隔离本机开发环境，
不构成 staging 或生产准入证据。

## 环境

- Docker Engine `29.5.3`
- PostgreSQL `17.5-alpine`，镜像 digest
  `sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa`
- SeaweedFS `3.85`，镜像 digest
  `sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a`
- Node.js `24.16.0`、Vite `8.2.2`、Vue `3.5`
- API 只使用 development 模式的 trusted-dev Token 和回环端口

## 命令与结果

全栈启动：

```powershell
docker compose -f compose.local.yaml up -d --build --wait --wait-timeout 240
```

结果：PostgreSQL、SeaweedFS、迁移和 API 均健康，Web 通过
`http://127.0.0.1:18080/` 提供控制台和 API 反向代理。

真实 PostgreSQL/S3 冒烟：

```powershell
.\scripts\smoke-local.ps1
```

结果摘要：

```json
{"base_url":"http://127.0.0.1:18080","tenant":"Local Asteria","folder_created":true,"upload_status":"committed","bytes_match":true,"listing_match":true,"part_count":2,"recycled":true,"purged":true}
```

该脚本验证：

1. `/healthz` 与 `/readyz` 返回 ready；
2. 创建租户目录；
3. 创建真实 S3 multipart 会话；
4. 使用服务端 `part_size` 上传两个分片并完成提交；
5. 下载授权返回的对象字节与上传字节完全一致；
6. 已提交文件出现在父目录列表；
7. 文件树进入回收站并可永久清理。

Web 验证：

```powershell
Push-Location web
npm run typecheck
npm run build
$env:PLAYWRIGHT_CHANNEL = "msedge"
npm run test:e2e
Pop-Location
```

结果：

- `vue-tsc --noEmit` 通过；
- Vite production build 通过；
- Playwright `1 passed`，覆盖自定义元素注册、Token 登录、目录渲染、新建目录和
  浏览器端 multipart 上传；
- 桌面 `1440x900` 和移动 `390x844` 截图均确认登录页和文件空间无重叠或空白渲染。

仓库级复核：

```powershell
$env:GOPROXY = "https://goproxy.cn,direct"
go test ./...
go vet ./...
go build ./...
npm run lint:actions
git diff --check
```

结果：全部通过。`go test ./...` 使用隔离的 memory、PostgreSQL 和 S3 契约测试；
本证据中的 `scripts/smoke-local.ps1` 另外验证真实 Compose 依赖上的 HTTP 与对象字节路径。

## 修复项

- 修正 `@zeus-web/ui` 聚合入口不注册组件的问题，改为显式加载匹配版本的
  `@zeus-web/button/wc/auto` 与 `@zeus-web/input/wc/auto`。
- 将 `@zeus-js/zeus` 固定到与组件包匹配的 `0.1.0-beta.8`，消除属性模型冲突。
- 将网页上传从单分片请求改为按 `part_size` 的完整 multipart 流程，并在失败时执行
  idempotent abort。
- 调整刷新与成功提示顺序，避免创建目录或上传成功提示被刷新立即清除。

## 未覆盖

- 未验证公网 DNS、TLS、CORS 或外部 presign 客户端。
- 未验证生产 OIDC、生产 PostgreSQL PITR/WAL、对象锁、KMS 或 HA。
- Playwright 使用确定性 API fixture 验证浏览器交互；真实 PostgreSQL/S3 字节路径由
  `scripts/smoke-local.ps1` 单独验证。

## 2026-10-22 复核

- Playwright 改为通过 `PLAYWRIGHT_PORT` 选择端口，并使用同源 mock signed URL；
  在 `43173` 端口重跑 `1 passed`，不再依赖或复用占用 `4173` 的其他本地服务。
- `go test ./...`、`go vet ./...`、`go build ./...`、`npm run typecheck`、`npm run build`、
  `npm run lint:actions`、`npm run lint:openapi`、`docker compose -f compose.local.yaml
  config --quiet` 和 `git diff --check` 全部通过。
- 同时按 ADR-0025 将 Go 最低/当前工具链提升到 1.26.10/1.27.3，升级
  `golang.org/x/text` 到 v0.41.0，并将 `source-map-js` 固定到 1.2.2；Go 1.26.10
  上的 `govulncheck@v1.9.0` 结果为 `No vulnerabilities found`。
- 本次复核只覆盖仓库门禁与浏览器流程，不替代 2026-09-16 的真实 PostgreSQL/SeaweedFS
  冒烟，也不改变 staging 或生产准入边界。
- 同日 PR #74 已合入 `main`（merge commit `ef60f9e`），新增回收站列表/恢复/永久删除与重命名/移动；`npm run typecheck`、
  `npm run build` 和扩展后的 Playwright 流程均通过（`1 passed`）。
