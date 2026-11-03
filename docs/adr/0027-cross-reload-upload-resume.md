# ADR-0027: 跨刷新上传恢复协议（设计先行）

- 状态：Proposed
- 日期：2026-11-02
- 范围：Web 控制台上传恢复、上传会话查询、分片清单
- 关联：ADR-0002、ADR-0006、ADR-0011、ADR-0013、ADR-0023
- 交付说明：本 ADR 只定义协议与兼容边界。跨刷新恢复的客户端实现与 `ListParts` 服务端实现
  均未在本迭代落地，依赖项见“未完成依赖”。

## Context

当前 Web 控制台的上传任务只存在于内存：

- `UploadSession` 在页面刷新、浏览器关闭或标签页崩溃后不可恢复；
- 进行中的分片 PUT 没有可查询的完成清单。`upload_part` 只在 `complete` 时写入，
  存储侧虽然有 S3 `ListParts`，但 `StorageProvider` 没有暴露该能力，控制面也没有对应路由；
- 客户端能重新拿到会话本身：`GET /api/v1/uploads/{id}` 已返回 `status`、`part_size`、
  `expected_size`、`expires_at`、`committed_file_id`。

因此“刷新后继续上传”在**会话级**已经可行，在**分片级**缺少只读能力。若要让客户端记住会话
ID，必须回答“记住什么、记在哪、谁能读”。本项目的既有约束是：

> 禁止通过记录 presigned URL、Token 或长期 S3 凭据实现恢复。

presigned URL 是短期 bearer 凭据（ADR-0023 明确要求不得记录），长期 S3 凭据属于存储
控制面，永远不能进入浏览器。恢复协议因此只能持久化**非敏感的会话句柄**。

## Decision

### 1. 客户端只持久化会话句柄，不持久化凭据

`sessionStorage`（按标签页、刷新存活、关闭即失效）保存一条最小记录：

```json
{
  "version": 1,
  "uploadId": "55f5efc1-51e8-43be-9408-e4b6bcda92e0",
  "tenantId": "…",
  "parentId": "…",
  "fileName": "report.bin",
  "size": 1048576,
  "lastModified": 1762000000000,
  "partSize": 8388608
}
```

明确不写入：presigned URL、`Authorization` 头、任何 S3 凭据、文件内容、`etag` 之外的
分片凭据。句柄本身不是授权凭据：每次恢复都必须重新通过 `Authorization` 头调用控制面，
服务端按 tenant/principal 重新鉴权。上传会话 ID 泄露不产生数据面访问能力。

选择 `sessionStorage` 而非 `localStorage`：恢复语义是“同一标签页刷新后继续”，
不应跨标签页、跨用户会话或长期留存。退出登录时清除。

### 2. 分片清单使用新增的只读端点（附加式变更）

```
GET /api/v1/uploads/{id}/parts
```

返回服务端**已确认**的分片：

```json
{
  "data": {
    "upload_id": "…",
    "status": "uploading",
    "part_size": 8388608,
    "parts": [
      { "part_number": 1, "etag": "…", "size": 8388608 }
    ]
  }
}
```

设计约束：

- **附加式**：新增路径与新增 schema，不修改 `GET /api/v1/uploads/{id}`、
  `POST /parts/sign`、`POST /complete` 的任何字段、状态码或语义。旧客户端不受影响，
  `verify-openapi-compat` 仍应通过。
- **只读**：不签发任何 URL，不改变会话状态，不触发存储写入。
- **数据来源分两层**：
  1. 控制面 `upload_part` 表（会话进入 `completing` 之后才有行）；
  2. 存储侧 `ListParts`（需要给 `StorageProvider` 增加方法，`s3store` 与 `memory`
     各实现一份）。
  两层以 part_number 合并，控制面记录优先。
- **鉴权**：与 `GET /api/v1/uploads/{id}` 相同，要求 `files:read` 且 tenant 匹配；
  只能读取自己的会话。
- **不返回** bucket、object key、storage upload ID，与既有 `Upload` schema 的泄露边界一致。

### 3. 恢复流程

1. 页面加载时读取 `sessionStorage` 句柄；不存在则不显示恢复提示。
2. 调 `GET /api/v1/uploads/{id}`：
   - `404` → 丢弃句柄（会话不存在或不属于当前身份）；
   - `401/403` → 丢弃句柄，走正常登录错误路径；
   - `committed` → 视为已完成，刷新目录后丢弃句柄；
   - `aborted/expired/failed` → 丢弃句柄并说明原因；
   - `created/uploading/completing` → 进入下一步。
3. 调 `GET /api/v1/uploads/{id}/parts` 取得已确认分片；`completing` 状态下不再上传，
   直接进入完成重试（幂等，见 ADR-0013）。
4. 仅重传缺失分片；每个分片重新 `sign`，**不复用旧 URL**。
5. 分片全部就绪后调用 `complete`，沿用现有 digest 幂等语义。

### 4. 不确定完成状态优先于恢复

若刷新发生在 `complete` 已发出但结果未知的时刻，客户端不得假定失败并重新上传：先用
`GET /api/v1/uploads/{id}` 观察 `committed_file_id`。仍为 `completing` 时沿用现有
`completion-unknown` 语义提示用户，而不是新建会话。

## Consequences

- 恢复不引入新的长期凭据；最坏情况是重新上传，永不降级为凭据泄露。
- 未实现 `ListParts` 前，客户端只能做会话级恢复：重新上传全部分片到同一会话。
  这在功能上正确（`complete` 只接受最终清单），但浪费带宽。
- 新增端点属于公开 API 面，需要 OpenAPI 条目、契约测试与 ADR（本文件即为该 ADR）。
- `StorageProvider` 增加 `ListParts` 属于 Go 内部接口扩展，需要更新 `memory` fake 与
  所有实现；不涉及已发布 Release Bundle 的修改。

## 未完成依赖

1. `StorageProvider.ListParts(ctx, objectKey, storageUploadID) ([]StoredPart, error)`：
   S3 `ListParts` 分页（`PartNumberMarker`/`IsTruncated`）与 `memory` 实现。
2. `POST /api/v1/uploads/{id}/parts/sign` 目前不记录已签名分片；若不做 ListParts，
   另一种方案是在签名时写 `upload_part` 占位行，但该行没有 etag，`complete` 的
   ON CONFLICT 语义会受影响，因此不采用。
3. UI：上传队列持久化、恢复提示、分片级进度。当前控制台是单文件上传，恢复入口需要
   先有队列模型。
4. 多标签页互斥：同一会话被两个标签页恢复时，需要服务端 revision 或客户端锁；
   当前 `BeginComplete` 已按 digest 幂等，但分片 PUT 并发写同一 part 的胜出者未定义。

## Rejected alternatives

- **持久化 presigned URL 以便直接续传**：违反本迭代的安全约束；URL 是 bearer 凭据且短期有效。
- **持久化开发 Token 或 S3 凭据**：同等更严重的泄露面。
- **使用 `localStorage` 长期保存会话句柄**：跨用户会话残留，且与“刷新恢复”语义不符。
- **在 `GET /api/v1/uploads/{id}` 响应里内嵌分片清单**：改变既有响应体积与语义，
  对旧客户端是行为变更，不如新增只读端点。
- **签名时写 `upload_part` 占位行**：会让“已签名”与“已上传”混淆，破坏完成对账。

## Verification

本迭代可验证的部分：

- OpenAPI 契约比较（`tools/verify-openapi-compat`）确保现有上传端点未被破坏；
- Web 单测覆盖错误分类、重试上界、取消、完成结果未知与清理失败不覆盖主错误；
- 恢复协议本身在实现落地前只有设计评审，不宣称已实现。
