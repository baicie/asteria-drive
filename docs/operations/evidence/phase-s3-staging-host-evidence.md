# Phase S3：Staging 现场取证清单（2026-11-05）

- 基线：`main` = `17d9e786924426354f4c4403e5238473cd46f179`（PR #107 合并后）
- 状态：**PREPARED / HOST_EVIDENCE_BLOCKED**
- 关联：[Phase S2 诊断](phase-s2-staging-diagnosis.md)、
  [S2 只读计划](phase-s1-staging-readonly-plan-20261104.md)、
  [容量处置方案](phase-s3-capacity-remediation-plan.md)、
  [dispatcher 修复方案](phase-s3-dispatcher-remediation-plan.md)、
  [部署 Gate 矩阵](phase-s3-deployment-gates.md)
- 边界：本文只是待执行的取证清单。**没有任何一条命令在本阶段被对主机执行。**

## 0. 结论边界（先读这一节）

本阶段**没有获得可用的主机通道**。授权在 2026-11-05 已给出，但执行前的通道审计
发现三个都成立的事实：

1. 仓库里**没有**主机地址：`ASTERIA_STAGING_SSH_HOST` 等只存在于 GitHub Secrets
   （`gh secret list` 返回空，说明当前 token 连读取名称的权限都没有）；
2. 本机环境**没有** `ASTERIA_*` / `STAGING_*` / `DEPLOY_*` 变量，也没有
   `~/.ssh/config`；
3. `~/.ssh/id_ed25519` 是个人工作密钥（`liuzhiwei@bonree.com`），
   `known_hosts` 里只有 gitlab / github / `82.156.234.84` 三类条目，
   **没有**可用于 staging 的托管主机记录。

按任务书：「如果尚无授权、没有可用的维护会话……标记 `HOST_EVIDENCE_BLOCKED`，
完成所有仓库侧工作并提交待执行清单。」因此：

- 主机文件摘要、dispatcher 配置、运行镜像、容量占用来源，全部保持 **UNKNOWN**；
- 我没有尝试用任何密钥去连接 `known_hosts` 里的地址来"碰运气"——那不是授权过的
  通道，且会把一个未知主机当成 staging；
- 本文任何"预期观察"都是**由仓库源码推导的假设**，不是现场结果。

**不要**把本文的命令清单、Gate 矩阵中的 PREPARED 状态、或修复方案的存在，
理解为阻塞已解决。`v0.2.0` 仍是 `DEPLOYMENT_BLOCKED`。

### 0.1 已交付的可执行取证脚本

为避免现场手工执行十几条命令时出错，本阶段把 P1–P3 收敛成一个脚本：

`scripts/s3-staging-readonly-evidence.sh`

- 只读：不含 `docker run/exec/rm/restart/pull/prune`、`systemctl`、`compose up/down`、
  任何 `rm`/`mv`/`chmod`/`install`/重定向写入；
- 只输出判定与数字：文件摘要只打印 `MATCH / MISMATCH / MISSING / UNKNOWN`，
  配置只打印「某个键指向哪个版本的脚本」，不打印摘要值与配置正文；
- 不采集 `.Config.Env`、挂载选项、`/proc/mounts`、容器日志、任何 Secret；
- 必须在 root 下运行（`EUID != 0` 直接退出）；
- `du` 需要显式 `--with-du` 才执行，默认跳过，避免无意中产生 I/O。

**离线验证**：用三套合成主机夹具（v0.1.1-only、Phase B 已安装、残缺主机）
在本地跑通，覆盖了 `MATCH / MISMATCH / MISSING / ABSENT / UNKNOWN` 全部分支，
并核对输出中不出现任何文件摘要或配置正文（只出现公开的 release digest）。
夹具测试暴露并修复了三个真实缺陷：

| 缺陷 | 后果 | 修复 |
| --- | --- | --- |
| `df` 字段顺序接反（`used` 与 `available` 互换） | 会把 5,538,200 当成使用率、把 87 当成可用 KiB，容量判定完全错误 | 与生产脚本的字段序对齐（`$4` Available、`$5` Capacity） |
| `compare` 的参数顺序与调用处相反 | 所有文件身份一律报 `MISMATCH` | 统一为 `compare <path> <expected>` |
| `python3` 在本机是 Store 垫片（`rc=49`） | `releases.json` 解析恒失败 | 先探测可用的解释器（`python3` → `python`），都不可用时显式报 `UNKNOWN` 而不是崩溃 |

第二个缺陷尤其值得记录：如果没做夹具测试，现场会拿到一屏 `MISMATCH`，
很可能被误读成"主机文件全被改过"。

## 1. 本阶段已离线核实的事实（VERIFIED，仓库侧）

| 事实 | 证据 |
| --- | --- |
| `main` = `17d9e78`，合并树与审查 HEAD `77018cb` 一致 | `git diff --stat 77018cb origin/main` 无输出 |
| v0.2.0 bundle 的 monitor/recovery/compose/deploy 摘要与工作流固定点一致 | `bundle.json` vs `.github/workflows/*.yml` vs 本地 `sha256sum` |
| controller / verifier 摘要与 bootstrap 期望值一致 | `sha256sum scripts/staging-release-controller.py` = `754ba671…`；`scripts/staging_runtime_verifier.py` = `77e537f7…` |
| 容量门禁是「`used > 85` **或** `available < 5 GiB`」 | `scripts/monitor-staging.sh:129` |
| recovery 的 `verify_capacity` 用 `used <= 85 && available >= 5 GiB` | `scripts/recover-staging.sh:175` |
| 取证清单的 `df` 解析与生产脚本逐字节相同 | 离线测试 16 项全部 PASS（见第 7 节） |
| 2026-11-05T07:01Z 的 Monitor `41984310536` 复原为 86% / 5,538,200 KiB | 哈希原像搜索，自校验通过 |

### 1.1 最新已核实的 Monitor

| Run | 时间（UTC） | Commit | 结果 |
| --- | --- | --- | --- |
| `41984310536` | 2026-11-05T07:01Z | `0bc4677`（PR #107 合并**之前**的 main） | v0.2.0 `dispatcher-rejected`；v0.1.1 `capacity-guard-failed @ host-capacity`；86% / 5,538,200 KiB |

**这不是合并后的 Monitor 验收。** 截至本阶段最后一次查询（2026-11-05T15:23Z），
GitHub 上**没有**基于新 `main`（`17d9e78`）的 Monitor 运行记录：最近一次仍是
07:01Z 的 `41984310536`。工作流 `state=active`，cron 为 `23 * * * *`，但该仓库的
调度实际并不每小时触发（最近 20 次的间隔在 2–11 小时之间），因此「暂无运行」不能
推出「工作流已停止」。本阶段**没有手动触发**任何运行。

## 2. 执行前提（缺一不可）

1. 用户**单独、明确**授权主机只读取证——**已于 2026-11-05 给出**；
2. **仍缺**：一条可用的主机通道。至少满足其一：
   - 操作者在**能访问主机的那台机器上**执行本文脚本，或
   - 把主机地址/端口/用户/密钥以安全方式提供到本会话（**不要**贴进聊天或 Issue；
     用本机环境变量或临时 `~/.ssh/config` 条目），或
   - 由操作者执行脚本并把输出（判定与数字）带回。
3. 由具备主机 **root 只读**权限的操作者执行；**不**复用 `asteria-deploy` 受限密钥，
   **不**修改 `authorized_keys`，**不**新建高权限入口。
4. 维护窗口建议选在两次调度之间（观察到的调度间隔 ≥2h，建议预留 90 分钟）。
5. 全程只读：不出现 `docker run/exec/rm/restart`、`systemctl restart`、
   `compose up/down`、`prune`、任何文件写入。
6. 输出保存在操作者本地；**不**粘贴到 Issue/PR/Artifact。摘要只记
   `MATCH / MISMATCH / MISSING / UNKNOWN`，**不记原始值**。
7. `du` 会产生明显 I/O：限范围、可中止；不对 `/proc`、`/sys`、网络挂载递归。

## 3. P1：现时容量与占用来源

### 3.0 一步完成（推荐）

在主机上以 root 运行：

```bash
sudo ./scripts/s3-staging-readonly-evidence.sh --with-du
```

脚本按 P1 → P2 → P3 顺序输出下面各节的字段，全部为判定与数字。
`--with-du` 会执行 `du -x -d1`（带 300 秒超时），**只在维护窗口内使用**；
不带该参数时 `du` 显示 `NOT_RUN`。

下面的分节命令用于：脚本某项报 `UNKNOWN` 时手工复核，或操作者希望逐条执行。

### 3.1 容量读数

```bash
hostname; date -u; id
df -Pk -- /
df -PTk -- /
df -Pi -- /
cat /proc/loadavg
free -m
```

判定：`Used% > 85` 或 `Available < 5,242,880 KiB` → 门禁判定正确。
两个都不触发 → 与 S2 时点不同，需用当时证据复核，不得直接结论为「已修复」。

### 3.2 Docker 占用

```bash
docker system df
docker system df -v 2>/dev/null | head -40
```

`docker system df -v` 只取摘要行，**不要**把整段输出贴出（含卷名与镜像名即可判断）。

### 3.3 日志占用

```bash
journalctl --disk-usage
du -sh /var/log 2>/dev/null
```

### 3.4 根目录一级分布

```bash
du -x -d1 -h / 2>/dev/null | sort -h | tail -20
```

`-x` 保证不跨文件系统；不进入 `/proc`、`/sys`。若耗时超过约 3 分钟，中止并改用
按目录逐个 `du -sh`。

### 3.5 逐层下钻（仅对 3.4 中最大的 2–3 个目录）

```bash
du -x -d1 -h /var/lib/docker 2>/dev/null | sort -h | tail -10
du -x -d1 -h /var/lib/docker/volumes 2>/dev/null | sort -h | tail -10
```

**不要**进入 `asteria-drive-staging-postgres-secrets`、
`asteria-drive-staging-seaweedfs-secrets`、`asteria-drive-staging-app-secrets`
的 `_data` 目录读取内容；只看 `du -sh` 的汇总大小。

### 3.6 明确不采集

- 挂载选项（`findmnt -o OPTIONS`、`/proc/mounts`、`mount`）——可能含 `addr=`、
  `username=`、`password=`、`credentials=`。
- Secret 文件内容、容器环境变量、完整 `docker inspect` JSON、容器日志正文。
- 任何私钥、DSN、Token。

### 3.7 记录模板

| 字段 | 值 |
| --- | --- |
| 采样时间（UTC） | |
| `df -Pk` Used% | |
| `df -Pk` Available (KiB) | |
| 文件系统类型 | |
| inode 使用率 | |
| `docker system df` Images / Containers / Volumes / Build Cache | |
| `journalctl --disk-usage` | |
| `du` 前 5 项（目录 + 大小） | |

## 4. P2：dispatcher 与 Phase B 安装合同

### 4.1 文件身份

```bash
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
```

```bash
stat -c '%U:%G %a %n' \
  /usr/local/libexec/asteria-staging-dispatch \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-monitor-v0.1.1 \
  /usr/local/libexec/asteria-staging-recovery \
  /usr/local/libexec/asteria-staging-recovery-v0.1.1 \
  /usr/local/libexec/asteria-staging-controller \
  /usr/local/libexec/staging_runtime_verifier.py \
  /etc/asteria-drive/staging-dispatch.conf \
  /etc/asteria-drive/staging-releases.json \
  /run/asteria-drive /run/asteria-drive/staging.lock \
  /etc/tmpfiles.d/asteria-drive-staging.conf 2>&1
```

### 4.2 期望值（本地比对，不记原始摘要）

| 文件 | 期望摘要（前 8 位，仅供本地比对） | 期望属主/权限 |
| --- | --- | --- |
| monitor | `d1e186cb` | `root:root 0755` |
| monitor-v0.1.1 | `3ef39670` | `root:root 0755` |
| recovery | `91c45a19` | `root:root 0755` |
| recovery-v0.1.1 | `3b2bb836` | `root:root 0755` |
| controller | `754ba671` | `root:root 0755` |
| verifier | `77e537f7` | `root:root 0644` |
| compose | `cba2ba28`（v0.2.0）/ `aa4336dc`（v0.1.1） | `root:root` 可读 |
| `staging-dispatch.conf` | —（配置文件本身无固定摘要） | `root:root 0644` |
| `staging-releases.json` | —（内容应含两个版本条目） | `root:root 0644` |
| `staging.lock` | — | `root:$deploy_gid 0660` |

### 4.3 dispatcher 配置的接受集合（只读四个键）

```bash
grep -E '^(monitor_script_sha256|monitor_v011_script_sha256|recovery_script_sha256|recovery_v011_script_sha256|controller_sha256|verifier_sha256)=' \
  /etc/asteria-drive/staging-dispatch.conf
```

判定分支（对照第 5 节的代际指纹）：

| 观察 | 含义 |
| --- | --- |
| 只有 `monitor_script_sha256` / `recovery_script_sha256` 两个键 | 旧版单摘要 dispatcher（`7c6565b` 或 `cf63a93` 代际） |
| 有 `monitor_v011_*` / `recovery_v011_*`，无 `controller_sha256` | 中间代际（`24d0a28`，**未**安装 controller） |
| 六个键齐全且含 `controller_sha256` / `verifier_sha256` | 新版（`b6dc862` 及以后，Phase B 形态） |
| 文件缺失或不可读 | `MISSING` / `UNKNOWN`，停止并上报 |

### 4.4 releases 清单（只读键名，不输出内容）

```bash
python3 - <<'PY'
import json
with open('/etc/asteria-drive/staging-releases.json', encoding='utf-8') as fh:
    data = json.load(fh)
print('versions:', ','.join(sorted(data)))
for version in sorted(data):
    print(version, 'keys:', ','.join(sorted(data[version])))
PY
```

期望：`v0.1.1,v0.2.0`，每项含 `compose_sha256, deploy_script_sha256, image,
schema_version, source_commit` 五个键、`schema_version=3`。

### 4.5 两个判定（严格分开）

| 判定 | 本清单能给出的部分 | 缺什么 |
| --- | --- | --- |
| `PHASE_B_OPERATIONAL_STATE_VERIFIED` | ① 文件摘要、② 配置接受集合 | ③ 自然调度中 v0.2.0 是否仍被 `dispatcher-rejected`，需等待一次自然调度 |
| `PHASE_B_EXECUTION_HISTORY_VERIFIED` | **不能给出** | 独立的 bootstrap 执行日志、维护记录/变更单、文件 mtime 序列 |

**禁止**：由 ①②③ 推导 `PHASE_B_EXECUTION_HISTORY_VERIFIED`；即使三项全部符合，
历史结论仍为 UNKNOWN。

## 5. bootstrap 代际指纹（用于把现场观察映射到一个仓库提交）

用 Git 历史对 `scripts/bootstrap-staging-host.sh` 做指纹，得到下表。现场只要看到
**配置文件键集合**与**是否存在 `-v0.1.1` 变体**，就能定位代际：

| 代际（commit） | monitor | monitor-v0.1.1 | controller | `validate_lock` | releases.json |
| --- | --- | --- | --- | --- | --- |
| `7c6565b`（2026-08-16 TLS rollout） | `3ef39670` | 无 | 无 | 无 | 无 |
| `cf63a93`（PR #80，v0.2.0 固定点） | `d1e186cb` | 无 | 无 | 无 | 无 |
| `24d0a28`（版本化 release bundle） | `d1e186cb` | `3ef39670` | 无 | 无 | 无 |
| `3403734` / `b6dc862` / `4e7ba13`（controller 代际） | `d1e186cb` | `3ef39670` | `754ba671` | 有 | 有 |

**关键推论（假设，需现场确认）**：

- 如果主机仍停在 `7c6565b` 代际，dispatcher 只接受 `3ef39670`，会**拒绝**
  `d1e186cb`——与 S2 观察到的「v0.1.1 被接受、v0.2.0 被拒绝」完全一致。
- 如果主机停在 `cf63a93` 代际，dispatcher 只接受 `d1e186cb`，会**拒绝**
  `3ef39670`——这与观察**矛盾**，因此可以**排除**该代际。
- 如果主机已执行 Phase B（`b6dc862` 及以后），两个摘要都被接受，
  v0.2.0 不应被拒——也与观察矛盾，因此同样可**排除**（除非配置被改过）。

也就是说：现场观察的接受集合本身就排除了 `cf63a93` 与 Phase B 代际，
最可能是 `7c6565b` 代际。**但这仍是推断，不是现场证据。**

## 6. P3：运行镜像与服务状态

```bash
docker ps --filter name=asteria-drive-staging --format '{{.Names}} | {{.Image}} | {{.Status}}'
```

```bash
docker inspect --format '{{.Name}} | {{.Config.Image}} | {{.Image}} | {{.State.Status}} | {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
  asteria-drive-staging-api-1 \
  asteria-drive-staging-postgres-1 \
  asteria-drive-staging-seaweedfs-1 2>&1
```

```bash
docker volume ls --filter name=asteria-drive-staging --format '{{.Name}} {{.Driver}}'
```

### 6.1 三个标识不能混为一谈

| 标识 | 含义 | 本例 |
| --- | --- | --- |
| `.Config.Image` | 创建容器时使用的**原始引用字符串** | 可能是 tag、`repo@digest` 或本地名 |
| `.Image` | 本地镜像 **ID**（`sha256:` + 本地内容地址） | 与 registry digest 无必然相等关系 |
| registry digest | registry 中 manifest 的 `sha256` | v0.1.1 `2b73f8a7…`、v0.2.0 `964e71a4…` |

判定规则：

| `.Config.Image` 观察 | 结论 |
| --- | --- |
| 含 `@sha256:2b73f8a7…` | 声明为 v0.1.1 |
| 含 `@sha256:964e71a4…` | 声明为 v0.2.0 |
| 含 `:v0.1.1` / `:v0.2.0` 等 tag | 需进一步核对：tag 可被重新指向，**不能**据此断言版本 |
| 其他 / 空 | `UNKNOWN`，停止并上报 |

若要确认 `repo@digest` 与 registry 一致，只使用只读元数据方式：

```bash
docker image inspect --format '{{json .RepoDigests}}' ghcr.io/baicie/asteria-drive:v0.2.0 2>/dev/null
```

**不执行** `docker pull`、`docker build`、`docker tag`、`docker run`。

### 6.2 记录模板

| 容器 | 是否存在 | `.Config.Image` | 版本判定 | `State.Status` | `Health.Status` |
| --- | --- | --- | --- | --- | --- |
| `asteria-drive-staging-api-1` | | | | | |
| `asteria-drive-staging-postgres-1` | | | | | |
| `asteria-drive-staging-seaweedfs-1` | | | | | |

另记：数据卷 `asteria-drive-staging-{postgres-data,seaweedfs-data,app-secrets,
postgres-secrets,seaweedfs-secrets}` 是否存在；是否观察到混合版本（例如 API 为
v0.2.0 而 compose 为 v0.1.1）。

## 7. 离线复核记录（本阶段已执行）

### 7.1 `df` 解析与容量判定（16 用例）

`df` 解析与容量判定用一个独立脚本离线测试，解析函数**逐字节复制**自
`scripts/monitor-staging.sh:parse_filesystem_output`，确保清单不会与生产契约漂移。

| 用例 | 结果 |
| --- | --- |
| ext4 标准输出 | PASS |
| device-mapper 长设备名 | PASS |
| overlay | PASS |
| 尾部空行 | PASS |
| 两行数据（拒绝） | PASS |
| `Available` 为 `-`（拒绝） | PASS |
| 只有表头（拒绝） | PASS |
| 86% 触发上限 | PASS |
| 84% + 5.5 GiB 通过 | PASS |
| 90% + 4.9 GiB 双触发 | PASS |
| 84% + 恰好 5 GiB 通过 | PASS |
| 84% + 5 GiB−1 KiB 触发下限 | PASS |
| 86% + 6 GiB 仍触发上限 | PASS |
| 摘要 MATCH / MISMATCH / MISSING | PASS |

共 16 项全部通过。该测试只验证**解析与判定逻辑**，不代表任何主机结果。

### 7.2 取证脚本的三套夹具测试

`scripts/s3-staging-readonly-evidence.sh` 用合成主机夹具离线跑通：

| 夹具 | 覆盖 |
| --- | --- |
| v0.1.1-only（预期形态） | 单槽位锚点、`ABSENT` 键、`MISSING` controller/releases、容量 BLOCK |
| Phase B 已安装 | 六键齐全、`monitor_slot_generation: v0.2.0`、`releases_json` 解析、`controller_state: PRESENT` |
| 残缺主机 | `MISSING` 文件、`UNKNOWN` 镜像引用 |

输出核对：除公开 release digest 外不出现任何 64 位十六进制串；不含
`password` / `secret` / `token` / `private_key` 字样。

### 7.3 脚本安全性扫描

对脚本本身做静态扫描，确认不含：`docker run/exec/rm/rmi/restart/stop/kill/pull/
build/tag/prune`、`systemctl`、`compose up/down`、`rm`、`mv`、`chmod`、`chown`、
`install`、`truncate`、对 `/etc|/usr|/opt|/var|/run` 的重定向写入、`findmnt`、
`/proc/mounts`、`.Config.Env`、完整 `docker inspect` JSON。扫描结果为空。

## 8. 禁止事项（本阶段与现场执行均适用）

- 清理磁盘，删除 Docker 镜像/容器/Volume/Build Cache
- 修改容量阈值
- 修改 dispatcher 配置或任何主机文件
- 执行 Phase B bootstrap
- 执行 Recovery Drill、数据库迁移
- 部署或回滚
- 手动触发 Monitor
- 发布 Release / tag、合并 PR
- 使用未经授权的 root/SSH 通道
- 读取或外传 Secret

## 9. 产出格式

现场取证完成后，另写
`phase-s3-staging-host-evidence-<date>.md`，只包含：

- 每项的 `MATCH / MISMATCH / MISSING / UNKNOWN`；
- `df` 的 `Used%`、`Available`、`Type`、inode；
- `docker system df` 四项汇总；
- `du` 前若干目录名与大小；
- 三个容器的版本判定与健康状态；
- 两个 Phase B 判定各自的结论。

不包含 Secret、DSN、私钥、完整配置文件、内部地址或原始 stderr。
