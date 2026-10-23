# Staging v0.2.0 运行验收报告（Sprint S1）

- 报告状态：`READY_FOR_HOST_VALIDATION`
- 实现 PR：[#83](https://github.com/baicie/asteria-drive/pull/83)（release bundles / state machine / rollback workflow）
- 实现说明：[staging-release-controller.md](https://github.com/baicie/asteria-drive/blob/codex/staging-release-controller/docs/acceptance/staging-release-controller.md)
- P0/P1 代码审查：`IMPLEMENTED` + `VERIFIED_LOCAL` + `VERIFIED_CI`（PR #83）
- 真实执行链：dispatcher -> 主机 `flock` -> root-owned host controller -> 版本化 deploy script -> evidence/state
- 生成时间：2026-10-23T08:55:00Z
- 起始产品 HEAD：`cf63a93480ba26c449ee2d0f93d3ea74df307c06`
- 最终产品 HEAD：`cf63a93480ba26c449ee2d0f93d3ea74df307c06`（本轮未修改产品代码；验收文档提交在 PR #82）
- 实际执行范围：仓库侧发布前核查（Phase A）、DB/API 兼容性审查、审批材料准备
- 未执行：root bootstrap（Phase B）、staging deployment（Phase C）、monitor/recovery（Phase D）

## 结论

`READY_FOR_HOST_VALIDATION`

当前没有 v0.2.0 的 staging 主机运行证据。Phase B/C/D 未执行。

DB/API 前向兼容性通过；失败尝试内自动回滚的数据库契约通过。PR #83 已提供固定 v0.1.1 bundle 的受控 `Rollback staging` workflow，但**真实主机回滚尚未演练**，且跨 GitHub Actions 与主机的操作不是原子事务。因此不得宣称自动回滚安全或 `ROLLBACK_VERIFIED`，Phase C 仍需独立批准。

## 固定点

| 项目 | 值 | 状态 |
| --- | --- | --- |
| Release | `v0.2.0` | PASS（已存在且 immutable） |
| Release URL | <https://github.com/baicie/asteria-drive/releases/tag/v0.2.0> | PASS |
| Source commit | `5169da638beac679c9cfed66757159474de879dc` | PASS |
| API OCI index | `sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853` | PASS |
| Compose SHA-256 | `cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005` | PASS（仓库） |
| Deploy script SHA-256 | `007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f` | PASS（仓库） |
| Monitor script SHA-256 | `d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69` | PASS（仓库） |
| Recovery script SHA-256 | `91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4` | PASS（仓库） |

## GitHub CI 与 Security

| 项目 | 链接 | 状态 |
| --- | --- | --- |
| main CI（cf63a93） | <https://github.com/baicie/asteria-drive/actions/runs/39908755699> | success |
| main Security（cf63a93） | <https://github.com/baicie/asteria-drive/actions/runs/39908755595> | success |
| required checks | 8 项 | PASS |

`Security / dependency-review` 在 main push 事件 skipped；PR 事件均 pass。

## Release 资产验证

已下载 Release assets 并执行 `sha256sum -c checksums.txt`，7 个文件全部通过：

- `asteria-drive_0.2.0_linux_amd64.tar.gz`
- `asteria-drive_0.2.0_linux_arm64.tar.gz`
- `asteria-drive-web_0.2.0_static.tar.gz`
- `asteria-drive_0.2.0.spdx.json`
- `asteria-drive-web_0.2.0.spdx.json`
- `checksums.txt`
- `release-manifest.json`

`gh attestation verify oci://ghcr.io/baicie/asteria-drive@sha256:964e...` 绑定 `refs/tags/v0.2.0` 和 source digest，退出码 0。

## Monitor 失败证据：39919598579

| 项目 | 值 |
| --- | --- |
| Run | [39919598579](https://github.com/baicie/asteria-drive/actions/runs/39919598579) |
| 事件 / head | `schedule` / `cf63a93480ba26c449ee2d0f93d3ea74df307c06` |
| 创建时间 | `2026-10-23T07:42:43Z` |
| 失败步骤 | `Probe capacity and health` |
| Artifact | `staging-monitor-evidence`，ID `12487180165` |
| Artifact digest | `sha256:006959538958041ce1e519e1a5eefe50a1cef4b39977f3af155feb0a298a0785` |
| `probe_exit` | `1` |
| `raw_evidence` | 0 bytes，SHA-256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| `remote_stderr` | 32 bytes，SHA-256 `1b39cdcbcdfd0380085e7e3ffff878cb0e97edb3afb8630236727d447d6a0164` |

`remote_stderr` 摘要与固定消息完全匹配：

```text
staging deploy command rejected\n
```

诊断边界：

- **已确认**：dispatcher 在 monitor 脚本执行前拒绝了强制命令，没有产生被接受的 evidence。
- **强指示**：主机 dispatcher 仍固定旧摘要，与 main 上的 v0.2.0 `MONITOR_SCRIPT_SHA256` 不匹配。
- **未证明**：API、PostgreSQL、SeaweedFS、TLS、磁盘、容量是否健康；空 evidence 不能证明服务不可用或可用。
- **未验证**：主机安装摘要、active Compose、运行镜像；必须按 Phase B Runbook 第 2 节只读采集。

调度延迟：cron `23 * * * *` 的 run 实际创建于 `07:42:43Z`，但无法证明对应 06:23 还是 07:23 的 cron occurrence，不能据此推断固定延迟；不得依赖精确 cron 时间保证升级原子性。

## DB / API 兼容性

v0.1.1 peeled source：`e8d26ded6e9138bbfdeac60f2487c5f835ab61a5`
v0.2.0 source：`5169da638beac679c9cfed66757159474de879dc`

| 检查 | 结果 |
| --- | --- |
| migration 目录树 | 相同：`e5cbd019ec75b35c28674ea0a94a7153999de5bf` |
| `0001_mvp.sql` / `0002_identity_access.sql` / `0003_production_readiness.sql` | 三个 blob 均相同 |
| `internal/postgres` / `internal/drive` / `internal/config` / `internal/s3store` / `internal/memory` | tree 相同 |
| `internal/server` 生产代码 | 未变更，仅测试变化 |
| `cmd/asteria-server` | 仅新增 `--healthcheck` CLI；staging Compose 未使用 |
| schema 版本 | v0.1.1 与 v0.2.0 均为 3；本次 migration 为 no-op |
| v0.1.1 API 兼容新 schema | PASS（schema 和 drive 代码均未变化） |
| 失败尝试自动回滚旧镜像的 DB 契约 | PASS（无不可逆 schema 变化） |

限制：该结论只适用于 v0.1.1→v0.2.0 这一对不可变产物，不推广到未来 migration。完整回滚由 PR #83 的固定 bundle workflow 和版本感知 monitor/recovery 覆盖；仍需真实主机演练。

## Phase B：Root bootstrap

状态：`BLOCKED_APPROVAL`

- 未执行任何 root/SSH 操作。
- 未读取主机文件、Secret、PKI 私钥或 Docker volume 内容。
- 未安装新的 dispatcher/monitor/recovery。
- 真实主机所有权、权限和当前摘要：`NOT_RUN`。
- Phase B 审批材料：[staging-v0.2.0-phase-b-approval.md](staging-v0.2.0-phase-b-approval.md)
- 操作 Runbook：[staging-v0.2.0-bootstrap-runbook.md](staging-v0.2.0-bootstrap-runbook.md)

## Phase C：Staging deployment

状态：`READY_FOR_HOST_VALIDATION`

| 项目 | 值 |
| --- | --- |
| workflow | `Deploy staging` |
| confirmation | `deploy-v0.2.0` |
| run ID | `NOT_RUN` |
| workflow SHA | `NOT_RUN` |
| deployment artifact | `NOT_RUN` |
| actual image digest | `NOT_RUN` |
| actual Compose SHA-256 | `NOT_RUN` |

Phase C 审批材料：[staging-v0.2.0-phase-c-approval.md](staging-v0.2.0-phase-c-approval.md)

未执行部署，未启动 deployment workflow，未通过 SSH 更新容器。

## Phase D：Monitor 与 Recovery

状态：`BLOCKED_APPROVAL`

| 项目 | 值 |
| --- | --- |
| monitor 成功 run ID | `NOT_RUN`（只有失败诊断 run `39919598579`） |
| monitor 成功 artifact | `NOT_RUN` |
| recovery run ID | `NOT_RUN` |
| recovery artifact | `NOT_RUN` |
| PostgreSQL TLS 运行验证 | `NOT_RUN` |
| S3 multipart 上传/下载 | `NOT_RUN` |
| schema/table/row 一致性 | `NOT_RUN` |
| 临时资源清理验证 | `NOT_RUN` |

历史 v0.1.1 monitor/recovery 证据不能当作 v0.2.0 证据。

## 当前 staging 实际版本

**未由本轮重新验证。**

仓库文档记录 staging 仍运行 `v0.1.1`；本轮没有读取主机、没有部署、没有成功 monitor，因此该记录仍是待验证状态，不能推断为 v0.2.0。

## PostgreSQL TLS、S3 与回滚

| 项目 | 状态 | 说明 |
| --- | --- | --- |
| PostgreSQL TLS verify-full | `NOT_RUN` | 只有历史 v0.1.1 证据；需要新 deployment/monitor 证据 |
| SCRAM / 明文拒绝 | `NOT_RUN` | 同上 |
| S3 multipart 上传/下载/字节校验 | `NOT_RUN` | 需要新 deployment artifact |
| 失败尝试内自动回滚代码 | PASS（代码审查） | previous Compose + `--pull never`，不删除数据卷 |
| 成功部署后的完整回滚 | `READY_FOR_HOST_VALIDATION` | PR #83 已加入 `Rollback staging` 固定 bundle workflow；仍需主机演练 |
| 旧 dispatcher 恢复 = 监控恢复 | **旧架构错误，已由 PR #83 修复** | 旧架构下 main 只发送 v0.2.0 SHA；版本感知 workflow 现在接受两个精确 allowlist pair |
| 实际回滚演练 | `NOT_RUN` | 未部署，无法验证 |

## 遗留风险与缺失证据

1. `staging` GitHub Environment 只有 branch policy，没有 required reviewers；部署门是手动 dispatch + confirmation，不是独立 Environment reviewer gate。
2. PR #83 之后，版本感知 monitor/recovery 会按 allowlist 选择 active 版本；旧架构下恢复旧 dispatcher 不会恢复 GitHub monitor。
3. PR #83 已加入受控的 `Rollback staging` workflow；仓库侧路径可验证，但真实主机回滚尚未执行，不能宣称 `ROLLBACK_VERIFIED`。
4. 没有真实主机基线，无法确认 installed dispatcher/monitor/recovery 摘要和所有权。
5. 没有 root bootstrap 备份 SHA-256、主机证据、操作者/复核人。
6. 没有 deployment run ID、artifact digest、真实 OCI/Compose 比对。
7. 没有成功 monitor/recovery 证据，不能声明 TLS、S3、schema/row 或清理通过。
8. PRD-09/PRD-10 仍为 `OPEN`；本次不关闭任何生产门槛。

## PRD-09 / PRD-10

| 门槛 | 状态 | 说明 |
| --- | --- | --- |
| `PRD-09` 独立安全与平台审批 | `OPEN` | 无独立 reviewer；本次为 staging 准备与代理审查 |
| `PRD-10` 不可变 Release、tag 保护与发布复核 | `OPEN` | `v0.2.0` 已 immutable，但仍为单人自审；不因 staging 验收关闭 |

## 下一阶段产品工程规划（仅建议，未实施）

1. 同源下载跳转/流式代理，消除浏览器弹窗限制。
2. 上传 API 错误分类：签名过期、401/403、网络瞬断、不可恢复错误。
3. 上传任务跨页面刷新恢复，评估 ETag/session 恢复协议。
4. API client、上传状态机、分页 composable 单元测试。
5. 真实对象存储环境下的故障注入与上传恢复验收。

## 下一步唯一最优先动作

先评审并批准完整的回滚契约：

1. 主机 root-owned 文件恢复；
2. 受保护 main 上的 staging workflow 固定点回滚 PR；
3. active Compose/image 恢复到 v0.1.1 的受控路径；
4. monitor/recovery artifact 验证。

在真实主机回滚演练和独立批准前，Phase B 可准备但不得执行 Phase C；不得宣称 staging 已升级。
