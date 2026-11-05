# Staging 只读诊断计划（2026-11-05，Phase S2 终审版）

- 前置：[Phase S1/S1.1 证据](phase-s1-staging-20261103.md)、
  [Phase S2 诊断](phase-s2-staging-diagnosis.md)、PR
  [#106](https://github.com/baicie/asteria-drive/pull/106)
- 状态：**等待授权**。本文件只是计划，尚未执行任何一条主机命令。
- 边界：全部步骤只读；不部署、不回滚、不重启、不写入主机任何文件、不读取
  Secret 内容、不运行有副作用的远端工作流。

## 0. 本版改了什么（相对 2026-11-04 初版）

| 变更 | 原因 |
| --- | --- |
| 删除 `docker exec … psql` 一段 | 违反「不执行 `docker exec`」；TLS 已有 2026-08-16 证据覆盖 |
| 删除 `docker compose -f … ps`，改用 `docker ps` | 避免读取 Compose 工程配置，减少只读面 |
| 摘要改为「本地比对、只记相等/不等」 | 避免把可能含低熵内容的文件摘要写进仓库或聊天 |
| 每条命令补预期输出、判定依据、权限、失败处理 | 落实逐条安全审查 |
| 增加容量分解命令 | S2 已把失败收敛到根文件系统容量，需要定位占用来源 |
| 增加「不绕过 dispatcher」说明 | 明确本计划不使用 deploy 凭据，也不改写 dispatcher |
| 删除 `findmnt … OPTIONS`，改用 `df -PTk`（2026-11-05） | 挂载选项可能含内部地址或凭据（`addr=`、`username=`、`password=`、`credentials=`），且与容量判定无关 |
| Phase B 判定拆成「当前文件状态」与「历史执行事实」（2026-11-05） | 现有文件集合不能证明 bootstrap 曾经执行，必须与配置和实际行为互相验证 |

## 1. 已确认、无需重复取证的事实

以下由 Phase S2 在仓库与 Artifact 侧完成（**VERIFIED**），主机侧只需确认现时值：

1. `v0.2.0` 探针被 dispatcher 拒绝：32 字节 stderr，SHA-256
   `1b39cdcb…` = `printf 'staging deploy command rejected\n'`。
2. `v0.1.1` 探针**实际运行**到 `host-capacity` 后退出，89 字节 stderr 的 SHA-256
   与 `staging monitor capacity failure: root filesystem is <used>% used with
   <avail> KiB available\n` 精确匹配（见 S2 诊断文档，含 10 次运行的还原值）。
3. 最近两次（`41719084766`、`41754371528`）为 **86% used / 5.35–5.39 GiB free**；
   2026-11-04 的三次自然调度（`41807941954`、`41883481449`、`41936343589`）仍为
   **86% used / 5.33–5.40 GiB free**：触发的是 **85% 使用率**上限，不是 5 GiB 下限。
4. SSH 传输、ED25519 主机密钥、授权、dispatcher `status` 路径、`v0.1.1` monitor
   脚本摘要（`3ef39670…`）均正常。

因此本计划只剩两件事要在主机上确认：**dispatcher/脚本的现时摘要**，以及
**容量的现时值与占用来源**。

## 2. 执行前提

1. 用户对本计划**逐项授权**，并指定维护窗口（建议在两次 `:23Z` 调度之间）。
2. 由具备主机 **root 只读**权限的操作者执行；本文不包含、也不索取任何私钥或密码。
3. **不绕过** Staging 的受限 SSH dispatcher：本计划使用独立的 root 维护会话，
   不复用 `asteria-deploy` 的受限密钥，也不修改 `authorized_keys` 或 dispatcher 配置。
4. 全程只读：不出现 `docker run`、`docker exec`、`docker rm`、`systemctl restart`、
   `compose up/down`、`flock` 竞争或任何文件写入。
5. 输出保存在本地；**不要**粘贴到 Issue/PR/Artifact。摘要类结果只记录
   「与期望值相等 / 不等」，不记录原始摘要值。

## 3. 步骤 A：文件身份与属主（对应 U1、U3）

**权限**：root。**耗时**：约 2 分钟。**副作用**：无。

```bash
hostname; date -u; id

for path in \
  /usr/local/libexec/asteria-staging-dispatch \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-monitor-v0.1.1 \
  /usr/local/libexec/asteria-staging-recovery \
  /usr/local/libexec/asteria-staging-recovery-v0.1.1 \
  /usr/local/libexec/asteria-staging-controller \
  /usr/local/libexec/staging_runtime_verifier.py \
  /etc/asteria-drive/staging-dispatch.conf \
  /etc/asteria-drive/staging-releases.json \
  /opt/asteria-drive/staging/compose.yaml; do
  if [[ -f "$path" && ! -L "$path" ]]; then
    printf '%s %s\n' "$(sha256sum "$path" | awk '{print $1}')" "$path"
  else
    printf 'MISSING-OR-SYMLINK %s\n' "$path"
  fi
done

stat -c '%U:%G %a %n' \
  /usr/local/libexec/asteria-staging-dispatch \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-monitor-v0.1.1 \
  /run/asteria-drive /run/asteria-drive/staging.lock \
  /etc/tmpfiles.d/asteria-drive-staging.conf 2>&1
```

| 项 | 内容 |
| --- | --- |
| 预期输出 | 每个文件一行 `<sha256> <path>`，或 `MISSING-OR-SYMLINK`；`stat` 每行 `<owner>:<group> <mode> <path>` |
| 判定依据 | 逐项与第 4 节期望值**在本地**比较；只记录「相等 / 不等 / 缺失」，不把主机摘要写进任何文档 |
| 权限要求 | root（`/usr/local/libexec` 与 `/etc/asteria-drive` 均 root 所有） |
| 失败处理 | 任一 `MISSING-OR-SYMLINK` 即为确定差异，停止后续步骤，按第 6 节整理差异清单 |

**只读替代**：若 `staging-dispatch.conf` 被判定为不宜比对摘要（它含脚本摘要与路径，
不含 Secret），可改为

```bash
grep -E '^(monitor_script_sha256|monitor_v011_script_sha256|recovery_script_sha256|recovery_v011_script_sha256)=' \
  /etc/asteria-drive/staging-dispatch.conf
```

只读取这四个键的值，用于回答「主机接受哪两个脚本摘要」；不要输出整个文件。

## 4. 期望值对照表

v0.1.1（`infra/docker/staging/releases/v0.1.1/bundle.json`）：

```
compose       aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637
monitor       3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279
recovery      3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a
deploy script cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044
image         sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842
schema        3
```

v0.2.0（`infra/docker/staging/releases/v0.2.0/bundle.json`）：

```
compose       cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005
monitor       d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69
recovery      91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4
deploy script 007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f
image         sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853
schema        3
```

### 4.1 判定表：只描述**当前文件状态**

| 观察 | 对当前文件状态成立的结论 | 不能据此推断 |
| --- | --- | --- |
| 无 `-v0.1.1` 变体、无 controller/verifier/releases.json，`monitor` = `3ef39670…` | 主机文件集合仍停在 v0.1.1 锚点 | 不能推断 Phase B **从未**执行（可能执行过又被回滚或部分覆盖） |
| 存在全部 v0.2.0 文件且摘要相符 | 当前文件集合与 v0.2.0 bundle 一致 | 不能单独证明 bootstrap 曾**完整执行**（文件可能被手工复制或分步放置） |
| 存在部分 v0.2.0 文件或摘要不符 | 当前文件集合与两个版本都不一致 | 不能判断是执行中断、手工复制还是回滚残留 |
| `staging-dispatch.conf` 只含一个 `monitor_script_sha256` 键 | 当前配置是旧版单摘要形态 | 不能证明**正在运行的 dispatcher 进程**加载的就是这份文件 |
| `staging-dispatch.conf` 缺失或不可读 | 无法判定配置 | 不得推断任何 Phase B 结论 |

**「Phase B 是否执行过」是历史事实，不是当前文件状态。** 本计划不把文件集合当作执行记录；
若要回答历史问题，还需要 bootstrap 自身的日志、文件 mtime 序列或维护记录等独立证据，
本轮不采集、也不假设它们存在。

### 4.2 配置必须与**实际行为**互相验证

S2 已确认的行为（VERIFIED）：同一分钟内，v0.1.1 monitor 摘要被接受并**实际执行**
（产出 evidence 并走到 `host-capacity`），v0.2.0 monitor 摘要在 `exec` 之前被拒绝
（32 字节拒绝文本、stdout 0 字节）。因此在主机上必须把**配置内容**与**该行为**对照：

| 配置观察 | 与 S2 行为是否一致 | 允许的结论 |
| --- | --- | --- |
| 配置接受 v0.1.1、不接受 v0.2.0 | 一致 | 现时配置即可解释行为；Phase B 历史仍为 UNKNOWN |
| 配置接受 v0.2.0，行为却拒绝 | **矛盾** | 配置不是权威来源（未重新加载、另有 dispatcher、或校验路径不同），停止并上报，不得据此下结论 |
| 配置接受两个摘要，且自然调度中 v0.2.0 不再被拒 | 一致 | Phase B 可能已执行且生效，但仍需文件摘要与时间戳佐证 |
| 配置缺失或键名与 dispatcher 支持的不符 | 无法对照 | 记录为 UNKNOWN，不推断 |

**「Phase B 已执行」只有在三条同时成立时才可写入结论**：
① 文件集合与 bundle 摘要一致；② dispatcher 配置接受 v0.2.0 摘要；③ 实际行为接受
v0.2.0 摘要（即 v0.2.0 探针能产出 evidence，而不是 32 字节拒绝文本）。
缺任一条，结论一律为 UNKNOWN。

条件 ③ **不在只读会话中主动制造**：本计划不执行 v0.2.0 探针。它由下一次自然调度
（每小时 `:23Z`）的 `Monitor staging` 运行被动产生——如果 Phase B 已生效，该 run 的
`attempt-v0.2.0.json` 就不再是 `dispatcher-rejected`。因此只读会话的产出是
「① 与 ②」，③ 需要等待一次自然调度后另行核对。

## 5. 步骤 B：运行身份（对应 U2）

**权限**：root 或 docker 组。**耗时**：约 1 分钟。**副作用**：无。

```bash
docker ps --filter name=asteria-drive-staging --format \
  '{{.Names}} | {{.Image}} | {{.Status}}'

docker inspect --format \
  '{{.Name}} | {{.Config.Image}} | {{.Image}} | {{.State.Status}} | {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
  asteria-drive-staging-api-1 \
  asteria-drive-staging-postgres-1 \
  asteria-drive-staging-seaweedfs-1 2>&1

docker volume ls --filter name=asteria-drive-staging --format '{{.Name}} {{.Driver}}'
```

| 项 | 内容 |
| --- | --- |
| 预期输出 | 每容器一行，5 个字段用 ` \| ` 分隔；`docker ps` 显示运行状态 |
| 判定依据 | `.Config.Image` = `…@sha256:2b73f8a7…` → v0.1.1；`…@sha256:964e71a4…` → v0.2.0；其他 → 未知版本，停止并上报 |
| 权限要求 | root 或 `docker` 组成员 |
| 失败处理 | 容器不存在或 `Status != running` 是独立故障，记录后停止，不要重启 |

**明确不采集**：`.Config.Env`、Mounts 细节、完整 `docker inspect` JSON、容器日志。

## 6. 步骤 C：容量现状与占用来源（对应已确认的失败点）

**权限**：root。**耗时**：约 3 分钟（`du` 视磁盘大小）。**副作用**：只读，
但 `du` 会产生 I/O；建议在维护窗口执行。

```bash
df -Pk -- /
df -PTk -- /       # Type 列替代 findmnt 的 FSTYPE；不采集 SOURCE/OPTIONS
df -Pi -- /
cat /proc/loadavg
free -m

# 占用来源（只读；按需二选一，不要同时跑）
docker system df
journalctl --disk-usage 2>/dev/null || true
du -x -d1 -h / 2>/dev/null | sort -h | tail -20
```

| 项 | 内容 |
| --- | --- |
| 预期输出 | `df -Pk` 一行数据；`df -PTk` 多一列 `Type`；`docker system df` 的 Images/Containers/Local Volumes/Build Cache 四项；`du` 的目录排行 |
| 判定依据 | `Used%` > 85 或 `Available` < 5,242,880 KiB → 门禁判定正确，问题在容量；两者都满足 → 与 S2 时点不同，需用当时证据复核 |
| 权限要求 | root（`journalctl`、`du` 全盘） |
| 失败处理 | `df -T` 不被支持（如 BusyBox）→ 跳过类型列，不影响容量判定；`df -Pk` 输出不可解析 → 记录原始输出，不推断；`du` 超时 → 中止，不影响结论 |

**明确不采集挂载选项**（2026-11-05 修正）：不使用 `findmnt -o OPTIONS`、`cat /proc/mounts`
或 `mount` 输出。挂载选项可能包含内部地址或凭据（`addr=`、`username=`、`password=`、
`credentials=`、Kerberos principal 等），而容量判定只需要 `Used%`、`Available` 与文件系统
类型，后者由 `df -PTk` 的 `Type` 列提供。`df` 的 `Filesystem` 列对根文件系统是设备名，
不含凭据；若观察到网络文件系统，只记录类型，不记录 SOURCE。

**禁止**：`docker system prune`、`docker volume prune`、`docker builder prune`、
删除日志、删除 PostgreSQL/SeaweedFS 数据、扩容、修改阈值。

## 7. 步骤 D：不执行的动作（记录用）

- 不手动触发 `Monitor staging` / `Drill staging recovery` / `deploy-staging` /
  `rollback-staging`。
- 不执行 `bootstrap-staging-host.sh`。
- 不做数据库迁移、恢复演练或任何 `docker exec`。

若后续需要「再跑一次分类」，那是独立授权项，且**优先**用自然调度（每小时 `:23Z`）
产生的运行，而不是手动触发。

## 8. 产出

一份新的只读取证记录，只包含：

- 每个文件「相等 / 不等 / 缺失」的布尔结论；
- 容器镜像摘要的匹配结论（v0.1.1 / v0.2.0 / 其他）；
- `df` 的 `Used%`、`Available`、文件系统类型；
- `docker system df` 的四个汇总数字；
- `du` 的前若干目录名与大小。

不包含：任何 Secret、DSN、私钥、完整配置文件内容、内部地址或原始 stderr。
