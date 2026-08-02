# ADR-0007: MVP 身份认证与租户隔离边界

- 状态：Accepted
- 日期：2026-08-02
- 范围：MVP 认证、授权与多租户

> 实施注记（2026-08-02）：trusted-dev Token 映射、常量时间比较、租户作用域和 production 启动
> 防护已经进入候选实现；完整跨租户真实适配器测试与日志 Secret 扫描仍待验收。

## Context

网盘保存高价值内容。自定义密码/JWT 协议、仅依赖客户端传入租户 ID或遗漏查询过滤都会形成高影响越权风险。本轮 MVP 只用于隔离开发与验收环境，需要先收窄身份能力，再逐步增加生产 OIDC、复杂 ACL 和匿名协作。

本 ADR 起草时仓库没有完整身份或租户实现；以下内容是本轮垂直切片持续适用的安全决策。

## Decision

### 身份认证

MVP 使用服务端配置的高熵不透明 trusted-dev Bearer Token，将每个 Token 固定映射到一个 `tenant_id` 和 `principal_id`。客户端 Header、Query 或 Body 不能改变映射结果。Token 使用常量时间比较，不进入日志、错误或仓库；显式 production/public 模式必须拒绝 trusted-dev 启动。该模式只用于隔离开发与验收，不是生产认证方案。

M2 用标准 OIDC/OAuth 2.0 替换 trusted-dev：交互式客户端采用 Authorization Code + PKCE，API 校验 Access Token 的签名、`issuer`、`audience`、时间声明和 scope，内部身份以 `(issuer, subject)` 为稳定键。Core 不自创密码协议或签名算法。

### 租户隔离

trusted-dev 映射同时确定主体和租户。所有租户业务表、唯一约束、索引和关联都带 `tenant_id`；Repository 方法必须要求租户作用域，跨租户资源与不存在资源统一返回 `404`。M2 接入 OIDC 后，成员关系和正式权限仍从 PostgreSQL 校验，不能从 Token 自声明任意租户。

对象 Key 使用服务端生成的租户作用域和不透明 Blob ID。MVP 预签名 URL 只在租户、节点状态和版本均通过校验后生成；正式 ACL/角色校验随 M2 权限模型引入。MVP 以应用层强制过滤、复合约束和越权集成测试作为主要边界；PostgreSQL RLS 作为公开多租户上线前的纵深防御评审项，而不是替代应用授权。

### 分享边界

匿名分享不属于本轮 MVP。M2 增加分享时必须单独建模租户归属、固定资源/能力、到期和撤销；数据库只保存高熵 Token 摘要，可选密码使用内存困难 KDF。每次下载签名前重新校验状态并签发分钟级 URL，绝不暴露长期凭据或公开 Bucket。

## Consequences

- 本轮无需引入 IdP，垂直切片可以确定性测试；它不得对公网或生产部署。
- M2 后认证协议和凭证生命周期由成熟 IdP 承担，Core 仍负责租户成员关系和资源授权。
- 每个查询和关联都携带租户作用域，schema 与 Repository 代码更冗长，但越权边界可测试。
- 匿名分享、协作上传和复杂群组 ACL 被明确延期。

## Rejected alternatives

- **Core 自行保存密码并发放自定义 JWT**：扩大高风险认证代码和密钥运维面，本轮没有必要。
- **信任 `X-Tenant-ID` 或资源 ID 即代表权限**：攻击者可替换标识访问其他租户数据。
- **仅靠开发约定过滤租户且无复合约束/测试**：一次遗漏即可形成跨租户泄露。
- **把 trusted-dev 当生产认证**：没有用户生命周期、撤销协议和标准 IdP 安全能力。
- **永久预签名 URL 或公开 Bucket ACL**：权限撤销和泄露收敛不可控。
- **跨租户仅凭内容 Hash 复用 Blob**：可能泄露文件存在性并绕过占有证明。

## Follow-up or triggers

- 在实现前完成 trusted-dev 配置格式、生产模式防护、日志脱敏和错误响应规范。
- 为每个 Repository 和 API 增加缺失/错误 Token、伪造租户和跨租户测试。
- M2 OIDC 实现前完成 issuer/audience、密钥轮换、时钟偏差和成员映射威胁模型。
- 公开多租户上线前评审 PostgreSQL RLS、服务间身份、审计防篡改和签名 URL TTL。
- 企业 SSO、多 IdP、分享、匿名上传、外部协作者、即时下载撤销或法律保留出现时，分别提出后续 ADR。
