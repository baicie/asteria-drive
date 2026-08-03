# ADR-0014: OIDC Resource Server 与租户 RBAC 边界

- 状态：Accepted
- 日期：2026-08-03
- 范围：M2-1 API 身份验证、主体映射、租户成员和基础授权
- 修订：ADR-0007 的 trusted-dev 阶段性身份边界

## Context

MVP 的 trusted-dev Bearer token 只适合隔离开发和验收环境。它把租户与主体直接写在进程配置中，无法
表达真实用户、同一主体加入多个租户、成员停用或角色权限，也不能作为公网服务的身份边界。

M2 需要先建立稳定的生产身份入口，同时保留控制面与数据面的边界。API 不应自己保存用户密码，也不应
把 OIDC provider 的外部 subject 直接当作 PostgreSQL 主键或业务租户 ID。

## Decision

1. Asteria API 在 M2-1 作为 OIDC Resource Server：客户端自行从 OIDC/OAuth2 provider 获取 Bearer JWT，
   API 通过 provider discovery/JWKS 验证 issuer、签名、audience、有效期和非空 `sub`，并允许最多 30 秒
   的 `nbf` 时钟偏差。本阶段不实现
   浏览器登录回调、密码登录或 refresh token 存储。
2. 外部身份由 `(issuer, subject)` 唯一映射到内部 `principal.id`。`principal` 是稳定的内部主体，OIDC
   subject 变化或 provider 切换不会直接改变业务外键。
3. 一个主体可以属于多个租户。OIDC 请求必须携带 `X-Tenant-ID`，服务端只在该租户存在 active
   `tenant_member` 关系时创建身份上下文；缺少或格式错误的选择器返回 `400`，未加入该租户返回 `403`。
4. M2-1 角色固定为 `owner`、`admin`、`editor`、`viewer`，权限集合为：
   - `owner`：全部权限；
   - `admin`：租户读取、文件读取/写入/删除；
   - `editor`：文件读取/写入；
   - `viewer`：文件读取。
   HTTP 路由在认证后执行权限中间件；Repository 仍强制 tenant 过滤，授权不能替代数据边界。
5. `tenant_member.status` 只有 `active` 和 `suspended`。suspended 成员与不存在成员对外统一为 `403`，
   不泄露租户成员关系。角色和成员状态由元数据真相源读取，不信任 JWT 自声明的租户或角色。
6. 首个 M2-1 交付使用一次性 `ASTERIA_OIDC_BOOTSTRAP_JSON` 预置主体和成员关系，启动时以幂等事务写入
   PostgreSQL。不会根据首次登录自动注册，也不提供无保护的自助提权 API；后续管理员 API 另行设计。
7. trusted-dev 继续只允许 `ASTERIA_ENV=development`，并为配置主体赋予 owner 权限。production 必须
   使用 `ASTERIA_AUTH_MODE=oidc`、PostgreSQL 和 S3；任何生产启动路径不得回退到 trusted-dev 或 memory。
8. OIDC 验证失败记录结构化错误码但不记录原始 token、JWT 内容或 provider 返回的敏感响应。OIDC discovery
   或 JWKS 依赖不可用时启动失败或 readiness 失败，不能降级为未验证 token。

## Consequences

- API 可以安全承接真实 provider 签发的 token，并支持一个主体选择多个租户。
- 部署需要先完成 provider 配置和成员 bootstrap；没有 bootstrap 的主体不能访问业务数据。
- 角色集合暂时是固定代码契约，细粒度 ACL、组同步、邀请和管理员 API 延后到后续 M2 ADR。
- OIDC provider 可用性成为认证依赖；verifier 对 JWKS 做短期缓存，密钥轮换仍需监控。
- trusted-dev 测试路径保持快速，不把 OIDC provider 变成单元测试依赖；真实 verifier 通过独立契约测试验证。

## Rejected alternatives

- **API 自己实现密码登录**：引入密码存储、重置、MFA 和安全审计范围，偏离 M2-1 的最小生产身份边界。
- **把 OIDC subject 直接作为 principal UUID**：subject 不是 UUID，且 provider 迁移会破坏业务外键。
- **从 JWT claims 直接接受 tenant/role**：调用方可伪造业务授权语义，绕过成员撤销和租户隔离。
- **首次登录自动创建成员**：无法建立可信的邀请/管理员批准边界，容易造成租户越权。
- **用 Redis 或内存缓存作为成员真相源**：失效会造成授权漂移；PostgreSQL 才是 MVP/M2 元数据真相源。

## Verification

- OIDC verifier 单元测试覆盖 discovery claims、issuer/audience/过期失败、空 subject 和 provider 依赖错误。
- PostgreSQL/内存契约测试覆盖主体映射、多个租户选择、suspended 成员、角色权限和幂等 bootstrap。
- HTTP 测试覆盖未认证、缺失/非法 `X-Tenant-ID`、未加入租户、viewer 写入拒绝和 admin/editor 正常路径。
- production 配置测试证明 trusted-dev、memory metadata 和 memory storage 均不能启动。
