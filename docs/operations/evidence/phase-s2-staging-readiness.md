# Phase S2：Staging 部署准入（2026-11-04）

- 基线：`main` = `0bc4677889ecbb51dee3a2e4853c65c1a734fc56`
- 关联：[Phase S2 诊断](phase-s2-staging-diagnosis.md)、
  [只读诊断计划](phase-s1-staging-readonly-plan-20261104.md)
- 边界：本轮不发布、不升级、不部署、不调用任何远端工作流。

## 1. `v0.2.0` 准入结论：**BLOCKED**

### 1.1 制品与固定点（VERIFIED）

| 项 | 值 | 状态 |
| --- | --- | --- |
| Release | `v0.2.0`，`isImmutable=true`，2026-10-22 发布 | VERIFIED |
| 资产 | `checksums.txt`、`release-manifest.json`、linux-amd64/arm64 tar.gz、Web 静态包、两份 SBOM | VERIFIED |
| source commit | `5169da638beac679c9cfed66757159474de879dc` | VERIFIED |
| OCI digest | `sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853` | VERIFIED（bundle 与 workflow 一致） |
| Compose digest | `cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005` | VERIFIED |
| monitor / recovery / deploy 脚本摘要 | `d1e186cb…` / `91c45a19…` / `007eb3a3…` | VERIFIED（三处一致） |
| Schema 版本 | 3 | VERIFIED |
| 是否含 #96/#97 | 否（源提交早于两次合并） | VERIFIED |

### 1.2 准入阻塞项

| # | 阻塞 | 证据 | 标签 |
| --- | --- | --- | --- |
| B1 | 主机根文件系统使用率 86–89%，超过 85% 门禁 | 10 次 monitor（2026-11-02 至 2026-11-04）的 89 字节 stderr 已精确复原（S2 诊断 2.2、第 8 节） | **VERIFIED 现象** |
| B2 | 主机 dispatcher 拒绝 `v0.2.0` monitor 摘要 | 32 字节 stderr = dispatcher 拒绝文本；同一次运行中 `v0.1.1` 摘要被接受 | **VERIFIED 行为** |
| B3 | 主机实际文件摘要、运行镜像、Phase B 状态（`PHASE_B_OPERATIONAL_STATE_VERIFIED` 与 `PHASE_B_EXECUTION_HISTORY_VERIFIED`） | 未读取主机 | **UNKNOWN** |
| B4 | 监控与恢复无成功运行 | 2026-10-23 起 monitor 全红，最近一次为 `41936343589`（2026-11-04T23:46Z）；drill 两次失败 | VERIFIED |

B1 与 B2 是两个**独立**阻塞：

- 即使完成 Phase B bootstrap（解决 B2），`v0.2.0` monitor 仍会在 `host-capacity`
  失败，因为主机磁盘现状不满足门禁；
- 即使清理磁盘（解决 B1），`v0.2.0` 探针仍会被旧 dispatcher 拒绝。

两者都必须解决，`v0.2.0` 才能进入部署窗口。

### 1.3 部署前必须依次完成

1. **主机只读取证**（计划第 3–6 节）：确认 B2 是「旧 dispatcher」还是「配置缺项」，
   并取得容量占用来源。
2. **容量处置**（需要授权）：由操作者在主机上清理到 `Used% ≤ 85` 且
   `Available ≥ 5 GiB`。本轮**不**给出删除命令，也不执行任何清理。
3. **Phase B bootstrap**：按
   [bootstrap runbook](../../acceptance/staging-v0.2.0-bootstrap-runbook.md)
   执行，需要维护窗口与审批。
4. **验证 monitor 正常路径**：一次自然调度即可，不必手动触发。
5. **Recovery Drill 前置**：同一容量门禁 + 源环境健康；下一次自然调度
   2026-11-09T04:17Z。
6. **回滚可行性**：`v0.2.0` 的回滚依赖 host controller 与 `controller-state.json`，
   两者都由 Phase B 安装；在 Phase B 之前不存在回滚路径。

## 2. 下一版本（当前 `main`）准入：**BLOCKED**

### 2.1 `v0.2.0` 之后新增的内容（VERIFIED）

| 变更 | 文件 | Schema 影响 |
| --- | --- | --- |
| `0004_upload_part_checksum.sql` | 新建表 `upload_part_signature` | 3 → 4 |
| `0005_part_signature_audit_only.sql` | 仅 `COMMENT ON TABLE` | 4 → 5 |
| `ListedPart` 契约 | 移除 `content_sha256`，新增必填 `reusable` + `storage_sha256` | API 破坏性变更 |
| Web 上传 | 结构化失败、取消触达服务端、跨刷新恢复 | 无 |
| S3 分片 checksum | `ASTERIA_S3_TRUST_PART_CHECKSUMS` 默认 false | 无 |

### 2.2 为什么不能先说下一版

1. **Schema 合同三处同时失效**：`staging-releases.json`、两个 `bundle.json`、
   monitor/recovery 脚本的 `schema_version=3` 断言都会与新库的 5 冲突。
2. **没有可回滚基线**：回滚目标是 `v0.2.0`，而它自己尚未在 staging 验证过。
3. **破坏性 API 变更无外部消费者清单**：仓库内已确认无其他消费者，但这是
   「仓库内」的结论，不是生产事实。

### 2.3 下一版本需要独立的发布候选

若要做下一版本，至少需要：

- 新的 Release 候选（不可变、带 digest 与 SBOM）；
- 更新 `staging-releases.json` 与两份 `bundle.json` 的 `schema_version=5`；
- 更新 monitor/recovery 脚本内的 `schema_version` 期望；
- 重新固定全部脚本与 compose 摘要（workflow env、bootstrap、bundle 三处必须一致）；
- 明确迁移 `0004`/`0005` 在 staging 数据上的执行与回滚策略（本轮不设计）。

## 3. 推荐路径

**A：先完成 `v0.2.0` 的 Staging 验收，再准备下一版本。**

理由：`v0.2.0` 与主机现有 schema 3 兼容，是唯一有完整仓库侧准入证据的版本；
它的两个阻塞（容量、dispatcher）都能在不改代码的前提下解决。

顺序与依赖：

```
主机只读取证 ──► 容量处置 ──► Phase B bootstrap ──► monitor 自然调度转绿 ──► v0.2.0 部署窗口
      │                              │
      └── 确认 B2 类型 ──────────────┘
```

每一步都需要单独授权；本轮只交付前一步的证据与方案。

## 4. 与 `v0.2.0` 应用代码的关系（避免过度表述）

本轮**没有**证据表明 `v0.2.0` 应用代码存在故障：两次失败都发生在应用代码执行之前
（dispatcher 拒绝；容量门禁在容器检查之前）。但同样**没有**证据表明它健康——
容器从未被检查过。正确表述是：

> 已观察到因工作流固定点切换与主机容量引发的部署合同问题；`v0.2.0` 应用代码的
> 正确性在 staging 上**尚未被验证**（BLOCKED，非 VERIFIED 通过）。

## 5. 下一阶段操作清单（按风险与依赖排序）

| 序 | 操作 | 风险 | 依赖 | 授权 |
| --- | --- | --- | --- | --- |
| 1 | 主机只读取证（计划第 3–4 节：摘要 + 属主） | 极低（`sha256sum`/`stat`） | 无 | 需主机 root 维护会话 |
| 2 | 主机只读取证（计划第 5–6 节：容器镜像 + 容量分解） | 低（`du` 有 I/O） | 1 完成 | 需主机 root 维护会话 |
| 3 | 容量处置：清理到 `Used% ≤ 85`、`Available ≥ 5 GiB` | **中高**：误删数据不可逆 | 2 完成，明确占用来源 | 需单独授权；禁止 prune 数据卷 |
| 4 | Phase B bootstrap | **高**：改写 root-owned dispatcher/脚本/authorized_keys | 3 完成、维护窗口、双人复核 | 需单独审批 |
| 5 | 观察自然调度 monitor 转绿 | 无（不触发） | 4 完成 | 无需额外授权 |
| 6 | `v0.2.0` 部署窗口 | 高：首次部署新版本 | 5 绿、备份与回滚就绪 | 需单独审批 |
| 7 | Recovery Drill 验收 | 低（隔离环境） | 6 完成 | 下一次自然调度即可 |

**建议**：1 与 2 可以一次维护会话完成，中间不改变任何状态。

## 6. 诊断能力建议（P2，**本轮不实现**）

分类器当前只记录 `classification` 与 `phase`。本轮为了拿到两个整数
（`disk_used_percent`、`disk_available_bytes`），不得不做一次受控的哈希原像搜索。
这两个值是**非敏感整数**，且已经在探针的 evidence JSON 里；把它们加进 attempt record
可以让下一次容量失败自解释，而不必再复原哈希。

不本轮实施的原因：这会再次改变刚通过审查的证据 Schema（v2），属于新的一轮变更，
应在独立 PR 中做，并同步更新两处内联代码与反例测试。建议实施时同时把
`schema` 升到 v3，避免消费方误判。

## 7. 遗留风险

- 容量门禁是「使用率 > 85%」而非「可用空间不足」触发；清理到 85% 边缘可能很快
  再次触发。建议目标留出余量（具体数值由操作者决定，本轮不设新阈值）。
- 10 次读数来自 2026-11-02 至 2026-11-04，趋势从 87% 升至 89% 后回落到 86% 并在
  86% 稳定；是否持续增长需要主机侧 `df` 时序确认，不能从 10 个点外推。
- 「不是 `df` 解析缺陷」的结论只在 S2 诊断第 3 节列出的 12 种输入形态与当前脚本
  版本范围内成立；主机原始 `df` 输出从未被读取。
- Recovery Drill 的 v0.1.1 失败阶段仍 UNKNOWN；在容量修复前，drill 大概率仍会失败，
  但失败阶段可能是 `source-identity` 也可能是 `capacity-before`。
- `CI / web-live` 仍未设为 required check（沿用 R3.1 结论）。
