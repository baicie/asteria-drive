# ADR-0028: 恢复的内容身份校验（分片 SHA-256 签名绑定）

- 状态：部分被取代（第 1 条“签名摘要即存储内容证明”由 [ADR-0029](0029-resume-reuses-only-storage-attested-parts.md) 取代）
- 日期：2026-11-03
- 范围：跨刷新恢复的内容完整性、上传授权接口的缓存语义、只读分片端点
- 关联：ADR-0006、ADR-0011、ADR-0013、ADR-0023、ADR-0027

## Context

ADR-0027 的恢复协议只比较**文件元数据**：名称、大小、`lastModified`。三者相同不等于
内容相同。两个不同文件可以完全满足这三项（例如同一份导出脚本在同一天生成两次，或用户
用同名的不同文件替换了原文件）。恢复流程如果据此跳过“服务端已有”的分片，就会把文件 A
的已上传分片与文件 B 的其余分片拼成同一个对象：这是一个静默的数据损坏，`complete` 只会
校验总大小，不会发现内容错配。

独立审查（PR #97 R2）要求：没有充分完整性证据时，不得跳过远端已上传分片。

同时审查发现三个正确性/契约问题：

1. `GET /api/v1/uploads/{id}/parts` 在会话过期时会写库并 Abort Multipart——GET 不应有
   持久化副作用，且该副作用与维护任务竞争。
2. 返回签名 URL 的 `POST /parts/sign` 与 `POST /files/{id}/download-authorizations`
   没有 `Cache-Control: no-store`，短期 bearer URL 可能被中间层缓存到超过签名有效期。
3. `ListParts` 分页游标不前进时会无限循环；分片数也没有显式上界。

## Decision

### 1. 分片内容摘要随签名绑定（服务端强制）

客户端在 `POST /api/v1/uploads/{id}/parts/sign` 中带上 `checksum = {sha256, base64}`，
即**该分片字节**的摘要。服务端在其为 `sha256` 且后端启用 checksum 头时：

- 把摘要写入 presigned PUT 的签名头 `x-amz-checksum-sha256`，S3 在 PUT 时逐字节校验，
  不匹配直接拒绝——这是存储侧强制执行，不是客户端承诺；
- 把摘要与分片大小持久化到 `upload_part_signature`（与冻结完成清单 `upload_part` 分开，
  不改动其 ON CONFLICT 语义）。

`GET /parts` 返回每个分片的 `content_sha256`。客户端恢复时：

- 只跳过“服务端有摘要 **且** 当前所选文件在该偏移处的字节摘要与之相同”的分片；
- 其余分片一律重传（包括无摘要、摘要不符、大小不符、越界、列表截断）。

因此最坏情况是多传一些字节，永远不会拼出混合内容。

### 2. 大文件成本

摘要按分片计算，不按整文件：整文件哈希会要求在发送第一个字节前完整读一遍文件。上传时
每个分片哈希一次（WebCrypto，浏览器线程池），恢复时只哈希需要比对的分片。1 GiB / 8 MiB
分片约为 128 次 8 MiB 哈希，Chromium 上约一秒 CPU，无额外网络流量。代价是恢复时对每个
候选分片多一次本地读取；对跳过分片省下的上传带宽而言通常划算，且用户可预期。

### 3. 只读端点保持只读

`GET /parts` 不再写库、不再 abort。TTL 已过时它在响应中报告 `status: expired` 与空分片，
真实的状态迁移与 multipart 清理仍由维护循环完成。这样 GET 可安全重试、可缓存为
`no-store`、也不会与维护租约竞争。

### 4. 签名 URL 一律不可缓存

`POST /parts/sign` 与下载授权接口显式返回 `Cache-Control: no-store`。分片清单端点同样
保持 `no-store`（它在会话推进时会变化）。

### 5. 前端取消语义三分离

- **Pause**：只停止本地传输，服务端 Multipart 会话与已落盘分片保留，句柄保留，可在刷新后
  继续。
- **Cancel**：停止传输并调用服务端 Abort，句柄清除。
- **Completion Unknown**：不 abort、不清句柄，由服务端状态核对决定（ADR-0011）。

`interruption` 在失败时刻求值，因为暂停发生在传输启动之后。

## Consequences

- 恢复永远不会基于元数据信任远端分片；即使攻击者构造同名同大小同时间戳的文件，也只能
  让客户端多传字节。
- 需要一次 schema 迁移（`upload_part_signature`），不改变既有 `upload_part` 语义。
- `complete` 的 checksum 语义保持不变：摘要只用于分片绑定，不替代完成时的对象校验。
- 客户端多一次本地哈希；无网络与存储侧额外成本。

## Rejected alternatives

- **只比较文件大小与 `lastModified`**：正是本次问题的来源。
- **整文件摘要**：发送前需二次全读，且无法定位是哪个分片不同。
- **把摘要写进 `upload_part`**：该表是完成时冻结的清单，写入会改变 idempotency 冲突语义。
- **恢复时一律重传全部分片**：正确但浪费带宽，且用户无法感知进度；有了存储侧强制绑定，
  可以安全地只跳过可证明的分片。

## Verification

- `internal/drive`：签名后清单带摘要、大小不符时不带摘要、未签名分片不带摘要、截断与 limit
  语义、过期只读。
- `internal/s3store`：游标不前进、截断页无游标、10000 分片上界。
- `internal/server`：签名与下载授权 `no-store`、viewer 可读不可签、同租户 editor 可读。
- Web：`resumeMultipart` 对同元数据异内容文件重传、对无摘要分片重传、乱序清单仍按分片号校验。
- 真实栈：`web/e2e/live-stack.spec.ts` 恢复用例下载恢复后的完整对象并逐字节比对。
