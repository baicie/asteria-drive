# Phase S3：dispatcher / Phase B 修复方案（2026-11-05）

- 基线：`main` = `17d9e786924426354f4c4403e5238473cd46f179`
- 状态：**PREPARED / HOST_EVIDENCE_BLOCKED**——未读取主机配置，未修改任何文件
- 关联：[现场取证清单](phase-s3-staging-host-evidence.md)、
  [容量恢复方案](phase-s3-capacity-remediation-plan.md)、
  [部署 Gate 矩阵](phase-s3-deployment-gates.md)、
  [bootstrap runbook](../../acceptance/staging-v0.2.0-bootstrap-runbook.md)
- 边界：不修改 dispatcher 配置、不执行 bootstrap、不跳过 SHA-256 校验、
  不改 immutable Release、不破坏 v0.1.1 回退。

## 0. 本文的证据状态

主机 dispatcher 文件与配置**从未被读取**。下面所有"分支"都是
`待现场确认`，不是已确定的原因。本文的价值在于：把每种可能的现场观察
映射到一个**具体、最小、可回滚**的修复路径，并写清每条的授权条件。

**不要把本文的分支当成"已诊断"。**

## 1. 已确认的观察（VERIFIED，来自 Artifact）

| 观察 | 证据 |
| --- | --- |
| v0.2.0 monitor 摘要在 `exec` 之前被拒 | stdout 0 字节；stderr 32 字节 `1b39cdcb…` = `printf 'staging deploy command rejected\n'` |
| v0.1.1 monitor 摘要被接受并**实际执行** | 同一次运行产出 evidence 并走到 `host-capacity` |
| 拒绝文本只由 dispatcher 的 `fail()` 产生 | `scripts/bootstrap-staging-host.sh` 各代际的 `fail()` 均为该字符串 |
| 工作流请求的 v0.2.0 摘要是 `d1e186cb…` | `.github/workflows/monitor-staging.yml:17` |
| 工作流请求的 v0.1.1 摘要是 `3ef39670…` | 同上 `:16` |
| `3ef39670…` 是 2026-08-16 主机上安装的 monitor | `docs/status.md` 记录 + `infra/docker/staging/releases/v0.1.1/monitor-staging.sh` 实测一致 |
| `d1e186cb…` 是 v0.2.0 bundle 的 monitor | `infra/docker/staging/releases/v0.2.0/bundle.json` + 实测一致 |

## 2. 用接受集合排除代际（仓库侧推论，非现场证据）

对 `scripts/bootstrap-staging-host.sh` 逐提交取指纹：

| 代际 | `monitor_script_sha256` | `monitor_v011_script_sha256` | controller | `validate_lock` | releases.json |
| --- | --- | --- | --- | --- | --- |
| `7c6565b`（2026-08-16） | `3ef39670` | 无 | 无 | 无 | 无 |
| `cf63a93`（PR #80） | `d1e186cb` | 无 | 无 | 无 | 无 |
| `24d0a28` | `d1e186cb` | `3ef39670` | 无 | 无 | 无 |
| `3403734` / `b6dc862` / `4e7ba13` | `d1e186cb` | `3ef39670` | `754ba671` | 有 | 有 |

把"v0.1.1 被接受、v0.2.0 被拒"作为约束：

| 代际 | 预期行为 | 与观察 |
| --- | --- | --- |
| `7c6565b` | 接受 `3ef39670`，拒绝 `d1e186cb` | **完全一致** |
| `cf63a93` | 接受 `d1e186cb`，拒绝 `3ef39670` | **矛盾，排除** |
| `24d0a28` / `3403734` / `b6dc862` / `4e7ba13` | 两个都接受 | **矛盾，排除** |

**结论（推论，需现场确认）**：主机最可能停在 `7c6565b`（2026-08-16 TLS
rollout）代际——即 **Phase B bootstrap 从未执行**。这与 `docs/status.md`
记录的"最后一次有证据的部署是 2026-08-16 的 v0.1.1"一致。

**但仍然不能写成事实**：配置可能被手工改过，也可能存在本文未枚举的中间状态。
只有 P2 取证（第 4.1–4.4 节）能把它变成 `PHASE_B_OPERATIONAL_STATE_VERIFIED`
或推翻。

### 2.1 两个判定继续分开

| 判定 | 现在 | 取证后最多能到 |
| --- | --- | --- |
| `PHASE_B_OPERATIONAL_STATE_VERIFIED` | UNKNOWN | 需要 ①摘要 ②配置 ③下一次自然调度行为，三者齐全 |
| `PHASE_B_EXECUTION_HISTORY_VERIFIED` | UNKNOWN | **即使三项齐全也仍是 UNKNOWN**，除非有独立 bootstrap 日志/维护记录 |

## 3. 五种现场形态与对应修复路径

### 形态 1：旧版单摘要 dispatcher（最可能）

**观察特征**：`staging-dispatch.conf` 只有 `monitor_script_sha256=3ef39670…`
与 `recovery_script_sha256=3b2bb836…`；无 `-v0.1.1` 文件；无 controller/verifier；
无 `staging-releases.json`。

**诊断**：主机停在 v0.1.1 锚点，Phase B 从未执行。

**修复**：执行 **Phase B bootstrap**（完整 runbook，不是补丁）。

- 覆盖 `/usr/local/libexec/asteria-staging-{dispatch,monitor,monitor-v0.1.1,recovery,recovery-v0.1.1,controller}`、
  `/usr/local/libexec/staging_runtime_verifier.py`、`/etc/asteria-drive/{staging-dispatch.conf,staging-releases.json}`；
- 保留 `asteria-deploy` 的授权公钥，重写 `authorized_keys` 为单一 `restrict,command=` 条目；
- **不**触碰 compose、容器、卷或数据。

**风险**：中高。改写 root-owned 可执行文件与授权入口；若脚本中途失败，
可能留下部分安装状态。

**回滚**：bootstrap 本身**没有**内置回滚。回滚方式是在维护窗口内重新运行
`7c6565b` 版本的 bootstrap（需要该版本脚本与四个 base64 输入），把主机恢复到
v0.1.1 锚点。**因此必须在执行前把当前文件完整备份到主机外的受控位置。**

**授权**：需要单独的 Phase B 审批、维护窗口、双人复核。

### 形态 2：新版 dispatcher，配置缺 v0.2.0 项

**观察特征**：六个键齐全，但 `monitor_script_sha256` 不是 `d1e186cb…`
（例如仍是 `3ef39670…`），或 `monitor_v011_script_sha256` 缺失。

**诊断**：Phase B 部分执行，或配置被手工编辑过。

**修复**：**重新完整执行 bootstrap**，不要手工编辑配置文件。

理由：bootstrap 会先校验 base64 输入的摘要、再 `bash -n` / `py_compile`、
再校验目标物理属性，最后才 `install`。手工编辑绕过了全部这些检查，
且下一次 bootstrap 会覆盖它——两次修复互相矛盾。

**风险**：与形态 1 相同。

**回滚**：与形态 1 相同。

**授权**：同形态 1。

### 形态 3：文件/固定点漂移

**观察特征**：配置正确，但某个文件的实测摘要与期望不符（例如 monitor 文件
摘要不是 `d1e186cb…`）。

**诊断**：文件被手工替换、部分覆盖，或磁盘损坏。

**修复**：先**取证**——记录该文件的摘要、mtime、属主；比对 bundle。
**不要**直接覆盖。若是单一文件漂移，重新执行完整 bootstrap 是唯一同时修复
文件与配置的方式。

**风险**：高。漂移原因未知时，覆盖可能掩盖更严重的问题（如磁盘故障）。

**回滚**：备份原文件后执行；失败时恢复备份。

**授权**：同形态 1，且需要先有漂移原因的证据。

### 形态 4：配置与实际执行行为矛盾

**观察特征**：配置接受 `d1e186cb…`，但自然调度中 v0.2.0 仍被拒。

**诊断**：三种可能——配置在自然调度**之后**被改动（dispatcher 每次连接都会重新
`source /etc/asteria-drive/staging-dispatch.conf`，见
`scripts/bootstrap-staging-host.sh` 的 dispatcher heredoc 第 4 行，因此"进程缓存了旧
配置"这一解释不成立）、另有 dispatcher 在 `authorized_keys` 的 `command=` 中、或校验
路径与配置键不同。

**修复**：**停止，不上手**。这类矛盾说明对系统的理解有缺口，任何修改都可能
加剧问题。需要：

1. 确认 `/etc/passwd` 中 `asteria-deploy` 的 shell 与 `authorized_keys` 的
   `command=` 指向哪个文件；
2. 确认该文件摘要与配置是否一致；
3. 若仍矛盾，按"未知故障"上报，另立调查。

**授权**：只读调查可继续；任何写入需要重新评估。

### 形态 5：UNKNOWN

**观察特征**：文件缺失、配置不可读、容器不存在等。

**处理**：记录为 `UNKNOWN`，停止。**不得**用"重新 bootstrap"来掩盖未知状态——
在不知道当前状态的情况下覆盖，可能破坏唯一可用的回退路径。

## 4. 通用约束（所有形态）

| 约束 | 理由 |
| --- | --- |
| 不修改 immutable Release | `v0.2.0` 的 `isImmutable=true`；改制品会破坏 attestation |
| 不跳过 SHA-256 校验 | bootstrap 的摘要校验是唯一的制品完整性保证 |
| 不手工替换已发布制品 | 用 bootstrap 的 base64 输入路径，它带校验 |
| 不破坏 v0.1.1 回退合同 | `monitor-v0.1.1` / `recovery-v0.1.1` / releases.json 的 v0.1.1 项必须保留 |
| 不绕过 controller / verifier / schema 门禁 | 它们是 rollback 与部署的前置 |
| 不改 compose 摘要 | 主机 compose 必须与 bundle 的 `compose_sha256` 一致 |

## 5. 修复后的验证顺序（设计）

1. **只读**：`sha256sum` 全部文件，逐个 `MATCH`；
2. **只读**：`grep` 配置的六个键，接受集合符合形态 1 的期望；
3. **只读**：`stat` 属主与权限（`root:root 0755/0644`，lock 为 `0:$deploy_gid:660`）；
4. **被动**：等待下一次自然调度 Monitor（每小时 cron，实测间隔 2–11 小时）；
   - 期望 v0.2.0 不再 `dispatcher-rejected`；
   - 若仍失败，失败阶段应变为 `host-capacity` 或更后（说明 dispatcher 已修好，
     剩下的是容量）；
5. **被动**：等待下一次 Drill（周一 04:17Z）。

**不要**手动触发。手动触发会污染"自然调度行为"这条证据。

## 6. 与容量修复的先后关系

B1（容量）与 B2（dispatcher）是**独立**阻塞：

```
容量修复 ──┐
           ├──► v0.2.0 部署窗口
Phase B ───┘
```

- 先做容量：风险低、可逆性高、不影响 dispatcher；
- 后做 Phase B：风险高、需要维护窗口、会改写 root 文件。

**但顺序不是强制的**：如果 Phase B 先做，v0.2.0 monitor 会立刻在
`host-capacity` 失败——这本身是一次有价值的验证（证明 dispatcher 已修好）。
反之如果容量先做，v0.1.1 monitor 会通过，但 v0.2.0 仍被拒。

建议：**先容量、后 Phase B**，因为容量修复的失败模式更可控。

## 7. 授权清单

| 动作 | 授权类型 | 前置 |
| --- | --- | --- |
| P2 只读取证 | 主机只读授权 | 无 |
| 确认形态 | —（分析） | P2 完成 |
| Phase B bootstrap | 单独审批 + 维护窗口 + 双人 | 形态 1/2/3 确认；备份完成 |
| 手工编辑配置 | **不允许** | — |
| 重新拉取镜像 | 部署审批 | 仅在镜像缺失时 |

## 8. 本轮未做

未访问主机、未读取配置文件、未修改 dispatcher、未执行 bootstrap、
未部署、未回滚、未触发任何工作流、未创建 Release/tag。
