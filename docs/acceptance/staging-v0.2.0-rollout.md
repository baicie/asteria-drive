# Staging v0.2.0 运行验收报告（Sprint S1）

- 报告状态：`BLOCKED_APPROVAL`
- 生成时间：2026-10-23T06:30:00Z
- 起始产品 HEAD：`cf63a93480ba26c449ee2d0f93d3ea74df307c06`
- 最终产品 HEAD：`cf63a93480ba26c449ee2d0f93d3ea74df307c06`（本轮未修改产品代码；验收文档单独提交）
- 实际执行范围：仓库侧发布前核查（Phase A）
- 未执行：root bootstrap（Phase B）、staging deployment（Phase C）、monitor/recovery（Phase D）

## 结论

`BLOCKED_APPROVAL`

当前没有 v0.2.0 的 staging 主机运行证据。不能声明 staging 已升级，也不能把本报告用于生产准入。

## 固定点

| 项目 | 值 | 状态 |
| --- | --- | --- |
| Release | `v0.2.0` | PASS（Release 已存在且 immutable） |
| Release URL | <https://github.com/baicie/asteria-drive/releases/tag/v0.2.0> | PASS |
| Source commit | `5169da638beac679c9cfed66757159474de879dc` | PASS |
| API OCI index | `sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853` | PASS |
| Compose SHA-256 | `cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005` | PASS（仓库） |
| Deploy script SHA-256 | `007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f` | PASS（仓库） |
| Monitor script SHA-256 | `d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69` | PASS（仓库） |
| Recovery script SHA-256 | `91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4` | PASS（仓库） |

## GitHub CI 与 Security

| 项目 | 链接 | 状态 |
| --- | --- | --- |
| main CI（cf63a93） | <https://github.com/baicie/asteria-drive/actions/runs/39908755699> | success |
| main Security（cf63a93） | <https://github.com/baicie/asteria-drive/actions/runs/39908755595> | success |
| required checks | 8 项 | PASS |

说明：`Security / dependency-review` 在 main push 事件中 skipped；在 PR #78/#79/#80/#81 的 PR 事件中为 pass。

## Release 资产验证

已下载 Release assets 并执行 `sha256sum -c checksums.txt`，7 个文件全部通过：

- `asteria-drive_0.2.0_linux_amd64.tar.gz`
- `asteria-drive_0.2.0_linux_arm64.tar.gz`
- `asteria-drive-web_0.2.0_static.tar.gz`
- `asteria-drive_0.2.0.spdx.json`
- `asteria-drive-web_0.2.0.spdx.json`
- `checksums.txt`
- `release-manifest.json`

`gh attestation verify oci://ghcr.io/baicie/asteria-drive@sha256:964e...` 绑定 `refs/tags/v0.2.0` 和 source digest，退出码 0。

## Phase B：Root bootstrap

状态：`BLOCKED_APPROVAL`

- 未执行任何 root/SSH 操作。
- 未读取主机文件、Secret、PKI 私钥或 Docker volume 内容。
- 未安装新的 dispatcher/monitor/recovery。
- 审批所需命令、摘要、影响范围和回滚方案见
  [`staging-v0.2.0-bootstrap-runbook.md`](staging-v0.2.0-bootstrap-runbook.md)。
- 真实主机所有权、权限和当前摘要：`NOT_RUN`，必须由操作者按 Runbook 第 2 节只读采集。

## Phase C：Staging deployment

状态：`BLOCKED_APPROVAL`

| 项目 | 值 |
| --- | --- |
| workflow | `Deploy staging` |
| confirmation | `deploy-v0.2.0` |
| run ID | `NOT_RUN` |
| workflow SHA | `NOT_RUN` |
| deployment artifact | `NOT_RUN` |
| actual image digest | `NOT_RUN` |
| actual Compose SHA-256 | `NOT_RUN` |

未执行部署，未启动 deployment workflow，未通过 SSH 更新容器。

## Phase D：Monitor 与 Recovery

状态：`BLOCKED_APPROVAL`

| 项目 | 值 |
| --- | --- |
| monitor run ID | `NOT_RUN` |
| monitor artifact | `NOT_RUN` |
| recovery run ID | `NOT_RUN` |
| recovery artifact | `NOT_RUN` |
| PostgreSQL TLS 运行验证 | `NOT_RUN` |
| S3 multipart 上传/下载 | `NOT_RUN` |
| schema/table/row 一致性 | `NOT_RUN` |
| 临时资源清理验证 | `NOT_RUN` |

没有新的 monitor/recovery 证据。仓库中历史 v0.1.1 证据不能被当作 v0.2.0 证据。

## 当前 staging 实际版本

**未由本轮重新验证。**

仓库文档记录 staging 仍运行 `v0.1.1`；本轮没有读取主机、没有部署、没有运行 monitor，因此该记录仍是待验证状态，不能推断为 v0.2.0。

## PostgreSQL TLS、S3 与回滚

| 项目 | 状态 | 说明 |
| --- | --- | --- |
| PostgreSQL TLS verify-full | `NOT_RUN` | 只有历史 v0.1.1 证据；需要新 deployment/monitor 证据 |
| SCRAM / 明文拒绝 | `NOT_RUN` | 同上 |
| S3 multipart 上传/下载/字节校验 | `NOT_RUN` | 需要新 deployment artifact |
| deployment 自动回滚代码 | PASS（代码审查） | previous Compose + `--pull never`，不删除数据卷 |
| 实际回滚演练 | `NOT_RUN` | 未部署，无法验证 |

## 遗留风险与缺失证据

1. `staging` GitHub Environment 当前只有 branch policy，没有 required reviewers；部署门是手动 `workflow_dispatch` + `deploy-v0.2.0` 确认，不是独立 Environment reviewer gate。
2. 合并 PR #80 后，受保护 main 的 monitor/recovery workflow 已固定 v0.2.0 摘要，而主机仍是 v0.1.1；在下一次 monitor `:23Z` 调度时预期 fail-closed。
3. 没有真实主机基线，无法确认当前 installed dispatcher/monitor/recovery 摘要和所有权。
4. 没有 root bootstrap 备份 SHA-256、主机证据或操作者/复核人。
5. 没有 deployment run ID、artifact digest、真实 OCI/Compose 比对。
6. 没有 monitor/recovery 运行证据，不能声明 TLS、S3、schema/row 或清理通过。
7. PRD-09/PRD-10 仍为 `OPEN`；本次不关闭任何生产门槛。

## PRD-09 / PRD-10

| 门槛 | 状态 | 说明 |
| --- | --- | --- |
| `PRD-09` 独立安全与平台审批 | `OPEN` | 无独立 reviewer；本次为 staging 准备与代理审查 |
| `PRD-10` 不可变 Release、tag 保护与发布复核 | `OPEN` | `v0.2.0` 已 immutable，但仍为单人自审；不因 staging 验收关闭 |

## 下一阶段产品工程规划（仅建议，未实施）

1. 同源下载跳转/流式代理，消除浏览器弹窗限制。
2. 上传 API 错误分类：签名过期、401/403、网络瞬断、不可恢复错误。
3. 上传任务跨页面刷新恢复，评估 ETag/session 恢复协议。
4. API client、上传状态机、分页 composable 的单元测试。
5. 真实对象存储环境下的故障注入与上传恢复验收。

## 下一步唯一最优先动作

完成 Phase B root bootstrap 审批，并在维护窗口执行
[`staging-v0.2.0-bootstrap-runbook.md`](staging-v0.2.0-bootstrap-runbook.md)；
取得单独部署授权后，再通过 `Deploy staging` → `deploy-v0.2.0` 执行 Phase C。
