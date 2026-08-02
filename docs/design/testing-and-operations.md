# Asteria Drive 后端 MVP 测试与运行设计

状态：设计基线，执行证据见 `docs/mvp/implementation-log.md`  
最后更新：2026-08-02

## 1. 范围与原则

本文只覆盖 P0-P4 后端 MVP：trusted-dev 租户边界、PostgreSQL Namespace、S3 Multipart 直传、下载授权和回收站。OIDC、分享、配额、Outbox、独立 Worker、预览和搜索是 M2，不是本轮测试环境或启动依赖。

测试投入按风险排序：

1. 跨租户访问和签名 URL 越权。
2. 重复完成、PostgreSQL/S3 失败窗口和 Blob 误删。
3. 目录环、名称冲突、回收后代可见性和恢复冲突。
4. API 请求边界、Secret 泄漏、依赖故障和优雅停止。
5. Cursor 稳定性与控制面性能。

fake 证明业务规则，真实 PostgreSQL/SeaweedFS 证明 SQL、事务和 S3 协议。两者不能相互替代。

## 2. 测试分层

### 2.1 单元测试

无网络、无真实数据库，覆盖：

- 文件名 NFC/大小写规格化、非法字符和长度。
- Cursor 编解码、查询作用域、HMAC 验签和篡改。
- 上传状态机、Part 清单、完成 digest 与非法迁移。
- 回收/恢复/清理规则与引用保护。
- S3 错误分类和统一领域错误映射。
- trusted-dev Token 常量时间匹配、缺失上下文默认拒绝。

Clock、ID Generator 和 Storage/Repository port 均可注入。并发测试不得依赖 `time.Sleep` 猜测顺序。

### 2.2 HTTP 组件测试

使用 `httptest` 和内存 adapter 覆盖：

- 路由、方法、Content-Type、未知字段、Body/Part 数量上限。
- 缺失/错误/正确 Token 的 `401/2xx`，伪造租户字段无效。
- 跨租户资源统一 `404`。
- 稳定错误 `code`、`request_id` 和响应 Header。
- handler 只接收有界 JSON，不接收或返回文件正文。
- Context 取消传递到 Repository/StorageProvider。

### 2.3 Repository 契约与 PostgreSQL 集成

同一行为套件先跑内存 Repository，再在真实 PostgreSQL 跑 adapter：

- 幂等租户/根目录初始化。
- 同目录唯一、复合租户外键和根目录约束。
- Keyset 分页、移动环防护和乐观并发。
- 上传完成行锁、单会话单提交与事务回滚。
- 回收子树传播、恢复冲突、引用保护和重启持久性。
- 空库执行迁移，以及重复启动不重复应用迁移。

PostgreSQL 测试使用隔离数据库或 Schema；不得用 SQLite 代替锁、索引与约束验证。

### 2.4 StorageProvider 契约

同一套契约跑内存 fake 和 SeaweedFS S3：

- Create/SignPart/Complete/Abort Multipart。
- Part Number、Upload ID、Key、HTTP 方法和 TTL 受签名约束。
- Complete 后 Head 的大小、ETag 和可用 Checksum 语义。
- 预签名 GET、`Content-Disposition`、完整下载和单 Range。
- 精确 Key Delete 幂等；不存在对象不导致错误复活。
- URL 过期失效；日志不记录完整签名 Query。

Multipart ETag 不能被测试误判为 MD5。供应商不支持完整 Checksum 时断言 `UNAVAILABLE/DECLARED`，不能虚报 `VERIFIED`。

### 2.5 端到端与故障注入

真实主路径：

1. Token 映射租户，创建多级目录。
2. 创建会话、签 Part，测试客户端直接 PUT 到 SeaweedFS。
3. 完成会话，重启 API 后仍能列出文件。
4. 获取下载 URL，验证完整和 Range 字节。
5. 回收、拒绝下载、恢复、再次回收并永久清理。
6. 用第二租户对每类资源做交叉访问，全部 `404` 且无副作用。

故障窗口：

| 注入点 | 必须收敛结果 |
| --- | --- |
| S3 Create 成功、会话插入失败 | 尽力 Abort；遗留可被孤儿对账识别 |
| S3 Complete 超时但对象已完成 | Head 后进入 `OBJECT_COMPLETED`，不创建第二对象 |
| 对象完成、数据库提交前失败 | 重试提交同一节点/版本/Blob |
| 两个并发 Complete | 最多一个业务结果，另一个返回同一结果或可重试冲突 |
| Abort/Delete 超时 | 状态不错误复活，维护任务可幂等重试 |
| PostgreSQL 不可用 | readiness `503`，health 仍反映进程存活 |

### 2.6 Fuzz 与竞态

优先 Fuzz 名称、Cursor、JSON、ETag/Checksum、Part 列表和 `Content-Disposition`。执行 `go test -race ./...`；任何跨租户或单提交竞态阻断 MVP。

## 3. 质量门禁

每次提交：

```text
gofmt -w <changed-go-files>
go test ./...
go vet ./...
go build ./...
```

候选版本增加：

```text
go test -race ./...
PostgreSQL adapter integration tests
SeaweedFS StorageProvider contract tests
HTTP/S3 end-to-end tests
OpenAPI syntax/route coverage check
```

不得删除失败测试、放宽租户断言或跳过迁移换取绿灯。外部依赖测试可以用环境变量显式启用，但 P4 验收必须实际运行并归档版本/命令。

## 4. 运行配置

配置集中解析并在启动前交叉验证：

| 配置 | 要求 |
| --- | --- |
| `ASTERIA_ENV` | `development` 或 `production`；production 禁止 trusted-dev |
| `ASTERIA_SERVER_ADDRESS` | 开发默认回环地址；公网监听需显式配置 |
| `ASTERIA_AUTH_MODE` | MVP 仅 `trusted-dev`；production 启动失败 |
| `ASTERIA_TRUSTED_TOKENS_JSON` | Secret；Token 到 tenant/principal 映射，不得记录 |
| `ASTERIA_METADATA_DRIVER` | `memory` 仅测试/演示；P4 使用 `postgres` |
| `ASTERIA_DATABASE_URL` | postgres 时必需，日志脱敏 |
| `ASTERIA_AUTO_MIGRATE` | 开发/验收可开；生产迁移流程单独执行 |
| `ASTERIA_STORAGE_DRIVER` | `memory` 仅测试；P4 使用 `s3` |
| `ASTERIA_S3_ENDPOINT/REGION/BUCKET` | s3 时必需；生产要求 HTTPS |
| `ASTERIA_S3_ACCESS_KEY_ID/SECRET_ACCESS_KEY` | Secret；优先短期工作负载凭据 |
| `ASTERIA_S3_PATH_STYLE` | SeaweedFS 本地环境为 true |
| `ASTERIA_S3_CHECKSUM_HEADERS` | 默认 false；只有真实契约验证支持 checksum request headers 的后端才启用 |
| `ASTERIA_CURSOR_HMAC_KEY` | Secret，至少 32 bytes |
| 签名/会话 TTL | 必须在安全上下限内；回收保留期随 SHOULD 维护任务延期 |
| Body/Part/文件大小上限 | 服务端固定上限，客户端不能提高 |

Secret 不进入仓库、镜像、命令参数、日志、Trace、Metrics 或错误响应。示例配置只能使用明显虚构值。

## 5. 启动、探针与关闭

启动顺序：配置校验 -> 日志 -> Repository -> Schema 版本 -> StorageProvider -> 预置租户根目录 -> HTTP -> 维护循环。

- `GET /healthz` 只证明进程和事件循环存活，不访问 PostgreSQL/S3。
- `GET /readyz` 检查 Repository；真实 S3 模式执行不传输文件字节的轻量检查。依赖异常返回 `503`，响应不泄露地址或凭据。
- 关闭时先停止接收请求，再取消维护循环，限时等待在途控制请求，最后关闭连接池。已签数据面 URL 不依赖 API 存活。

HTTP server 必须配置 Header/Read/Write/Idle 超时、最大 Header 和有界 JSON Body。请求 ID 在入口产生或验证后传递；日志使用路由模板而非原始含签名 Query 的 URL。

## 6. 本地集成环境

P4 的 Compose 环境最小包含 PostgreSQL 和 SeaweedFS S3。API 可在宿主机运行，以确保响应中的签名 URL 对测试客户端可达。步骤必须能：

1. 启动干净依赖并等待健康。
2. 创建隔离 Bucket/数据库，运行迁移。
3. 注入两个虚构 tenant/principal Token。
4. 运行 Repository、Storage 契约和 E2E。
5. 只清理明确的测试 Bucket/数据库；失败时保留诊断但不保留 Secret。

测试清理目标必须由固定测试前缀和随机 run ID 双重限制，不能指向生产。

## 7. 可观测性

结构化日志最少包含时间、级别、request_id、路由模板、状态类别、耗时和分类错误。可记录内部不透明 tenant/principal ID；默认不记录文件名。禁止记录 Authorization、Token、数据库 URL、S3 Secret、Object Key、完整预签名 URL或文件正文。

MVP 指标：

- HTTP 请求/延迟/在途，标签限路由模板、方法和状态类别。
- PostgreSQL 连接池使用、等待、事务延迟和错误。
- S3 控制调用数、延迟和分类错误，不按 Key 打标签。
- 上传会话按状态数量、最老 `OBJECT_COMPLETED`、取消/过期/失败数。
- GC 候选、跳过引用、删除成功/失败和物理容量债务。

tenant/node/key 不作为 Prometheus 标签。预签数据传输不在 API Trace 中；客户端可用会话 ID 与 request ID 做受控关联。

## 8. 维护任务与 Runbook

所有维护任务使用稳定租约、批次上限、Context 取消、有界退避和 Kill Switch；删除前支持 dry-run。

上传对账：

- `CREATED/UPLOADING` 到期：停止签名，Abort，确认后转 `EXPIRED`。
- `OBJECT_COMPLETED`：Head 验证后重放同一提交事务。
- `COMMITTED`：只核对，不重复创建。
- 可能遗留对象的 `FAILED`：等待安全期后进入孤儿候选。

孤儿/回收清理：发现 -> 安全等待 -> 再确认会话/版本/Blob/Head -> 小批精确 Key 删除。禁止仅凭对象年龄、路径前缀或引用计数直接批量删除。

故障处置：

- PostgreSQL 不可用：readiness 失败，暂停新控制请求和 GC；恢复后先检查 `OBJECT_COMPLETED` 和长事务。
- S3 控制错误：暂停新会话/签名，不删除数据库状态；恢复后分状态对账。
- Blob 缺失/校验失败：停止签名并关闭相关 GC，从对象版本/备份恢复，不能删除元数据掩盖损失。
- 疑似跨租户暴露：最高优先级阻断端点/版本，保存证据，停止新签名并执行完整资源矩阵审计。

## 9. 备份与联合恢复

公网生产前必须完成 PostgreSQL PITR/WAL 与对象存储保护策略。数据库和 S3 无法原子备份：恢复后数据库缺失但对象存在的是延迟孤儿候选；数据库引用但对象缺失是严重完整性事件。

联合恢复先以维护模式启动，对活动版本、Blob、精确对象 Head/大小/Checksum 生成报告；确认保护窗口前保持 GC Kill Switch。初始目标可记录为元数据 `RPO <= 5 分钟`、`RTO <= 60 分钟`，只有演练实测后才能承诺。

## 10. 性能基线

控制面和数据面分开测量。暂定 SLO 以 `docs/mvp/scope.md` 为准；报告记录 Git 提交、依赖版本、CPU/内存、连接池、数据规模、RTT、持续时间、p50/p95/p99 和错误分类。

至少验证 100 个并发活动上传会话；API 内存不随文件大小线性增长。文件吞吐由客户端到 S3 单独报告，不能归功于 Go API，也不能让测试流量经过控制面。

## 11. 交付证据

候选版本需要归档：

- 格式、单元、竞态、vet、build 输出。
- 空库迁移、Repository/Storage 共享契约测试。
- PostgreSQL + SeaweedFS 主路径、Range、跨租户和故障注入结果。
- 日志 Secret 扫描与 production 拒绝 trusted-dev 的结果。
- SLO/100 会话报告与环境说明。
- OpenAPI、配置、迁移、运行和故障处置文档。

实现进度和实测结果只写入 `docs/mvp/implementation-log.md`；本设计文档不把计划写成已完成事实。
