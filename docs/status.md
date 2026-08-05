# Asteria Drive 当前交付状态

> 最后核对：2026-08-05。本页是当前交付范围和状态的权威摘要；完成声明仍须由
> [实施与验收记录](mvp/implementation-log.md) 中的代码、命令或远端设置证据支持。
> MVP 文档保留其历史范围和验收语义，产品 Phase 2-4 另立目标。

| 交付面 | 当前状态 | 已有证据与剩余边界 |
| --- | --- | --- |
| 后端 MVP P0-P4 | 已完成并合入 `main` | Namespace、直传上传、下载、回收、PostgreSQL/SeaweedFS 真实依赖验收 |
| M2-1 身份与基础权限 | 已完成并合入 `main` | OIDC Resource Server、内部主体、租户成员和 owner/admin/editor/viewer 基础 RBAC |
| M2-2 成员生命周期 | 已完成并合入 `main` | 成员列表、角色/状态更新、最后活动 owner 保护；不含邀请和成员删除 |
| CI、安全门禁与分支保护 | 已启用 | 七项 required checks、CODEOWNERS、Dependabot、secret scanning 和 push protection |
| Release 与 API 加固 | 已实现，综合交付 PR 待闭环 | 多架构归档、checksums、SBOM、OIDC provenance、OpenAPI compatibility 和代码所有权；候选提交与远端 checks 仍待产生 |
| 可靠性与可观测性 | 代码与仓库证据已完成 | `Idempotency-Key`、自动维护、Prometheus、fuzz/property、百万节点完整性和 10 分钟控制面 SLO 证据已归档；写路径与平台告警仍需独立测量 |
| Phase 1 生产化 | 代码、迁移和本地验证已完成；外部发布门待闭环 | 邀请、成员删除、正式 ACL、审计、Secret 读取、备份恢复演练和部署加固均有仓库证据；生产 PITR/对象不可变性、平台证据、独立安全评审和维护者审批仍为硬门槛 |
| Phase 2-4 愿景 | 不在当前交付目标 | 同步、预览、搜索、分块、多地域等另立产品目标 |

在生产 PITR/对象不可变性、候选 OCI digest、托管 CI 结果和两名授权评审人完成独立签核前，项目不得描述为公网生产就绪。
