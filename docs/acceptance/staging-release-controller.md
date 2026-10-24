# Staging Release Controller v1

状态：`READY_FOR_HOST_VALIDATION`（仓库侧实现；未执行任何主机操作）

本文件描述 staging `v0.1.1` 与 `v0.2.0` 之间受控切换的仓库实现。它不声明
`ROLLBACK_VERIFIED` 或 `STAGING_DEPLOYED`，也不代替维护窗口审批。

## 1. 安全边界

- 版本白名单只有 `v0.1.1` 和 `v0.2.0`；workflow input 不能新增版本、镜像、
  script path、commit 或任意 SHA-256。
- 每个版本由代码审查后的静态 bundle 固定：tag、source commit、OCI digest、
  Compose SHA-256、deploy/monitor/recovery script SHA-256、schema 和 evidence
  schema。
- 主机 forced-command dispatcher 只接受这两个 release pair 的 upload 与执行；
  其他组合一律拒绝。
- 仍然使用固定 ED25519 host key、独立 deploy key、forced command、SHA-256
  和 root-owned 文件；未增加任何 bypass 或 shell 入口。
- 不删除、不重建、不清空 PostgreSQL / SeaweedFS / Secret volumes。

## 2. 静态 Release Bundle

目录：`infra/docker/staging/releases/<version>/`

| 文件 | 作用 |
| --- | --- |
| `bundle.json` | 版本、source、image、Compose/script 摘要、schema、evidence schema、允许操作 |
| `compose.yaml` | 该版本的不可变 Compose 快照 |
| `deploy-staging.sh` | 该版本的部署/回滚执行器 |
| `monitor-staging.sh` | 该版本的独立 monitor |
| `recover-staging.sh` | 该版本的恢复演练 |

当前 allowlist：

| | v0.1.1 | v0.2.0 |
| --- | --- | --- |
| source | `e8d26de...` | `5169da6...` |
| API image | `sha256:2b73f8a7...` | `sha256:964e71a4...` |
| Compose | `aa4336dc...` | `cba2ba28...` |
| deploy | `cd200fa3...` | `007eb3a3...` |
| monitor | `3ef39670...` | `d1e186cb...` |
| recovery | `3b2bb836...` | `91c45a19...` |
| schema | 3 | 3 |

`v0.1.1` 不是 Immutable Release；它通过独立核验固定 OCI digest、peeled source
和 OCI provenance 使用，不单纯信任 tag 名称。

## 3. Release State Machine

仓库侧实现：`internal/staging`。

状态：

```text
IDLE -> PREPARED -> ACTIVATING -> VERIFYING -> ACTIVE
                                      |           |
                                      v           v
                                   FAILED    ROLLING_BACK
                                                  |
                                                  v
                                         ROLLBACK_VERIFYING
                                                  |
                                                  v
                                                ACTIVE
```

`RECOVERY_REQUIRED` 是终止态：任何人工恢复未完成时，控制器拒绝继续自动切换。

实现约束：

- 每个请求绑定唯一 `run_id`、`attempt`、目标版本和预期当前版本。
- 状态原子写入；写入失败即 fail-closed，不继续激活。
- 激活和回滚后必须重新读取真实 host state，不能以 workflow 缓存值作为成功依据。
- 同一 run 重放是幂等的；同一 run 改目标被拒绝。
- 并发操作由 GitHub Actions concurrency 和主机 `flock` 双重串行化。
- `Resume` 只做状态重建，不启动新的切换；目标/前版本不匹配时进入
  `RECOVERY_REQUIRED`。
- `Monitor` 只接受 allowlist 中的活动版本，并校验该版本对应的 Compose、image
  和 schema；未知版本或摘要不匹配直接失败。

边界：`internal/staging` 是仓库侧控制面/状态机模型；真实主机执行链为：

```text
GitHub Actions deploy/rollback
  -> forced-command dispatcher (asteria-deploy)
  -> /run/asteria-drive/staging.lock (flock)
  -> /usr/local/libexec/asteria-staging-controller (root:root 0755, executed by asteria-deploy)
  -> staging_runtime_verifier.py (root:root 0644)
  -> reviewed version-specific deploy-staging.sh
  -> deployment-evidence.json + controller-state.json
```

状态文件的真实信任边界：

- 控制器进程 UID 是 `asteria-deploy`，不是 root；`controller-state.json` 由
  deploy 用户创建并拥有，目录为 `/opt/asteria-drive/staging`（
  `asteria-deploy:<deploy_group> 0750`），文件必须 regular、非 symlink、
  `deploy:deploy 0600`。
- 该文件是**可被 deploy 用户篡改的操作日志**，不是 root 可信凭据。它只用于
  拒绝重复执行和记录中断；任何 `ACTIVE` 结论都必须由实时 Docker/DB/readiness
  快照重新证明。
- deploy 用户属于 `docker` 组，实际具备主机管理员等效能力，不能虚构
  “root vs deploy”隔离。真正的边界是固定 SSH host key、forced command、固定
  版本 bundle 和 root-owned 可执行文件；远程 workflow 不能写状态文件。
- controller 在提交 ACTIVE、幂等重放、回滚补偿和开始新切换前调用
  `RuntimeVerifier`：active Compose SHA-256、`docker inspect` 实际镜像与
  镜像 ID、容器状态、`/readyz`、`/healthz`、PostgreSQL
  `MAX(version)` 和持久化版本一致性。
- 状态写入 tmp+fsync+rename+目录 fsync。`RECOVERY_REQUIRED` 会阻止后续变更；
  `ATTEMPTING` 中断要求人工恢复，不会重复执行破坏性步骤。
- 跨 GitHub Actions 与主机仍不构成原子事务。

## 4. Rollback Workflow

新增 `.github/workflows/rollback-staging.yml`：

- 仅 `workflow_dispatch`，只允许受保护 `main`。
- 使用 `staging` Environment。
- 人工输入固定确认字符串 `rollback-v0.1.1`。
- 固定 v0.1.1 tag/source/image/Compose；从 release bundle 上传旧 Compose 和
  旧 deploy script。
- 执行 `gh attestation verify`（release workflow、source-ref、source-digest、
  非 self-hosted runner）。
- 仍走固定 SSH host key、独立 SSH key、forced command。
- 上传后由 host dispatcher 校验目标摘要，在 `flock` 内交给 host controller，再执行目标版本 deploy script；controller 持久化状态并验证 evidence。
- 完成前验证运行 image、Compose、schema、health、readiness、storage verifier、
  TLS 和卷保留 evidence；缺任一验收不声明成功。
- 产出 `staging-rollback-evidence`，不含 secret。

旧 v0.1.1 的 `deploy-staging.sh` 本身带有失败补偿：激活失败会恢复到执行前的
active Compose/image；因此回滚失败不会停在“未知版本”。

## 5. Version-aware Monitor / Recovery

`monitor-staging.yml` 与 `drill-staging-recovery.yml` 不再只固定最新版本：

1. 先尝试 `v0.2.0` 的精确 script SHA 和摘要集合。
2. 只有以下可证明的版本选择失败才允许回退 v0.1.1：
   - dispatcher 精确返回 `staging deploy command rejected`；
   - monitor evidence `status=failed` 且 `last_phase=compose-identity`；
   - recovery evidence `status=failed` 且 `last_phase=source-identity`。
3. SSH 网络错误、锁争抢、真实 health/readiness/TLS/PostgreSQL/S3 失败、evidence
   损坏、recovery 已启动临时资源后的失败，**一律不 fallback**，直接失败。
4. 回退尝试也必须使用 v0.1.1 的精确 script/Compose/image；第二次失败直接结束，
   不会掩盖为成功。

旧主机的旧 dispatcher 只接受旧 pair，因此 P0 可回退到 v0.1.1 monitor；新主机
bootstrap 同时安装两个版本的 monitor/recovery，并允许两个精确 SHA。

## 6. 主机 Bootstrap 变更

`scripts/bootstrap-staging-host.sh` 新增：

- 安装 `asteria-staging-monitor-v0.1.1` 和 `asteria-staging-recovery-v0.1.1`；
- 安装 root-owned `asteria-staging-controller`、
  root-owned `staging_runtime_verifier.py` 和
  `/etc/asteria-drive/staging-releases.json`；
- dispatcher 按请求 SHA 选择精确版本路径，不接受任意 SHA；
- `upload-compose`/`upload-script` 只接受两个 release bundle 的精确 pair；
- 创建 `/run/asteria-drive`（`root:<deploy_group> 0750`）和
  `/run/asteria-drive/staging.lock`（`root:<deploy_group> 0660`），并通过
  `/etc/tmpfiles.d/asteria-drive-staging.conf` 在重启后重建；
- `deploy`/`status`/`recovery` 使用该锁的 `flock`，先校验 owner/group/mode 和
  symlink，与 GitHub concurrency 双重串行化；
- 新增 `ASTERIA_STAGING_MONITOR_V011_B64` /
  `ASTERIA_STAGING_RECOVERY_V011_B64` / `ASTERIA_STAGING_CONTROLLER_B64` /
  `ASTERIA_STAGING_RUNTIME_VERIFIER_B64` 输入，均按 SHA-256 固定。

## 7. P0/P1 审查修复

- **P0-A 锁生命周期**：固定锁路径、root:deploy_group 目录/文件、0660、tmpfiles
  重启重建、dispatcher `validate_lock` fail-closed；`internal/cicheck` 的 Linux
  锁集成测试覆盖获取/释放、并发、重建、symlink 拒绝。
- **P0-B 真实执行链**：dispatcher 的 `deploy` 不再直接执行 deploy script，而是
  在锁内调用 host controller（root-owned 文件，但以 deploy UID 运行）；controller
  持久化 `controller-state.json`（deploy 所有、0600，仅操作日志），在激活前写
  `ATTEMPTING`，完成后仅在实时 Runtime Verifier 通过后原子提交 `ACTIVE`，
  失败/证据不完整进入 `RECOVERY_REQUIRED`；`ATTEMPTING` 中断不会重复执行。
- **P0-state 信任边界**：不声称 root:root 状态。deploy 用户属于 docker 组，
  本就具备主机管理员等效能力；状态可由 deploy 篡改，因此只作为拒绝重复执行
  的日志。controller 校验 state regular file、非 symlink、owner=当前 UID、
  0600、parent=deploy root；任何 ACTIVE 都必须经真实 Docker/DB/readiness 证明。
- **P1-A 切换前验证**：Go controller 在 Deploy/Rollback 前读取 active state，
  要求版本在 allowlist、Compose/image/schema 与 bundle 一致且健康；否则
  进入 `RECOVERY_REQUIRED`。Host controller 在切换前从 active Compose 读取版本和
  摘要，并由 Runtime Verifier 检查实际容器镜像、镜像 ID、容器状态、API
  readiness/health 和 PostgreSQL schema。
- **P1-B 回滚 Schema/Evidence**：Rollback 与 compensate 校验 evidence 的
  schema/status/last_phase/run ID/attempt/workflow SHA/release source/
  image/Compose/schema；回滚后重新读取真实 host snapshot，`rollback_succeeded=true`
  但容器未恢复不得写 ACTIVE。
- **P1-C 原子 ACTIVE**：ACTIVE 记录在一次 `Store.Save` 中完整提交（含 identity
  和 digest）；失败回退原状态；FileStore 使用 tmp+fsync+rename+目录 fsync，
  corrupt/incomplete 记录拒绝加载；replay/Resume 重新读取 host。
- **P1-D 版本选择分类**：只有 dispatcher rejection 或
  `compose-identity`/`source-identity` 证据失败才 fallback；真实健康失败、
  网络、锁、evidence 损坏和 recovery 部分成功失败均直接失败。

## 8. 测试

- `internal/staging`：38 状态机/bundle 测试，覆盖 P1-A/B/C 负向路径。
- `internal/cicheck`：release bundle 摘要、rollback workflow 固定点、bootstrap
  双 pair/锁/controller/verifier、monitor/recovery 版本选择分类、Linux 锁生命周期、
  非 root 锁获取与替换拒绝、Python host controller 故障注入入口。
- `scripts/tests`：26 个隔离 Python 测试，直接驱动 Controller/RuntimeVerifier
  分支和伪 Docker/DB 适配器：镜像漂移、API 停止、schema 错误、未恢复的假回滚、
  错误 run/workflow、ACTIVE replay 漂移、状态权限/属主/symlink 篡改。
- Shell：`bash -n` 覆盖 bootstrap、deploy、monitor、recovery、controller。
- CI：Go test/race/vet/build、action-validator、OpenAPI、Security。
- `action-validator` 的 schema 尚不支持 deploy/rollback workflow 使用的
  `permissions.attestations`，因此这两个 workflow 由 `internal/cicheck` 的 YAML
  解析和固定点断言覆盖；`npm run lint:actions` 覆盖 ci/security/monitor/recovery。

## 9. 状态定义与主机验证

状态必须区分：

- `IMPLEMENTED`：仓库代码已写。
- `VERIFIED_LOCAL`：本地 Go/Shell 隔离测试。
- `VERIFIED_CI`：PR CI/Security 全绿。
- `READY_FOR_HOST_VALIDATION`：仓库实现和真实执行契约可测试，但未接触主机。
- `ROLLBACK_VERIFIED`：真实主机回滚演练通过（尚未达到）。
- `STAGING_DEPLOYED`：真实主机 v0.2.0 部署通过（尚未达到）。

仓库实现不等于已部署。主体验证前仍然需要：

1. 人工批准并在维护窗口执行新的 root bootstrap；
2. 用真实 host 只读采集 active Compose/image/schema、锁 metadata，以及
   `controller-state.json` 的 `deploy:deploy 0600` ownership/mode；
3. 在隔离维护窗口完成 `v0.2.0` deploy；
4. 执行 `rollback-v0.1.1` 并验证 evidence、卷保留、schema 和监控；
5. 再执行一次 recovery drill。

未完成上述真实主机演练前，状态只能是 `READY_FOR_HOST_VALIDATION`，不能标记
`ROLLBACK_VERIFIED` 或 `STAGING_DEPLOYED`。
