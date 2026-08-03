# ADR-0011: 上传完成的确定性失败与未知结果分流

- 状态：Accepted
- 日期：2026-08-03
- 范围：Multipart 完成失败、`FAILED` 终态与清理
- 修订：ADR-0006 的完成失败状态转换

## Context

完成请求在调用对象存储前会冻结 Part 清单摘要并进入 `COMPLETING`。对象存储可能明确拒绝错误 ETag、
缺失 Part 或校验和，也可能因为超时返回未知结果。两类结果不能采用同一处理方式：明确拒绝后继续保留
`COMPLETING` 会让会话既不能修正也不能取消；把超时直接标成失败又可能删除实际上已经完成的对象。

## Decision

1. 语法层可确定的非法 Part 清单在 `BeginComplete` 前拒绝，会话状态和 completion digest 不变。
2. 对象存储明确返回不可重试的 `invalid_request`，且精确 `HEAD` 确认对象不存在时，会话以相同
   completion digest 原子转为 `FAILED`，记录稳定 failure code，再尽力 Abort 原 Multipart。
3. 对象已经可由精确 `HEAD` 读取，但大小或完整校验和与会话声明不符时，会话先持久化为 `FAILED`，
   再尽力删除该会话唯一对象 Key。持久终态阻止并发提交，因此该精确删除不会与元数据提交竞争。
4. 超时、连接中断、`5xx` 或其他结果未知错误不得转为 `FAILED`。会话保持 `COMPLETING`，重试继续用
   相同 digest 执行精确 `HEAD` 并收敛到 `OBJECT_COMPLETED` 或可重试错误。
5. `FAILED` 是终态。客户端需要创建新上传会话提交修正后的 Part 清单；相同会话不会接受另一 digest。
   清理失败只会留下可识别孤儿，不会让文件进入 Namespace。
6. 用户取消仍只允许 `CREATED/UPLOADING -> ABORTED`。内部确定性完成失败使用独立 Repository 转换，
   不能借取消接口把未知的 `COMPLETING/OBJECT_COMPLETED` 错误改写为已中止。

## Consequences

- 错误 ETag、缺失 Part、对象大小或完整校验和不匹配不会留下不可操作的 `COMPLETING` 会话。
- 结果未知时仍优先保守对账，避免误删已完成或即将提交的对象。
- 尽力清理失败时需要后续孤儿对账；该自动维护仍按 ADR-0008 留在 SHOULD，但失败状态和对象 Key 已可识别。
- Repository fake 与 PostgreSQL 必须对 digest、允许来源状态、终态幂等和跨租户行为保持一致。

## Rejected alternatives

- **所有完成错误都转 FAILED**：网络超时可能掩盖已完成对象并触发误删。
- **所有完成错误都保留 COMPLETING**：确定性错误无法恢复，会泄漏 Multipart 且误导客户端重试。
- **允许 FAILED 会话替换 Part 清单后重试**：破坏首次 completion digest 的幂等边界，并使并发完成结果不确定。
- **通过公开 Abort 接口结束 COMPLETING**：客户端无法判断对象存储的真实结果，可能与迟到的完成竞争。

## Verification

- 内存与 PostgreSQL Repository 契约验证 `COMPLETING -> FAILED`、相同请求幂等、不同 digest 冲突、
  跨租户拒绝，以及 `OBJECT_COMPLETED/COMMITTED` 不可错误失败。
- Service 故障注入分别验证确定性对象存储拒绝进入 `FAILED` 并清理 Multipart，以及未知完成结果通过
  `HEAD` 和并发重试只产生一个文件。
