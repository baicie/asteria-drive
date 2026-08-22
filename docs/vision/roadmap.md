# 产品实施路线（Phase 1-4）

> 状态：产品阶段规划，最后核对 2026-08-19。Phase 编号不等于后端 MVP 的 P0-P4。
> 当前事实以 [交付状态](../status.md) 为准，生产准入以
> [Phase 1 生产收口清单](../operations/production-readiness.md) 为准。

## 编号与完成语义

| 编号 | 含义 | 当前判断 |
| --- | --- | --- |
| MVP P0-P4 | 后端 Namespace、上传下载、回收站和真实 PostgreSQL/S3 验收 | 已完成的历史交付基线 |
| M2-1/M2-2 | OIDC、内部主体、成员解析、RBAC 和成员生命周期 | 仓库代码已完成 |
| Phase 1 仓库范围 | MVP 加邀请、成员删除、组、正式 ACL、审计、可靠性、部署与恢复工具 | 仓库代码、迁移和契约已完成 |
| Phase 1 生产准入 | 把同一候选发布到公网生产环境所需的平台控制和独立批准 | 未完成；硬门槛保持开放 |
| Phase 2-4 | 同步、预览、CDC、企业合规和多地域等后续产品能力 | 尚未启动 |

“Phase 1 仓库代码已完成”只表示本仓库内的实现与可复现验证已经交付，不表示公网生产就绪，也不表示完整终端产品已经交付。生产准入必须针对一个不可变候选和一个明确环境逐项验收，不能由 staging 证据或配置样例推导。

## 当前检查点

### 已完成：Phase 1 仓库交付

- OIDC Resource Server、内部 principal、租户成员和 owner/admin/editor/viewer RBAC。
- 成员列表与角色/状态更新、最后 active owner 保护、邀请创建/撤销/接受和成员删除。
- 租户组及组成员管理、principal/group 继承式 allow-only ACL、追加式审计查询与 NDJSON 导出。
- 文件夹与文件元数据、S3 Multipart 直传、下载授权、Range、回收/恢复/永久清理。
- PostgreSQL 持久化、对象完整性验证、幂等、维护任务、Prometheus 指标和安全配置门禁。
- Release 归档、checksums、SBOM、provenance，以及边界明确的 `v0.1.1` staging 部署、监测和逻辑恢复证据。

### 未完成：Phase 1 公网生产准入

- 生产 OIDC/IdP 生命周期与主体停用/密钥轮换演练。
- PostgreSQL 托管 TLS、加密 PITR/WAL 与经批准的 RPO/RTO 恢复演练。
- 对象存储版本控制或 Object Lock、静态加密、生命周期和版本恢复演练。
- KMS 或 secret-controller 托管与轮换；公网 ingress TLS、HA 和故障转移。
- 生产监控、SIEM、外部告警、容量计划与真实公网 presign 客户端实测。
- 不可变 GitHub Release、tag 保护、release Environment 独立 reviewer，以及独立安全/平台批准。

这些项目的 owner、证据、状态和验收标准统一维护在
[生产收口清单](../operations/production-readiness.md)，全部关闭前不得标记为公网生产就绪。

### 尚未交付：完整用户产品

本仓库当前是后端控制面。Web 客户端、桌面同步客户端、分享链接、配额、历史版本 API、预览和搜索不在已完成声明内；它们需要各自的产品目标、交互验收和端到端证据。

## Phase 2：同步和协作

- Tauri 桌面客户端、本地 SQLite 索引、文件变更监听。
- 文件版本、冲突副本、内容 Hash 秒传。
- 图片/视频/Office 预览和 OpenSearch。

## Phase 3：高性能增量同步

- FastCDC 内容定义分块、块级去重、Manifest。
- 多端增量同步和小块 Pack 合并。
- 大目录分片和 PostgreSQL 多 Shard。

## Phase 4：企业和多地域

- 企业 SSO、DLP、水印、合规审计扩展与法律保留。
- 冷热分层、跨地域复制和超大租户独立集群。

## 愿景能力归属

| 能力 | 归属 |
| --- | --- |
| 模块化单体、控制/数据分离、整文件 Blob、S3 Multipart、SeaweedFS | MVP P0-P4，已交付 |
| OIDC、主体/成员、RBAC、邀请、成员删除、组、ACL、基础审计 | Phase 1 仓库代码，已交付 |
| 生产身份、恢复、对象不可变、密钥、网络、监控、容量和审批控制 | Phase 1 生产准入，开放 |
| Web 客户端、分享、配额、历史版本 API | 后续产品目标，尚未排入已完成范围 |
| Tauri 同步、预览、搜索 | Phase 2 |
| CDC 分块、多 Shard、多地域 | Phase 3-4 |

进入新 Phase 前，必须完成上一阶段适用的验收或留下有 owner、期限和风险批准的延期记录。愿景文档不能覆盖安全评审、生产收口清单或当前交付状态中的硬门槛。
