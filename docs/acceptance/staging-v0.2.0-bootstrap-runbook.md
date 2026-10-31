# Staging Root Bootstrap 最小权限 Runbook（v0.2.0）

- 状态：Phase B `BLOCKED_APPROVAL`，尚未执行；Phase C 仓库侧已实现为 `READY_FOR_HOST_VALIDATION`，尚未部署
- 源提交：`cf63a93480ba26c449ee2d0f93d3ea74df307c06`
- 目标：把 root-owned dispatcher / monitor / recovery / 配置更新到 v0.2.0 固定点
- 不在本 Runbook 中读取、导出或写入任何应用 Secret、数据库密码、PKI 私钥或 SSH 私钥
- 以下命令以默认 `asteria-deploy` 为例；若现有 host 使用其他用户，必须替换命令中的用户名并显式设置 `ASTERIA_DEPLOY_USER`。

## 0. 批准前必须确认

| 字段 | 值 |
| --- | --- |
| 目标 staging host | 从 GitHub `staging` Environment secret `ASTERIA_STAGING_SSH_HOST` 取，不在文档记录 |
| 部署用户 | 默认 `asteria-deploy`；必须与现有 host 一致 |
| SSH 端口 | 从 `ASTERIA_STAGING_SSH_PORT` 取 |
| host key | 从 `ASTERIA_STAGING_SSH_KNOWN_HOSTS` 取；必须是已固定的 ED25519 key |
| 维护窗口 | 最近一次成功 monitor 之后、下一次 `:23Z` 之前 |
| 操作者 / 复核人 | 必须明确 |
| 批准时间 | UTC |
| 回滚负责人 | 必须明确 |

## 1. 本地固定点检查（操作员工作站）

```bash
cd /path/to/asteria-drive
git rev-parse HEAD
# 必须为 cf63a93480ba26c449ee2d0f93d3ea74df307c06

sha256sum \
  scripts/bootstrap-staging-host.sh \
  scripts/staging-release-controller.py \
  scripts/staging_runtime_verifier.py \
  scripts/monitor-staging.sh \
  scripts/recover-staging.sh \
  scripts/deploy-staging.sh \
  infra/docker/staging/compose.yaml \
  infra/docker/staging/releases/v0.1.1/monitor-staging.sh \
  infra/docker/staging/releases/v0.1.1/recover-staging.sh \
  infra/docker/staging/releases/v0.1.1/compose.yaml \
  infra/docker/staging/releases/v0.1.1/deploy-staging.sh
```

预期：

```text
f4483d165a907388427632bf5412c5309034f1a4be9c15428238578aa2291cfa  scripts/bootstrap-staging-host.sh
5ccb8d18aae92d1040c20d76eb27dadd0c9ca7c04334d733b34bc4f69d9eaa38  scripts/staging-release-controller.py
77e537f7cfc8824c8558b72466161065770b4488110458c84e0246dadcf422d7  scripts/staging_runtime_verifier.py
d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69  scripts/monitor-staging.sh
91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4  scripts/recover-staging.sh
007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f  scripts/deploy-staging.sh
cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005  infra/docker/staging/compose.yaml
3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279  infra/docker/staging/releases/v0.1.1/monitor-staging.sh
3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a  infra/docker/staging/releases/v0.1.1/recover-staging.sh
aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637  infra/docker/staging/releases/v0.1.1/compose.yaml
cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044  infra/docker/staging/releases/v0.1.1/deploy-staging.sh
```

任一不匹配：停止，不执行 Phase B。

## 2. 主机只读基线检查（root）

以下命令不读取 Secret 文件内容，只读元数据、摘要、容器和卷状态。

```bash
hostname
date -u
id

docker version --format '{{.Server.Version}}'
docker compose version

# 当前 active Compose 和 root-owned 文件摘要
sha256sum \
  /opt/asteria-drive/staging/compose.yaml \
  /usr/local/libexec/asteria-staging-dispatch \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-recovery \
  /usr/local/libexec/asteria-staging-controller \
  /usr/local/libexec/staging_runtime_verifier.py \
  /etc/asteria-drive/staging-dispatch.conf \
  /etc/asteria-drive/staging-releases.json \
  /run/asteria-drive/staging.lock

# 所有权/权限
stat -c '%U:%G %a %n' \
  /opt/asteria-drive/staging \
  /opt/asteria-drive/staging/compose.yaml \
  /usr/local/libexec/asteria-staging-dispatch \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-recovery \
  /usr/local/libexec/asteria-staging-controller \
  /usr/local/libexec/staging_runtime_verifier.py \
  /etc/asteria-drive/staging-dispatch.conf \
  /etc/asteria-drive/staging-releases.json \
  /run/asteria-drive \
  /run/asteria-drive/staging.lock \
  /etc/tmpfiles.d/asteria-drive-staging.conf \
  /etc/asteria-drive/staging-postgres-pki \
  /etc/asteria-drive/staging-postgres-pki/issuer

锁的固定契约：`/run/asteria-drive` 为 `root:<deploy_group> 0750`，
`/run/asteria-drive/staging.lock` 为 `root:<deploy_group> 0660`；`controller-state.json` 为 `asteria-deploy:<deploy_group> 0600`（deploy 可篡改日志，不是 root 凭据）；
`/etc/tmpfiles.d/asteria-drive-staging.conf` 为 `root:root 0644`。任何
symlink、非 root owner、非 0660 mode 或错误 group 都必须 fail-closed。

# 当前运行身份（只读）
docker compose -p asteria-drive-staging -f /opt/asteria-drive/staging/compose.yaml ps
docker inspect --format '{{.Config.Image}}' asteria-drive-staging-api-1

# 数据/Secret/PKI 只检查卷存在和挂载点，不读取内容
docker volume ls --filter name=asteria-drive-staging
getent passwd asteria-deploy
```

记录的 v0.1.1 基线（用于比对，不代表已从主机读取）：

| 项目 | v0.1.1 记录值 |
| --- | --- |
| API image | `ghcr.io/baicie/asteria-drive@sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842` |
| Compose SHA-256 | `aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637` |
| monitor script SHA-256 | `3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279` |
| recovery script SHA-256 | `3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a` |
| deploy script SHA-256 | `cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044` |

如果主机实际摘要与上述任一记录不符，停止。不要假设主机仍在 v0.1.1。

## 3. 备份 root-owned 文件（不备份数据卷和 Secret）

```bash
umask 077
backup="/root/asteria-staging-bootstrap-backup-$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$backup"

deploy_home="$(getent passwd asteria-deploy | cut -d: -f6)"
[[ -n "$deploy_home" ]]

tar -C / -czf "$backup/root-owned-before-v0.2.0.tgz" \
  usr/local/libexec/asteria-staging-dispatch \
  usr/local/libexec/asteria-staging-monitor \
  usr/local/libexec/asteria-staging-recovery \
  etc/asteria-drive/staging-dispatch.conf \
  "${deploy_home#/}/.ssh/authorized_keys"

sha256sum "$backup/root-owned-before-v0.2.0.tgz" |
  tee "$backup/root-owned-before-v0.2.0.tgz.sha256"
```

该备份只包含 root-owned 脚本、配置和 deploy 公钥 `authorized_keys`，不包含应用 Secret、PKI 私钥或 Docker volume 内容。

## 4. 准备新脚本（root）

```bash
stage="/root/asteria-staging-bootstrap-$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 "$stage"

# 由操作员用批准的传输方式放到 root-only 目录
install -m 0700 /root/transfer/bootstrap-staging-host.sh "$stage/"
install -m 0600 /root/transfer/monitor-staging.sh "$stage/"
install -m 0600 /root/transfer/recover-staging.sh "$stage/"
install -m 0600 /root/transfer/monitor-staging-v0.1.1.sh "$stage/"
install -m 0600 /root/transfer/recover-staging-v0.1.1.sh "$stage/"
install -m 0600 /root/transfer/staging-release-controller.py "$stage/"
install -m 0600 /root/transfer/staging_runtime_verifier.py "$stage/"

sha256sum "$stage/bootstrap-staging-host.sh" \
          "$stage/monitor-staging.sh" \
          "$stage/recover-staging.sh" \
          "$stage/monitor-staging-v0.1.1.sh" \
          "$stage/recover-staging-v0.1.1.sh" \
          "$stage/staging-release-controller.py" \
          "$stage/staging_runtime_verifier.py"
```

必须与第 1 节摘要一致；两个 v0.1.1 文件来自
`infra/docker/staging/releases/v0.1.1/`，摘要分别为 `3ef39670...` 和
`3b2bb836...`。

## 5. 从现有 authorized_keys 提取 deploy 公钥

不读取 SSH 私钥。使用当前已经授权的公钥：

```bash
deploy_home="$(getent passwd asteria-deploy | cut -d: -f6)"
authorized="$deploy_home/.ssh/authorized_keys"
[[ -f "$authorized" && ! -L "$authorized" ]]

mapfile -t keys < <(grep -oE 'ssh-ed25519 [A-Za-z0-9+/=]+ [^ ]*' "$authorized")
[[ "${#keys[@]}" -eq 1 ]] || { echo 'expected exactly one deploy public key'; exit 1; }

export ASTERIA_DEPLOY_PUBLIC_KEY_B64="$(printf '%s' "${keys[0]}" | base64 -w0)"
test -n "$ASTERIA_DEPLOY_PUBLIC_KEY_B64"
```

## 6. 准备 monitor/recovery base64 输入

```bash
export ASTERIA_STAGING_MONITOR_B64="$(base64 -w0 "$stage/monitor-staging.sh")"
export ASTERIA_STAGING_RECOVERY_B64="$(base64 -w0 "$stage/recover-staging.sh")"
export ASTERIA_STAGING_MONITOR_V011_B64="$(base64 -w0 "$stage/monitor-staging-v0.1.1.sh")"
export ASTERIA_STAGING_RECOVERY_V011_B64="$(base64 -w0 "$stage/recover-staging-v0.1.1.sh")"
export ASTERIA_STAGING_CONTROLLER_B64="$(base64 -w0 "$stage/staging-release-controller.py")"
export ASTERIA_STAGING_RUNTIME_VERIFIER_B64="$(base64 -w0 "$stage/staging_runtime_verifier.py")"
test -n "$ASTERIA_STAGING_MONITOR_B64"
test -n "$ASTERIA_STAGING_RECOVERY_B64"
test -n "$ASTERIA_STAGING_MONITOR_V011_B64"
test -n "$ASTERIA_STAGING_RECOVERY_V011_B64"
test -n "$ASTERIA_STAGING_CONTROLLER_B64"
test -n "$ASTERIA_STAGING_RUNTIME_VERIFIER_B64"
```

若系统 `base64` 不支持 `-w0`，使用 `base64 "$file" | tr -d '\n'`。

## 7. 执行 root bootstrap（需 Phase B 审批）

```bash
cd "$stage"
./bootstrap-staging-host.sh
```

预期最后一行：

```text
staging host bootstrap is ready
```

该步骤只安装/覆盖：

- `/usr/local/libexec/asteria-staging-dispatch`
- `/usr/local/libexec/asteria-staging-monitor`
- `/usr/local/libexec/asteria-staging-recovery`
- `/etc/asteria-drive/staging-dispatch.conf`
- `asteria-deploy` 的 `authorized_keys`（只保留本次提供的 deploy 公钥）

影响范围：

- 会覆盖上述 root-owned 文件；不会重启现有容器。
- 会把 deploy 用户加入 `docker` 组（幂等）。
- 不会修改应用 Secret/PKI 内容；完整存在时跳过创建，部分存在时 fail-closed。
- 不会创建或删除持久化数据卷；缺失的 secret volume 会被创建。
- `authorized_keys` 会被替换为单一 deploy 公钥，其他旧 key 不再被保留。

## 8. Bootstrap 后只读验证

```bash
sha256sum \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-recovery

stat -c '%U:%G %a %n' \
  /usr/local/libexec/asteria-staging-dispatch \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-recovery \
  /etc/asteria-drive/staging-dispatch.conf \
  "$deploy_home/.ssh/authorized_keys"

cat /etc/asteria-drive/staging-dispatch.conf
```

预期：

- monitor SHA-256 = `d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69`
- recovery SHA-256 = `91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4`
- 三个脚本为 `root:root 755`，配置为 `root:root 644`，authorized_keys 为 deploy 用户 `600`
- 配置中的 compose/deploy/monitor/recovery 四个摘要与第 1 节一致

此时 active API 仍是 v0.1.1；monitor/recovery 会因摘要不匹配而 fail-closed，必须立即进入 Phase C。

## 9. 失败与回滚

### 9.1 Bootstrap 失败

1. 不删除任何 volume。
2. 优先重新运行同一 bootstrap；脚本对已完整存在的 secret/PKI 不做隐式轮换，对部分状态 fail-closed。
3. 若重复失败，恢复第 3 节备份：

```bash
tar -C / -xzf "$backup/root-owned-before-v0.2.0.tgz"
sha256sum /usr/local/libexec/asteria-staging-* \
  /etc/asteria-drive/staging-dispatch.conf
```

4. **恢复旧 root-owned 文件后，monitor 选择由 PR #83 的版本感知逻辑决定。** 如果 workflow 已包含版本感知 fallback，它会尝试 v0.2.0 失败后使用 v0.1.1 的精确 script/compose/image 三元组；如果仍是旧版固定 v0.2.0 的 workflow，才需要按 9.3 的 legacy 路径临时回滚 workflow pin。

### 9.2 部署失败且自动回滚到 v0.1.1

适用前提：deployment 证据显示 `rollback_succeeded=true`，active runtime 已由部署脚本恢复到 previous v0.1.1。

1. 核对 deployment artifact：`rollback_attempted=true`、`rollback_succeeded=true`、`deployed_image_ref`/`deployed_image_id` 回到旧值。
2. 不删除 `postgres-data`、`seaweedfs-data` 或任何 secret volume。
3. 恢复第 3 节旧 root-owned 文件备份。PR #83 的版本感知 monitor/recovery 会从 v0.2.0 失败后回退到 v0.1.1；不需要为了 monitor 再回滚 workflow pin。
4. 若 workflow 还没有版本感知逻辑（PR #83 未合并），才执行 9.3 的 legacy workflow pin 回滚；在完成前 monitor/recovery 继续 fail-closed。
5. 如果 active Compose 已回到 v0.1.1，不需要再运行 deploy；如果 rollback 未成功，停止并标记 `FAILED`，不得用任意 SSH Docker 命令绕过 workflow。

### 9.3 完整回滚：固定 bundle + 版本感知监控（优先路径）

PR #83 之后，优先路径不再需要把 main 的 monitor/recovery pin 改回旧版本。最小权限、可审核、不绕过 SHA-256 的路线：

1. **冻结新变更**：停止继续部署，保留 deployment 失败 evidence 和 artifact digest。
2. **确认主机 bootstrap 已安装双版本 monitor/recovery**：新 dispatcher 同时接受 v0.1.1/v0.2.0 的精确 pair；不需要恢复旧 dispatcher 才能监控旧版本。若尚未执行新 bootstrap，先按第 3 节备份并在维护窗口完成。
3. **LEGACY fallback（仅在 PR #83 之前）**：若 dispatcher/workflow 仍是旧架构，才需要受保护 main 的回滚 PR 恢复以下固定点；PR #83 合并后不需要这一步：
   - `.github/workflows/monitor-staging.yml` 的 `MONITOR_SCRIPT_SHA256`、Compose SHA、API digest；
   - `.github/workflows/drill-staging-recovery.yml` 的 `RECOVERY_SCRIPT_SHA256`、Compose SHA、API digest；
   - `.github/workflows/deploy-staging.yml` 的 tag/source/API digest/Compose SHA 和 confirmation；
   - 对应的 `scripts/validate`/cicheck 契约。
   参考旧基线提交 `2dc40b92e621cee3f05cb6d21cebda00070eee6e` 的 staging 固定点。

   旧 v0.1.1 固定点（回滚 PR 必须逐项核对）：

   | 项目 | 旧值 |
   | --- | --- |
   | Release / source | `v0.1.1` / `e8d26ded6e9138bbfdeac60f2487c5f835ab61a5` |
   | API image digest | `sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842` |
   | Compose SHA-256 | `aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637` |
   | Deploy script SHA-256 | `cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044` |
   | Monitor script SHA-256 | `3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279` |
   | Recovery script SHA-256 | `3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a` |
   | Deploy confirmation | `deploy-v0.1.1` |

   注意：v0.1.1 的 tag object 为 `1b0a4b3e79f38b3b8f6b5a6f2bbeacc2f4533994`，peeled source 为 `e8d26de...`；不要用错 tag object。
4. **运行 `Rollback staging`**：手动 dispatch，输入 `rollback-v0.1.1`，使用固定 v0.1.1 bundle；严格 host key、OCI provenance、Compose/script SHA 和 evidence 校验全部保留。不得用 SSH 直接改 Docker。
5. **核对运行态**：
   - 若 deployment 自动回滚已恢复 v0.1.1 active Compose/image，直接进入监控恢复；
   - 若运行态仍是 v0.2.0，只能在回滚 PR 合并后，通过受控的旧版 deploy workflow/经批准的回滚 workflow 部署 v0.1.1；不得用 SSH 直接改 Docker。
6. **验证活动版本属于 allowlist**：host 上 v0.1.1 monitor/recovery 的精确 SHA、active Compose SHA 和 image digest 必须与 bundle 一致；版本感知 workflow 必须选中 v0.1.1。任一不匹配，monitor/recovery 仍会失败。
7. **验证监控/恢复**：手工触发 monitor/recovery（不是依赖 cron），保存 artifact；确认没有删除或重建活动数据卷。
8. **记录证据**：host 文件摘要、`Rollback staging` run ID、artifact digest、active Compose/image digest、restore 结果；只有走 legacy 路径才记录 workflow pin PR。

当前限制：

- PR #83 已加入 `Rollback staging` workflow；成功部署后走固定 v0.1.1 bundle 的受控回滚路径。
- PR #83 的版本感知路径不需要 workflow pin 回滚 PR；只有旧架构 legacy fallback 才需要单独的 pin PR。
- 因此仓库侧状态为 **`READY_FOR_HOST_VALIDATION`**；真实主机回滚未执行前不得宣称 `ROLLBACK_VERIFIED` 或“自动回滚安全”。
- 失败尝试内的自动回滚仅覆盖 previous active Compose 存在的 runtime 恢复；成功部署后的回滚由 `Rollback staging` 的固定 bundle 路径覆盖。

### 9.4 监控连续性

- 不依赖 cron 精确时间：GitHub Actions 调度可能延迟 1 小时以上；升级原子性必须由维护窗口和人工检查保证。
- bootstrap 完成后、deployment 完成前，monitor 会按 allowlist 选择 v0.1.1；若两个版本都不匹配则 fail-closed，记录为摘要/版本不匹配。
- 完整回滚后，只通过 workflow 手工触发 monitor/recovery；不要用任意 SSH 命令验证或修改服务。

## 10. 过渡窗口规则

- 不暂停、不修改 monitor/recovery workflow；不以禁用监控换取窗口。
- 维护窗口必须同时覆盖 Phase B 和 Phase C；窗口开始前完成只读基线、备份和审批。
- 不依赖 `:23Z` 作为硬边界；观测到 run 创建于 07:42:43Z，但无法证明对应哪一次 cron occurrence。窗口以人工开始/结束和状态检查为准。
- 窗口内 monitor 触发时，版本感知 workflow 会选择 v0.1.1 的精确 pair；若两个版本都不匹配或 host 正在锁内执行，才 fail-closed，并记录为预期信号而不是数据故障。
- 若窗口内无法完成 deployment，执行第 9 节的 `Rollback staging` 固定 bundle 回滚；不留下半升级 host。
- recovery 每周 `17 4 * * 1`，避开该窗口。

## 11. 审批输出要求

执行 Phase B 前，必须由批准人确认：

- 目标 host 和 host key 指纹；
- 维护窗口起止 UTC；
- 第 1 节摘要；
- 第 3 节备份文件路径和 SHA-256；
- 影响范围：仅 root-owned dispatcher/monitor/recovery/config/authorized_keys；
- 回滚：第 9 节；
- Phase C 需要**另一条**明确部署授权；
- Phase C 在真实主机回滚演练和独立批准前保持 `READY_FOR_HOST_VALIDATION`；
- 任何人不得把“自动失败回滚”表述为“成功部署后的自动回滚”。
