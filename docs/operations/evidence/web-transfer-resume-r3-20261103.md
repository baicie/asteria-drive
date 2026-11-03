# Web 传输可靠性 R3：完整性判定与取消语义收口（2026-11-03）

- 基线：`a47e53c619bb3d2954e1194c2549d6935d7362fd`（PR #97 的 R2 交付状态）
- 分支：`claude/web-transfer-reliability-8ace5b`（#97，叠加在 #96 之上）
- 关联 ADR：[ADR-0029](../../adr/0029-resume-reuses-only-storage-attested-parts.md)（新增，部分取代
  [ADR-0028](../../adr/0028-resume-content-identity.md)）、[ADR-0027](../../adr/0027-cross-reload-upload-resume.md)
- 边界：未合入 `main`、未打 tag、未发布 Release；未接触 staging 主机、数据卷或部署/回滚 Workflow。

## P0 原始失败复现（真实 PostgreSQL 17.5 + SeaweedFS 3.85）

新增 `TestLiveReSignKeepsStoredPartIdentity` 严格按审查步骤编排。在 R2 代码上运行，第一步
（列出分片）即失败：

```
part listing claims the stored part holds file B's bytes after only B's signature was issued:
content_sha256="wC+FcG5Z03OHa5GICc/JUhxSK45jEi3sl2N0ynDX6Ik="
stored etag="\"5f080ab8bad1ef0f8dbe0d9dbae59b11\""
```

- `wC+F...` = 文件 B 第 1 片的 SHA-256（客户端声明）
- `5f08...` = 文件 A 第 1 片的 MD5，即存储中**实际**字节的身份

继续按 R2 客户端规则恢复并完成后，下载对象逐字节比对：

```
LISTING after A stored + B signed only: content_sha256 = B 的摘要（etag 仍是 A 的）
R2 RULE: skip part 1 because the listing reports the selected file's digest
OBJECT size=5243017 equalsA=false equalsB=false firstPartMatchesA=true
```

即最终对象 **前 5 MiB 是 A、其余是 B**，而客户端与界面都报告“恢复成功”。这正是R2 声称
已经消除的静默拼接。

## 根因

`upload_part_signature` 记录的是**申请签名时客户端声明的摘要**。签名与 PUT 之间没有任何
原子性：

- PUT 可能根本没发出（用户取消、断网、页面关闭）；
- 同一分片号可以再次签名，控制面覆写摘要而**存储不变**；
- 后端可以接受一个与声明不符的 PUT（见下）。

`GET /parts` 把最近一次签名直接合并到 `ListParts` 结果上，于是“客户端说过什么”被当成
“存储里有什么”。完成时 S3 只按提交的分片清单与大小拼接，不会发现内容错配。

## 存储侧能力的实测结论（SeaweedFS 3.85）

| 探测 | 结果 |
| --- | --- |
| 预签名 PUT 带错误的 `x-amz-checksum-sha256` | **200，被接受**（SDK 直传同样如此） |
| `ListParts` 的 `ChecksumSHA256/CRC32/CRC32C/SHA1` | 全部为空 |
| 分片 ETag | = 存储字节的 MD5（A、B、短分片逐一验证） |
| `CompleteMultipartUpload` 选择哪一个同号分片 | 由请求里提交的 ETag 决定 |
| 完成后 `HeadObject` / `GetObject` 的 checksum | 空 |
| 完成前读回已存分片自行哈希 | 不可行：403（原始 GET）、NoSuchKey（`GetObject?partNumber`） |
| `Content-MD5` 不匹配的 PUT | 500 拒绝（真实强制），但浏览器 WebCrypto 无法计算 MD5 |

因此 ADR-0028 所称“摘要被绑定进签名头，由存储逐字节强制”在 SeaweedFS 3.85 上是**空操作**；
恢复的正确性不能建立在该后端未实现的特性上。

## 修复策略（为什么能证明存储内容身份）

`GET /parts` 现在只返回存储能自证的东西：

```json
{ "part_number": 1, "etag": "…", "size": 8388608,
  "storage_sha256": "…", "reusable": true }
```

`reusable` 由服务端判定，必须同时满足：分片号在存储返回中**唯一**、大小等于会话几何、
**存储自己**报告了 SHA-256。签名表降级为审计记录，任何读取路径都不再使用。

客户端保留第二道校验：只有 `reusable` 为真**且**本地哈希所选文件在该偏移处的字节等于
`storage_sha256` 时才跳过；其余一律重新签名并重传。

这是一个“证明责任在存储”的设计：存储说“我这儿是这串字节的 SHA-256”，客户端才可能
跳过；否则唯一安全的动作是重传。SeaweedFS 3.85 不报告分片 checksum，因此在它上面恢复
退化为“重传全部分片、保留会话与句柄”——正确但更慢，且永远不会拼接。

## P1 取消语义

`uploadMultipart` 与 `resumeMultipart` 现在共用同一套语义：

| 操作 | 本地 | 服务端 | 句柄 | 提示 |
| --- | --- | --- | --- | --- |
| Pause | 停止 | 保留 | 保留 | 可刷新后继续 |
| Cancel | 停止 | Abort | 成功即清除 | 已取消 |
| Abort 失败 | 停止 | 未知 | **保留** | “未能通知服务端，该会话可能仍然存在” |
| Completion Unknown | 停止 | 不 Abort | 保留 | 提交结果待核对 |

R2 的 `resumeMultipart` 只中断 `AbortController`，从未调用 `gateway.abort`——恢复中的
取消根本没有触达服务端。现在两条路径都会 `DELETE /api/v1/uploads/{id}`，失败时不再
静默宣称取消成功。

## 向后兼容

- 迁移 `0005_part_signature_audit_only.sql` 只加 `COMMENT ON TABLE`，不改列、不改行。
  `TestRepositoryPartSignaturesSurviveForwardMigration` 在只应用到 `0004` 的库上创建会话
  与签名，再跑完整迁移，验证历史行与会话完好。
- 已发布的 `0001`–`0004` 未改动。
- OpenAPI `ListedPart` 移除 `content_sha256`、新增必填 `reusable` 与 `storage_sha256`：这是
  破坏性字段变更，随 ADR-0029 提交；仓库内无其他消费者。

## 命令与结果（本地真实栈）

| 命令 | 结果 |
| --- | --- |
| `go build ./...` / `go vet ./...` | 通过 |
| `gofmt`（108 个受版本控制的 `.go`，按 LF 规范化后检查） | 通过 |
| `go test ./... -count=1` | 15 个包全部通过 |
| `go test -race ./... -count=1` | 通过 |
| 集成套件（真实 PostgreSQL 17.5 + SeaweedFS 3.85） | `verified 24 required integration tests across 3 packages` |
| `npm run typecheck` | 通过 |
| `npm run test:unit` | 10 文件 / 173 用例 |
| `npm run build` | 通过 |
| `npm run test:e2e`（fixture） | 20 passed，4 skipped（live 按设计跳过） |
| `npm run test:e2e -- grep "live stack"`（真实栈，`ASTERIA_S3_CHECKSUM_HEADERS=true`） | 4 passed（上传/下载、恢复、P0 混合内容、**取消恢复触达服务端**） |
| `npm run lint:openapi` / `verify-openapi-compat` / `lint:actions` | 通过 |

## CI / Security（远端，提交 `33b059c`）

| 检查 | 结果 | 链接 |
| --- | --- | --- |
| `CI` | success：quality / race / web / api-contract / integration / **web-live** 全部通过 | <https://github.com/baicie/asteria-drive/actions/runs/41602026483> |
| `Security` | success：codeql / codeql-web / dependency-review / govulncheck | <https://github.com/baicie/asteria-drive/actions/runs/41602026564> |

`CI / integration` 输出 `verified 24 required integration tests across 3 packages`；
`CI / web-live` 输出 `Running 4 tests using 1 worker` → `4 passed`，即本轮新增的
“取消恢复触达服务端”用例已在远端真实栈上运行，而非本地专属。

## 未覆盖 / 风险

- 恢复跳过分片的能力依赖后端报告分片 checksum **且**该后端通过 R3.1 的契约鉴定
  （`ASTERIA_S3_TRUST_PART_CHECKSUMS`，默认 false）。SeaweedFS 3.85 不报告，因此该后端
  上恢复等于全量重传。跳过路径本身由 `internal/drive` 与 `internal/s3store` 的单测覆盖，
  但**没有**在真实 AWS S3 上跑过，因此该路径不作为已完成能力宣传（见
  [R3.1 证据](web-transfer-resume-r3-1-20261103.md)）。
- `ListParts` 超过 1000 分片的真实分页仍未在 SeaweedFS 上验证（仅 XML 桩覆盖协议）。
- 多标签页并发恢复未实现互斥。
- `CI / web-live` 尚未设为 required check。
