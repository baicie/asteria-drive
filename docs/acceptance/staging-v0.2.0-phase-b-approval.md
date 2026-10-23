# Phase B 审批材料：Staging Root Bootstrap v0.2.0

- 材料状态：`READY_FOR_APPROVAL`（Phase B 本身）
- 执行依赖：人工批准 + 目标主机 root shell
- 关联 PR：#82（验收文档）；实现 PR：[#83](https://github.com/baicie/asteria-drive/pull/83)
- 操作 Runbook：[staging-v0.2.0-bootstrap-runbook.md](staging-v0.2.0-bootstrap-runbook.md)

准备时间：2026-10-23T08:55:00Z

本材料不包含 SSH 私钥、数据库密码、应用 Secret、PKI 私钥或 Docker volume 内容。

## 1. 审批字段

| 字段 | 值（执行前填写） |
| --- | --- |
| 目标 host（来自 `ASTERIA_STAGING_SSH_HOST`） | `TBD` |
| SSH 端口（来自 `ASTERIA_STAGING_SSH_PORT`） | `TBD` |
| 部署用户（默认 `asteria-deploy`） | `TBD` |
| 固定 ED25519 host key 指纹（来自 `ASTERIA_STAGING_SSH_KNOWN_HOSTS`） | `TBD` |
| 维护窗口开始（UTC） | `TBD` |
| 维护窗口结束（UTC） | `TBD` |
| 操作者 | `TBD` |
| 复核人 | `TBD` |
| 回滚负责人 | `TBD` |
| Change record / 工单 | `TBD` |

## 2. 固定摘要

| 文件 | SHA-256 |
| --- | --- |
| `scripts/bootstrap-staging-host.sh` | `2128f1a67fd5e21b9ce0b89ee5b7e56e97693c154c05e3828c48670af1322b56` |
| `scripts/monitor-staging.sh` | `d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69` |
| `scripts/recover-staging.sh` | `91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4` |
| `scripts/deploy-staging.sh` | `007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f` |
| `infra/docker/staging/compose.yaml` | `cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005` |
| `infra/docker/staging/releases/v0.1.1/compose.yaml` | `aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637` |
| `infra/docker/staging/releases/v0.1.1/deploy-staging.sh` | `cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044` |
| `infra/docker/staging/releases/v0.1.1/monitor-staging.sh` | `3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279` |
| `infra/docker/staging/releases/v0.1.1/recover-staging.sh` | `3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a` |
| `scripts/staging-release-controller.py` | `ebc339f407f704f72c72bf85769f2c8818e4e288c3600e86d2332aacf80eb751` |

## 3. 只读基线（执行前必须记录）

在目标 host 上以 root 执行 Runbook 第 2 节，并保存原始输出：

- `hostname`、`date -u`、`id`
- Docker Engine / Compose 版本
- active Compose 路径和 SHA-256
- `/usr/local/libexec/asteria-staging-{dispatch,monitor,recovery,controller}` 摘要、owner、mode
- `/run/asteria-drive` 和 `staging.lock` 的 owner/group/mode，以及 tmpfiles.d 配置
- `/usr/local/libexec/asteria-staging-monitor-v0.1.1` 和
  `asteria-staging-recovery-v0.1.1` 的存在性、摘要、owner、mode（若已安装）
- `/etc/asteria-drive/staging-dispatch.conf` 摘要、owner、mode
- `docker compose ps` 和 API `.Config.Image`
- `docker volume ls --filter name=asteria-drive-staging`
- PKI 目录 metadata（只读 `stat`，不读取私钥内容）
- 磁盘/内存/容量只读检查

预期旧基线（必须与主机实际观测比对，不得假设）：

| 项目 | v0.1.1 记录值 |
| --- | --- |
| API image | `sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842` |
| Compose SHA-256 | `aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637` |
| monitor script SHA-256 | `3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279` |
| recovery script SHA-256 | `3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a` |
| deploy script SHA-256 | `cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044` |

如果只读基线与任何记录不符：**ABORT**，不执行 bootstrap。

## 4. 备份

执行前创建：

```bash
backup="/root/asteria-staging-bootstrap-backup-$(date -u +%Y%m%dT%H%M%SZ)"
```

备份内容：旧 dispatcher、monitor、recovery、staging-dispatch.conf、deploy `authorized_keys`。

| 项目 | 值 |
| --- | --- |
| 备份文件 | `TBD` |
| 备份 SHA-256 | `TBD` |
| 备份 owner/mode | `root:root 0600` |

不备份/不导出：SSH 私钥、应用 Secret、数据库密码、PKI 私钥、Docker volume 数据。

## 5. 执行步骤

严格按 Runbook 第 1–8 节执行：

1. 本地摘要校验；
2. 主机只读基线；
3. 备份 root-owned 文件；
4. 准备新 bootstrap/monitor/recovery；
5. 从现有 authorized_keys 提取 deploy 公钥；
6. 生成 monitor/recovery v0.1.1/v0.2.0 与 controller 的 base64 输入；
7. 运行 `bootstrap-staging-host.sh`；
8. 只读验证安装摘要、owner、mode 和 dispatcher config。

不读取私有 key，不打印 Secret。

## 6. 成功条件

全部满足才可记录 Phase B success：

- bootstrap 输出最后一行 `staging host bootstrap is ready`；
- `/usr/local/libexec/asteria-staging-monitor` SHA-256 = `d1e186cb...`；
- `/usr/local/libexec/asteria-staging-recovery` SHA-256 = `91c45a19...`；
- dispatcher/config 为 root:root，脚本 755，config 644；
- `authorized_keys` 为 deploy 用户 600，且只含批准的 deploy 公钥；
- dispatcher config 中 compose/deploy/monitor/recovery 四个摘要与固定点一致；
- 现有 API 容器未被重启或修改；
- 活动数据卷、Secret 文件、PKI 未被覆盖。

## 7. 失败 / 中止条件

立即 ABORT 并执行回滚：

- 本地或主机摘要不匹配；
- secret/PKI 部分初始化；
- 备份无法创建或 SHA-256 无法记录；
- bootstrap 非零退出；
- 安装后 owner/mode 或摘要不匹配；
- 任何应用 Secret/PKI 被意外改写；
- 无法在维护窗口内完成 Phase C 前置检查。

Phase B 失败不授权 Phase C。

## 8. Phase B 回滚

1. 不删除 volume；
2. 恢复第 4 节备份；
3. 校验旧 dispatcher/monitor/recovery/config/authorized_keys 的摘要和 owner/mode；
4. **不得宣称旧 dispatcher 恢复即恢复 GitHub monitor**；PR #83 的版本感知 monitor/recovery 才会按 allowlist 选择 active 版本。完整回滚见 Runbook 9.3；
5. 若无法恢复，标记 `FAILED` 并停止。

## 9. 监控/恢复交接

Phase B 完成后、Phase C 完成前：

- monitor 可能 fail-closed；
- 不得通过暂停 workflow 或修改 main 来掩盖；
- 记录预期失败为摘要不匹配，不是服务不可用；
- Phase C 成功并获得新 deployment artifact 后，才可手工触发 monitor/recovery。

## 10. 审批签署

```text
批准人 / 角色：
UTC 时间：
目标 host / host key 指纹：
维护窗口：
备份 SHA-256：
批准 Phase B：YES / NO
Phase C 另行审批：YES / NO
```
