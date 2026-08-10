# Asteria Drive 当前交付状态

> 最后核对：2026-08-10。本页是当前交付范围和状态的权威摘要；完成声明仍须由
> [实施与验收记录](mvp/implementation-log.md) 中的代码、命令或远端设置证据支持。
> MVP 文档保留其历史范围和验收语义，产品 Phase 2-4 另立目标。

| 交付面 | 当前状态 | 已有证据与剩余边界 |
| --- | --- | --- |
| 后端 MVP P0-P4 | 已完成并合入 `main` | Namespace、直传上传、下载、回收、PostgreSQL/SeaweedFS 真实依赖验收 |
| M2-1 身份与基础权限 | 已完成并合入 `main` | OIDC Resource Server、内部主体、租户成员和 owner/admin/editor/viewer 基础 RBAC |
| M2-2 成员生命周期 | 已完成并合入 `main` | 成员列表、角色/状态更新、最后活动 owner 保护；不含邀请和成员删除 |
| CI、安全门禁与分支保护 | 已启用并完成远端复核 | 七项 required checks、CODEOWNERS、Dependabot、secret scanning 和 push protection；PR #35 的七项保护检查与额外 CodeQL 全部通过 |
| Release 与 API 加固 | 已实现并合入 `main`，`v0.1.0` 与 `v0.1.1` 已发布 | `v0.1.1` 从 PR #37 合并提交发布；run `31298871230` 的归档、checksums、SPDX SBOM、Release subject provenance 与 OCI provenance 已独立验证，OCI digest 为 `sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842` |
| staging 当前部署 | `v0.1.0` 已通过 GitHub Actions 部署并验证；`v0.1.1` TLS rollout 待运行 | 现有 run `31260549517` attempt 2 证明固定 OCI digest、server-managed secrets、迁移、鉴权读写、metrics、storage verifier、loopback bindings 和容量门禁；私有 PKI 变更在获得 protected-main 合并、root bootstrap 与 deployment/monitor/recovery v2 artifact 前不得描述为已部署 |
| staging 周期监测 | 已通过受保护 `main` 启用并实跑 | 每小时及手工 workflow 仅执行 root-owned、摘要固定的只读探针；run `31265767434` 的 artifact 证明 Compose/镜像/容器、loopback、健康、metrics 与 85%/5 GiB 容量门禁；这只是 staging 信号，不是生产监控、SIEM、外部告警或容量规划 |
| staging 恢复演练 | 已通过受保护 `main` 自动化并实跑 | 每周及手工 workflow 对现有 staging 数据做有界逻辑备份并在隔离临时资源中恢复；run `31292745979` 的 artifact 证明 schema/table/row、storage verifier、应用 smoke、容量和零残留；边界为 `staging-recovery-not-production`，不代表 PITR/WAL、对象版本恢复或生产 RPO/RTO |
| 外部预签名端点 | 仓库能力已实现，平台证据待补 | S3 控制调用与客户端签名端点已解耦，production 对两者都强制 HTTPS；仍需用户自有 DNS/TLS、同一对象命名空间路由、CORS 和真实公网客户端上传下载证据 |
| PostgreSQL TLS 门禁 | production 配置门禁与 `v0.1.1` release 已完成；staging v2 运行证据待补 | production 整个 DSN 必须来自文件且只允许一个 `sslmode=verify-full`；当前增量增加主机私有 CA、`DNS:postgres`、TLS 1.2 下限、明文拒绝、SCRAM、`pg_stat_ssl` 和证据白名单，但必须等 protected-main 部署 artifact 才能关闭 staging 运行缺口；该私有 CA 不证明生产 CA/DNS/轮换 |
| 可靠性与可观测性 | 代码与仓库证据已完成 | `Idempotency-Key`、自动维护、Prometheus、fuzz/property、百万节点完整性和 10 分钟控制面 SLO 证据已归档；写路径与平台告警仍需独立测量 |
| Phase 1 生产化 | 代码、迁移、Release 与既有 staging 平台证据已完成；公网生产门待闭环 | 邀请、成员删除、正式 ACL、审计、Secret 读取、备份恢复演练、部署加固、`v0.1.1` provenance 和既有 `v0.1.0` staging 部署均有证据；staging TLS v2 与生产 PITR、对象不可变性、TLS/HA/监控、密钥托管及独立审批仍为硬门槛 |
| Phase 2-4 愿景 | 不在当前交付目标 | 同步、预览、搜索、分块、多地域等另立产品目标 |

在 HTTPS OIDC/IdP 生命周期、平台托管 PostgreSQL `verify-full` 与 PITR/WAL、对象锁/版本控制、
KMS 或 secret-controller 轮换、公网 TLS ingress、生产监控/SIEM 与外部告警、主机级 HA、
外部 presign 端点的平台 DNS/TLS/CORS 与客户端实测、容量规划及独立安全/平台审批完成前，
项目不得描述为公网生产就绪。

## 最近一次复核

2026-08-10，PostgreSQL production TLS guardrail PR
[#37](https://github.com/baicie/asteria-drive/pull/37) 已以 merge commit
`e8d26ded6e9138bbfdeac60f2487c5f835ab61a5` 合入受保护 `main`。随后 release workflow
[31298871230](https://github.com/baicie/asteria-drive/actions/runs/31298871230) 发布
[`v0.1.1`](https://github.com/baicie/asteria-drive/releases/tag/v0.1.1)。独立校验确认
checksums、SPDX 2.3 SBOM、四个 Release subject provenance 和 OCI provenance；多架构
OCI digest 为
`sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842`。
这关闭 release 缺口，但不等于 staging TLS 已上线：当前主机证据仍来自 `v0.1.0`，需要
ADR-0024 变更合入、root bootstrap、受保护 `main` 部署以及 deployment/monitor/recovery v2
artifact 的独立复核。即使这些 staging 证据通过，公网生产 CA/DNS/轮换、PITR/WAL、HA 和
独立审批仍保持开放。

2026-08-09，恢复演练 PR
[#34](https://github.com/baicie/asteria-drive/pull/34) 的七项保护检查与额外 CodeQL
全部通过，并以 merge commit `0a7a5360dbb0fd0ad6f46e262929a7a2e318cb18` 合入
`main`。主机随后安装 root-owned、SHA-256 固定为
`0e03a8e03ee2a98c277818e172822a401614f778f58f8c3e85130a83467402a6` 的恢复脚本，
并在私钥不落盘的情况下轮换受限 SSH 密钥；任意 shell、端口转发、旧参数格式和错误脚本
摘要均被拒绝。恢复工作流
[31292745979](https://github.com/baicie/asteria-drive/actions/runs/31292745979) attempt 1
从该合并提交成功运行。artifact `9031929907` 的 digest 为
`sha256:e1edc196bf2b33d08f2567509136bdd94b23a73ddde01a6a28bdc4e87d9accad`；
独立内存校验确认归档只有 `recovery-evidence.json`，其 SHA-256 为
`4d1f55d5ac166cae12b87c35046a456e78710217e24762b9b78de29a5b5034c7`。
证据显示 schema `3 -> 3`、15 张表、总行数 `12 -> 12`、storage verifier `1/1`
且无发现、恢复后应用检查全部通过、清理完成，根盘在演练前后均为 62%。
`object_versions_restored=false`、`pitr_wal_replayed=false`，因此只关闭 staging 逻辑恢复
自动化与证据缺口，不关闭生产 PITR/WAL、对象不可变性或 RPO/RTO 门禁。

2026-08-08，周期监测 PR
[#32](https://github.com/baicie/asteria-drive/pull/32) 的七项保护检查与额外 CodeQL
全部通过，并以 merge commit `51fdd808fdec0708ef8408fb8d6944991bdb7c29` 合入
`main`。主机随后安装 root-owned、SHA-256 固定为
`8c7e3526c091e0c73b5300f8ec105c027bf9fbee750554f716f15772c6d4e134` 的只读探针，
受限 SSH 密钥在不落盘私钥的情况下轮换；`prepare`、`cleanup`、`status` 均通过，
任意 shell 命令被拒绝。监测工作流
[31265767434](https://github.com/baicie/asteria-drive/actions/runs/31265767434) attempt 1
从该合并提交成功运行。artifact `9024088965` 的 digest 为
`sha256:349d2e98bbe548a00a49180403d085eea173346ce9b259b44d4a467e822deab3`；
独立内存校验确认归档摘要、唯一 JSON 成员、字段白名单和全部证明，观测到 load1 `0.00`、
内存使用率 `27.39%`、根盘使用率 `62%`、`15332425728` bytes 可用。该证据只关闭
staging 周期容量与健康信号，不关闭生产监控、SIEM、外部告警或容量规划。

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
