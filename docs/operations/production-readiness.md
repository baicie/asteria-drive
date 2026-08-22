# Phase 1 生产收口清单

状态：`OPEN`

最后核对：2026-08-22

适用范围：Asteria Drive Phase 1 公网生产准入

本清单是生产 Definition of Done，不是平台已完成声明。仓库代码、`v0.1.1` release 和 staging TLS v2 证据是输入；它们不能替代目标生产环境的运行证据。所有行关闭前，项目只能描述为“Phase 1 仓库能力已交付，公网生产准入未完成”。

## 候选与环境绑定

开始验收时填写并冻结以下字段。任何候选代码、生成物、workflow、部署清单或相关平台控制变化都会使受影响行重新变为 `OPEN`。

| 字段 | 值 |
| --- | --- |
| Production environment / account | `UNASSIGNED` |
| Candidate commit | `UNASSIGNED` |
| Release tag and URL | `UNASSIGNED` |
| OCI manifest digest | `UNASSIGNED` |
| Rendered deployment digest | `UNASSIGNED` |
| Change record | `UNASSIGNED` |
| Target approval date | `UNASSIGNED` |

状态只能使用：`OPEN`、`IN PROGRESS`、`BLOCKED`、`COMPLETE`。`COMPLETE` 必须同时具备 owner、验收证据、复核人和 UTC 时间；配置样例、计划、staging 结果或可变链接均不能单独关闭生产行。

## 验收矩阵

| ID | 门槛 | Owner | 状态 | 当前证据与边界 | `COMPLETE` 验收标准和必需证据 |
| --- | --- | --- | --- | --- | --- |
| `PRD-01` | OIDC / IdP 生命周期 | `UNASSIGNED` | `OPEN` | 仓库有 issuer/audience/signature/time 校验、服务端成员解析和负向测试；没有生产 IdP 生命周期证据。 | 绑定生产 issuer/client/audience；证明 JWKS/签名密钥轮换、subject 停用、成员撤销和紧急访问流程；在目标环境完成错误 issuer/audience、过期 token、停用用户与轮换前后登录测试；附 IdP 配置摘要、测试记录、审计记录和复核人。 |
| `PRD-02` | PostgreSQL TLS、PITR/WAL 与 RPO/RTO | `UNASSIGNED` | `OPEN` | production 配置强制 file-only DSN 和 `sslmode=verify-full`；[staging TLS v2 恢复证据](evidence/staging-tls-rollout-20260816.md) 只证明逻辑恢复，明确 `pitr_wal_replayed=false`。 | 生产 CA/DNS `verify-full` 实测通过且明文连接被拒；连续加密 base backup/WAL 归档有保留、监控和访问控制；从隔离环境恢复到选定时间点，记录 backup/WAL ID、恢复点、数据校验、实测 RPO/RTO、清理和失败处置；业务 owner 与平台复核人批准目标和结果。 |
| `PRD-03` | 对象版本、不可变性与静态加密 | `UNASSIGNED` | `OPEN` | S3 私有访问、opaque key、完整性 verifier 和恢复工具已实现；最新 staging artifact `9265397749` 明确 `object_versions_restored=false`。 | 目标 bucket 禁止公开访问并启用版本控制；对 Object Lock/保留策略作显式批准决定；启用平台/KMS 静态加密、最小 IAM、生命周期和删除保护；隔离恢复指定对象版本并由 storage verifier 校验 metadata/object 一致性；附 bucket policy、设置快照、KMS key ID、版本恢复记录和复核。 |
| `PRD-04` | KMS 与 Secret 轮换 | `UNASSIGNED` | `OPEN` | 仓库支持 file-based secret 注入和 server-managed staging secrets；没有生产 KMS/secret-controller 或轮换证据。 | 生产 secret 由 KMS、workload identity 或受控 secret-controller 提供，不写入镜像、Git、Actions artifact 或普通日志；完成数据库、S3、OIDC/client 与运维凭据轮换及回滚演练；证明旧凭据失效、服务持续可用、访问有审计且轮换周期/owner 已登记。 |
| `PRD-05` | 公网 ingress TLS 与 HA | `UNASSIGNED` | `OPEN` | 仓库含 hardened container、Kubernetes baseline、NetworkPolicy 和 loopback staging；没有公网入口或主机级 HA 证据。 | 生产 DNS 和受信 CA 证书有效，强制 HTTPS/TLS policy，管理端点不公网暴露；至少跨故障域部署并验证 readiness、滚动更新、单实例/节点/故障域失效和容量下的故障转移；ingress rate/connection limits、CNI default-deny 与证书自动续期实测通过；附渲染清单 digest、探测与故障演练记录。 |
| `PRD-06` | 生产监控、SIEM 与外部告警 | `UNASSIGNED` | `OPEN` | Prometheus 指标和每小时 staging monitor 已有证据；它们不是生产监控、SIEM 或通知链路。 | 目标环境采集 API、依赖、worker、容量、TLS/证书和备份信号；审计/安全日志进入有保留和访问控制的 SIEM；为可用性、错误率、延迟、容量、备份/WAL、证书和安全事件建立阈值；逐条触发测试并证明外部值班通知、确认、升级与恢复关闭，且输出不含 token、presigned URL 或 secret。 |
| `PRD-07` | 外部 presign DNS/TLS/CORS/客户端 | `UNASSIGNED` | `OPEN` | 控制端点与客户端 public endpoint 已分离，production 强制 HTTPS；尚无用户网络的真实公网实测。 | 从生产网络外的普通客户端通过公网 API 创建 multipart、对 public S3 endpoint 上传并完成提交，再执行完整下载和 Range 下载；证明 DNS、证书链/SNI、CORS preflight/headers、同一 bucket/key 命名空间和 TTL 正确；响应不得暴露 control endpoint、长期凭据或内部 bucket/key；保存去敏流量摘要和对象校验值。 |
| `PRD-08` | 容量计划与负载余量 | `UNASSIGNED` | `OPEN` | 仓库有百万节点完整性和控制面 SLO 工具，staging 只提供单机磁盘/健康采样；没有生产规模和增长计划。 | 业务 owner 批准 tenant、成员、节点、对象字节、目录宽度、请求/上传并发和增长假设；在候选生产拓扑完成读写/上传/维护/恢复负载测试；记录瓶颈、至少一个批准的峰值余量、扩容阈值与 lead time；容量告警和扩容 runbook 经演练。 |
| `PRD-09` | 独立安全与平台批准 | `UNASSIGNED` | `OPEN` | [安全评审记录](../security/review-record.md) 是旧候选模板，不是批准；没有绑定当前生产候选和环境的签核。 | 独立安全 reviewer 和平台 reviewer 分别核对本矩阵、威胁模型、候选 diff、运行证据和全部 findings；无未处置 high finding，例外均有 owner/expiry/risk decision；在访问受控的 change record 中签署 reviewer identity、UTC、commit、release URL、OCI/manifest digest、环境和证据链接。 |
| `PRD-10` | 不可变 GitHub Release、tag 保护与发布复核 | `UNASSIGNED` | `OPEN` | 仓库 release workflow 已主动检查 Immutable Releases、重复 Release/OCI tag 和发布后 immutable 状态；2026-08-22 GitHub API 显示 `immutable-releases.enabled=false`，`v0.1.0`/`v0.1.1` 均 `isImmutable=false`，repository rulesets 为空，`release` Environment 没有 required reviewer。现有历史 Release 有 checksums、SPDX SBOM、Release/OCI provenance 和固定 OCI digest，但不是不可变证据。 | GitHub API/设置证据证明候选 Release 已 immutable；repository ruleset 对 `v*` 禁止非受控创建、更新和删除；release Environment 仅允许受控 tag 且要求非发布者的授权 reviewer；从 tag 到受保护 main、源码、checksums、SBOM、provenance 和 OCI digest 完成独立复核，并保存 environment approval 与规则证据。 |

## 当前可复用证据

- 最新 staging 恢复：run [`31953850132`](https://github.com/baicie/asteria-drive/actions/runs/31953850132)，artifact `9265397749`，artifact digest `sha256:a5944afd9514755a2469e1c26eacb8836ebdec70549c0bcab5278c5c2477f619`。它证明 schema `3 -> 3`、15 表、`17 -> 17` 行、storage verifier `2/2`、source PostgreSQL TLS 1.3 和清理完成；claim boundary 为 `staging-recovery-not-production`。
- 当前发布基线：`v0.1.1`，source `e8d26ded6e9138bbfdeac60f2487c5f835ab61a5`，OCI digest `sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842`。它证明归档、checksums、SBOM 和 provenance，不证明 `PRD-10` 的外部治理设置。
- 2026-08-22 平台设置复核：`gh api repos/baicie/asteria-drive/immutable-releases` 返回 `enabled=false`；`branches/main/protection` 保留七个 required checks、strict 和 administrator enforcement，但 `required_pull_request_reviews=null`；`rulesets` 返回空数组；`environments/release` 只有 branch policy，没有 required reviewer。以上结果是开放门槛的当前证据，不是生产批准。
- 控制映射见 [Security Control Matrix](../security/control-matrix.md)，生产恢复步骤见 [Backup and Restore](backup-and-restore.md)，部署边界见 [Deployment](deployment.md)。

## 关闭规则

1. Owner 在对应行的外部 change record 附上不可变或带 digest 的证据，并记录证据产生时间和目标环境。
2. 非 owner reviewer 复现关键检查，确认敏感信息已去除，再把状态改为 `COMPLETE`。
3. Security reviewer 核对所有高风险行；Platform reviewer 核对所有平台运行证据。一个人不能同时充当发布者和最终独立 reviewer。
4. 所有行 `COMPLETE`、所有 candidate 字段已绑定且无开放 high finding 后，才可签署生产准入。
5. 候选或平台控制变化后按影响面重新打开行；定期控制仍按 [control matrix](../security/control-matrix.md) 的频率复核。
