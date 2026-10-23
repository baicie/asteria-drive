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
- 上传后由 host dispatcher 校验目标摘要，再执行 deploy script。
- 完成前验证运行 image、Compose、schema、health、readiness、storage verifier、
  TLS 和卷保留 evidence；缺任一验收不声明成功。
- 产出 `staging-rollback-evidence`，不含 secret。

旧 v0.1.1 的 `deploy-staging.sh` 本身带有失败补偿：激活失败会恢复到执行前的
active Compose/image；因此回滚失败不会停在“未知版本”。

## 5. Version-aware Monitor / Recovery

`monitor-staging.yml` 与 `drill-staging-recovery.yml` 不再只固定最新版本：

1. 先尝试 `v0.2.0` 的精确 script SHA 和摘要集合；
2. 若失败，再尝试 `v0.1.1` 的精确 script SHA 和摘要集合；
3. 只允许二者之一成功，并校验 evidence 中对应的 Compose/image/script 摘要；
4. 两个都失败则 workflow 失败；未知版本不会静默通过。

旧主机的旧 dispatcher 只接受旧 pair，因此 P0 可回退到 v0.1.1 monitor；新主机
bootstrap 同时安装两个版本的 monitor/recovery，并允许两个精确 SHA。

## 6. 主机 Bootstrap 变更

`scripts/bootstrap-staging-host.sh` 新增：

- 安装 `asteria-staging-monitor-v0.1.1` 和 `asteria-staging-recovery-v0.1.1`；
- dispatcher 按请求 SHA 选择精确版本路径，不接受任意 SHA；
- `upload-compose`/`upload-script` 只接受两个 release bundle 的精确 pair；
- `deploy`/`status`/`recovery` 使用 `/run/asteria-drive-staging.lock` 的
  `flock`，与 GitHub concurrency 双重串行化；
- 新增 `ASTERIA_STAGING_MONITOR_V011_B64` /
  `ASTERIA_STAGING_RECOVERY_V011_B64` 输入，均按 SHA-256 固定。

## 7. 测试

- `internal/staging`：allowlist、bundle 摘要、状态迁移、幂等重放、并发锁、
  状态写入失败、激活失败补偿、验证失败补偿、回滚验证失败、未知版本、schema
  不匹配、monitor 版本识别、resume 重建。
- `internal/cicheck`：release bundle 文件摘要、rollback workflow 固定点、
  bootstrap 双 pair、monitor/recovery 版本感知、无任意 SHA / secret / bypass。
- Shell：`bash -n` 覆盖 bootstrap、deploy、monitor、recovery。
- CI：Go test/race/vet/build、action-validator、OpenAPI、Security。

## 8. 尚未执行 / 需要主机验证

仓库实现不等于已部署。主体验证前仍然需要：

1. 人工批准并在维护窗口执行新的 root bootstrap；
2. 用真实 host 只读采集 active Compose/image/schema；
3. 在隔离维护窗口完成 `v0.2.0` deploy；
4. 执行 `rollback-v0.1.1` 并验证 evidence、卷保留、schema 和监控；
5. 再执行一次 recovery drill。

未完成上述真实主机演练前，状态只能是 `READY_FOR_HOST_VALIDATION`，不能标记
`ROLLBACK_VERIFIED` 或 `STAGING_DEPLOYED`。
