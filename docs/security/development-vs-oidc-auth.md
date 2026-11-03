# 开发 Token 登录与生产 OIDC 登录边界

> 适用范围：`web/` 控制台登录入口、`ASTERIA_AUTH_MODE` 的两种取值，以及两者之间的
> 迁移路径。最后核对：2026-11-02。

## 1. 结论

`trusted-dev` 与 `oidc` 不是同一能力的两个开关，而是两个不同威胁模型下的认证方式：

| | `trusted-dev`（当前 Web 控制台唯一可用） | `oidc`（生产目标） |
| --- | --- | --- |
| 凭据形式 | 运维预先写入 `ASTERIA_TRUSTED_TOKENS_JSON` 的静态不透明串 | IdP 签发的 OAuth2 access token（JWT） |
| 主体来源 | Token 到 `tenant_id`/`principal_id` 的静态映射 | issuer + audience + 签名 + 时间声明校验后解析主体 |
| 生命周期 | 无过期、无轮换、无撤销语义 | 由 IdP 决定，可短期化 |
| 允许环境 | 仅 `ASTERIA_ENV=development`；`production` 启动即拒绝 | 允许 `production`，强制 HTTPS issuer |
| 浏览器持有 | 用户手工粘贴，存 `localStorage` | 目标为登录跳转后的短期 token，不落长期存储 |
| 多用户 | 每个 token 手写映射，无登录 UI | OIDC 登录流程 + 租户成员解析 |

`internal/config` 的校验是硬边界：`ASTERIA_ENV=production` 且 `ASTERIA_AUTH_MODE=trusted-dev`
会在监听端口前失败，不存在“生产环境悄悄用开发 Token”的路径。

## 2. 当前 Web 控制台的定位

`web/` 是**本地开发控制台**，不是生产客户端：

- 登录卡片只有“API Token”输入框，没有 OIDC 跳转、回调、刷新或登出流程；
- token 存放于 `localStorage['asteria.token']`，刷新后自动连接；
- 页面直接展示本地默认开发 Token（`local-development-token-change-me-…`），帮助首次启动；
- 退出登录只清除本地状态，不通知 IdP。

因此，**发布 Web 静态包不等于发布生产登录体验**。`v0.1.1` / `v0.2.0` 的 Web 包在这
一点上与仓库当前代码一致：它能对接 `oidc` 模式的后端（只要手工提供有效 access token），
但不提供 OIDC 登录流程。

## 3. 边界规则（必须保持）

1. **`trusted-dev` 不得出现在任何生产启动路径**：不得通过环境变量覆盖、Compose 默认值或
   部署脚本回退到 `trusted-dev` + `memory` 存储。ADR-0014 已记录该约束。
2. **不得为生产编写开发 Token 引导**：README/运维文档中的默认 Token 只用于
   `compose.local.yaml` 与本地开发。
3. **不得把开发 Token 写进 Web 包**：`local-development-token-change-me-…` 只作为
   登录页提示文本存在；构建产物不得内置可用凭据。
4. **不得以 Cookie 会话替代 OIDC**：控制面是 OAuth2 Resource Server，凭据通过
   `Authorization: Bearer` 传递；引入浏览器 Cookie 会话需要新的 CSRF 与同源策略 ADR。
5. **不得记录 token**：`apiResponse` 只在请求头注入，日志与错误信息不得包含 token；
   Web 端不得把 token 写入 URL、`sessionStorage` 之外的持久化位置或遥测。

## 4. 迁移到生产 OIDC 需要补齐的工作

以下均为**未完成**项，不能在交付说明中描述为已具备：

1. Web 端 OIDC Authorization Code + PKCE 登录、回调处理、静默续期与登出。
2. 生产构建配置注入 issuer、client id、scope 与回调地址；不得硬编码。
3. token 存储策略（内存优先，刷新页面要求重新登录，或短期 refresh 机制）与
   `sessionStorage` 泄露面的评审。
4. 多租户切换：OIDC 主体可能属于多个 tenant，控制台需要租户选择器与
   `TenantSelector` 头的显式传递。
5. Playwright 需要覆盖 OIDC 流程（可用 mock IdP），当前所有 E2E 都基于开发 Token。
6. `docs/openapi.yaml` 的 `bearerFormat: opaque trusted-development token` 需要为
   OIDC 增加说明，或拆分为两个 security scheme 描述。

## 5. 验证方式

- `internal/config` 单测覆盖 `production + trusted-dev` 拒绝启动（见
  `internal/config/config_test.go`）。
- CI 的 `go test ./...` 与 `CI / integration` 覆盖配置校验路径。
- Web 侧当前只有开发 Token 登录的 Playwright 覆盖；OIDC 覆盖缺失是已知风险，
  见交付说明的“后续风险”。
