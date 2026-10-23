# Phase C 审批材料：Staging v0.2.0 Deployment

- 材料状态：`READY_FOR_HOST_VALIDATION`
- 执行依赖：Phase B 完成且有真实主机证据 + 独立 Phase C 批准
- 关联 PR：#82（验收文档）；实现 PR：[#83](https://github.com/baicie/asteria-drive/pull/83)
- 操作 Runbook：[staging-v0.2.0-bootstrap-runbook.md](staging-v0.2.0-bootstrap-runbook.md)

准备时间：2026-10-23T08:55:00Z

本材料不包含 SSH 私钥、数据库密码、应用 Secret、PKI 私钥或 Docker volume 内容。

## 1. 审批字段

| 字段 | 值（执行前填写） |
| --- | --- |
| 目标 host | `TBD` |
| 部署用户 / 端口 | `TBD` |
| host key 指纹 | `TBD` |
| 维护窗口开始 / 结束（UTC） | `TBD` |
| 操作者 | `TBD` |
| 复核人 | `TBD` |
| 回滚负责人 | `TBD` |
| Phase B evidence reference | `TBD` |
| Change record / 工单 | `TBD` |

## 2. 固定点

| 项目 | 值 |
| --- | --- |
| Release tag | `v0.2.0` |
| Source commit | `5169da638beac679c9cfed66757159474de879dc` |
| API image | `ghcr.io/baicie/asteria-drive@sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853` |
| Compose SHA-256 | `cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005` |
| Deploy script SHA-256 | `007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f` |
| Monitor script SHA-256 | `d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69` |
| Recovery script SHA-256 | `91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4` |

## 3. 部署前门槛（全部必须 PASS）

- Phase B 已完成，主机证据包含旧/新 root-owned 摘要、owner、mode、备份 SHA-256；
- 主机当前 v0.1.1 服务健康已由只读检查确认；
- active Compose、运行 API image 与记录的 v0.1.1 基线一致；
- PostgreSQL/SeaweedFS 容器健康，磁盘 ≥ 5 GiB 可用且 < 85% 使用；
- 活动 postgres-data、seaweedfs-data、secret volumes 均存在，未被修改；
- PostgreSQL TLS 证书链、SAN、SCRAM、HBA 只读检查通过；
- `staging` Environment 约束已人工确认（当前只有 branch policy，没有 required reviewers）；
- 完整回滚契约已批准：主机文件恢复 + GitHub workflow 摘要回滚 + 受控运行态恢复。

任何一项不满足：`ABORT`，不触发部署。

## 4. 唯一允许的部署方式

```text
GitHub Actions → Deploy staging → Run workflow
ref: main
confirmation: deploy-v0.2.0
```

禁止：

- SSH 直接执行任意 Docker 更新；
- 绕过 workflow、forced-command dispatcher 或 Environment；
- 手动改 active Compose 后重启容器；
- 删除数据卷或重建 Secret/PKI。

## 5. 预期证据

| 证据 | 期望 |
| --- | --- |
| deployment artifact | `staging-deployment-evidence` |
| deployment schema | `asteria-drive-staging-deployment/v2` |
| release_tag | `v0.2.0` |
| source_commit | `5169da638beac679c9cfed66757159474de879dc` |
| image | `sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853` |
| compose_sha256 | `cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005` |
| migration_schema_before/after | `3 / 3` |
| rollback_attempted | 失败时为 `true` |
| rollback_succeeded | 失败且恢复成功时为 `true` |
| storage verifier | artifact 存在，字段完整 |
| 运行 API image ref/id | 与固定 image 和本地 config ID 一致 |
| loopback | 18080/19090/18333 仅绑定 127.0.0.1 |
| PostgreSQL TLS | verify-full、SCRAM、明文拒绝 |
| S3 smoke | multipart 上传、下载、字节一致 |
| metrics | Prometheus scrape 成功 |
| capacity | 85% / 5 GiB 门禁通过 |

所有 artifact 必须交叉核对 run ID、workflow SHA、head SHA、image digest、Compose SHA 和目标环境。

## 6. 成功条件

- deployment workflow success；
- active image digest 与固定点一致；
- schema `3 -> 3`；
- health/readiness 通过；
- PostgreSQL TLS/SCRAM/明文拒绝通过；
- 真实 S3 multipart 上传下载字节一致；
- metrics、loopback、容量门禁通过；
- 未修改或删除活动数据卷/Secret/PKI；
- artifact digest 和 run 身份已记录。

## 7. 失败 / 中止条件

立即中止并执行回滚：

- Phase B 证据缺失或摘要不匹配；
- 主机只读基线异常；
- deployment 证据缺失、字段不符或 digest 不匹配；
- image/Compose/run 身份不一致；
- migration 非 3/3；
- health/readiness 失败；
- TLS/S3/metrics/容量门禁失败；
- 任何数据卷、Secret、PKI 被意外修改；
- 完整回滚契约未获批准。

不得删除数据卷；不得用任意 SSH Docker 命令绕过。

## 8. 回滚契约（当前 BLOCKED）

### 失败尝试内

- deployment 脚本在 previous active Compose 存在时自动恢复旧 PostgreSQL/SeaweedFS/API；
- DB schema 未变化，v0.1.1 API 兼容；
- 不删除活动卷。

### 成功部署后

PR #83 已提供受控的 `Rollback staging` workflow：使用固定 v0.1.1 bundle、严格主机校验和独立 rollback evidence。完整回滚仍需要：

1. 恢复旧 root-owned dispatcher/monitor/recovery/config/authorized_keys；
2. 受保护 main 上的 staging workflow 固定点回滚 PR（monitor/recovery/deploy）；
3. active Compose/image 恢复到 v0.1.1 的受控路径；
4. 手工 monitor/recovery 验证。

在 1–4 完成真实主机演练并获得独立批准前，状态为 `READY_FOR_HOST_VALIDATION`，不得部署，也不得标记 `ROLLBACK_VERIFIED`。

## 9. Phase D 交接

Phase C 成功后，另需授权：

- monitor workflow（所有 success 字段、artifact digest）；
- recovery drill（schema/table/row、storage verifier、清理、活动卷未变）；
- 记录 PostgreSQL TLS、S3、metrics、容量证据。

Recovery 证据只能声明 staging 逻辑恢复，不能声明生产 PITR/WAL、对象版本恢复或 HA。

## 10. 审批签署

```text
Phase B evidence：
批准人 / 角色：
UTC 时间：
维护窗口：
完整回滚契约是否批准：YES / NO
批准 Phase C：YES / NO
```
