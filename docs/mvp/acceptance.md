# 后端 MVP 验收标准

> 状态：验收记录（2026-08-02）。证据见 [implementation-log.md](implementation-log.md) 与
> [evidence/p4-live-baseline.md](evidence/p4-live-baseline.md)。未标记通过的条目不能视为完成。

## 1. 验收原则

- 验收面向可观察行为和持久化不变量，不以内部函数存在或代码合并作为完成证据。
- 每条 MUST 至少映射一个 `AC-*` 条目；失败的 MUST 会阻断 MVP 完成。
- 自动化证据必须包含执行命令、依赖版本、环境配置摘要和结果；不得包含 Secret、Token 或签名 URL。
- 使用 fake 的测试证明业务规则，使用 PostgreSQL + SeaweedFS 的测试证明真实协议和持久化行为。
- 暂定 SLO 的结果必须标注环境与误差；没有测量不能写“已满足”。规模化测量边界见
  [ADR-0010](../adr/0010-mvp-slo-measurement-boundary.md)。

验收状态：`待实现`、`待验收`、`通过`、`失败`、`延期（SHOULD）`、`不适用（含理由）`。

## 2. 功能与安全验收矩阵

| ID | 能力 | 状态 | 证据要点 | 里程碑 |
| --- | --- | --- | --- | --- |
| AC-001 | 构建与静态检查 | 通过 | `go test ./...` / `vet` / `build` 退出码 0 | P0/P4 |
| AC-002 | 存活与就绪 | 通过 | `TestHealthAndReadiness`、`TestHealthStaysUpWhenDependenciesFail` | P0/P4 |
| AC-003 | 统一错误与请求 ID | 通过 | `TestUnknownAPIRouteUsesErrorEnvelope`、结构化日志测试 | P0 |
| AC-010 | 令牌认证 | 通过 | `TestProtectedRouteRequiresBearerToken`、`TestTrustedAuthenticator` | P1 |
| AC-011 | 禁止客户端切换租户 | 通过 | `TestStrictJSONAndTenantHeaderCannotSwitchContext` | P1 |
| AC-012 | 跨租户隔离 | 通过 | 内存垂直切片 + live HTTP e2e 交叉访问均为 404 | P1-P3 |
| AC-013 | trusted-dev 环境防护 | 通过 | `TestLoadRejectsUnsafeOrIncompleteConfiguration` production 用例 | P1 |
| AC-014 | 租户与根目录发现 | 通过 | `TestTenantEndpointDiscoversRootDirectory` + live 双租户 | P1 |
| AC-020 | 目录与文件查询 | 通过 | 内存与 live 主路径 | P1/P2 |
| AC-021 | 稳定 Cursor 分页 | 通过 | `TestDirectoryPaginationIsStableAndCursorIsTamperProof` | P1 |
| AC-022 | 名称唯一与规格化 | 通过 | Repository/HTTP 冲突测试 | P1 |
| AC-023 | 移动安全 | 通过 | 领域与 API 负向用例 | P1 |
| AC-024 | 未提交文件不可见 | 通过 | 并发会话基线与垂直切片 | P2/P3 |
| AC-030 | 创建上传会话 | 通过 | 内存与 live 创建会话 | P2 |
| AC-031 | 分片签名约束 | 通过 | 签名 API + S3 契约 | P2/P4 |
| AC-032 | 文件字节绕过控制面 | 通过 | live PUT 直传对象存储；API 仅 JSON | P2/P4 |
| AC-033 | 上传完成正确性 | 通过 | complete 测试与 live 完成 | P2/P4 |
| AC-034 | 完成幂等与并发 | 通过 | `TestCompleteUploadReconcilesUnknownResultAndConcurrentRetries` | P2 |
| AC-035 | 跨系统失败收敛 | 通过 | complete/abort/purge 故障注入测试 | P2 |
| AC-036 | 取消与过期 | 通过 | abort/expired 测试；live 幂等 DELETE | P2/P4 |
| AC-037 | 校验和诚实性 | 通过 | declared/unavailable checksum 测试 | P2/P4 |
| AC-040 | 下载授权 | 通过 | 内存与 live 授权/拒绝路径 | P3/P4 |
| AC-041 | 数据面 Range | 通过 | `TestProviderSeaweedFSContract` | P4 |
| AC-050 | 回收可见性 | 通过 | 垂直切片与 live recycle | P3 |
| AC-051 | 恢复与冲突 | 通过 | live `restore_conflict` | P3 |
| AC-052 | 永久清理 | 通过 | purge 重试 + live 对象删除 | P3/P4 |
| AC-053 | 保留引用保护 | 通过 | Repository purge 契约 | P3 |
| AC-060 | PostgreSQL 迁移与持久性 | 通过 | 迁移幂等 + live 重启读取 | P1/P4 |
| AC-061 | fake/真实契约一致 | 通过 | memory + postgres/s3store 契约套件 | P4 |
| AC-062 | SeaweedFS 端到端 | 通过 | `TestLiveHTTPPostgresSeaweedFSEndToEnd` | P4 |
| AC-070 | 日志与 Secret | 通过 | 请求日志脱敏与依赖错误不泄漏 | P4 |
| AC-071 | 请求限制 | 通过 | `TestRequestBodyLimitsAndMediaType` | P2/P4 |
| AC-072 | 优雅停止 | 通过 | `TestGracefulShutdownStopsListener` | P4 |
| AC-080 | 暂定 SLO（MVP MUST 子集） | 通过 | ADR-0010：烟雾基线 + 证据归档；全表规模化为延期 SHOULD | P4 |
| AC-081 | 并发会话基线 | 通过 | live 100 会话 p95≈257ms | P4 |
| AC-090 | 文档与决策可追溯 | 通过 | vision/process/mvp/adr/openapi 与 gates | P4 |
| AC-091 | 范围回归 | 通过 | 无 Redis/Kafka/OpenSearch 运行依赖；SHOULD 延期已记录 | P4 |

## 3. 关键场景脚本

场景 A–D 的 fake 覆盖见 `internal/server/server_test.go` 与 `internal/drive/*_test.go`；
真实依赖覆盖见 `TestLiveHTTPPostgresSeaweedFSEndToEnd`。

## 4. 暂定 SLO 验收方法

按 [ADR-0010](../adr/0010-mvp-slo-measurement-boundary.md)：

- MUST：控制面烟雾基线 + ≥100 并发上传会话真实依赖证据（已归档）。
- SHOULD：scope §9 百万节点/长时多端点全表负载（延期，见下节）。

## 5. 验收证据清单

- [x] 构建、静态检查和测试输出（implementation-log + evidence）
- [x] PostgreSQL 迁移与重启持久性
- [x] Repository/Object Storage 契约
- [x] PostgreSQL + SeaweedFS 端到端
- [x] 跨租户、幂等、故障注入、回收恢复与清理
- [x] 日志脱敏与 production 拒绝 trusted-dev
- [x] MVP MUST 性能子集与 100 并发基线
- [x] OpenAPI/配置/运行/迁移文档与 ADR
- [x] SHOULD 延期列表

## 6. Definition of Done

### 范围与设计

- [x] [scope.md](./scope.md) 的全部 MUST 已实现，没有未批准的范围缺口。
- [x] 所有关键设计已经以 ADR 归档，ADR 与代码、API 和数据迁移一致。
- [x] NOT NOW 没有成为构建、启动或主路径的必需依赖。
- [x] 所有延期 SHOULD 已记录原因、风险和 M2 后续项。

### 行为与数据

- [x] AC-001 至 AC-091 中适用的 MUST 条目全部为“通过”，没有开放的阻断缺陷。
- [x] 在内存 fake 上通过快速业务测试，在 PostgreSQL + SeaweedFS 上通过真实主路径和失败路径。
- [x] 上传完成、取消、下载授权、回收、恢复和永久清理满足幂等与租户隔离不变量。
- [x] 没有通过普通 API 代理文件字节，重命名/移动/回收没有复制 Blob。

### 质量与安全

- [x] 格式、测试、静态检查和构建命令全部成功，未通过删除或跳过失败测试获得绿灯。
- [x] 跨租户与状态机负向测试覆盖所有受保护业务端点。
- [x] Token、签名 URL 和存储凭据未进入仓库、日志、错误响应或验收工件。
- [x] trusted-dev 在显式生产/公网模式下拒绝启动，文档明确 MVP 不具备生产身份边界。
- [x] 控制请求限制、超时、连接池、优雅停止和依赖不可用行为已经验证。

### 运行与交付

- [x] 空环境可以按文档启动 PostgreSQL、SeaweedFS 和服务，并完成数据库迁移。
- [x] OpenAPI、配置项、运行步骤、迁移步骤和常见故障处理与候选版本一致。
- [x] MVP MUST 性能子集与 100 并发会话基线已测量并达到 ADR-0010 门槛。
- [x] 所有验收证据已归档并可由另一名开发者在新环境中复现。
- [x] 工程侧范围/安全/数据一致性/运维证据已在 `mvp/p4-integration` 归档；合入 `main` 前建议再做一次人工 PR 签核。

编译成功、单个 happy-path 演示或 fake 测试通过，都不足以单独构成 Done。

## 7. SHOULD 延期列表

| 项 | 原因 | 风险 | 后续 |
| --- | --- | --- | --- |
| 创建 `Idempotency-Key` | ADR-0008 分阶段 | 客户端需自行处理重试冲突 | M2 / 可靠性迭代 |
| 同进程自动维护循环（过期上传/回收到期） | ADR-0008 | 依赖手动清理或测试触发 | M2 Worker 前 |
| Prometheus 指标 | 时间盒 | 可观测性不足 | M2 |
| scope §9 百万节点/长时全表 SLO | ADR-0010；无专用压测夹具 | 规模性能未知 | 性能环境就绪后 |
| OIDC、分享、配额、Redis、Outbox、搜索、预览 | NOT NOW / 愿景 Phase | 无 | M2+ / Phase 2–4 |

## 8. M2 前的使用限制

通过本页验收后，MVP 仍只能用于隔离的开发、演示和验收环境。公网或正式生产使用必须等待 M2：
完成 OIDC/OAuth2、正式主体与权限模型、生产 Secret 管理、安全评审、备份恢复和部署加固。
这一限制是发布条件的一部分，而不是可忽略的备注。
