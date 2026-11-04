# Phase S2：Staging 故障定因（2026-11-04）

- 基线：`main` = `0bc4677889ecbb51dee3a2e4853c65c1a734fc56`（PR
  [#106](https://github.com/baicie/asteria-drive/pull/106) 合并后）
- 关联：[Phase S1/S1.1 证据](phase-s1-staging-20261103.md)、
  [只读诊断计划](phase-s1-staging-readonly-plan-20261104.md)
- 边界：未访问主机、未运行任何远端工作流、未读取 Secret、未修改环境。
  本文结论全部来自 GitHub Actions 日志、Artifact、仓库源码与 Git 历史。

## 0. 结论摘要

| 结论 | 标签 |
| --- | --- |
| `v0.2.0` 探针被 dispatcher 拒绝，stderr 恰为 `staging deploy command rejected` | **VERIFIED** |
| `v0.1.1` 探针实际运行，并在 `host-capacity` 阶段因**根文件系统使用率 86–89%** 退出 | **VERIFIED** |
| 触发的是 85% 使用率上限（最近两次 5 GiB 下限并未触发） | **VERIFIED** |
| 失败不是 `df` 解析缺陷：89 字节消息与 `verify_capacity` 的格式逐字节吻合 | **VERIFIED** |
| `#96`/`#97` 与本故障无关 | **VERIFIED** |
| 主机当前 dispatcher/脚本摘要、运行镜像版本、Phase B 是否执行 | **UNKNOWN** |
| Recovery Drill 的 `v0.1.1` 具体失败阶段 | **UNKNOWN** |
| 是否需要在主机上清理磁盘、清理到多少 | **BLOCKED**（需主机只读取证 + 授权） |

## 1. 本次调查的关键方法：哈希原像复原

S1.1 的分类器按设计不保留任何自由文本，只保留 stderr 的 SHA-256 与字节数。因此
S1 阶段把 89 字节消息标成了 UNKNOWN。

本轮用**受控的哈希原像搜索**把它解了出来，方法本身可复核：

1. 消息格式来自仓库源码 `scripts/monitor-staging.sh` 的 `verify_capacity`：

   ```bash
   printf 'staging monitor capacity failure: %s is %s%% used with %s KiB available\n' \
     "$label" "$used_percent" "$available_kib" >&2
   ```

2. 未知量只有 `used`（0–100）与 `avail`（0–9,999,999），且消息总长必须等于
   stderr 的 89 字节——这把搜索空间再压缩到 `len(label)+len(used)+len(avail) = 24`。
   对 `root filesystem`（15 字符），只剩三种组合：1+8、2+7、3+6 位。
3. 全空间穷举所有组合，与 Artifact 记录的 SHA-256 逐一比对。

**自校验**：程序先把一个已知明文
（`root filesystem is 86% used with 6000000 KiB available`，SHA-256
`8e386f01…`）放进目标集合，确认搜索能找回它；只有自校验通过，`no match` 才有意义。
（S1 阶段曾因哈希表键值写反而误判为「无匹配」，本轮已修正并用自校验锁住。）

## 2. Monitor 根因

### 2.1 `v0.2.0` 探针：dispatcher 拒绝（VERIFIED）

两次运行（`41719084766`、`41754371528`）及此前五次，`v0.2.0` 的
`attempt-v0.2.0.json` 完全一致：

```json
{
  "exit_code": 1,
  "classification": "dispatcher-rejected",
  "phase": null,
  "evidence": { "present": true, "size_bytes": 0, "sha256": "e3b0c442…" },
  "stderr":   { "present": true, "size_bytes": 32,
                "sha256": "1b39cdcbcdfd0380085e7e3ffff878cb0e97edb3afb8630236727d447d6a0164" }
}
```

`1b39cdcb…` 精确等于 `printf 'staging deploy command rejected\n'`。这条消息只由
dispatcher 的 `fail()` 产生，且 stdout 为空（0 字节）——说明远端命令在 `exec`
monitor 之前就被拒绝。

结合 `v0.1.1` 探针**能够运行**（见 2.2），可以确定：主机 dispatcher 的允许集合
接受 `v0.1.1` 摘要（`3ef39670…`），不接受 `v0.2.0` 摘要（`d1e186cb…`）。这与
仓库里「旧版 dispatcher 只比较单个 `monitor_script_sha256`」的实现一致，但**主机
上到底是哪个版本、配置文件写了什么，仍未读取**（UNKNOWN）。

按 S2 任务书要求的判断矩阵：

| 选项 | 判定 |
| --- | --- |
| A. 主机仍使用 v0.1.1 dispatcher，拒绝 v0.2.0 属预期版本限制 | **与全部观察一致**（v0.1.1 可运行、v0.2.0 被拒），但未读主机，不能标 VERIFIED |
| B. 已装 v0.2.0 dispatcher 但文件/配置摘要不匹配 | **不能排除**；若 `staging-dispatch.conf` 缺 `d1e186cb…` 一项也会得到同样现象 |
| C. 摘要正确但加载配置/调用路径异常 | **不能排除** |
| D. 证据不足 | 仅对「主机内部状态」成立；对「拒绝行为本身」已是 VERIFIED |

判定 A 与 B 的区别只需要一条只读命令：
`grep -E '^(monitor_script_sha256|monitor_v011_script_sha256)=' /etc/asteria-drive/staging-dispatch.conf`。

### 2.2 `v0.1.1` 探针：真实的容量门禁触发（VERIFIED）

两次新运行的 `attempt-v0.1.1.json`：

```json
{
  "exit_code": 1,
  "classification": "capacity-guard-failed",
  "phase": "host-capacity",
  "evidence": { "present": true, "size_bytes": 2199, "sha256": "e432f99f…" },
  "stderr":   { "present": true, "size_bytes": 89,
                "sha256": "cd7c7683…" }
}
```

89 字节 stderr 的原像复原结果：

| Run | 时间（UTC） | used | available | 触发 85% 上限 | 触发 5 GiB 下限 |
| --- | --- | --- | --- | --- | --- |
| `41400035337` | 2026-11-02T06:27Z | 87% | 5,227,464 KiB（4.99 GiB） | 是 | 是 |
| `41489190998` | 2026-11-02T17:14Z | 87% | 5,122,288 KiB（4.88 GiB） | 是 | 是 |
| `41549153916` | 2026-11-02T23:56Z | 88% | 5,068,908 KiB（4.83 GiB） | 是 | 是 |
| `41593365487` | 2026-11-03T06:59Z | 88% | 5,004,688 KiB（4.77 GiB） | 是 | 是 |
| `41662858662` | 2026-11-03T15:20Z | 89% | 4,597,900 KiB（4.38 GiB） | 是 | 是 |
| `41719084766` | 2026-11-03T21:08Z | 86% | 5,605,908 KiB（5.35 GiB） | 是 | 否 |
| `41754371528` | 2026-11-04T01:29Z | 86% | 5,650,912 KiB（5.39 GiB） | 是 | 否 |

自洽性检验：由 `avail/(1-used)` 反推的总容量在 37.6–40.3 GiB 之间，与
2026-08-16 记录的总量 41,020,608 KiB（39.1 GiB）一致；同期记录的 available
（6,242,176 KiB）与该总量也吻合。7 次独立运行给出同一量级的数值，进一步排除
偶然碰撞。

**因此：**
- `df` 成功执行、输出满足解析器要求（否则脚本会在 `capture_host_filesystem`
  就 `exit 1` 且**不会**打印这条消息）；
- 文件系统身份是根文件系统（消息里的 label 就是 `root filesystem`）；
- 使用率 86–89% **超过** 85% 上限 → 门禁判定正确；
- 最近两次可用空间 5.35/5.39 GiB **高于** 5 GiB 下限，所以当前触发条件**只有**
  使用率。

> 注意：`capacity-guard-failed` 这个分类在 S1.1 里只由「阶段名」推出，
> 本轮才用原像复原证实它确实是容量消息。分类标签本身仍不等于根因——根因是
> **主机根文件系统使用率达到 86–89%**，这是本轮新增的 VERIFIED 事实。

### 2.3 与 `#96`/`#97` 无关（VERIFIED）

```
git diff --name-only fb0e5e0 9d9f63d | grep -iE 'staging|infra/|scripts/'   → 无输出
```

首次 monitor 失败（2026-10-23T07:42Z）早于 #96 的任何提交；合并后
2026-11-03T06:59Z 的失败与合并前同形。

## 3. `df` 解析兼容性（VERIFIED：无缺陷）

用仓库里的 `parse_filesystem_output` 原始代码跑真实格式样例（离线）：

| 输入形态 | 结果 |
| --- | --- |
| GNU coreutils ext4 / overlay / device-mapper（名称很长） | 正确解析 |
| BusyBox `df -P` | 正确解析 |
| Capacity 列无 `%` | 正确解析 |
| 尾部空行 / CRLF | 正确解析 |
| 文件系统名含空格、表格前有 warning 行、`Available` 为 `-`、千位分隔符、两行数据 | 全部 fail-closed（返回 1） |

结论：解析器对真实 Linux `df` 输出兼容，对异常输出保守拒绝。**不存在解析缺陷**，
因此本轮不提交解析修复。

## 4. Recovery Drill

| Run | 日期 | 观察 | 标签 |
| --- | --- | --- | --- |
| `40305621267` | 2026-10-26 | 两版探针均 `probe_exit=1`；stderr 32 字节，`1b39cdcb…` | VERIFIED：dispatcher 拒绝 |
| `41445462781` | 2026-11-02 | `v0.2.0` 在 `source-identity` 失败（version mismatch，符合预期：主机 compose 为 v0.1.1），v0.1.1 随后失败，stderr **0 字节**，evidence 2299 字节 | 现象 VERIFIED；v0.1.1 具体阶段 UNKNOWN |

为什么 `v0.1.1` 的 drill 阶段仍 UNKNOWN：recovery 脚本的
`verify_capacity` **不打印任何消息**（只返回非零），而 `source-identity` 的
失败路径也是静默 `exit 1`。0 字节 stderr 与两者都相容，无法区分。

对 evidence 字节数的模拟（用 v0.1.1 recovery 脚本的真实 writer，逐阶段）：

```
initialize                      2295
source-identity                 2300
capacity-before                 2300
prepare-isolation               2302
（实际观测 2299）
```

差异在 1–3 字节内，不足以判定；**不据此下结论**。要定论需要主机侧只读取证或下一次
带分类器的 drill 运行（下一次自然调度为 2026-11-09T04:17Z）。

## 5. 仍未确定的远端事实

| 项 | 需要的只读证据 |
| --- | --- |
| dispatcher 实际摘要与配置 | `sha256sum /usr/local/libexec/asteria-staging-dispatch`；`grep -E '^(monitor_script_sha256\|monitor_v011_script_sha256)=' /etc/asteria-drive/staging-dispatch.conf` |
| monitor/recovery 脚本是否安装 v0.2.0 变体 | 同路径 `sha256sum` + `stat` |
| Phase B bootstrap 是否执行 | 由上面两组摘要推断：无 `-v0.1.1` 变体 / 无 controller / 无 `staging-releases.json` ⇒ 未执行 |
| 当前运行镜像版本 | `docker inspect --format '{{.Config.Image}}' asteria-drive-staging-api-1` |
| 容量占用来源 | `df -Pk -- /`、`docker system df`、`du -x -d1 -h /` |

命令与判定规则见
[只读诊断计划](phase-s1-staging-readonly-plan-20261104.md)。

## 6. 本轮未做

- 未访问主机，未运行 `df`、`docker`、`systemctl` 或任何远端命令。
- 未触发 monitor / drill / deploy / rollback 工作流。
- 未修改容量阈值、未清理磁盘、未改任何脚本固定点。
- 未创建 Release / tag，未合并任何 PR。
