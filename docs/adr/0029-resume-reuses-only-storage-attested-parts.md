# ADR-0029: 恢复只复用存储侧可证的分片

- 状态：Accepted
- 日期：2026-11-03
- 范围：跨刷新恢复的内容完整性判定、取消语义、分片清单契约
- 取代：ADR-0028 第 1 条（“签名摘要即存储内容证明”）与其第 3/5 条的实现细节
- 关联：ADR-0006、ADR-0011、ADR-0027

## Context

ADR-0028 把“客户端申请签名时声明的分片 SHA-256”当成 S3 已存分片的内容证明：
`POST /parts/sign` 写入 `upload_part_signature`，`GET /parts` 把该行合并到 `ListParts`
结果上，客户端据此跳过“已上传”分片。R2 独立审查指出该推理不成立，并在真实
PostgreSQL + SeaweedFS 上复现：

1. 用文件 A 的摘要签第 1 片并 PUT 成功 → 存储中是 A。
2. 用文件 B 的摘要再次签同一分片 → 控制面覆写摘要，**存储不变**。
3. B 的 PUT 未发出 → `GET /parts` 返回 B 的摘要，`etag` 仍是 A 的。
4. 按 R2 客户端规则用 B 恢复 → 第 1 片被跳过，完成的对象是 **A 的前半 + B 的后半**。

签名是**意图**，PUT 才是**事实**；两者之间可以失败、可以中断、可以被后续签名覆盖。
把意图当事实，得到的不是“多传字节”，而是静默的数据损坏。

同轮对 SeaweedFS 3.85 的实测（`ListParts` 与 PUT 的 checksum 头）：

| 检查 | 结果 |
| --- | --- |
| 带错误的 `x-amz-checksum-sha256` PUT | **200，被接受** |
| `ListParts` 返回 `ChecksumSHA256`/`CRC32`/`CRC32C`/`SHA1` | 全部为空 |
| 分片 ETag | = 存储字节的 MD5（逐字节验证） |
| 完成后 `HeadObject` 的 checksum | 空 |
| 已存分片能否读回自行哈希 | 不能（GET 403 / NoSuchKey） |
| `Content-MD5` 不匹配的 PUT | 500，拒绝 |

结论：`x-amz-checksum-sha256` 在 SeaweedFS 3.85 上**不提供任何存储侧保证**，R2 所说的
“存储强制绑定”在该后端是空操作；而浏览器无法用 WebCrypto 计算 MD5，所以也不能改用
`Content-MD5` 来绑定。

## Decision

### 1. 分片可复用必须以存储侧证据为条件

`ListParts` 的每一项只有在**同时**满足以下条件时才标记 `reusable: true`：

1. 该分片号在存储返回中只出现一次（SeaweedFS 对同一分片号的重复 PUT 会保留多个条目，
   完成时由提交的 ETag 决定用哪一份，因而不存在单一已存身份）；
2. 大小等于会话几何推出的该分片应有大小；
3. **对象存储自己**报告了该分片的 SHA-256。

`upload_part_signature` 保留但降级为审计记录：记录“客户端何时声明了什么”，**任何**
读取路径都不再把它当作已存内容的证明。`GET /parts` 返回 `storage_sha256` 与
`reusable`，不再返回 `content_sha256`。

### 2. 证据不足时保守重传

后端不报告分片 checksum（SeaweedFS 3.85）、分片号重复、大小不符、清单截断、已冻结完成
清单——一律 `reusable: false`。客户端对每个 `reusable: false` 的分片重新签名并重传。
代价是带宽；收益是**任何**后端上都只会多传，不会拼接。

客户端仍保留第二道校验：本地哈希所选文件在该偏移处的字节，只有等于
`storage_sha256` 才跳过。服务端的 `reusable` 是必要条件，本地哈希是充分条件，两者缺一
不可。

### 3. 取消必须触达服务端

`uploadMultipart` 与 `resumeMultipart` 共用同一套中断语义，并在失败时刻求值：

| 操作 | 本地传输 | 服务端 multipart | 句柄 |
| --- | --- | --- | --- |
| Pause | 停止 | 保留 | 保留，可刷新后恢复 |
| Cancel | 停止 | **Abort** | 成功后清除 |
| Abort 失败 | 停止 | 未知（可能仍存活） | **保留**，提示未确认 |
| Completion Unknown | 停止 | 不 Abort | 保留，由服务端状态核对 |

完成请求一旦可能到达服务端，取消就不再 Abort（ADR-0011）。Abort 失败不吞掉：
取消作为主错误、清理失败附着其上（`UploadFailure.cleanup`），界面明确提示“未能通知
服务端”，并把句柄留作唯一线索。

### 4. 契约与迁移

- OpenAPI `ListedPart`：`content_sha256` → `storage_sha256` + `reusable`（`reusable`
  为必填）。这是一次**破坏性字段变更**，随 ADR 一起提交；当前没有外部消费者。
- 迁移 `0005_part_signature_audit_only.sql` 只加表注释，不改列、不改行：已部署数据库
  与历史会话原样可用（`TestRepositoryPartSignaturesSurviveForwardMigration` 在只应用
  到 0004 的库上验证）。

## Consequences

- 在 SeaweedFS 3.85 上，恢复会重传**所有**分片（无存储 checksum），即退化为“重新上传，
  但保留会话与句柄”。在报告分片 checksum 的 S3 兼容后端上才能跳过可证分片。
- 恢复的正确性不再依赖任何单一后端的可选特性，也不依赖客户端的诚实。
- 签名表不再影响行为，只用于诊断；未来若移除它，不会改变恢复语义。

## Rejected alternatives

- **保留“最近一次签名即证明”**：正是本次缺陷；重签只需一次控制面请求，无需任何存储交互。
- **用 `Content-MD5` 绑定**：SeaweedFS 会强制，但浏览器无法计算 MD5（WebCrypto 不提供）。
- **服务端在完成前读回分片自行哈希**：实测不可行（403 / NoSuchKey），且会把恢复成本
  转嫁到控制面带宽。
- **完成时由服务端用 `x-amz-checksum-sha256` 校验**：SeaweedFS 3.85 不校验该头，实测
  错误摘要仍返回 200。

## Verification

- `internal/drive`：重签不改变已存身份、重复分片号不可复用、大小不符不可复用、无存储
  checksum 不可复用。
- `internal/server`：真实 PostgreSQL + SeaweedFS 的 `TestLiveReSignKeepsStoredPartIdentity`
  复现 R2 场景并要求最终对象逐字节等于重新选择的文件。
- Web 单测：`reusable` 为假必须重传；cancel 必须 DELETE；abort 失败如实上报并保留句柄。
- 真实栈浏览器：`cancelling a resume aborts its multipart upload on the server`、
  `refuses to splice a different file into a resumed session`。
