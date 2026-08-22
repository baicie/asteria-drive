# Asteria Drive 文档索引

本目录归档 Asteria Drive 的产品蓝图、MVP 范围、实施步骤、详细设计、API 契约、关键架构决策、
工程流程和可复现证据。设计目标与已验证事实分开记录，避免把计划误写为已交付能力。

| 文档 | 内容 | 权威性 |
| --- | --- | --- |
| [status.md](status.md) | 当前交付能力、进行中缺口与后续产品边界 | **当前交付状态摘要** |
| [vision/README.md](vision/README.md) | 产品蓝图入口：可从小规模扩到 PB | 长期愿景，非 MVP 完成声明 |
| [vision/architecture.md](vision/architecture.md) | 商业化总体架构与选型演进 | 愿景；不扩大本轮 MUST |
| [vision/roadmap.md](vision/roadmap.md) | 产品 Phase 1–4（区别于 MVP P0–P4） | 产品阶段规划 |
| [vision/anti-patterns.md](vision/anti-patterns.md) | 明确避免的设计 | 长期约束 |
| [process/branch-workflow.md](process/branch-workflow.md) | 里程碑分支、PR 门禁、ADR 触发条件 | 工程流程 |
| [mvp/README.md](mvp/README.md) | 已完成 MVP 的历史范围、路线、验收与实施记录 | 历史交付基线 |
| [m2/identity-access.md](m2/identity-access.md) | M2-1 OIDC、主体、成员、RBAC 边界和交付拆分 | M2-1 实现设计与证据入口 |
| [m2/member-management.md](m2/member-management.md) | M2-2 tenant member listing and lifecycle updates | M2-2 implementation contract |
| [operations/deployment.md](operations/deployment.md) | Hardened image, Kubernetes baseline, secret injection, and rollout | Production operations |
| [operations/backup-and-restore.md](operations/backup-and-restore.md) | Isolated backup, restore, verification, and drill procedure | Production recovery |
| [operations/production-readiness.md](operations/production-readiness.md) | Phase 1 生产门槛、owner、证据与验收标准 | **公网生产 Definition of Done** |
| [security/threat-model.md](security/threat-model.md) | Production assets, trust boundaries, threats, controls, and residual risk | Security review baseline |
| [security/control-matrix.md](security/control-matrix.md) | Security requirements mapped to repository and platform evidence | Security assurance |
| [process/ci-system.md](process/ci-system.md) | GitHub Actions CI topology, staged implementation, and merge gates | CI design and rollout evidence |
| [openapi.yaml](openapi.yaml) | 已注册 HTTP 路由的 OpenAPI 3.1 契约 | HTTP 机器可读契约 |
| [design/api.md](design/api.md) | HTTP 语义、状态机和示例说明 | HTTP 人读设计 |
| [design/architecture.md](design/architecture.md) | 模块、运行时、数据流与扩展边界 | 详细架构设计 |
| [design/data-model.md](design/data-model.md) | PostgreSQL 实体、约束和事务边界 | 数据设计 |
| [design/testing-and-operations.md](design/testing-and-operations.md) | 测试分层、故障注入和运维门槛 | 验证与运行设计 |
| [adr/README.md](adr/README.md) | Architecture Decision Records 索引 | 关键决策及取舍 |
| [spec.md](spec.md) | 初始服务骨架规格，已被 MVP 文档取代 | 历史归档 |

## 冲突裁决顺序

当文档发生冲突时，按以下顺序判断：

1. Accepted ADR 的安全/一致性约束
2. MVP scope（[`mvp/scope.md`](mvp/scope.md)）
3. OpenAPI / API 契约
4. MVP 详细设计（`design/`）
5. MVP 路线图
6. 产品愿景（`vision/`）——愿景不得覆盖本轮 MUST/NOT NOW

当前交付摘要见 [status.md](status.md)；实现进度和验收结论只以
[mvp/implementation-log.md](mvp/implementation-log.md) 中可复现的证据为准。Phase 1 仓库代码完成不等于公网生产准入；生产声明还必须满足
[operations/production-readiness.md](operations/production-readiness.md) 的候选绑定、平台证据和独立审批。
