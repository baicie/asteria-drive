# Staging v0.2.0 发布前核查（Sprint S1）

- 核查时间：2026-10-23T06:26:43Z
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
| C. Staging 部署 | `BLOCKED_APPROVAL` | 依赖 B，且需要独立部署授权 |
| D. Monitor / Recovery | `BLOCKED_APPROVAL` | 依赖 C；未执行远端探针或恢复演练 |
| E. 最终 rollout 报告 | `BLOCKED_APPROVAL` | 已生成模板，真实主机证据为 `NOT_RUN` |
| F. 下一轮产品规划 | `PASS` | 仅提出候选方向，未实施产品功能 |

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

## A4. 回滚与破坏性清理

- 部署脚本在激活后失败时，若存在 previous active Compose，则使用 previous Compose、`--pull never` 恢复 PostgreSQL、SeaweedFS 和 API，并重新校验 readiness 与固定镜像身份。
- 首次部署且无 previous Compose 时，失败清理只删除候选容器和网络，不删除持久化数据卷或 secret volumes。
- 候选清理使用 `docker compose down --remove-orphans`，没有 `-v`，没有 persistent volume 删除。
- recovery drill 只删除带当前 run 标签的临时容器、网络、临时恢复卷和临时目录；活动 `postgres-data`、`seaweedfs-data` 和 secret volumes 不是清理目标。
- bootstrap 对部分初始化的 secret volumes 和 PKI 采取 fail-closed，不会隐式轮换或覆盖。

结论：PASS（代码契约层面）；真实主机回滚能力仍须在 Phase C 的真实部署证据中确认。

## A5. Root bootstrap 前的摘要不匹配窗口

已确认：

- 远端 host 仍记录为 `v0.1.1`，最新一次成功 monitor 是 run `39885137443`（`2026-10-23T01:38:26Z`，旧 main `2dc40b9`）。
- 合并 PR #80 后，受保护 `main` 的 monitor/recovery workflow 已固定 v0.2.0 摘要，但主机 dispatcher 仍是 v0.1.1 的旧摘要。
- monitor cron 为 `23 * * * *`；在下一次调度时，新的 `MONITOR_SCRIPT_SHA256` 会被旧 dispatcher 拒绝，workflow 预期 fail-closed。
- recovery cron 为 `17 4 * * 1`，下一个可能窗口是 2026-10-26 04:17Z。

过渡策略（不降低安全门槛）：

1. 不暂停、不绕过、不修改 monitor/recovery workflow 或 dispatcher。
2. 在最近一次成功 monitor 之后、下一次 `:23Z` 之前完成 root bootstrap，并立即在同一维护窗口执行 v0.2.0 部署。
3. 窗口内如果 monitor 触发而部署尚未完成，允许它 fail-closed；这是预期中的“摘要不匹配”，不是数据故障。
4. 如果部署自动回滚到 v0.1.1，立即用 Phase B 的 root-owned 文件备份恢复旧 dispatcher/monitor/recovery/config/authorized_keys，再恢复旧监控；不得删除数据卷。
5. 若无法在窗口内完成，停止并回滚 bootstrap 文件，恢复 v0.1.1 监控，不留下半升级状态。

状态：分析 PASS；实际过渡执行 `BLOCKED_APPROVAL`。

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

- Phase C：`BLOCKED_APPROVAL`。未触发 `Deploy staging`。
- Phase D：`BLOCKED_APPROVAL`。未触发 monitor/recovery，未获取任何新 artifact。
- 当前 staging 实际运行版本：**未由本轮重新验证**；仓库记录仍为 `v0.1.1`，不能推断为 v0.2.0。

## 结论

`BLOCKED_APPROVAL`：仓库侧发布前核查 PASS；Phase B/C/D 等待明确审批和真实主机证据。

下一步唯一最优先动作：完成 root bootstrap 审批并在维护窗口执行 Phase B，然后取得独立部署审批后执行 Phase C。
