# Phase S1 / S1.1：Staging 故障定位、诊断证据安全与部署准入（2026-11-03）

- 审查基线：`9d9f63d14bb72a498f0f2352110ec866defc17fb`（`main`，#96/#97 合并后）
- 调查分支：`fix/staging-diagnostics-20261103`（Draft PR
  [#106](https://github.com/baicie/asteria-drive/pull/106)）
- 边界：未部署 Staging、未执行远端恢复、未操作远端数据库/容器/Secret/数据卷、
  未创建或删除 Release/tag、未合并 PR、未运行有副作用的远端工作流。
- 结论标签：**VERIFIED**（有独立证据）/ **UNKNOWN**（证据不足）/ **BLOCKED**（有明确阻塞）/
  **NOT_RUN**（尚未执行）。

## 0. 结论边界（先读这一节）

本次调查**没有访问远端主机**。所有关于主机内部状态的结论都只能是 UNKNOWN，
不能因为工作流日志看起来像某种原因就把它写成事实。

**VERIFIED**

1. 旧失败（2026-10-23 起）的远端 stderr 摘要与 dispatcher 拒绝文本的摘要一致：
   `1b39cdcb…` 精确等于 `printf 'staging deploy command rejected\n'`。
2. 新失败（2026-11-02 起）确实存在 89 字节 stderr，并且探针报告
   `status=failed`、`last_phase=compose-identity`（恢复演练为 `source-identity`）。
3. 旧工作流没有保存足以诊断的明文原因：raw stderr 在退出前被删除，Artifact 只有摘要。
4. #96/#97 的合并发生在监控首次失败之后；两次合并没有改动
   `staging` / `infra/` / `scripts/` 任何文件。
5. `v0.2.0` Release 制品完整且 immutable，其 source commit `5169da6` 早于 #96/#97。

**UNKNOWN**

1. 当前主机上 dispatcher 与 monitor/recovery 脚本的实际摘要。
2. 当前主机实际运行的镜像版本（`docs/status.md` 中的 `v0.1.1` 是 2026-08-16 的证据）。
3. 89 字节 stderr 的具体原因（**2026-11-04 已由 Phase S2 复原**，见下方更正与
   [S2 诊断](phase-s2-staging-diagnosis.md)）。
4. Phase B bootstrap 是否已在主机上执行。
5. 主机磁盘、Docker 数据目录、容器健康、PostgreSQL TLS 的现时状态。

**不做如下推断**：不把“SSH 失败”“主机宕机”“版本不匹配”“磁盘满”写成事实；
不把“未读取主机”的部署状态标为 VERIFIED。

## 1. 调查方法

1. `gh` 只读列举全部 `Monitor staging`、`Drill staging recovery`、`deploy-staging`、
   `rollback-staging` 运行，取最近 12 次定点运行而非任务书列出的两次。
2. 下载每次失败运行的 `staging-monitor-evidence` / `staging-recovery-evidence` artifact，
   逐字段比对 `collection-status.json`、`fallback-decision.txt`、`probe-attempts.txt`。
3. 以 Git 历史重建工作流与脚本的摘要谱系（逐提交哈希），确定固定点与主机侧可能安装的版本。
4. 对无法反推的 89 字节 stderr，用已知明文自校验的程序穷举容量门禁消息空间，
   确认它不是 `verify_capacity` 的输出。
5. 对 `#96`/`#97` 做路径级影响分析。

## 2. 故障分析

### 2.1 旧失败（2026-10-23 起）：工作流固定点切换引发的部署合同问题

`main` 在 `cf63a93`（PR #80，2026-10-23）把 monitor/recovery 的固定点切换到 `v0.2.0`
摘要。历史对照：

| 阶段 | 工作流固定点 | 主机 dispatcher 是否接受 | 结果 |
| --- | --- | --- | --- |
| ≤ `2dc40b9`（2026-10-23T01:38Z） | v0.1.1 `3ef3…` | 是 | success（`39885137443`） |
| `cf63a93` 起 | v0.2.0 `d1e1…` | 否 | 每次失败 |

已核实的第一批失败证据（`39919598579`，2026-10-23T07:42Z）：

```
raw_evidence: present=true  size_bytes=0
remote_stderr: present=true size_bytes=32
sha256(remote_stderr) = 1b39cdcbcdfd0380085e7e3ffff878cb0e97edb3afb8630236727d447d6a0164
```

`1b39cdcb…` 精确等于 `printf 'staging deploy command rejected\n'` 的 SHA-256（32 字节）。
该消息只由 dispatcher 的 `fail()` 产生，即主机拒绝了请求的摘要。该形态在
`39919598579`、`40033205274`、`40237695258`、`40511495870`、`40843755897`、
`41202754988`、`41320513177` 七次运行中完全一致（VERIFIED）。

这只证明**工作流请求的摘要被主机拒绝**；主机上 dispatcher 文件的实际摘要、以及
Phase B bootstrap 是否执行过，均未读取主机，属 UNKNOWN（见第 0 节）。

### 2.2 新失败（2026-11-02 起）：现象 VERIFIED，原因 UNKNOWN

`41400035337`（2026-11-02T06:27Z）起证据形态改变：

```
collection-status.json: probe_exit=1, raw_evidence.size_bytes=2199, remote_stderr.size_bytes=89
fallback-decision.txt : "fallback: v0.2.0 probe proved version mismatch or dispatcher rejection"
probe-attempts.txt    : "v0.2.0 probe_exit=1"、"v0.1.1 probe_exit=1"
```

`41489190998`、`41549153916`、`41593365487` 完全同形（89 字节 stderr，两版都失败）。
由此可以确认（VERIFIED）：

- dispatcher **已经**接受并运行了探针——否则不会产出 2199 字节 evidence 与 89 字节 stderr；
- fallback 判定走的是 `status=failed && last_phase=compose-identity`，不是 dispatcher 拒绝文本；
- v0.1.1 探针随后也失败；`raw_evidence` 被清空、stderr 被覆盖，最终只剩第二次尝试的
  89 字节 stderr 与一个哈希。

89 字节 stderr 的具体内容在本阶段 **UNKNOWN**。当时的排除项：

- 不是 `staging deploy command rejected`（32 字节，哈希不符）；
- 当时认为「不是 `staging monitor capacity failure: …`」：对 0–9%、10–99%、100% 的 used
  值、4 个文件系统标签、7 位可用空间做了穷举，在 89 字节长度约束下无匹配；
- 不是常见 docker/curl/flock/df/command-not-found 单行错误（159 条候选全不匹配）。

> **2026-11-04 更正（Phase S2）**：上面的第二条**是错的**。当时的穷举程序把哈希表
> 键值写反，`no match` 是工具缺陷而不是证据。修正并用已知明文自校验后，89 字节消息
> 被精确复原为
> `staging monitor capacity failure: root filesystem is <86–89>% used with <…> KiB available`。
> 详见 [Phase S2 诊断](phase-s2-staging-diagnosis.md)。本节其余结论不变。

**旧工作流没有保存足以诊断的明文原因**（VERIFIED）：它只留摘要，且 raw 文件在退出前删除。
这正是 S1.1 要修复的 P1 问题——修复方式见第 3 节，而不是靠继续猜。

### 2.3 恢复演练：现象 VERIFIED，原因 UNKNOWN

| Run | 日期 | 失败阶段 | 证据 |
| --- | --- | --- | --- |
| `40305621267` | 2026-10-26 | v0.2.0 `source-identity`，随后 v0.1.1 亦失败 | 32 字节 stderr，`1b39cdcb…`（dispatcher 拒绝） |
| `41445462781` | 2026-11-02 | v0.2.0 `source-identity`，随后 v0.1.1 亦失败 | 89 字节 stderr |

两次都未进入备份、隔离恢复或数据校验阶段，因此**不存在恢复正确性缺陷的证据**；
失败发生在读取源身份之前。`41445462781` 的 v0.1.1 回退同样失败且原文被删除，
原因 UNKNOWN。

### 2.4 与 #96/#97 及 `v0.2.0` 应用代码的关系

```
git diff --name-only fb0e5e0 9d9f63d | grep -iE 'staging|infra/|scripts/'   → 无输出
```

两次合并只触及 `web/`、`internal/{drive,s3store,memory,config,postgres,cicheck}`、
文档与 CI。首次 monitor 失败（2026-10-23T07:42Z）早于 #96 的任何提交（首个提交
`a3c049b`，2026-11-02）。合并后 2026-11-03T06:59Z 的失败（`41593365487`）与合并前的
`41549153916`（2026-11-02T23:56Z）同形，**不是回归**（VERIFIED）。

准确表述：**已观察到因工作流固定点切换而引发的部署合同问题，但尚无证据表明 `v0.2.0`
应用代码本身存在故障。** 本文不声称 `v0.2.0` 镜像“没有问题”——只声称现有失败证据发生在
应用代码被执行之前，既不构成对它的指控，也不构成对其正确性的证明。

## 3. 代码修复

### 3.1 P1：不再把任何自由文本 stderr 写入 Artifact

**初版被否决。** S1 的第一版每次尝试写入一份经正则脱敏的 stderr 摘录。独立审查指出
黑名单脱敏无法证明不会漏掉新形式的密钥。**该实现已完全移除**，包括：

- `remote-stderr-excerpt.txt`；
- 全部正则清洗逻辑与 `PROBE_DEPLOY_HOST` 替换；
- 摘录相关的截断常量。

**现行实现：白名单分类。** Artifact 里每次尝试只有一个 JSON 记录，字段全部是固定格式
或固定集合：

| 字段 | 来源 | 允许取值 |
| --- | --- | --- |
| `schema` | 常量 | `asteria-drive-staging-probe-attempt/v2` |
| `candidate` | 工作流常量 | `v<digits>.<digits>.<digits>` |
| `exit_code` | 探针退出码 | `-?[0-9]{1,3}` |
| `classification` | 固定集合 | 见下表 |
| `phase` | 探针自身 JSON 的 `last_phase` | 仅限已审阅脚本声明的阶段名，否则 `null` |
| `evidence` / `stderr` | 本地文件 | `present`、`size_bytes`、`sha256` |

分类规则（首个匹配即返回，未匹配一律 `unknown`）：

| 分类 | 判定依据 |
| --- | --- |
| `success` | 退出码 0 |
| `dispatcher-rejected` | stderr 非空且**每一行都恰好等于** `staging deploy command rejected` |
| `ssh-auth-failed` | 单行且完全匹配 `Permission denied (publickey\|password\|keyboard-interactive[…]).` |
| `ssh-host-key-failed` | 单行且完全匹配 `Host key verification failed.` 或 `WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!` |
| `ssh-connect-failed` | 单行且完全匹配 `ssh: connect to host … port …: <固定原因>` |
| `compose-identity-failed` | `status=failed` 且 `last_phase=compose-identity` |
| `source-identity-failed` | `status=failed` 且 `last_phase=source-identity` |
| `capacity-guard-failed` | `status=failed` 且阶段属于四个容量阶段之一 |
| `remote-command-failed` | `status=failed` 且阶段已声明但不在上述几类 |
| `unknown` | 其余全部情况 |

安全性质：

- **不保留任何自由文本**；SSH 类分支只做整体匹配并丢弃文本，只留标签；
- `phase` 只在属于脚本真实声明的 22 个阶段名时才输出，否则丢弃；
- 原始 `raw_evidence` / `raw_stderr` 仍在 Runner 退出时由 `EXIT` trap 删除，
  Artifact 目录里只有上述记录；
- 成功探针不产生任何 stderr 文本（连摘录文件都不存在）；
- 分类器失败只打印一行，不改变探针退出码与 fallback 决策。

### 3.2 P2：运行日志给出分类与阶段

失败时打印分类器已写出的 `probe-summary.txt`。每行只有候选版本、退出码、固定分类标签
和已声明的阶段名，不含任何远端自由文本：

```
attempt v0.2.0 exit=1 classification=compose-identity-failed phase=compose-identity
```

### 3.3 为什么不抽成脚本文件

monitor 工作流的既有契约禁止 `actions/checkout@`（见
`internal/cicheck/staging_deployment_test.go` 的 forbidden 列表），因此无法调用仓库内脚本。
两段内联代码保持**逐字节相同**，并由测试断言，避免漂移。

## 4. 测试与验证

| 命令 | 结果 |
| --- | --- |
| `go build ./...` / `go vet ./...` | 通过 |
| `go test ./... -count=1` | 15 个包全部通过 |
| `go test -race ./... -count=1` | 通过 |
| `python3 -m unittest discover -s scripts/tests -v` | 38 tests OK（4 skipped，平台相关） |
| `action-validator`（两个改动的工作流） | 通过 |
| 两个工作流内嵌 Python 块 `compile()` | 8/8 通过 |
| 抽取后 probe shell `bash -n` | 通过 |

新增/重写 `internal/cicheck/staging_probe_evidence_test.go`：

1. **白名单正例 11 条**：dispatcher 拒绝、三种 SSH 失败、四个阶段分类、
   未知失败、未声明阶段被丢弃、成功探针。
2. **敏感信息反例 11 条**：Bearer/JWT、`AWS_SECRET_ACCESS_KEY`、`access_token` 查询参数、
   IPv4、IPv6、多行 PEM、`private_key`/`passphrase`、PostgreSQL DSN、SSH 主机名、
   未知短密钥、多行混合负载。每条都必须归为 `unknown`，**且 Artifact 目录里不得出现
   任何模拟值**（逐文件扫描）。
3. **两份内联代码逐字节相同**，并断言 Schema 与十个分类标签齐全，且
   `remote-stderr-excerpt` / `PROBE_DEPLOY_HOST` / `redact(` / `MAX_LINES` 不会回归。
4. **原始输出仍然临时**：断言两个工作流都有删除 raw 文件的 `EXIT` trap，
   且 raw 路径不指向 Artifact 目录。
5. **非法候选**：`../../etc/passwd`、`v0.2.0/../x`、空串均被拒且不写文件。
6. **Linux 上缺少 Python 时测试失败而非跳过**；仅非 Linux 平台才 `Skip`。

测试按真实布局布置：raw 文件在 `$RUNNER_TEMP` 等价目录，只有分类记录进入 Artifact
等价目录——这正是 S1 初版测试的缺陷（把 raw 文件放进同一目录扫描，掩盖了真实边界）。

## 5. 仍无法确定的远端主机问题

以下只能靠授权的主机侧只读取证，**本次不猜、不改代码规避**：

1. **89 字节 stderr 的具体原因**：分类器只会把它归入 `unknown` 或某个已声明阶段，
   不再保留文本。
2. **主机 dispatcher 与脚本的实际摘要**：需要 `sha256sum` 读取
   `/usr/local/libexec/asteria-staging-{dispatch,monitor,recovery}` 与
   `/etc/asteria-drive/staging-dispatch.conf`，确认 Phase B bootstrap 是否执行过。
3. **主机现时运行版本**：需要 `docker inspect` 读取
   `asteria-drive-staging-api-1` 的 `.Config.Image`。

对应只读命令与预期比对值见
[staging-v0.2.0-bootstrap-runbook.md](../acceptance/staging-v0.2.0-bootstrap-runbook.md)
第 2 节；执行前需要用户明确授权。

## 6. 部署准入评估

### 6.1 `v0.2.0`：BLOCKED

| 项 | 状态 | 依据 |
| --- | --- | --- |
| Release 制品完整性 | VERIFIED | `isImmutable=true`；`checksums.txt`、`release-manifest.json`、双架构 tar.gz、Web 静态包、两份 SBOM 齐全 |
| 制品与 `main` 的关系 | VERIFIED | source commit `5169da6`；不含 #96/#97 |
| Staging 升级路径 | BLOCKED | 需要 Phase B bootstrap 把主机 dispatcher/脚本更新到 v0.2.0 摘要；主机是否已执行 UNKNOWN |
| 备份与回滚 | BLOCKED | 回滚依赖 host controller 与 `controller-state.json`，二者由 Phase B 安装 |
| 配置/迁移/存储兼容 | VERIFIED（仓库侧） | `v0.2.0` 的 `schema_version=3` 与现有 0001–0003 一致；迁移文件未改动 |
| 监控与恢复验收 | BLOCKED | 两者自 2026-10-23 起均无成功运行 |

### 6.2 当前 `main`（下一版本候选）：BLOCKED，且**不得**先于 v0.2.0 部署

`v0.2.0` 之后 `main` 新增：

| 变更 | 内容 | 部署影响 |
| --- | --- | --- |
| 迁移 `0004_upload_part_checksum.sql` | 新表 `upload_part_signature` | 前向兼容，已在真实 PostgreSQL 验证 |
| 迁移 `0005_part_signature_audit_only.sql` | 仅 `COMMENT ON TABLE` | 无列/行变更 |
| API 合同 | `ListedPart`：移除 `content_sha256`，新增必填 `reusable` + `storage_sha256` | **破坏性**；仓库内无其他消费者，需确认无外部客户端 |
| Web 上传/取消/暂停/恢复 | 结构化失败、服务端 Abort、跨刷新恢复 | 仅前端 |
| S3 分片 checksum 信任 | 默认 `ASTERIA_S3_TRUST_PART_CHECKSUMS=false`，恢复走全量重传 | 默认安全；**无需**在 staging 开启 |
| 部署/监控/恢复工作流 | 本次仅改诊断，未改固定点 | 仍固定在 `v0.2.0` 摘要 |

迁移 `0004`/`0005` 会让 Schema 从 3 变为 5，而 `staging-releases.json`、`bundle.json`、
monitor/recovery 脚本全部假定 `schema_version=3`。**直接部署当前 `main` 会同时打破三处
合同**：controller 的迁移后 schema 断言、recovery 的 `3 -> 3` 校验、monitor 的身份检查。

### 6.3 推荐：先完成 `v0.2.0`，再准备下一版本

1. `v0.2.0` 与主机现有 schema 3、`v0.1.1` 回滚路径都兼容，是唯一有完整仓库侧准入证据的版本；
2. 监控/恢复失败发生在应用代码执行之前，尚无证据表明 `v0.2.0` 应用代码有故障
   （2.1/2.4），但**必须先有安全的观测能力**才能验证部署——这正是第 3 节的修复；
3. 跳过 `v0.2.0` 直接发下一版会把“schema 3→5 + 破坏性 API 变更 + 首次 bootstrap”
   叠加在一次不可回滚的窗口里。

处置顺序（每一步都需要单独授权）：

1. 用修复后的工作流再跑一次 monitor，拿到结构化分类；若仍为 `unknown`，
   转由授权的主机侧只读取证；
2. 依 runbook 第 2 节做主机只读基线，确认 dispatcher 摘要与运行镜像；
3. 若主机仍是 v0.1.1 锚点，执行 Phase B bootstrap（root，需要维护窗口与双人复核）；
4. 执行 Phase C 部署 `v0.2.0`，用 monitor/recovery 的正常路径验收；
5. 另立版本：迁移到 0005、更新 `staging-releases.json`/bundle/脚本的 `schema_version`、
   重新固定摘要，再走完整 Phase B/C。

## 7. 遗留风险

- 89 字节 stderr 与主机现时状态仍是 UNKNOWN。修复后只能保证**下一次**失败被安全地归入
  一个固定分类，不能回溯已删除的原始输出。
- Artifact 现在只有分类标签与摘要，无法回答“远端到底说了什么”。这是有意的取舍：
  需要原文时应在主机侧做只读、受控的取证，而不是放宽 Artifact 的保密边界。
- `ssh-*` 分类来自 ssh(1) 的固定文本，但远端命令理论上可以模仿它，所以分类标签是
  **辅助信息**，不构成对远端的可信断言；它永远不携带远端文本。
- `CI / web-live` 仍未设为 required check（沿用 R3.1 的结论）。
- `v0.2.0` 的 `deploy-staging.sh` / compose 摘要与当前 `main` 不同；本次修复**没有**
  改这些固定点。
