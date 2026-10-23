# Staging Root Bootstrap 最小权限 Runbook（v0.2.0）

- 状态：`BLOCKED_APPROVAL`，尚未执行
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
- `asteria-deploy` 的 `authorized_keys`

不重启现有容器，不修改应用 Secret/PKI 内容，不删除 volume。

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

### Bootstrap 失败

1. 不删除任何 volume。
2. 优先重新运行同一 bootstrap；脚本对已完整存在的 secret/PKI 不做隐式轮换，对部分状态 fail-closed。
3. 若重复失败，恢复第 3 节备份：

```bash
tar -C / -xzf "$backup/root-owned-before-v0.2.0.tgz"
sha256sum /usr/local/libexec/asteria-staging-* \
  /etc/asteria-drive/staging-dispatch.conf
```

4. 恢复旧 dispatcher/monitor/recovery 后，旧 monitor 应重新可用；验证一次只读 monitor 后再继续。

### 部署失败且自动回滚到 v0.1.1

1. 确认部署证据显示 `rollback_succeeded=true`。
2. 立即恢复第 3 节旧 root-owned 文件备份，使旧 monitor/recovery 摘要重新匹配。
3. 不删除 `postgres-data`、`seaweedfs-data` 或任何 secret volume。
4. 如果备份恢复失败，停止并标记 `FAILED`，不继续重试 deployment。

## 10. 过渡窗口规则

- 不暂停、不修改 monitor/recovery workflow。
- 在最近一次成功 monitor 后执行 bootstrap，并在下一次 `:23Z` 前完成 Phase C deployment。
- 窗口内 monitor 触发时，预期因旧 active Compose/旧 image 与 v0.2.0 pin 不匹配而 fail-closed；这是预期信号，不是数据故障。
- 若窗口内无法完成 deployment，恢复旧 root-owned 文件并回到 v0.1.1，不留下半升级 host。
- recovery 每周 `17 4 * * 1`，避开该窗口。

## 11. 审批输出要求

执行 Phase B 前，必须由批准人确认：

- 目标 host 和 host key 指纹；
- 维护窗口起止 UTC；
- 第 1 节摘要；
- 第 3 节备份文件路径和 SHA-256；
- 影响范围：仅 root-owned dispatcher/monitor/recovery/config/authorized_keys；
- 回滚：第 9 节；
- Phase C 需要**另一条**明确部署授权。
