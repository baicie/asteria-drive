# Staging v0.2.0 发布前核查（Sprint S1）

- 核查时间：2026-10-23T06:26:43Z；最终修订：2026-10-23T08:55:00Z
- 仓库：`https://github.com/baicie/asteria-drive`
- 起始 HEAD：`cf63a93480ba26c449ee2d0f93d3ea74df307c06`
- 当前本地 HEAD：`cf63a93480ba26c449ee2d0f93d3ea74df307c06`
- 工作区：干净
- 目标：只准备 staging v0.2.0 候选和审批材料；本轮不得把 staging 结果提升为生产就绪。

## 状态摘要

| 阶段 | 状态 | 说明 |
| --- | --- | --- |
| A. 发布前核查 | `PASS` | 仓库、Release、OCI、摘要和回滚代码核查通过 |
| B. Root bootstrap | `BLOCKED_APPROVAL` | 未获 root 主机操作授权；只准备 Runbook |
| C. Staging 部署 | `BLOCKED_ROLLBACK_CONTRACT` | 依赖 B；完整回滚契约闭合前不得部署 |
| D. Monitor / Recovery | `BLOCKED_APPROVAL` | 依赖 C；未执行远端探针或恢复演练 |
| E. 最终 rollout 报告 | `BLOCKED_ROLLBACK_CONTRACT` | 模板和失败证据已更新；成功路径未执行 |
| F. 下一轮产品规划 | `PASS` | 仅提出候选方向，未实施产品功能 |
| 回滚契约 | `BLOCKED_ROLLBACK_CONTRACT` | DB/API 前向兼容 PASS；成功部署后的完整回滚缺少受控、原子路径 |

## A1. main 与 required checks

| 项目 | 结果 |
| --- | --- |
| `main` HEAD | `cf63a93480ba26c449ee2d0f93d3ea74df307c06` |
| `main` CI run | [39908755699](https://github.com/baicie/asteria-drive/actions/runs/39908755699)，success |
| `main` Security run | [39908755595](https://github.com/baicie/asteria-drive/actions/runs/39908755595)，success |
| required checks | `CI / quality`、`CI / race`、`CI / api-contract`、`CI / integration`、`CI / web`、`Security / govulncheck`、`Security / dependency-review`、`Security / codeql` |
| required review | 无（2026-10-22 按仓库 owner 指示移除；PRD-09/PRD-10 仍开放） |
| strict / admin enforcement / conversation resolution | 启用 |
| 强推 / 删除 main | 禁止 |

说明：`main` push 的 Security run 中 `Security / dependency-review` 为 skipped，这是该 workflow 只在 PR 事件执行依赖差异审查的设计；PR #78/#79/#80/#81 的 dependency-review 均为 pass。

## A1b. Monitor run 39919598579 失败诊断

| 项目 | 值 |
| --- | --- |
| Run | [39919598579](https://github.com/baicie/asteria-drive/actions/runs/39919598579) |
| 事件 | `schedule`，head `cf63a93480ba26c449ee2d0f93d3ea74df307c06` |
| 创建时间 | `2026-10-23T07:42:43Z` |
| 失败步骤 | `Probe capacity and health` |
| 上传步骤 | `Upload monitor evidence`，success |
| Artifact | `staging-monitor-evidence`，ID `12487180165`，digest `sha256:006959538958041ce1e519e1a5eefe50a1cef4b39977f3af155feb0a298a0785` |
| `probe_exit` | `1` |
| `raw_evidence` | 0 bytes，SHA-256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`（空文件） |
| `remote_stderr` | 32 bytes，SHA-256 `1b39cdcbcdfd0380085e7e3ffff878cb0e97edb3afb8630236727d447d6a0164` |

`remote_stderr` 的 32 字节摘要与 dispatcher 的固定拒绝消息完全匹配：

```text
staging deploy command rejected\n
```

因此可以确认的范围：

- **已确认**：强制命令在 dispatcher 阶段被拒绝，monitor 脚本尚未产生被接受的 evidence。
- **强指示**：主机 dispatcher 仍固定旧摘要，与 main 上的 v0.2.0 `MONITOR_SCRIPT_SHA256` 不匹配；这与“旧 dispatcher 拒绝新 monitor 命令”一致。
- **未证明**：API、PostgreSQL、SeaweedFS、TLS、磁盘或容量是否健康。空 evidence 不能证明服务不可用，也不能证明服务可用。
- **未验证**：主机实际安装摘要、active Compose、容器镜像身份，必须以 Phase B 只读基线确认。

调度延迟：workflow cron 为 `23 * * * *`，但实际 run 创建于 `07:42:43Z`，说明 GitHub Actions 调度可延迟至少 79 分钟。不得把精确 cron 时间当作升级原子性或监控连续性的保证。

## A2. Release、tag、OCI 与 provenance

| 固定点 | 实际值 | 结果 |
| --- | --- | --- |
| Release | `v0.2.0` | PASS |
| Release immutable | `isImmutable=true` | PASS |
| annotated tag object | `c4ae294a47ba761e4c8066056b5e999bd868689e` | PASS |
| peeled source commit | `5169da638beac679c9cfed66757159474de879dc` | PASS |
| release manifest commit | `5169da638beac679c9cfed66757159474de879dc` | PASS |
| API OCI index | `sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853` | PASS |
| API platforms | `linux/amd64`、`linux/arm64` | PASS |
| provenance verify | `gh attestation verify` exit 0，绑定 `refs/tags/v0.2.0` 和 source digest | PASS |
| Release assets | API amd64/arm64、Web static、API/Web SPDX、checksums、manifest | PASS |
| checksums | 全部 7 个文件 `sha256sum -c` 通过 | PASS |

Release 页面：<https://github.com/baicie/asteria-drive/releases/tag/v0.2.0>

## A3. 摘要与验证契约

| 文件 | SHA-256 | 与固定点一致 |
| --- | --- | --- |
| `infra/docker/staging/compose.yaml` | `cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005` | PASS |
| `scripts/deploy-staging.sh` | `007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f` | PASS |
| `scripts/monitor-staging.sh` | `d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69` | PASS |
| `scripts/recover-staging.sh` | `91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4` | PASS |
| `scripts/bootstrap-staging-host.sh` | `e1f9013f4ca9de66fae7a8c64c24ef7d50e86d627bcbec1860be08e79247c519` | 本次 bootstrap 输入 |

交叉核对：

- `deploy-staging.yml` 固定 `RELEASE_TAG=v0.2.0`、source commit、API digest、Compose SHA-256。
- `monitor-staging.yml` 固定新的 monitor 脚本摘要、Compose SHA-256、API digest。
- `drill-staging-recovery.yml` 固定新的 recovery 脚本摘要、Compose SHA-256、API digest。
- `bootstrap-staging-host.sh` 固定 compose/deploy/monitor/recovery 四个摘要。
- `internal/cicheck/staging_deployment_test.go` 通过，包含 release identity、Compose 标签、脚本摘要、forced-command 边界和部署/监测/恢复契约。

结论：PASS。

## A3b. v0.1.1 → v0.2.0 数据库与 API 兼容性

对比对象：

- v0.1.1 annotated tag object `1b0a4b3e79f38b3b8f6b5a6f2bbeacc2f4533994`，peeled source `e8d26ded6e9138bbfdeac60f2487c5f835ab61a5`
- v0.2.0 source `5169da638beac679c9cfed66757159474de879dc`

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| migration 目录树 | 完全相同 | Git tree `e5cbd019ec75b35c28674ea0a94a7153999de5bf` |
| `0001_mvp.sql` | 相同 | blob `559657fdc4478c213ecbb15ceac9e5ad7255b761` |
| `0002_identity_access.sql` | 相同 | blob `a19bd152abe7ada114881262c26619f418fbb734` |
| `0003_production_readiness.sql` | 相同 | blob `713ee71d2a683282b84eca86cabba86e06fc607b` |
| `internal/postgres` tree | 相同 | `36be5a8d4f5058fe054f3512cdd51fb04829c6c4` |
| `internal/drive` tree | 相同 | `d91f6231495afdc4fbab6a626ed21e9544c4a7d7` |
| `internal/config` tree | 相同 | `5922684f78ea608438efe20d15fc04814e6e69e7` |
| `internal/s3store` / `internal/memory` | 相同 | 树对象一致 |
| `internal/server` 生产代码 | 未变更 | 仅 `server_test.go` 变化 |
| `cmd/asteria-server` | 仅新增 `--healthcheck` CLI | 未改变数据库/HTTP 业务契约；staging Compose 未使用该 CLI |

部署脚本仍会：

1. 启动候选 PostgreSQL/SeaweedFS；
2. 读取 `MAX(version)`；
3. 执行 `asteria-migrate`；
4. 强制 schema version 为 `3`；
5. 启动候选 API 并验证固定镜像身份。

由于 v0.1.1 与 v0.2.0 的 migration 文件和 PostgreSQL/drive 代码完全一致，本次是 **schema 3 的 no-op 迁移**。v0.1.1 API 在 schema 3 上仍然兼容；失败路径自动恢复 previous v0.1.1 image 不存在不可逆的数据库 schema 契约风险。

限制：

- 这是针对 **v0.1.1→v0.2.0 这一对不可变产物** 的结论，不推广到未来 migration。
- “自动失败回滚”只在部署尝试失败且 previous active Compose 存在时发生；它不是“成功部署后的通用回滚”。
- 完整回滚还涉及主机 dispatcher/monitor/recovery 和 GitHub Workflow 摘要契约，见 A5b；该部分仍为 `BLOCKED_ROLLBACK_CONTRACT`。

结论：DB/API 前向兼容 PASS；完整回滚契约 BLOCKED。

## A4. 回滚与破坏性清理

- 部署脚本在激活后失败时，若存在 previous active Compose，则使用 previous Compose、`--pull never` 恢复 PostgreSQL、SeaweedFS 和 API，并重新校验 readiness 与固定镜像身份。
- 首次部署且无 previous Compose 时，失败清理只删除候选容器和网络，不删除持久化数据卷或 secret volumes。
- 候选清理使用 `docker compose down --remove-orphans`，没有 `-v`，没有 persistent volume 删除。
- recovery drill 只删除带当前 run 标签的临时容器、网络、临时恢复卷和临时目录；活动 `postgres-data`、`seaweedfs-data` 和 secret volumes 不是清理目标。
- bootstrap 对部分初始化的 secret volumes 和 PKI 采取 fail-closed，不会隐式轮换或覆盖。

结论：PASS（代码契约层面）；真实主机失败路径仍须在 Phase C 的真实部署证据中确认。

## A5. Root bootstrap 前的摘要不匹配窗口

已确认：

- 远端 host 仍记录为 `v0.1.1`，最新一次成功 monitor 是 run `39885137443`（`2026-10-23T01:38:26Z`，旧 main `2dc40b9`）。
- 合并 PR #80 后，受保护 `main` 的 monitor/recovery workflow 已固定 v0.2.0 摘要，但主机 dispatcher 仍是 v0.1.1 的旧摘要。
- 2026-10-23 07:42:43Z 的 monitor run `39919598579` 已 fail-closed；artifact 证明 dispatcher 拒绝了命令（见 A1b），但没有产生主机健康 evidence。
- recovery cron 为 `17 4 * * 1`，下一个可能窗口是 2026-10-26 04:17Z。
- GitHub Actions cron 有显著延迟：cron `23 * * * *` 的 run 实际创建于 `07:42:43Z`，不能依赖精确调度时间保证升级原子性。

过渡策略（不降低安全门槛）：

1. 不暂停、不绕过、不修改 monitor/recovery workflow 或 dispatcher。
2. 在受控维护窗口内先完成 Phase B bootstrap，再立即执行 Phase C deployment；不以 cron 时刻作为原子性保证。
3. 在 bootstrap 完成到 deployment 完成之间，monitor 可能 fail-closed；这是预期摘要不匹配，不是服务不可用的证明。
4. 失败后不得只恢复旧 dispatcher 就宣称监控恢复；完整回滚必须同时恢复主机运行态和 GitHub Workflow 摘要契约（A5b）。
5. 如果不能在窗口内完成，恢复旧 root-owned 文件并保持 v0.1.1 运行态；不得留下半升级 host。

状态：分析 PASS；实际过渡执行 `BLOCKED_APPROVAL`。

## A5b. 完整回滚契约

**错误判断已修复**：仅恢复旧 dispatcher/monitor/recovery 文件，不足以恢复 GitHub monitor。
main 上的 `.github/workflows/monitor-staging.yml` 仍会发送 v0.2.0 的 `MONITOR_SCRIPT_SHA256`；旧 dispatcher 只接受旧摘要，因此仍会拒绝。同样，recovery workflow 会发送 v0.2.0 recovery 摘要。

完整回滚至少需要同时满足：

| 维度 | 回滚目标 | 当前是否有受控自动化 |
| --- | --- | --- |
| 主机 root-owned 文件 | 旧 dispatcher/config/monitor/recovery | 有备份，需 root 手工恢复 |
| 主机运行态 | 旧 active Compose + v0.1.1 image + 兼容 schema | 仅“部署尝试失败且 previous Compose 存在”时自动；成功后回滚没有 workflow |
| GitHub Workflow 摘要契约 | monitor/recovery/deploy 的旧 env 和输入 | 无；需要受保护 main 上的回滚 PR |
| 监控连续性 | 旧 host 摘要 + 旧 workflow 摘要 + 旧 active Compose 同时匹配 | 非原子，需要维护窗口 |
| 活动数据卷 | 保持 postgres-data、seaweedfs-data、secret volumes | 不允许删除/重建 |

因此：

- **失败尝试内的自动回滚**：DB/API 契约 PASS，因为 migration 未变化；不删除数据卷。
- **成功部署后的完整回滚**：`BLOCKED_ROLLBACK_CONTRACT`。当前没有 `Rollback staging` 受控 workflow，也没有把“恢复主机文件 + 回滚 GitPins + 恢复 active Compose/image”做成可审计的单一路径。
- 不得宣称“自动回滚安全”；不得以旧 dispatcher 恢复代替 GitHub workflow 摘要回滚。

最小权限、可审核、不绕过 SHA-256 的候选路线见 Runbook 第 9 节；该路线需要新的受保护 PR/审批，不在本次 PR #82 内实施。

## A6. 生产边界

- 本轮只允许产生 staging 证据。
- `docs/operations/production-readiness.md` 的 `PRD-01` 至 `PRD-10` 仍为 `OPEN`。
- PRD-09（独立安全与平台审批）和 PRD-10（不可变 Release/tag 保护与发布复核）不会因 staging v0.2.0 验收而自动关闭。
- staging 的 loopback、development auth、私有 CA、内部非 TLS S3 不构成生产 TLS/HA/PITR/Object Lock/OIDC 证据。

结论：PASS（边界明确）。

## 仓库验证结果

| 检查 | 结果 |
| --- | --- |
| `go test ./... -count=1` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `go test -race` 本地 | `NOT_RUN`（Windows 环境缺少 CGO/C 编译器；CI `CI / race` PASS） |
| `npm run typecheck` | PASS |
| `npm run build` | PASS |
| `npm run test:e2e` | PASS，17 tests |
| `npm run lint:openapi` | PASS（4 个既有 warning，无 error） |
| OpenAPI route contract | PASS |
| `npm run lint:actions` | PASS |
| `git diff --check` | PASS |

CI 参考：

- main CI：<https://github.com/baicie/asteria-drive/actions/runs/39908755699>
- main Security：<https://github.com/baicie/asteria-drive/actions/runs/39908755595>

## Phase B 审批请求

Phase B 的实际 root 操作未执行，状态 `BLOCKED_APPROVAL`。精确操作、摘要、影响范围和回滚方案见：

[`staging-v0.2.0-bootstrap-runbook.md`](staging-v0.2.0-bootstrap-runbook.md)
与 [`staging-v0.2.0-phase-b-approval.md`](staging-v0.2.0-phase-b-approval.md)。

审批前必须由操作者填入并确认：

- 目标 host 标识（从 GitHub `staging` Environment secret `ASTERIA_STAGING_SSH_HOST` 获取，不在文档中记录明文）；
- 部署用户与端口；
- 固定的 ED25519 host key 指纹；
- 维护窗口（建议在成功 monitor 后、下一次 `:23Z` 前）；
- 操作者、复核人、UTC 时间；
- 备份文件路径和 SHA-256；
- 回滚负责人。

注意：GitHub `staging` Environment 当前只有 branch policy，没有 required reviewers。`Deploy staging` 是手动 `workflow_dispatch` 且要求 `deploy-v0.2.0` 确认；它不提供独立的 Environment reviewer gate。因此本次流程仍要求人工审批，且不得把“workflow 可触发”误认为“已获审批”。

## Phase C/D 状态

- Phase C：`BLOCKED_ROLLBACK_CONTRACT`。未触发 `Deploy staging`；在完整回滚契约获批前不应部署。审批材料见 [`staging-v0.2.0-phase-c-approval.md`](staging-v0.2.0-phase-c-approval.md)。
- Phase D：`BLOCKED_APPROVAL`。未触发 monitor/recovery，未获取任何成功 artifact。
- 当前 staging 实际运行版本：**未由本轮重新验证**；仓库记录仍为 `v0.1.1`，不能推断为 v0.2.0。
- monitor run `39919598579` 的失败只证明 dispatcher 拒绝，不证明 v0.1.1 服务健康或不可用。

## 结论

`BLOCKED_ROLLBACK_CONTRACT`：Phase A 仓库核查 PASS；DB/API 前向兼容 PASS；失败尝试内自动回滚的数据库契约 PASS；但**成功部署后的完整回滚没有受控路径**，且恢复旧 dispatcher 不能恢复 GitHub monitor。

下一步唯一最优先动作：先评审并批准“主机运行态 + GitHub Workflow 摘要契约”的完整回滚路线（Runbook 第 9 节），再批准 Phase B；在该路线获批前不得执行 Phase C。
