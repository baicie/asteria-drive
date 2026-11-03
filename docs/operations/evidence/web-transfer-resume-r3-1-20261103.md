# Web 传输可靠性 R3.1：最终合并门禁（2026-11-03）

- 审查基线：`cf98070fc7e12b694b7bc5e76403bddfcf386dea`（R3 交付状态）
- 分支：`claude/web-transfer-reliability-8ace5b`（PR #97，叠加在 #96 之上）
- 关联 ADR：[ADR-0029](../../adr/0029-resume-reuses-only-storage-attested-parts.md)、
  [ADR-0027](../../adr/0027-cross-reload-upload-resume.md)
- 边界：未合入 `main`、未打 tag、未发布 Release；未接触 staging 主机、数据卷或部署/回滚
  Workflow。未新增产品功能，未重新设计恢复架构。

## A. 首次上传清理失败不再丢句柄

### 原始失败复现

`uploadMultipart()` 对非取消失败会尝试自动 Abort；Abort 自身失败时已经把清理错误附着为
`UploadFailure.cleanup`。但 `useUploadTask.start()` 的非取消分支无条件清除句柄。临时还原
修复前的分支后，新增用例在真实 Hook 上失败：

```
FAIL  src/features/uploads/use-upload-task.test.ts > resume handle lifecycle on a fresh upload
      > keeps the handle when a non-cancel failure cannot be cleaned up
AssertionError: expected null to match object { uploadId: 'upload-1' }

- Expected: { "uploadId": "upload-1" }
+ Received: null
```

同一用例在修复前的提示里也没有“远端状态尚未确认”，只有主错误：

```
AssertionError: expected '分片 1 上传重试耗尽（4 次）：网络请求失败，正在重试'
                to contain '远端状态尚未确认'
```

### 修复

非取消失败且 `UploadFailure.cleanup` 存在时：

1. 保留 `sessionStorage` 句柄（若本次任务确实写入过），因为服务端 multipart 可能仍存活，
   其已存分片仍可恢复；
2. 主错误保持原文，追加“清理远端上传失败（…），远端状态尚未确认”，不把清理错误伪装成
   主错误；
3. 若本次任务从未写入句柄，则清除任何陈旧句柄，避免恢复指向错误会话。

Abort 成功、用户取消、Pause、Completion Unknown 的既有语义不变：成功清理才清除句柄，
取消/pause 仍按 R3 的状态机处理，完成结果未知时绝不 Abort、绝不丢句柄。

### 测试

- `web/src/features/uploads/upload-manager.test.ts`
  - `attaches a failed automatic abort to a non-cancellation failure`
  - `keeps no cleanup annotation when the automatic abort succeeds`
- `web/src/features/uploads/use-upload-task.test.ts`
  - `keeps the handle when a non-cancel failure cannot be cleaned up`
  - `drops the handle when a non-cancel failure is cleaned up successfully`

## B. live 取消用例不再“假拦截”

### 根因

旧用例使用 `page.route('**/s3/**')`。真实预签名 URL 由 SeaweedFS 直接提供：

```
http://127.0.0.1:18333/<bucket>/<object-key>?X-Amz-Algorithm=...&X-Amz-Signature=...&partNumber=1
```

路径中不存在 `/s3/` 段，glob 从不匹配，route 回调从未执行；所谓“挂起 PUT”并不存在，用例
实际上在和真实上传赛跑。取消若发生在 PUT 完成之后，仍可能通过，完全没有走到中断路径。

### 修复

- 以真实请求特征匹配：查询参数同时含 `X-Amz-Signature` 与 `partNumber`；
- 只挂起 `PUT`，并在拦截器内部发出 `putInFlight` 信号；
- 点击取消前 `await putInFlight`，断言首个拦截项是 `PUT `，消除固定 sleep 与时序赌博；
- 在请求层记录发往 `/api/v1/uploads/{id}` 的 `DELETE`，断言该会话恰好一次；
- 取消后轮询服务端状态为 `aborted|gone`，断言浏览器句柄已清除，并列出目录确认没有提交
  任何文件。

### 真实栈结果

```
Running 4 tests using 1 worker
ok 1 ... uploads a two-part file to real S3, lists it, and downloads the exact bytes
ok 2 ... resumes an interrupted upload after a reload and produces exactly the selected bytes
ok 3 ... refuses to splice a different file into a resumed session (P0 mixed content)
ok 4 ... cancelling a resume aborts its multipart upload on the server
4 passed
```

恢复用例的标题不再声称“不重传分片”：在 SeaweedFS 3.85 上可复用分片为零，正确行为就是
重传并保证最终对象逐字节等于所选文件。

控制面日志同时给出了服务端侧的独立证据（已脱敏）：

```
{"level":"INFO","msg":"http request completed","method":"DELETE",
 "route":"DELETE /api/v1/uploads/{id}","status":204}
```

## C. 生产基线 0003 数据的前向迁移

新增 `TestBaseline0003DataSurvivesForwardMigration`（真实 PostgreSQL 17.5，无 mock）：

1. 只应用 `0001`–`0003`，构造已部署库的真实形状：tenant + 根目录（延迟外键）、blob、
   `file_version` 与带 `current_version_id` 的 file node（同一事务）、一个 `completing`
   的旧上传会话及其冻结的 `upload_part`；
2. 运行本分支 `Migrate` 到 `0005`；
3. 断言：
   - tenant 根节点指针、文件 → blob/版本、`reference_count=1` 均未改变；
   - 旧会话的 `status/completion_digest/revision` 与冻结分片 ETag/大小逐项不变；
   - 旧会话没有签名行，读取返回空而不是错误，迁移**没有伪造或回填** checksum；
   - 升级后可写入并读回新的 `upload_part_signature` 行；
   - `asteria_schema_migration` 精确为 `0001..0005`，已发布迁移未被改写。

结果：`PASS`。该用例已加入 `.github/integration-tests.json`，清单现为 **25** 个必跑集成
用例（postgres 21 / s3store 1 / server 3）。

## D. 存储兼容性边界

### 问题

“后端返回了 checksum 字段”不等于“后端校验过分片内容”。SeaweedFS 3.85 是反例：错误的
`x-amz-checksum-sha256` PUT 返回 200，`ListParts` 对四种算法全部返回空。若把“有值”当作
证据，换一个会回显但不校验的 S3 兼容实现就会重新打开静默拼接。

### 收紧后的规则

`StoredPart` 新增 `ChecksumAttested`；`reusable` 必须同时满足：

1. 分片号在存储返回中唯一；
2. 大小等于会话几何；
3. 存储报告 SHA-256；
4. **该部署显式声明**此确切后端通过契约鉴定：报告值描述所存字节，且与签名摘要不符的
   PUT 会被拒绝。

实现：

- `s3store.Options.TrustPartChecksums`（环境变量
  `ASTERIA_S3_TRUST_PART_CHECKSUMS`）默认 **false**；
- 只有 `ASTERIA_S3_CHECKSUM_HEADERS=true` 时该信任才可能生效；配置层对
  “trust=true 且 checksum headers=false”直接启动失败，避免静默降级；
- `memory` 存储按实现保证置 `ChecksumAttested=true`（单测因此仍覆盖跳过路径）；
- 其他 S3 兼容后端默认不鉴定，恢复全量重传。

| 后端 | 报告摘要 | 拒绝不匹配 PUT | 可跳过 | 依据 |
| --- | --- | --- | --- | --- |
| `memory` | 有 | 由实现保证 | 是 | 代码内证明 |
| SeaweedFS 3.85 | 无 | 否 | 否 | 实测 |
| AWS S3 | 有 | 未在本仓库验证 | 默认否 | 无契约测试证据 |
| 其他 S3 兼容 | 未知 | 未知 | 否 | 默认保守 |

**AWS S3 的分片跳过路径不宣传为已完成能力**：仓库内没有任何真实 AWS 环境上的“错误摘要
必须被拒绝 + `ListParts` 摘要等于所存字节”的契约证据。操作者只有对自己部署的确切后端
跑通该测试后才能显式开启开关。

### 测试

- `internal/s3store`: `TestListPartsAttestsChecksumsOnlyWhenTheBackendIsTrusted`
  （默认 / 仅 trust / trust+headers 三种组合）；
- `internal/drive`: `TestListUploadPartsDoesNotTrustAnUnattestedReportedChecksum`
  （报告摘要保留用于诊断，但 `reusable=false`）；
- `internal/config`: `TestLoadGatesPartChecksumTrustOnSignedChecksumHeaders`
  （默认 false；显式组合保留；不安全组合启动失败）。

## E. 验收命令与结果

| 命令 | 结果 |
| --- | --- |
| `go build ./...` / `go vet ./...` | 通过 |
| `gofmt`（111 个受版本控制的 `.go`，LF 规范化后检查） | 通过 |
| `go test ./... -count=1` | 15 个包全部通过 |
| `go test -race ./... -count=1` | 通过 |
| 集成套件（真实 PostgreSQL 17.5 + SeaweedFS 3.85） | `verified 25 required integration tests across 3 packages` |
| `npm run typecheck` | 通过 |
| `npm run test:unit` | 10 文件 / 177 用例 |
| `npm run build` | 通过 |
| `npm run test:e2e`（fixture） | 20 passed，4 skipped（live 按设计跳过） |
| `npm run test:e2e -- --grep "live stack"`（真实栈，checksum headers=true，trust 未设置） | 4 passed |
| `npm run lint:openapi` | valid（3 个既有 warning，0 error） |
| `go run ./tools/verify-openapi-compat`（对 `origin/main`） | 通过 |
| `npm run lint:actions` | 通过 |

真实栈在 **trust 未开启**的默认配置下运行，因此 B 的“恢复”用例实际走了全量重传路径：
这既验证了保守边界，也再次证明最终对象逐字节等于所选文件。

## CI / Security（已验证提交）

| 提交 | CI | Security | 结果 |
| --- | --- | --- | --- |
| `ce67fbd`（代码） | [41611040615](https://github.com/baicie/asteria-drive/actions/runs/41611040615) | [41611040682](https://github.com/baicie/asteria-drive/actions/runs/41611040682) | 全部通过 |
| `366a80f`（证据初版） | [41611739001](https://github.com/baicie/asteria-drive/actions/runs/41611739001) | [41611738627](https://github.com/baicie/asteria-drive/actions/runs/41611738627) | 全部通过 |
| `33d0c2f`（证据更新） | [41612387677](https://github.com/baicie/asteria-drive/actions/runs/41612387677) | [41612387766](https://github.com/baicie/asteria-drive/actions/runs/41612387766) | 全部通过 |
| `079a678`（最终代码提交） | [41613535873](https://github.com/baicie/asteria-drive/actions/runs/41613535873) | [41613535904](https://github.com/baicie/asteria-drive/actions/runs/41613535904) | 全部通过 |

四次运行的代码内容相同；`ce67fbd` 之后的提交只修改文档（证据与合并清单），不改变代码内容。`CI / integration` 在
`33d0c2f` 与 `079a678` 上均输出 `verified 25 required integration tests across 3 packages`；
`CI / web-live` 输出 `Running 4 tests using 1 worker` → `4 passed`，即 B 修复后的取消用例与
默认保守恢复路径均在远端真实栈上运行。上表中每个提交的六个 CI job 与四个 Security job
均为 success。

`ce67fbd` 只把必跑集成清单的数量断言从 20 更新为 21（postgres），使 `CI / quality` 与
`CI / race` 的 `TestRepositoryIntegrationManifestCoverage` 与新增基线用例一致；上一提交
`aeffd58` 的 `CI / web-live` 已通过，失败仅来自这条计数断言。

## 未覆盖 / 风险

- AWS S3 及任何未做契约鉴定的后端：恢复全量重传；跳过路径没有真实 AWS 证据，不作为已
  完成能力宣传。这是有意的安全默认值，不是遗漏。
- `ListParts` 超过 1000 分片的真实分页仍未在 SeaweedFS 上验证（仅 XML 协议桩覆盖）。
- 多标签页并发恢复未实现互斥。
- `CI / web-live` 尚未设为 required check。
- R3 的 P0 复现、取消语义与向后兼容证据继续有效，见
  [R3 证据](web-transfer-resume-r3-20261103.md)。

## 合并建议

独立审查已确认 R3.1 核心正确性门禁通过，无需继续开发 R3.2。合并顺序固定为
**先 #96、后 #97**：

1. 先合并 #96（HEAD `15cd5f5`）到 `main`；
2. 再合并 #97（独立增量，HEAD 以合并准备后的实际提交为准）到 `main`。

#96 是 #97 的基底；若颠倒顺序，#97 会把 #96 的全部 Web 改动一并带入，PR #96 随后将无
内容可合。若 #96 使用 Squash，必须先按 `git rebase --onto origin/main 15cd5f5` 重放 #97
的独立增量并 `--force-with-lease` 推送，待新 HEAD 的 CI/Security 全绿后再合并 #97；
禁止用 “Update branch” 把 #96 内容重新合入 #97。完整清单、预期 SHA 与回滚步骤见
[merge-plan-96-97-20261103.md](merge-plan-96-97-20261103.md)。

本次交付不执行合并、不发布、不操作 Staging；等待用户明确授权。
