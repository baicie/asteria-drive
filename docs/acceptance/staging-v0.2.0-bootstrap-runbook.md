# Staging Root Bootstrap 最小权限 Runbook（v0.2.0）

- 状态：Phase B `BLOCKED_APPROVAL`，尚未执行；Phase C 因完整回滚契约未闭合为 `BLOCKED_ROLLBACK_CONTRACT`
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
  scripts/monitor-staging.sh \
  scripts/recover-staging.sh \
  scripts/deploy-staging.sh \
  infra/docker/staging/compose.yaml
```

预期：

```text
e1f9013f4ca9de66fae7a8c64c24ef7d50e86d627bcbec1860be08e79247c519  scripts/bootstrap-staging-host.sh
d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69  scripts/monitor-staging.sh
91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4  scripts/recover-staging.sh
007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f  scripts/deploy-staging.sh
cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005  infra/docker/staging/compose.yaml
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
  /etc/asteria-drive/staging-dispatch.conf

# 所有权/权限
stat -c '%U:%G %a %n' \
  /opt/asteria-drive/staging \
  /opt/asteria-drive/staging/compose.yaml \
  /usr/local/libexec/asteria-staging-dispatch \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-recovery \
  /etc/asteria-drive/staging-dispatch.conf \
  /etc/asteria-drive/staging-postgres-pki \
  /etc/asteria-drive/staging-postgres-pki/issuer

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

sha256sum "$stage/bootstrap-staging-host.sh" \
          "$stage/monitor-staging.sh" \
          "$stage/recover-staging.sh"
```

必须与第 1 节摘要一致。

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
test -n "$ASTERIA_STAGING_MONITOR_B64"
test -n "$ASTERIA_STAGING_RECOVERY_B64"
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

4. **恢复旧 root-owned 文件并不等于恢复 GitHub monitor。** main 上的 monitor workflow 仍发送 v0.2.0 的 `MONITOR_SCRIPT_SHA256`；旧 dispatcher 只接受旧摘要，因此仍会拒绝。必须同时完成 9.3 的 workflow 摘要回滚。

### 9.2 部署失败且自动回滚到 v0.1.1

适用前提：deployment 证据显示 `rollback_succeeded=true`，active runtime 已由部署脚本恢复到 previous v0.1.1。

1. 核对 deployment artifact：`rollback_attempted=true`、`rollback_succeeded=true`、`deployed_image_ref`/`deployed_image_id` 回到旧值。
2. 不删除 `postgres-data`、`seaweedfs-data` 或任何 secret volume。
3. 恢复第 3 节旧 root-owned 文件备份。此时旧主机脚本已就位，但 monitor 仍不会恢复，因为 GitHub workflow 仍固定 v0.2.0 摘要。
4. 执行 9.3 的 GitHub workflow 摘要回滚；在完成前，monitor/recovery 继续 fail-closed。
5. 如果 active Compose 已回到 v0.1.1，不需要再运行 deploy；如果 rollback 未成功，停止并标记 `FAILED`，不得用任意 SSH Docker 命令绕过 workflow。

### 9.3 完整回滚：同时恢复主机与 GitHub 摘要契约

完整回滚不是单一步骤。最小权限、可审核、不绕过 SHA-256 的路线：

1. **冻结新变更**：停止继续部署，保留 deployment 失败 evidence 和 artifact digest。
2. **恢复主机 root-owned 文件**：用第 3 节备份恢复旧 dispatcher/config/monitor/recovery 和 authorized_keys；校验旧摘要。
3. **准备受保护 main 的回滚 PR**：只回滚 staging 固定点，不动产品代码；至少恢复：
   - `.github/workflows/monitor-staging.yml` 的 `MONITOR_SCRIPT_SHA256`、Compose SHA、API digest；
   - `.github/workflows/drill-staging-recovery.yml` 的 `RECOVERY_SCRIPT_SHA256`、Compose SHA、API digest；
   - `.github/workflows/deploy-staging.yml` 的 tag/source/API digest/Compose SHA 和 confirmation；
   - 对应的 `scripts/validate`/cicheck 契约。
   参考旧基线提交 `2dc40b92e621cee3f05cb6d21cebda00070eee6e` 的 staging 固定点。
4. **回滚 PR 必须走正常 branch protection、required checks 和人工审批**；不得直接 push main、不得绕过 Environment、不得修改 ruleset。
5. **恢复运行态**：
   - 若 deployment 自动回滚已恢复 v0.1.1 active Compose/image，直接进入监控恢复；
   - 若运行态仍是 v0.2.0，只能在回滚 PR 合并后，通过受控的旧版 deploy workflow/经批准的回滚 workflow 部署 v0.1.1；不得用 SSH 直接改 Docker。
6. **验证摘要三元组同时匹配**：旧 host monitor SHA + 旧 workflow `MONITOR_SCRIPT_SHA256` + 旧 active Compose SHA。任一不匹配，monitor 仍会拒绝或失败。
7. **验证监控/恢复**：手工触发 monitor/recovery（不是依赖 cron），保存 artifact；确认没有删除或重建活动数据卷。
8. **记录证据**：host 文件摘要、workflow PR/commit、run ID、artifact digest、active Compose/image digest、restore 结果。

当前限制：

- 仓库没有 `Rollback staging` workflow；成功部署后没有自动回滚路径。
- 回滚 PR 尚未创建；本次只准备审批材料，不创建代码 PR。
- 因此完整回滚契约状态为 **`BLOCKED_ROLLBACK_CONTRACT`**，不得宣称“自动回滚安全”。
- 失败尝试内的自动回滚仅覆盖 previous active Compose 存在的 runtime 恢复；它不覆盖 GitHub workflow pins，也不覆盖成功部署后的回滚。

### 9.4 监控连续性

- 不依赖 cron 精确时间：GitHub Actions 调度可能延迟 1 小时以上；升级原子性必须由维护窗口和人工检查保证。
- bootstrap 完成后、deployment 完成前，monitor 可能 fail-closed；记录为预期摘要不匹配。
- 完整回滚后，只通过 workflow 手工触发 monitor/recovery；不要用任意 SSH 命令验证或修改服务。

## 10. 过渡窗口规则

- 不暂停、不修改 monitor/recovery workflow；不以禁用监控换取窗口。
- 维护窗口必须同时覆盖 Phase B 和 Phase C；窗口开始前完成只读基线、备份和审批。
- 不依赖 `:23Z` 作为硬边界；cron 延迟已观察到至少 79 分钟。窗口以人工开始/结束和状态检查为准。
- 窗口内 monitor 触发时，预期因旧 active Compose/旧 image 与 v0.2.0 pin 不匹配而 fail-closed；这是预期信号，不是数据故障。
- 若窗口内无法完成 deployment，执行第 9 节回滚，恢复旧 host 文件并完成 workflow 摘要回滚；不留下半升级 host。
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
- Phase C 在完整回滚契约（主机运行态 + GitHub workflow 摘要）获批前保持 `BLOCKED_ROLLBACK_CONTRACT`；
- 任何人不得把“自动失败回滚”表述为“成功部署后的自动回滚”。
