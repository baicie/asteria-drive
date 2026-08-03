# M2-1 身份与权限实现边界

## 目标

把 MVP 的 trusted-dev 身份映射升级为 OIDC/OAuth2 Bearer JWT Resource Server，并建立可审计的内部主体、
租户成员和固定角色权限模型。

本阶段不做登录页面、密码账户、邀请、自助注册、分享、配额、同步或管理员 Web 控制台。

## 请求流程

```text
Bearer JWT
    |
    v
OIDC discovery/JWKS verifier
    |
    v
(issuer, subject) -> principal.id
    |
    +-- X-Tenant-ID -> active tenant_member
    |
    v
Principal + tenant + role + permissions
    |
    v
HTTP authorization -> Service -> tenant-scoped Repository
```

## 配置

OIDC 模式至少需要：

- `ASTERIA_AUTH_MODE=oidc`
- `ASTERIA_OIDC_ISSUER`
- `ASTERIA_OIDC_CLIENT_ID`
- `ASTERIA_OIDC_BOOTSTRAP_JSON`
- production 还必须使用 PostgreSQL 元数据和 S3 对象存储（development 可用内存 adapter 做测试）

Bootstrap 是 JSON 数组，每项包含 `issuer`、`subject`、`principal_id`、`tenant_id`、`display_name` 和
`role`。启动时会创建或确认内部主体和成员关系；它不是动态用户注册接口。生产 Secret/配置系统负责
保护其中的租户拓扑信息和 provider 地址。

## 角色权限

| 角色 | tenant:read | files:read | files:write | files:delete |
| --- | ---: | ---: | ---: | ---: |
| owner | yes | yes | yes | yes |
| admin | yes | yes | yes | yes |
| editor | no | yes | yes | no |
| viewer | no | yes | no | no |

认证失败返回 `401 unauthenticated`；OIDC 缺失或非法租户选择器返回 `400 invalid_request`；认证成功但
成员关系无效或角色不足返回 `403 forbidden`。Repository 的 tenant 条件仍是最终数据隔离边界。

## 交付拆分

1. ADR、配置 schema 和 PostgreSQL `principal`/`tenant_member` 迁移。
2. 内存/PostgreSQL 主体解析与幂等 bootstrap 契约。
3. OIDC discovery/JWKS verifier 和认证上下文。
4. Server 路由权限中间件与生产启动保护。
5. API/OpenAPI、负向测试、真实 OIDC verifier 契约和实施日志。

## 后续入口

管理员成员管理、邀请/组同步、细粒度 ACL、审计事件和 provider logout 需要新的 ADR；不能通过扩大本阶段
bootstrap 或接受 JWT 自声明角色来临时实现。
