# ADR-0013: 已完成对象的 Namespace 接纳失败与幂等对账

- 状态：Accepted
- 日期：2026-08-03
- 范围：`OBJECT_COMPLETED`、`COMMITTED`、完成重试与对象清理
- 修订：ADR-0006、ADR-0011 的完成后失败边界

## Context

对象存储完成 Multipart 后，文件仍可能因父目录已回收或同名项并发提交而无法进入 Namespace。如果会话
继续停留在 `OBJECT_COMPLETED`，公开取消又拒绝该状态，客户端无法放弃对象，存储占用会无限期保留。
另一方面，已经 `COMMITTED` 的完成请求若通过只允许活动文件的下载查询恢复结果，文件回收后相同 digest
会错误返回 404。

`NoSuchUpload` 与紧随其后的 `HEAD 404` 也不能作为上传会话不存在处理：会话已经持久化为
`COMPLETING`，该组合既可能是确定性丢失，也可能处于完成结果尚不可观察的窗口。

## Decision

1. `COMMITTED` 完成重试按 upload session 和 completion digest 直接读取已提交 Node/Version/Blob 结果，
   不使用活动 Namespace 或下载可见性过滤。回收和清理不改变“该会话已经提交这个文件 ID”的事实。
2. `OBJECT_COMPLETED` 后的父目录不可用和名称冲突属于确定性 Namespace 接纳失败。Repository 在 tenant
   Namespace 锁内把会话持久化为 `FAILED`，分别记录稳定 failure code `parent_unavailable` 或
   `name_conflict`，且不创建 Blob、Version 或 Node 元数据。
3. 只有 `FAILED` 已持久化后才删除该会话的精确对象 Key。对象删除失败时，重复取消该失败会话会再次
   执行 Delete；存储拒绝发生在对象完成前时则继续重试 Abort Multipart。
4. `CompleteMultipart` 返回 `not_found` 且精确 `HEAD` 也返回 `not_found` 时，会话保持 `COMPLETING`，
   对外返回可重试 `dependency_unavailable`。只有明确的 `invalid_request + HEAD not_found` 才按
   ADR-0011 转为确定性 `FAILED`。
5. S3 `NoSuchBucket` 表示配置的存储依赖不可用，映射为可重试 `dependency_unavailable`；它不得与
   `NoSuchKey` 或 `NoSuchUpload` 一起映射为用户资源 404。

## Consequences

- 并发完成最多产生一个提交结果；失败接纳不会留下无法由用户重试清理的对象。
- 回收或永久清理文件后，完成请求仍返回已提交会话所绑定的文件结果，不重新创建元数据。
- 未知完成结果宁可保持可对账状态，也不返回误导客户端停止重试的 404。
- 自动孤儿扫描仍属于 ADR-0008 的 SHOULD，但所有需要扫描的失败会话都有稳定状态、failure code 和对象 Key。

## Rejected alternatives

- **允许公开 Abort 任意 `OBJECT_COMPLETED`**：它可能与正在提交元数据的并发完成者竞态并误删对象。
- **名称冲突始终保留 `OBJECT_COMPLETED`**：要求用户先修改另一个文件才能释放存储，不是可终止状态机。
- **完成重试调用下载查询**：把 Namespace 当前可见性错误混入上传幂等事实。
- **把 `NoSuchUpload + HEAD 404` 返回为 404**：上传会话实际存在且仍为 `COMPLETING`，错误不可重试语义不成立。

## Verification

- Service 测试覆盖完成后同名冲突、父目录不可用、对象删除失败后的取消重试。
- 完成并回收/永久清理文件后，以相同 digest 重试仍返回同一 committed file ID。
- 故障注入覆盖 `NoSuchUpload + HEAD 404` 返回可重试依赖错误且保持 `COMPLETING`。
- S3 adapter 单元测试区分 `NoSuchBucket`、`NoSuchKey` 和 `NoSuchUpload`。
