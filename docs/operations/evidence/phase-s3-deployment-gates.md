# Phase S3：`v0.2.0` 部署 Gate 矩阵（2026-11-05）

- 基线：`main` = `17d9e786924426354f4c4403e5238473cd46f179`
- 状态：**PREPARED / HOST_EVIDENCE_BLOCKED**
- 关联：[现场取证清单](phase-s3-staging-host-evidence.md)、
  [容量恢复方案](phase-s3-capacity-remediation-plan.md)、
  [dispatcher 修复方案](phase-s3-dispatcher-remediation-plan.md)
- 标签：`VERIFIED`（有独立证据）/ `BLOCKED`（有明确阻塞）/ `UNKNOWN`（证据不足）/
  `NOT_RUN`（尚未执行）

## 0. 总体判定

**`v0.2.0`：`DEPLOYMENT_BLOCKED`。**

七项 Gate 中 0 项 VERIFIED 通过、4 项 BLOCKED、3 项 UNKNOWN、0 项 NOT_RUN。
**准备了修复方案不等于阻塞已解决。**

## 1. Gate 矩阵

| Gate | 判定目标 | 当前状态 | 依据 | 解除条件 |
| --- | --- | --- | --- | --- |
| **G1** | Root filesystem 容量门禁 | **BLOCKED** | 11 次 Monitor 全部 > 85%（86–89%），最近 `41984310536` = 86% / 5,538,200 KiB | 主机实测 `Used% ≤ 85` 且 `Available ≥ 5 GiB`，并留出建议余量 |
| **G2** | dispatcher 接受 v0.2.0 monitor 摘要 | **BLOCKED** | v0.2.0 每次 `dispatcher-rejected`（32 字节 `1b39cdcb…`）；同 run 的 v0.1.1 被接受 | 自然调度中 `attempt-v0.2.0.json` 不再是 `dispatcher-rejected` |
| **G3** | 脚本与 Compose 固定点 | **VERIFIED（仓库侧）** | bundle / workflow / 本地 `sha256sum` 三处一致：monitor `d1e186cb…`、recovery `91c45a19…`、compose `cba2ba28…`、controller `754ba671…`、verifier `77e537f7…` | 主机侧摘要 `MATCH`（待 P2） |
| **G4** | 当前镜像及容器健康 | **UNKNOWN** | 探针从未走到 `container-identity`；主机从未读取 | P3 取证：三容器 `running` + `healthy`，`.Config.Image` 可判定版本 |
| **G5** | Phase B operational state | **UNKNOWN** | 未读取主机文件/配置；仅有"接受集合"推论（指向 `7c6565b` 代际） | ①文件摘要 ②配置接受集合 ③自然调度行为三者齐全 |
| **G6** | Recovery 前置条件 | **BLOCKED** | Drill `40305621267`、`41445462781` 均失败；且 drill 同样受容量门禁约束 | G1 解除 + 一次自然调度的 Drill 通过 |
| **G7** | 数据与回滚安全 | **BLOCKED** | 回滚依赖 host controller 与 `controller-state.json`（`scripts/staging-release-controller.py:227`），两者都由 Phase B 安装；Phase B 前不存在回滚路径 | G5 达成 + `controller-state.json` 存在且可读 |
| **G8** | v0.2.0 staging 部署准入 | **BLOCKED** | 依赖 G1–G7 | 全部解除 |

### 1.1 关于 G3 的说明

G3 是**唯一**仓库侧已 VERIFIED 的 Gate，但它只证明"仓库里的固定点互相一致"，
**不**证明主机上安装的就是这些文件。主机侧部分属于 G5，仍为 UNKNOWN。
把 G3 写成"通过"时必须在括号里注明「仓库侧」。

### 1.2 关于 G7 的重要性

G7 是最容易被忽略的 Gate：在没有 controller 的主机上，`v0.2.0` 部署失败后
**无法自动回滚**。这意味着 G1/G2 的修复顺序不能只看风险高低，
还必须保证在任何部署尝试之前 G7 已经成立。

## 2. 依赖关系

```
G3（仓库侧固定点，已 VERIFIED）
      │
      ├──► G2 ──► G5 ──► G7
      │      （dispatcher）  （Phase B）   （回滚路径）
      │
G1（容量）──────► G6（Recovery）
      │
      └──────────────► G4（容器健康，独立）
                              │
                              ▼
                          G8（部署准入）
```

- G1 与 G2 互相独立，可以并行修复；
- G5 依赖 G2 的观察（自然调度行为是 G5 的第③项）；
- G7 依赖 G5（controller 由 Phase B 安装）；
- G8 依赖全部。

## 3. 逐 Gate 的取证缺口

| Gate | 缺什么证据 | 由哪一步补 |
| --- | --- | --- |
| G1 | 主机 `df` 实测值 + 占用来源 | S3-P1（第 3 节） |
| G2 | 主机 `staging-dispatch.conf` 的接受集合 | S3-P2（第 4.3 节） |
| G3 | 主机文件摘要 | S3-P2（第 4.1 节） |
| G4 | 三容器状态与镜像身份 | S3-P3（第 6 节） |
| G5 | ① + ② | S3-P2；③ 需下一次自然调度 |
| G6 | 容量 + 一次成功 Drill | G1 修复后被动等待 |
| G7 | controller / `controller-state.json` 是否存在 | S3-P2（第 4.1 节） |

## 4. 状态标签的使用规则

| 标签 | 允许的写法 | 禁止的写法 |
| --- | --- | --- |
| VERIFIED | "仓库侧三处摘要一致（VERIFIED）" | "固定点已通过"（省略"仓库侧"） |
| BLOCKED | "G2 BLOCKED：v0.2.0 被拒" | "G2 已定位，即将解除" |
| UNKNOWN | "G4 UNKNOWN：未读取主机" | "容器大概率健康" |
| NOT_RUN | "Drill 本轮未执行" | "Drill 预计通过" |

**特别禁止**：把"已准备修复方案""已完成取证清单""已排除某代际"写成阻塞解除。
本轮三项都做了，G1/G2 仍是 BLOCKED。

## 5. 解除路径与预计需要的授权

| 序 | 动作 | 解除 | 授权 |
| --- | --- | --- | --- |
| 1 | S3-P1 只读取证 | 补齐 G1 的实测值 | 主机只读 |
| 2 | S3-P2 只读取证 | 补齐 G2/G3 主机侧、G5①②、G7 存在性 | 主机只读 |
| 3 | S3-P3 只读取证 | 补齐 G4 | 主机只读 |
| 4 | 容量处置 | G1 | 单独授权（破坏性） |
| 5 | Phase B bootstrap | G2/G5/G7 | 单独审批 + 维护窗口 |
| 6 | 等待自然调度 Monitor | G2 的行为验证 | 无需（被动） |
| 7 | 等待自然调度 Drill | G6 | 无需（被动） |
| 8 | `v0.2.0` 部署 | G8 | 单独审批 |

第 1–3 步可以在**一次维护会话**内完成，中间不改变任何状态。

## 6. 与 v0.1.1 的关系

`v0.1.1` 当前**可运行**（Monitor 的 v0.1.1 探针被 dispatcher 接受），
但同样卡在容量门禁。因此：

- `v0.1.1` 不是"健康基线"，只是"能被执行"；
- 即使决定暂不升级 v0.2.0，G1 的容量问题仍需修复，否则 Monitor 与 Drill
  会持续失败。

## 7. 本轮未做

未访问主机、未执行任何 Gate 的现场验证、未部署、未回滚、未修改阈值、
未触发工作流、未创建 Release/tag。

本矩阵的所有 BLOCKED/UNKNOWN 在获得主机只读授权并完成取证前**不会改变**。
