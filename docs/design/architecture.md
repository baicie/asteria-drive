# Asteria Drive MVP 架构设计

状态：MVP 实现设计；验证状态见 `docs/mvp/implementation-log.md`  
适用范围：MVP P0-P4  
最后更新：2026-08-02

## 1. 文档目的与权威边界

本文定义 Asteria Drive 本轮后端 MVP 的系统边界、模块职责、依赖方向、一致性策略和部署形态。
MVP 范围、阶段和完成证据分别以 [`../mvp/scope.md`](../mvp/scope.md)、
[`../mvp/roadmap.md`](../mvp/roadmap.md) 和 [`../mvp/acceptance.md`](../mvp/acceptance.md) 为准；
若本文与三者冲突，必须先修正文档冲突，不能用本文扩大实现范围。

本文描述稳定架构边界，不单独声明实现或验收完成度。核心原则是：Asteria Drive
实现网盘产品层，不重造底层分布式对象存储；普通文件字节只在客户端和 S3 兼容数据面之间传输，
控制面只处理身份上下文、命名空间、上传协调、下载授权和回收状态。

## 2. 本轮 MVP 与后续目标

### 2.1 本轮 MUST

| 能力 | 本轮边界 |
| --- | --- |
| 进程形态 | 一个 Go 模块化单体 `asteria-server`；后台维护任务可以同进程运行 |
| 身份 | 服务端配置的高熵 `trusted-dev` Bearer Token，固定映射 `tenant_id` 和 `principal_id`；正式角色/ACL 属于 M2 |
| 元数据 | PostgreSQL 是租户、节点、文件版本、Blob、上传会话和回收状态的唯一真相源 |
| 数据面 | S3 Multipart Upload 直传；小文件也用一个最终分片完成 |
| Namespace | 创建/读取目录、列出直接子项、读取文件、重命名、移动 |
| 下载 | 为活动且已提交文件签发短期、限定 `GET` 的下载授权 |
| 回收 | 回收、列表、严格恢复、永久清理；目录后代随回收根项一起不可见 |
| 运行基线 | 迁移、统一错误、请求 ID、日志脱敏、健康/就绪、优雅停止、输入限制 |
| 验证 | 内存 fake、PostgreSQL adapter、S3 adapter，并在 SeaweedFS S3 端点验证 |

本轮没有租户自助创建 API。租户、根目录和开发令牌映射由服务启动时的受控配置初始化；认证调用方
通过只读 `GET /api/v1/tenant` 获取当前租户和根目录 ID，不能选择其他租户。
`trusted-dev` 只能用于隔离的开发、演示和验收环境；显式生产或公网模式必须拒绝以该身份模式启动。

### 2.2 本轮 SHOULD

- OpenAPI 契约及可运行示例。
- Prometheus 格式的控制面指标。
- 创建目录和创建上传会话的有限期 `Idempotency-Key` 去重。
- 节点乐观并发字段和可预测的移动、重命名、恢复冲突。
- 同进程扫描过期上传和到期回收项；不拆独立 Worker。
- 可复现的 PostgreSQL + SeaweedFS 本地集成环境。

SHOULD 若延期，必须在验收记录中归档原因、风险和 M2 跟进项。

### 2.3 M2/目标架构，不属于本轮实现

| 能力 | 后续方向 | 本轮约束 |
| --- | --- | --- |
| OIDC/OAuth2、SSO、正式会话 | 替换 `trusted-dev`，引入 issuer/audience/签名/时间声明和主体映射 | 不能成为 MVP 启动依赖 |
| 正式 IAM/ACL 与组织管理 | 细化角色、授权、成员和租户管理 | 本轮只消费令牌配置中的固定上下文 |
| 分享链接 | 分享 Token、密码、撤销、公开下载 | 本轮不提供分享模块或路由 |
| 配额与计费 | 预留、确认、释放、账本和计费 | 本轮不把配额写入上传事务 |
| Outbox 与独立 Worker | 事务事件、异步消费、独立扩缩容 | 本轮不要求 Outbox 表、消费者或独立进程 |
| 审计导出、搜索、预览、扫描、同步 | 通过稳定应用服务或事件契约扩展 | 不进入本轮热路径或运行依赖 |
| 微服务、多地域、元数据分片 | 达到可测量拆分条件后演进 | MVP 保持单体和单 PostgreSQL 事务边界 |

Redis、Kafka/Redpanda、OpenSearch、CDN、API Gateway/WAF 可以出现在长期部署中，但不是本轮
构建、启动、主路径或验收的必需组件。

## 3. 系统上下文

```text
                         JSON control requests
Client ------------------------------------------------+
  |                                                     |
  |                                                     v
  |                                           +------------------+
  |                                           | asteria-server  |
  |                                           | modular monolith|
  |                                           +----+--------+----+
  |                                                |        |
  |                                      metadata |        | S3 control/sign
  |                                                v        v
  |                                           +----------+ +-------------+
  |                                           |PostgreSQL| |S3-compatible|
  |                                           |truth     | |object store |
  |                                           +----------+ +-------------+
  |                                                            ^
  +------------------- PUT parts / GET / Range ----------------+
                           file bytes
```

客户端与 `asteria-server` 之间是控制面；客户端与对象存储之间是数据面。服务端可调用 S3 的
Multipart、签名、`HEAD` 和删除等控制操作，但 HTTP Handler 不接收或转发普通文件正文。

## 4. 模块化单体

```text
asteria-server
|
+-- transport/http      routing, decoding, body limits, error mapping
+-- auth                trusted-dev token mapping and request context
+-- namespace           directories, files, rename, move, visibility
+-- upload              multipart sessions, signing, complete, abort
+-- download            downloadable-state check and signed GET grants
+-- recycle             trash, restore, purge state and reference checks
+-- operations          probes, shutdown, metrics, same-process maintenance
+-- ports               metadata transaction and object-storage contracts
|
+-- adapters/postgres   PostgreSQL repositories and migrations
+-- adapters/s3         S3-compatible object-storage implementation
`-- adapters/memory     deterministic test fakes and fault injection
```

### 4.1 模块职责

| 模块 | 拥有的行为 | 不允许承担 |
| --- | --- | --- |
| `transport/http` | 路由、认证中间件接入、JSON 校验、请求 ID、响应映射 | 业务规则、SQL、S3 SDK 调用 |
| `auth` | 常量时间风格 Token 匹配、可信租户/主体上下文、环境防护 | 信任客户端提交的租户或主体字段 |
| `namespace` | 树结构、名称规格化、直接子项、移动无环、普通查询可见性 | 文件字节、`ListObjects` 目录查询 |
| `upload` | 会话状态机、对象 Key、Multipart 签名、完成协调、取消/过期 | 代理上传正文、绕过完成事务创建文件 |
| `download` | 文件可下载性判断、下载文件名策略、短期 `GET` 签名 | 代理下载正文、返回存储凭据 |
| `recycle` | 回收根项、后代隐藏、恢复冲突、清理状态和 Blob 引用判定 | 失败时把已清理元数据恢复为可见 |
| `operations` | 探针、优雅停止、指标、同进程扫描和对账入口 | 绕过应用服务直接修改业务状态 |
| PostgreSQL adapter | 租户过滤、事务、约束、错误分类、Keyset 查询 | 把 SQL/驱动类型暴露给 domain/application |
| S3 adapter | Multipart、签名、`HEAD`、Abort、Delete 和能力差异映射 | 用户权限、目录语义或业务状态机 |

### 4.2 依赖方向

```text
HTTP adapter ---> application services ---> domain rules
                       |                         |
                       +----------> ports <------+
                                      |
                              +-------+-------+
                              v               v
                         PostgreSQL           S3
```

- domain/application 不依赖 HTTP、SQL 驱动或具体 S3 SDK。
- Handler 只解码有界 JSON、取得服务端身份上下文、调用应用服务并映射结果。
- port 由应用层需要定义；adapter 返回稳定的领域错误，不泄漏供应商结构。
- 所有 Repository 操作显式接收可信 `tenant_id`；禁止先按裸 ID 查询再过滤租户。
- 跨模块写入由一个显式用例编排；需要原子性的元数据变更共用一个 PostgreSQL 事务。
- 内存 fake 与真实 adapter 通过同一 port 契约测试，业务规则只实现一次。

## 5. 身份与租户信任边界

```text
Authorization: Bearer <opaque-token>
                 |
                 v
        configured token mapping
                 |
                 +--> tenant_id
                 +--> principal_id
```

每个受保护请求必须满足：

1. `Authorization` 使用 Bearer 方案且 Token 存在于服务端配置映射中。
2. 请求上下文只使用映射得到的 `tenant_id` 和 `principal_id`；Header、Body、Query
   中的同名值不能覆盖它们。
3. Repository 的读取和写入都把可信 `tenant_id` 作为条件。
4. 不存在与跨租户资源统一返回 `404 not_found`，避免资源枚举。
5. Token、签名 URL、存储密钥不进入日志、错误或验收工件。
6. 显式生产/公网模式与 `trusted-dev` 同时启用时，进程在监听端口前失败。

M2 用 OIDC/OAuth2 替换令牌映射时，应保持下游应用服务接收的可信上下文形状稳定；身份提供方
声明仍不能替代 Repository 的租户条件。

## 6. 元数据与对象存储边界

PostgreSQL 是以下 MVP 信息的唯一真相源：

- 租户、主体映射所引用的内部 ID 和租户根目录。
- `file_node` 的目录树、活动/回收/清理状态和当前版本引用。
- 不可变 `file_version` 与 `blob`。
- `upload_session`、必要的 `upload_part` 事实、S3 Multipart 标识和最终提交结果。
- 回收根项、原父目录、原名称、删除时间和清理状态。

对象存储只保存不可变文件内容。对象 Key 由服务端生成，例如：

```text
blobs/{tenant_id}/{random_blob_id}
```

用户路径、显示名称、主体标识和客户端提供的对象 Key 不进入真实 Key。目录读取只能查询
PostgreSQL；重命名、移动、回收和恢复只改变元数据，不复制对象。MVP 不做覆盖、版本历史 UI、
秒传或去重；一个已提交文件节点只有一个当前版本，一个版本引用一个 Blob。

`ObjectStorage` port 至少提供以下稳定语义：

```text
CreateMultipartUpload
SignUploadPart
CompleteMultipartUpload
AbortMultipartUpload
StatObject
SignGetObject
DeleteObject
CheckReady
```

S3 兼容不意味着 Checksum、ETag 或 Multipart 限制完全一致。adapter 必须显式表达可验证的
完整校验和能力；不支持时记录 `checksum_status`，不能把客户端声明值标成服务端已验证。

## 7. 上传状态机与一致性

```text
CREATED -> UPLOADING -> OBJECT_COMPLETED -> COMMITTED
   |           |                 |
   +-----------+-----------------+--> FAILED
   +-----------+--------------------> ABORTED
   +-----------+--------------------> EXPIRED
```

`COMMITTED`、`ABORTED`、`EXPIRED` 和 `FAILED` 是终态；具体允许迁移由
[`data-model.md`](./data-model.md) 固化。非法迁移返回稳定冲突，不通过补写文件节点规避状态机。

PostgreSQL 与对象存储之间没有分布式事务。主路径是：

```text
PostgreSQL                         S3
----------                         --
create upload session
                  ------------->  CreateMultipartUpload
store provider upload id
                  <-------------  signed URLs / client PUT parts
                  ------------->  CompleteMultipartUpload
                  ------------->  HEAD / size / checksum capability
mark OBJECT_COMPLETED
transaction: create Blob + immutable version + file node,
             save committed result, mark COMMITTED
```

关键收敛规则：

- 完成请求以上传会话 ID 为业务幂等边界；重复或并发完成返回同一文件结果。
- S3 Complete 超时代表结果未知。重试先按确定的对象定位执行 `HEAD`/对账，不能新建另一对象。
- 对象已完成而数据库事务失败时，会话和确定性对象定位使其可识别；重试完成或同进程对账收敛。
- 元数据提交事务只创建一个 Blob、一个不可变版本、一个文件节点并保存会话提交结果。
- 未进入 `COMMITTED` 的会话不会在 Namespace 暴露文件，也不能获得下载授权。
- 取消和过期清理重复执行安全；仍存在 Multipart 时尝试 Abort。

## 8. 关键请求流

### 8.1 创建目录、读取与移动

```text
Client -> HTTP -> trusted context -> namespace service -> PostgreSQL
```

目录创建和移动在数据库约束内维护
`(tenant_id, parent_id, normalized_name)` 的活动名称唯一性。移动目录前验证目标不是自身或后代。
根目录不能改名、移动或回收。列表采用 Keyset Cursor，默认 50、最大 200，不使用深度 `OFFSET`。

### 8.2 Multipart 直传

```text
Client       API/Upload          PostgreSQL             S3
  | create       |                    |                  |
  |------------->| auth/validate      |                  |
  |              |--create multipart------------------->|
  |              |--save session---->|                  |
  |<--session----|                    |                  |
  | sign part    |                    |                  |
  |------------->|--presign specified part------------->|
  |<--URL--------|                    |                  |
  | PUT bytes ------------------------------------------>|
  | complete     |                    |                  |
  |------------->|--complete/stat----------------------->|
  |              |--commit Blob/version/node/result---->|
  |<--file-------|                    |                  |
```

无论文件大小，都使用 Multipart；小文件允许一个最终分片。签名只授权会话保存的对象 Key、一个
Part Number、`PUT` 方法和短 TTL。完成清单必须严格有序且 Part Number 唯一。

### 8.3 下载授权

```text
Client -> API -> active/committed/tenant checks -> PostgreSQL
Client <- short-lived signed GET URL ---------- API
Client ---------------- GET / Range ----------> S3
```

授权绑定确定对象、`GET`、过期时间和安全编码的下载文件名。API 只返回 URL 与 `expires_at`，
不返回长期凭据，也不代理对象响应体。

### 8.4 回收、恢复与永久清理

- 回收文件或目录先写入回收状态；普通 Namespace 和下载授权统一按不可见处理。
- 回收目录时，只给当时仍活动的后代写入同一回收根 ID；已经独立回收的子项保持原回收根，恢复与清理
  只选择持久化归属于目标根的节点。详见 [ADR-0009](../adr/0009-independent-nested-recycle-roots.md)。
- 恢复只回到原父目录和原名称；父目录不存在或名称占用返回 `409 restore_conflict`，不自动改名。
- 永久清理先把元数据提交为不可见、不可恢复的清理状态，再判断 Blob 是否仍有引用。
- 无引用对象可以在请求内删除并返回完成，也可进入同进程幂等维护流程；对象删除失败时保持
  不可见并重试，不能回滚为活动节点。

## 9. 维护任务边界

手动永久清理是 MUST。以下维护工作属于 SHOULD，可在 `asteria-server` 内用租约或串行调度运行：

- 扫描并标记过期上传会话，幂等 Abort 残留 Multipart。
- 重试已完成对象但元数据未提交的可识别会话。
- 删除已处于清理状态且没有元数据引用的对象。
- 按后续维护策略配置的保留期处理到期回收项；候选实现尚未执行自动保留期清理。

这些任务必须调用与 HTTP 用例相同的应用服务规则，并有超时、批次上限和可观察积压。MVP 不需要
Outbox、不消费消息代理，也不构建独立 Worker 进程。独立 Worker 是 M2 在负载或故障隔离需求
经过测量后的部署演进。

## 10. 可用性与故障语义

| 故障 | 用户可见行为 | 收敛措施 |
| --- | --- | --- |
| PostgreSQL 不可用 | 新控制请求失败；`readyz` 返回 `503` | 恢复后按幂等边界重试 |
| S3 控制面不可用 | 无法创建/签发/完成新的对象操作 | 返回 `dependency_unavailable`，不伪造成功 |
| Part PUT 中断 | 客户端仅重传该 Part | 会话 TTL 内保留，过期后幂等 Abort |
| Complete 成功但响应超时 | 客户端查询并重试同一会话 | `HEAD`/会话状态确认后继续提交 |
| 对象完成但数据库提交失败 | 文件保持不可见 | 重试完成或同进程对账；不产生重复节点 |
| 下载授权签发后 API 退出 | 已签发 URL 在 TTL 内仍可用 | 数据面不依赖 API 实例持续在线 |
| 对象删除失败 | 节点仍不可见 | 保持清理状态并幂等重试 |

`healthz` 只反映进程存活，不访问依赖。`readyz` 必须检查 PostgreSQL；启用真实对象存储时还要
执行不传输文件字节的轻量可达性检查。依赖结果不确定时不能返回成功。

## 11. MVP 部署拓扑

```text
1 x asteria-server (API + optional same-process maintenance)
1 x PostgreSQL
1 x SeaweedFS/S3-compatible endpoint
```

应用实例不保存用户文件、上传分片或授权真相。配置包含数据库连接、S3 端点/Bucket、签名 TTL、
上传会话 TTL、回收保留期、限制值和 trusted-dev Token 映射。Secret 通过环境或 Secret 载体注入，
不得提交仓库。迁移是显式、版本化的发布步骤；空库必须能前进到当前版本。

多实例、负载均衡、PostgreSQL 备份/PITR 和对象存储持久性仍应在 P4 运维说明中验证或说明，
但不因此引入会话黏性、独立 Worker 或消息系统。

## 12. M2 目标拓扑与拆分条件

M2 可在不改变控制面/数据面边界的前提下演进为：

```text
OIDC Provider -> Gateway/WAF -> stateless API instances
                                  |          |
                                  v          v
                              PostgreSQL     S3/CDN
                                  |
                              Outbox/Worker (when justified)
```

只有满足至少一个可测量条件时才拆独立服务或 Worker：

- 维护任务需要与 API 显著不同的资源模型或独立扩缩容。
- 后台故障反复影响 API，且进程隔离可明确降低故障面。
- 模块的数据和接口契约稳定，并由独立团队拥有。
- 单体发布、构建或数据库事务边界成为已量化瓶颈。
- 模块需要不同运行时，例如预览、媒体处理或病毒扫描。

引入 Outbox、消息代理、分享、配额或搜索时必须分别补充 ADR、迁移、契约测试、失败补偿和运维
方案，不能把它们作为跳过 MVP 验收项的理由。

## 13. 架构不变量

实现和评审必须持续验证：

1. 普通文件字节不经过 `asteria-server`。
2. 受保护请求的租户/主体来自服务端 Token 映射，客户端不能切换租户。
3. 所有资源读写显式受 `tenant_id` 约束；跨租户与不存在统一为 `404`。
4. PostgreSQL 是 MVP 元数据真相源；S3 `ListObjects` 不实现目录查询。
5. 对象 Key 不含用户路径；重命名、移动、回收和恢复不复制 Blob。
6. Blob 和版本不可变；本轮一个上传会话最多提交一个新文件结果。
7. 上传完成事务原子创建 Blob、版本、文件节点并保存会话结果。
8. PostgreSQL/S3 之间的失败通过持久状态、确定对象定位、幂等重试和对账收敛。
9. 回收目录的全部后代对普通查询和下载授权不可见。
10. 只有最后一个元数据引用被永久清理后，Blob 才能进入对象删除流程。
11. 大目录和回收站只使用稳定 Keyset Cursor，不使用深度 `OFFSET`。
12. OIDC、分享、配额、Outbox 和独立 Worker 不属于本轮 MVP 运行依赖。

## 14. 相关设计

- [`api.md`](./api.md)：本轮 REST 路由、认证、错误、Cursor、幂等和签名协议。
- [`data-model.md`](./data-model.md)：实体、索引、不变量、状态机和事务设计；实现前需与本轮范围复核。
- [`testing-and-operations.md`](./testing-and-operations.md)：测试、部署、监控和故障处理；实现前需与本轮范围复核。
- [`../mvp/acceptance.md`](../mvp/acceptance.md)：可复现验收证据和 Definition of Done。
