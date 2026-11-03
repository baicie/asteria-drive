# Phase S1：Staging 故障定位与部署准入证据（2026-11-03）

- 审查基线：`9d9f63d14bb72a498f0f2352110ec866defc17fb`（`main`，#96/#97 合并后）
- 调查分支：`fix/staging-diagnostics-20261103`
- 边界：未部署 Staging、未执行远端恢复、未操作远端数据库/容器/Secret/数据卷、未创建
  Release 或 tag、未合并本分支 PR。
- 结论标签：**VERIFIED**（有独立证据）/ **BLOCKED**（有明确阻塞）/ **UNKNOWN**（证据不足）/
  **NOT_RUN**（尚未执行）。

## 1. 调查方法

1. `gh` 只读列举全部 `Monitor staging`、`Drill staging recovery`、`deploy-staging`、
   `rollback-staging` 运行，取最近 12 次定点运行而非任务书列出的两次。
2. 下载每次失败运行的 `staging-monitor-evidence` / `staging-recovery-evidence` artifact，
   逐字段比对 `collection-status.json`、`fallback-decision.txt`、`probe-attempts.txt`。
3. 以 Git 历史重建工作流与脚本的摘要谱系（`git rev-list --all -- <path>` 逐提交哈希），
   确定固定点与主机侧可能安装的版本。
4. 对无法反推的 89 字节 stderr，用已知明文的自校验程序穷举容量门禁消息空间，
   确认它不是 `verify_capacity` 的输出。
5. 对 `#96`/`#97` 做路径级影响分析。

远端主机本身未访问（边界要求），所有主机侧结论都标注为推断或 UNKNOWN。

## 2. 根因：两个独立故障，都与 #96/#97 无关

### 2.1 版本合同（2026-10-23 起）：VERIFIED

`main` 在 `cf63a93`（PR #80，2026-10-23）把 monitor/recovery 的固定点切换到 `v0.2.0`
摘要，但主机仍运行 `v0.1.1` 时代的 dispatcher。历史对照：

| 阶段 | 工作流固定点 | 请求的摘要 | 主机 dispatcher 接受 | 结果 |
| --- | --- | --- | --- | --- |
| ≤ `2dc40b9`（2026-10-23T01:38Z） | `MONITOR_SCRIPT_SHA256` = v0.1.1 `3ef3…` | 与主机一致 | 是 | success（`39885137443`） |
| `cf63a93` 起 | `MONITOR_SCRIPT_SHA256` = v0.2.0 `d1e1…` | 主机未安装该文件 | 否 | 每次失败 |

`cf63a93` 的 dispatcher 仍只做 `[[ "$requested_monitor_sha" == "$monitor_script_sha256" ]]`
的单值比较，而仓库里此时已存在 v0.2.0 摘要；`b6dc862`（同日 20:20）才加入双版本选择和
`fallback_allowed`。因此从 `cf63a93` 到 `b6dc862` 之间的运行连回退逻辑都不存在。

已核实的第一批失败证据（`39919598579`，2026-10-23T07:42Z）：

```
raw_evidence: present=true  size_bytes=0
remote_stderr: present=true size_bytes=32
sha256(remote_stderr) = 1b39cdcbcdfd0380085e7e3ffff878cb0e97edb3afb8630236727d447d6a0164
```

`1b39cdcb…` 精确等于 `printf 'staging deploy command rejected\n'` 的 SHA-256（32 字节）。
这条消息只由 dispatcher 的 `fail()` 产生，即主机拒绝了请求的摘要。该形态在
`39919598579`、`40033205274`、`40237695258`、`40511495870`、`40843755897`、
`41202754988`、`41320513177` 七次运行中完全一致（VERIFIED）。

### 2.2 第二种失败（2026-11-02 起）：VERIFIED 现象，UNKNOWN 根因

`41400035337`（2026-11-02T06:27Z）起证据形态改变：

```
collection-status.json: probe_exit=1, raw_evidence.size_bytes=2199, remote_stderr.size_bytes=89
fallback-decision.txt : "fallback: v0.2.0 probe proved version mismatch or dispatcher rejection"
probe-attempts.txt    : "v0.2.0 probe_exit=1"、"v0.1.1 probe_exit=1"
```

`41489190998`、`41549153916`、`41593365487` 完全同形（89 字节 stderr，两版都失败）。
这说明：

- dispatcher **已经**接受并运行了探针（否则不会产出 2199 字节的 evidence 与 89 字节 stderr）；
- `fallback_allowed` 的判定路径**不是** `grep -Fxq 'staging deploy command rejected'`，
  而是 `status=failed && last_phase=compose-identity`（否则回退文本会写成 “fallback denied”）；
- v0.1.1 探针随后也失败，且 `raw_evidence` 被 `: >` 清空、`remote_stderr` 被覆盖，
  最终 artifact 里只剩第二次尝试的 89 字节 stderr 和一个哈希。

89 字节 stderr 目前 **UNKNOWN**。已排除的可能：

- 不是 `staging deploy command rejected`（32 字节，哈希不符）；
- 不是 `staging monitor capacity failure: …`：分别对 0–9%、10–99%、100% 的 used 值、
  4 个文件系统标签、7 位可用空间做了全空间穷举（程序先用已知明文自校验哈希实现），
  在 89 字节长度约束下无匹配；
- 不是常见 docker/curl/flock/df/command-not-found 单行错误（159 条候选全不匹配）。

**这本身就是缺陷**：工作流在退出前 `rm -f` 掉 `raw_evidence` 与 `raw_stderr`，
artifact 只留摘要，导致一次真实的主机侧失败在事后无法诊断。仅凭现有证据
**不能**把 89 字节失败归因于 SSH、磁盘、Compose 漂移或探针自身。

### 2.3 恢复演练：同一形态：VERIFIED 现象，UNKNOWN 根因

| Run | 日期 | 失败阶段 | 证据 |
| --- | --- | --- | --- |
| `40305621267` | 2026-10-26 | v0.2.0 `source-identity`，随后 v0.1.1 亦失败 | 32 字节 stderr，`1b39cdcb…`（dispatcher 拒绝） |
| `41445462781` | 2026-11-02 | v0.2.0 `source-identity`，随后 v0.1.1 亦失败 | 89 字节 stderr，`78526cdf…` evidence |

两次都没有进入备份、隔离恢复或数据校验阶段，因此**不存在**恢复正确性缺陷的证据；
失败发生在读取源身份之前。`40305621267` 与 monitor 同期失败同因（dispatcher 拒绝）。

`41445462781` 的 v0.1.1 回退同样失败，且原始 stderr 被删除，根因无法判定。

### 2.4 与 #96/#97 的关系：VERIFIED 无关

```
git diff --name-only fb0e5e0 9d9f63d | grep -iE 'staging|infra/|scripts/'   → 无输出
```

两次合并只触及 `web/`、`internal/{drive,s3store,memory,config,postgres,cicheck}`、
文档与 CI。第一次 monitor 失败（2026-10-23T07:42Z）早于 #96 的任何提交
（#96 首个提交 `a3c049b`，2026-11-02）。`main` 合并后 2026-11-03T06:59Z 的失败
（`41593365487`）与合并前的 `41549153916`（2026-11-02T23:56Z）同形，**不是回归**。

## 3. 本次代码修复（有界，只修可证缺陷）

### 3.1 P1：失败运行保留可诊断证据

**缺陷**：`monitor-staging.yml` / `drill-staging-recovery.yml` 只保留远端 stderr 的
SHA-256 与字节数。运维在失败后拿不到任何可用信号，直接导致 2.2/2.3 的根因至今 UNKNOWN。

**修复**：两次探针尝试后都调用一段内联 Python，写入
`attempt-<candidate>.json`（每次尝试独立的摘要与字节数）与
`remote-stderr-excerpt.txt`（有界、脱敏的 stderr 摘录）。

保留的既有安全约束：

- **原始** `raw_evidence` / `raw_stderr` 仍在退出前删除，artifacts 里没有原始输出；
- 摘录最多 40 行 / 每行 400 字符 / 全程 200 行，超长截断；
- 脱敏覆盖：URL 内嵌凭据、`password|token|secret|api_key|access_key` 赋值、
  PEM 体、`--password` 一类参数、IPv4、32+ 位 hex、32+ 位混合字符 base64，
  以及**本次部署的 SSH 主机名**（`PROBE_DEPLOY_HOST`，属 staging secret）；
- 候选版本号必须匹配 `v<digits>.<digits>.<digits>`，否则不写任何文件。

**为什么内联而不是脚本文件**：monitor 工作流的既有契约禁止 `actions/checkout@`
（`internal/cicheck/staging_deployment_test.go` 的 forbidden 列表），因此它无法调用
仓库内脚本。为保持这一约束不变，代码内联，并由 Go 测试从 YAML 里抽出真实代码执行。

**不掩盖故障**：辅助脚本失败时只打印一行 `probe evidence helper failed with exit N`，
返回值仍是探针自身退出码；`fallback_allowed`、`PROBE_EXIT`、证据字段校验、
`status/last_phase` 身份检查全部未放宽。

### 3.2 P2：失败时在运行日志里给出阶段

在 “no approved staging … probe succeeded” 之后，额外打印远端报告的
`status` 与 `last_phase`（两者都用 `[a-z-]{1,32}` 白名单校验后才输出）。
此前只有 evidence artifact 里有这两个字段，而工作流日志完全看不出失败停在哪一步。

## 4. 测试与验证

| 命令 | 结果 |
| --- | --- |
| `go build ./...` / `go vet ./...` | 通过 |
| `gofmt -l internal/cicheck` | 新增文件已格式化 |
| `go test ./... -count=1` | 15 个包全部通过 |
| `go test -race ./... -count=1` | 通过 |
| `python3 -m unittest discover -s scripts/tests -v` | 38 tests OK（4 skipped 为平台相关） |
| `npx action-validator .github/workflows/monitor-staging.yml` | 通过 |
| `npx action-validator .github/workflows/drill-staging-recovery.yml` | 通过 |
| 两个工作流内嵌 Python 块 `compile()` | 10/10 通过 |
| 抽取后的 probe shell 步骤 `bash -n` | 通过（393 / 404 行） |

新增回归测试 `internal/cicheck/staging_probe_evidence_test.go`：

- 从**两个**工作流分别抽取真实内联块并执行（不是复制一份代码来测），验证：
  写入 `attempt-v0.2.0.json`、`exit_code` 正确、`stderr.sha256` 为 64 位、
  摘录保留失败信号、`supersecret` / IP / token / 部署主机名均被脱敏、
  摘录绑定到具体尝试；
- 拒绝 `../../etc/passwd`、`v0.2.0/../x`、空串三种候选，且不产生任何文件；
- 主机无可用 Python 时显式 `Skip`，不静默通过（Windows 的 `python3.exe` store shim
  退出码为 49，测试会识别并改用可用的解释器）。

正常路径与失败路径都已覆盖：正常路径是 `run_probe` 成功时不写摘录文件；
失败路径是上面两条——拒绝非法候选、以及脱敏后仍保留信号。

## 5. 未修复：需要主机访问的问题

以下三项只有拿到主机 root 只读输出才能定论，**本次不猜测、不改代码规避**：

1. **2.2 的 89 字节 stderr**：需要主机上重新触发一次并保留当次 stderr（本次修复会在
   下次运行自动保留脱敏摘录）。
2. **主机 dispatcher 的实际摘要**：需要 `sha256sum` 读取
   `/usr/local/libexec/asteria-staging-{dispatch,monitor,recovery}` 与
   `/etc/asteria-drive/staging-dispatch.conf`，确认 Phase B bootstrap 是否执行过。
3. **主机现时运行版本**：需要 `docker inspect` 读取
   `asteria-drive-staging-api-1` 的 `.Config.Image`。`docs/status.md` 里的 `v0.1.1`
   是 2026-08-16 的证据，不能作为 2026-11-03 的事实。

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
| API 合同 | `ListedPart`：移除 `content_sha256`，新增必填 `reusable` + `storage_sha256` | **破坏性**；仓库内无其他消费者，但需确认无外部客户端 |
| Web 上传/取消/暂停/恢复 | 结构化失败、服务端 Abort、跨刷新恢复 | 仅前端 |
| S3 分片 checksum 信任 | 默认 `ASTERIA_S3_TRUST_PART_CHECKSUMS=false`，恢复走全量重传 | 默认安全；**无需**在 staging 开启 |
| 部署/监控/恢复工作流 | 本次仅加诊断，未改固定点 | 仍固定在 `v0.2.0` 摘要 |

迁移 `0004`/`0005` 会让 Schema 从 3 变为 5，而 staging 的
`staging-releases.json`、`bundle.json`、monitor/recovery 脚本全部假定
`schema_version=3`。**直接部署当前 `main` 会同时打破三处合同**：
controller 的迁移后 schema 断言、recovery 的 `3 -> 3` 校验、monitor 的身份检查。

### 6.3 推荐：A —— 先完成 `v0.2.0`，再准备下一版本

理由：

1. `v0.2.0` 与主机现有 schema 3、`v0.1.1` 回滚路径都兼容，是唯一有完整仓库侧准入证据的
   版本；
2. 监控/恢复的失败与 `v0.2.0` 无关（2.1/2.4 已证），但**必须先修好观测能力**才能验证
   `v0.2.0` 部署是否成功——这正是 3.1 的修复；
3. 跳过 `v0.2.0` 直接发下一版会把“schema 3→5 + 破坏性 API 变更 + 首次 bootstrap”
   三个风险叠加在一次不可回滚的窗口里。

处置顺序（每一步都需要单独授权）：

1. 用本次修复后的工作流再跑一次 monitor，拿到脱敏摘录，确认 2.2 的 89 字节根因；
2. 依 runbook 第 2 节做主机只读基线，确认 dispatcher 摘要与运行镜像；
3. 若主机仍是 v0.1.1 锚点，执行 Phase B bootstrap（root，需要维护窗口与双人复核）；
4. 执行 Phase C 部署 `v0.2.0`，用 monitor/recovery 的正常路径验收；
5. 另立版本：迁移到 0005、更新 `staging-releases.json`/bundle/脚本的 `schema_version`、
   重新固定摘要，再走完整 Phase B/C。

## 7. 遗留风险

- 89 字节 stderr 与主机现时状态仍是 UNKNOWN；本次修复只保证**下一次**失败可诊断，
  不能回溯已删除的原始输出。
- 摘录是脱敏后的文本，不是原始证据；若未来需要更强的取证，应在主机侧写只读日志，
  而不是放宽 artifact 的脱敏边界。
- `CI / web-live` 仍未设为 required check（沿用 R3.1 的结论）。
- `v0.2.0` 的 `deploy-staging.sh` / compose 摘要与当前 `main` 不同；本次修复**没有**
  改这些固定点，因此 monitor/recovery 仍只认 v0.2.0 与 v0.1.1 两个精确版本。
