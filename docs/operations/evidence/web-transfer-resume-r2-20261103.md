# Web 传输可靠性 R2 正确性加固：本地验收证据（2026-11-03）

- 审查基线：`ce11e54a6af1d2c8208e2b262b3513810e8c2901`
- 分支：`claude/web-transfer-reliability-8ace5b`（#97，叠加在 #96 之上）
- 关联 ADR：[ADR-0028](../../adr/0028-resume-content-identity.md)（新增）、
  [ADR-0027](../../adr/0027-cross-reload-upload-resume.md)
- 边界：未合入 `main`、未打 tag、未发布 Release；未接触 staging 主机、数据卷或部署/回滚
  Workflow。

## 修复项与复现

### P0 恢复可能拼接不同文件的内容

**复现**：文件 A、B 名称、大小、`lastModified` 完全一致但字节不同；上传 A 的第 1 片，
然后用 B 恢复。旧实现只比较这三项元数据，会把 A 的第 1 片当作“已上传”跳过，再用 B 的
其余分片完成对象，得到混合内容。

**根因**：恢复的“已上传”判据只有元数据，没有内容证据。

**修复**：客户端在 `POST /parts/sign` 带分片 SHA-256；服务端把摘要绑定进 presigned PUT 的
`x-amz-checksum-sha256`（存储侧逐字节强制），并持久化到 `upload_part_signature`；
`GET /parts` 返回 `content_sha256`；恢复只跳过“服务端有摘要且当前文件对应字节摘要相同”的
分片，其余一律重传。

**回归测试**：`upload-manager.test.ts` 的 P0 用例（同元数据异内容 → 两片都重传）；
`live-stack.spec.ts` 的 `refuses to splice a different file into a resumed session`，
在真实 PostgreSQL + SeaweedFS 上下载恢复后的对象并逐字节比对（B 相等、A 不相等）。

**大文件成本**：按分片哈希，1 GiB / 8 MiB 分片约 128 次 8 MiB 哈希，Chromium 上约 1 秒
CPU，无额外网络流量；恢复时只哈希需要比对的分片。

### P1 取消 / 暂停 / 结果未知语义

- **Pause**：只停本地传输，服务端会话与已落盘分片保留，句柄保留。
- **Cancel**：调用服务端 Abort 并清除句柄。
- **Completion Unknown**：不 abort、不清句柄。

`interruption` 在失败时刻求值（暂停发生在传输启动之后）。测试：
`use-upload-task.test.ts` 的 pause（`deletes == []`、句柄保留）与 cancel
（DELETE 恰好一次、句柄清除）；`upload-reliability.spec.ts` 的暂停/取消浏览器用例。

### P1 只读分片端点、权限与缓存

- `GET /parts` 不再写库、不再 Abort Multipart：TTL 过后只报告 `status: expired`，
  真实迁移由维护循环完成（`TestListUploadPartsReportsExpiryWithoutWritingIt`、
  `TestUploadPartsListingIsReadOnlyForAnElapsedSession`）。
- 会话是**租户内共享**的只读资源：同租户 viewer 可读、不可签名，editor 可读
  （`TestUploadPartsListingIsTenantSharedAndReadOnlyForViewers`），文档与实现一致。
- `POST /parts/sign` 与下载授权接口返回 `Cache-Control: no-store`
  （`TestSignedURLResponsesAreNotCacheable`）。

### P2 S3 与 Web 边界

- `ListParts` 游标必须严格前进、页数上限 20、收集上限 10000 分片
  （`TestListPartsRejectsANonAdvancingCursor`、
  `TestListPartsRejectsATruncatedPageWithoutACursor`、
  `TestListPartsStopsAtTheS3PartLimit`）。
- 恢复完成提示在目录刷新**之后**发出，避免被 `loadDirectory` 的 `clearNotice` 擦除。
- `uploadedParts` 语义修正为“本次恢复实际发送的分片数”，不再等于已验证分片数。
- 错误/网络中断/未知状态不丢句柄：5xx 与网络失败保留，400/401/403/404 与终态丢弃。

## 命令与结果（本地）

| 命令 | 结果 |
| --- | --- |
| `go build ./...` / `go vet ./...` | 通过 |
| `go test ./... -count=1` | 全部包通过 |
| `go test -race ./... -count=1` | 全部包通过 |
| 集成套件（真实 PostgreSQL 17.5 + SeaweedFS 3.85） | 22/22 清单用例，`verified 22 required integration tests across 3 packages` |
| `npm run typecheck` | 通过 |
| `npm run test:unit` | 10 文件 / 167 用例 |
| `npm run build` | 通过 |
| `npm run test:e2e`（fixture） | 20 passed，3 skipped（live 按设计跳过） |
| `npm run test:e2e --grep "live stack"`（真实栈，`ASTERIA_S3_CHECKSUM_HEADERS=true`） | 3 passed（上传/下载、恢复、P0 混合内容） |
| `npm run lint:openapi` / `verify-openapi-compat` / `lint:actions` | 通过 |

## 真实栈说明

`CI / web-live` 现以 `ASTERIA_S3_CHECKSUM_HEADERS=true` 启动控制面。没有该开关时 S3 不校验
签名中的 checksum 头，摘要绑定不会生效，恢复会安全地退化为“无证据 → 全部重传”。该行为由
`TestListUploadPartsWithoutSignatureHasNoIdentity` 与客户端“无摘要必须重传”的用例覆盖。

## 未覆盖 / 风险

- `ListParts` 在 SeaweedFS 上仍未验证超过 1000 分片的真实分页（仅用 XML 桩覆盖协议）。
- 多标签页互斥未实现；只拒绝单标签页内重复恢复。
- `CI / web-live` 仍需多次运行证明稳定，尚未设为 required check。
