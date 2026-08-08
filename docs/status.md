# Asteria Drive 当前交付状态

> 最后核对：2026-08-08。本页是当前交付范围和状态的权威摘要；完成声明仍须由
> [实施与验收记录](mvp/implementation-log.md) 中的代码、命令或远端设置证据支持。
> MVP 文档保留其历史范围和验收语义，产品 Phase 2-4 另立目标。

| 交付面 | 当前状态 | 已有证据与剩余边界 |
| --- | --- | --- |
| 后端 MVP P0-P4 | 已完成并合入 `main` | Namespace、直传上传、下载、回收、PostgreSQL/SeaweedFS 真实依赖验收 |
| M2-1 身份与基础权限 | 已完成并合入 `main` | OIDC Resource Server、内部主体、租户成员和 owner/admin/editor/viewer 基础 RBAC |
| M2-2 成员生命周期 | 已完成并合入 `main` | 成员列表、角色/状态更新、最后活动 owner 保护；不含邀请和成员删除 |
| CI、安全门禁与分支保护 | 已启用并完成远端复核 | 七项 required checks、CODEOWNERS、Dependabot、secret scanning 和 push protection；PR #30 的七项保护检查与额外 CodeQL 全部通过 |
| Release 与 API 加固 | 已实现并合入 `main`（PR #21/#22），`v0.1.0` 已发布 | 多架构归档、checksums、SBOM、OIDC provenance、OpenAPI compatibility 和代码所有权；Release 资产与 OCI provenance 已验证 |
| `v0.1.0` staging 部署 | 已通过 GitHub Actions 部署并验证 | 受保护 `main`、固定 OCI digest、server-managed secrets、迁移、鉴权读写、metrics、storage verifier、loopback bindings 和容量门禁均有 run `31260549517` attempt 2 的 artifact 证据；边界为 `staging-not-production` |
| 可靠性与可观测性 | 代码与仓库证据已完成 | `Idempotency-Key`、自动维护、Prometheus、fuzz/property、百万节点完整性和 10 分钟控制面 SLO 证据已归档；写路径与平台告警仍需独立测量 |
| Phase 1 生产化 | 代码、迁移、Release 与 staging 平台证据已完成；公网生产门待闭环 | 邀请、成员删除、正式 ACL、审计、Secret 读取、备份恢复演练、部署加固、`v0.1.0` provenance 和 staging 部署均有证据；生产 PITR、对象不可变性、TLS/HA/监控、密钥托管与独立审批仍为硬门槛 |
| Phase 2-4 愿景 | 不在当前交付目标 | 同步、预览、搜索、分块、多地域等另立产品目标 |

在 HTTPS OIDC/IdP 生命周期、PostgreSQL `verify-full` 与 PITR/WAL、对象锁/版本控制、
KMS 或 secret-controller 轮换、公网 TLS ingress、监控/SIEM、主机级 HA、外部 presign
端点、长期容量告警及独立安全/平台审批完成前，项目不得描述为公网生产就绪。

## 最近一次复核

2026-08-08，容量防护 PR
[#29](https://github.com/baicie/asteria-drive/pull/29) 与默认 Docker volume options 修复 PR
[#30](https://github.com/baicie/asteria-drive/pull/30) 已合入 `main`；PR #30 的
`CI / quality`、`CI / race`、`CI / api-contract`、`CI / integration`、
`Security / govulncheck`、`Security / dependency-review`、`Security / codeql` 和额外
CodeQL 均通过。部署工作流
[31260549517](https://github.com/baicie/asteria-drive/actions/runs/31260549517) attempt 2
从合并提交 `ddc9f126945a97fb152b8a805929abca18477654` 成功部署固定的 `v0.1.0`
镜像。artifact `9022865120` 的 digest 为
`sha256:d1cc889fd4386594d1d26d728bdb868ed107b597f8a6db091913f15f1055bf03`；
证据显示根盘、Docker 数据盘与两个数据卷均为 62% 使用率、约 14.3 GiB 可用，
所有迁移、健康、鉴权、上传下载、metrics、存储校验与 loopback 检查均通过。

`v0.1.0` 的注释标签指向 `8878d9eaaf88973c522a4f4742ea960acd63d503`。Release
工作流 [31015266832](https://github.com/baicie/asteria-drive/actions/runs/31015266832)
成功生成并上传两个 Linux 架构归档、SPDX SBOM、manifest 和 checksums；GHCR
镜像 `ghcr.io/baicie/asteria-drive:v0.1.0` 的多架构 manifest digest 为
`sha256:f5da244cba2055764a8caae7b9e9a752cc8f07356c0d7ae6397a6a7992e0cccc`，
release 文件和 OCI 镜像均已完成 OIDC provenance attestation。
