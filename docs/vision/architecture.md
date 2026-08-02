# Asteria Drive 目标架构（产品蓝图）

> 状态：长期愿景。描述可从小规模扩展到 PB 级的高性能网盘架构。
> 本轮交付边界以 [MVP scope](../mvp/scope.md) 为准；本文不得被解读为“已实现”或“本轮 MUST”。

## 1. 原则与技术选型方向

| 维度 | 方向 |
| --- | --- |
| 产品边界 | 自己做网盘产品层；不重造分布式对象存储 |
| 服务形态 | 先模块化单体 + Worker；达规模后再拆服务 |
| 服务端语言 | Go |
| Web 前端 | TypeScript + React/Vue（后续） |
| 桌面同步 | Tauri + Rust（后续） |
| 元数据 | PostgreSQL + Redis（Redis 非 MVP 运行依赖） |
| 对象存储 | 默认 SeaweedFS；企业私有云 Ceph RGW；公有云 S3/OSS/COS |
| 消息 | PostgreSQL Outbox + Kafka/Redpanda（后续） |
| 搜索 | OpenSearch（后续） |
| 可观测性 | OpenTelemetry + Prometheus + Grafana（指标为 MVP SHOULD） |
| 协议 | 外部 REST；内部 gRPC（后续）；文件传输用 S3 Multipart |

## 2. 总体架构

```text
┌─────────────────────────────────────────────────────────────┐
│                        客户端层                              │
│ Web / Mobile / Tauri Desktop / CLI / WebDAV                 │
└───────────────┬──────────────────────┬──────────────────────┘
                │ 控制请求              │ 文件数据
                ▼                       ▼
┌──────────────────────────┐   ┌──────────────────────────────┐
│ CDN / WAF / API Gateway  │   │ CDN / S3 Gateway            │
└───────────────┬──────────┘   └──────────────┬───────────────┘
                ▼                             ▼
┌────────────────────────────────┐   ┌─────────────────────────┐
│        Drive Core              │   │      Object Storage     │
│ Auth / IAM                     │   │ SeaweedFS / Ceph RGW    │
│ File Metadata                  │   │ Public Cloud S3         │
│ Upload Coordinator             │   └─────────────────────────┘
│ Download Authorization         │
│ Share / ACL / Quota            │
│ Version / Recycle Bin          │
└───────────┬───────────┬────────┘
            │           │
            ▼           ▼
┌─────────────────┐  ┌───────────────────────┐
│ PostgreSQL      │  │ Redis                 │
│ 元数据真相源     │  │ 缓存/限流/临时会话     │
└────────┬────────┘  └───────────────────────┘
         │ Outbox
         ▼
┌─────────────────────────────────────────────────────────────┐
│ Kafka / Redpanda                                            │
│ 预览、转码、索引、病毒扫描、审计、通知、生命周期、垃圾回收      │
└─────────────────────────────────────────────────────────────┘
```

### 控制平面

负责：用户与组织权限、目录树、文件版本、上传会话、分享链接、配额、搜索和审计。

### 数据平面

负责：分片上传、下载、Range、副本/纠删码、完整性、冷热分层。

控制平面故障时，不能创建新上传或获取新下载授权；数据平面不应因某个应用实例故障而中断
已签发凭证下的文件传输。

已接受决策：[ADR-0002](../adr/0002-control-data-plane-separation.md)。

## 3. 核心数据模型

路径属于元数据；文件内容属于不可变 Blob。不要把用户路径直接映射成对象 Key。

### file_node

表示用户可见的文件和目录。关键约束：`UNIQUE(tenant_id, parent_id, normalized_name)`。
目录查询使用 `(tenant_id, parent_id, sort_key, id)` 索引与游标分页，禁止大目录深度 `OFFSET`。

### file_version

一个文件节点可对应多个版本；恢复历史版本只需修改 `current_version_id`。
MVP 每个文件仅一个已提交版本。

### blob

不可变对象定位与校验信息。重命名、移动、分享不改变 Blob，也不复制数据。

### upload_session / upload_part

Multipart 会话、预期大小、状态与过期时间。

### 其他目标表（多数不属于本轮 MVP）

`share_link`、`acl_entry`、`quota_ledger`、`recycle_entry`、`device`、`sync_cursor`、
`audit_log`、`event_outbox`。

MVP 实体与不变量见 [data-model.md](../design/data-model.md) 与 [scope.md](../mvp/scope.md)。

## 4. 文件存储模型

### 方案 A：整文件不可变对象（第一阶段默认）

一个文件版本对应一个对象，例如 `blob/{tenant_bucket}/{blob_id}`。

优点：链路短、对 S3/CDN/Range 友好、对象数量可控、运维简单、大文件吞吐高。

已接受决策：[ADR-0004](../adr/0004-immutable-whole-file-blobs.md)。

### 方案 B：内容寻址分块（后续同步场景）

桌面客户端按内容定义分块，配合 Manifest。适合高频修改大文档与增量同步；不应对所有文件强制 CAS。

### 推荐混合策略

| 场景 | 存储模式 |
| --- | --- |
| 浏览器普通上传 | 整文件对象 |
| 视频、压缩包、大模型权重 | 整文件对象 |
| 桌面同步 | 可选内容定义分块 |
| 高频修改大型文档 | 分块 CAS |
| 秒传 | 整文件 Hash 或块 Hash（需 PoP） |
| 小于 1 MB | 整文件或合并 Pack |

去重默认限制在同一租户内。跨租户秒传必须加入 Proof of Possession。

## 5. 上传与下载链路

### 上传

```text
创建上传 → 检查权限/配额 → upload_session → 签发分片 URL
  → 客户端直传对象存储 → 提交 Part/ETag → CompleteMultipart
  → 元数据事务提交版本 → Outbox 发出 FileCommitted（后续）
  → 异步预览/扫描/索引（后续）
```

状态机：`CREATED → UPLOADING → OBJECT_COMPLETED → COMMITTED`，另有 `ABORTED` /
`EXPIRED` / `FAILED`。对象与数据库不做分布式事务；孤儿对象靠状态机与 GC 收敛。

已接受决策：[ADR-0006](../adr/0006-multipart-upload-state-machine.md)。

### 下载

控制面检查 ACL/可见性后签发短期 GET URL；客户端从 CDN 或对象存储下载。
仅特殊场景（水印、实时转码、服务端解密、DLP 等）才代理字节。

## 6. 对象存储选型

| 选项 | 定位 |
| --- | --- |
| SeaweedFS | 默认推荐；中小到大规模、S3 接口、部署相对简单 |
| Ceph RGW | 大型企业私有云、多 PB、需块/文件/对象统一时再评估 |
| 公有云 S3/OSS/COS | 公有云部署 |
| JuiceFS | 内部 POSIX 辅助（转码/预览 Worker），非终端热路径 |
| MinIO | 新建长期项目不建议作为默认开源对象存储 |

应用通过统一 `StorageProvider` 切换提供者，不把 SDK 类型泄漏进领域层。
已接受决策：[ADR-0005](../adr/0005-storage-provider-s3-seaweedfs.md)。

## 7. 服务拆分演进

第一阶段不要拆十几个微服务。推荐 `drive-core` 模块化单体；独立进程优先按 Worker 演进：

```text
drive-api / drive-worker / preview-worker / scan-worker / index-worker
```

达到独立扩缩容、明确所有权、数据边界稳定、发布瓶颈或故障隔离需求后再拆微服务。
元数据 Namespace 不应过早拆散。已接受决策：[ADR-0001](../adr/0001-modular-monolith-and-workers.md)。

## 8. 高性能关键设计

1. 数据绕过业务服务器。
2. Blob 永远不可变；覆盖等于新版本。
3. 移动和重命名只改元数据。
4. 元数据按租户分片：初期单集群 Hash Partition → 中期多 Shard → 超大租户专属 Shard。
5. 大目录 Keyset Pagination。
6. Redis 不是真相源（缓存/限流/短期会话）。
7. 耗时操作异步化（预览、扫描、索引等）。

PostgreSQL / Redis / Outbox 边界见 [ADR-0003](../adr/0003-postgresql-redis-outbox.md)。

## 9. 高可用与安全（目标）

### 应用与数据

- API / Worker 无状态多实例；PostgreSQL Primary + Standby、PITR、恢复演练。
- SeaweedFS 多 Master/Filer/Volume，跨故障域；应用库与存储节点分离。
- 初期主地域强一致、异地异步复制；避免一上来跨地域同步写入。

### 安全必须项（多数属 M2+）

OIDC/OAuth2、短期签名、租户隔离、mTLS、KMS 信封加密、Checksum、审计、防盗链与限流、
分享密码与有效期、回收站延迟物理删除、病毒扫描隔离区。端到端加密为可选模式并接受能力折损。

MVP 仅使用 `trusted-dev` 令牌边界，见 [ADR-0007](../adr/0007-mvp-identity-tenancy-sharing.md)。

## 10. 目标仓库形态（远期）

```text
apps/        web, desktop, mobile, admin
services/    drive-core, worker, preview/media/scan workers
packages/    api-contract, storage-sdk, upload-sdk, crypto, sync-engine
infra/       PostgreSQL, Redis, Kafka, SeaweedFS, OpenSearch, OTel
```

当前仓库聚焦 Go 控制面 MVP（`cmd/`、`internal/`、`docs/`、`compose.yaml`）。

## 11. StorageProvider 接口方向

```go
type StorageProvider interface {
    CreateMultipartUpload(ctx context.Context, req CreateRequest) (*Upload, error)
    SignUploadPart(ctx context.Context, req SignPartRequest) (*SignedURL, error)
    CompleteMultipartUpload(ctx context.Context, req CompleteRequest) error
    AbortMultipartUpload(ctx context.Context, uploadID string) error
    SignDownload(ctx context.Context, objectKey string, ttl time.Duration) (*SignedURL, error)
    StatObject(ctx context.Context, objectKey string) (*ObjectInfo, error)
    DeleteObject(ctx context.Context, objectKey string) error
}
```

在不修改网盘核心业务的情况下切换 SeaweedFS、Ceph RGW、AWS S3、OSS、COS 等 S3 兼容存储。
