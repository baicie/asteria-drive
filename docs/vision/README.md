# Asteria Drive 产品蓝图

> 状态：长期愿景归档。本文档描述可从小规模逐步扩展到 PB 级的商业化目标架构。
> **不是**当前 MVP 的完成声明，也不扩大 [MVP scope](../mvp/scope.md) 的 MUST。

## 核心原则

自己做网盘产品层，不自己重造底层分布式对象存储。

最重要的一点：

> 文件数据不能经过普通业务服务转发。业务服务只负责鉴权、元数据和签发上传下载凭证，
> 客户端直接与对象存储或 CDN 传输文件。

## 与 MVP 的关系

| 文档层 | 作用 |
| --- | --- |
| 本目录 `docs/vision/` | 产品蓝图与 Phase 1–4 演进 |
| [`docs/mvp/`](../mvp/) | 本轮收窄后端垂直切片的范围、路线与验收 |
| [`docs/adr/`](../adr/) | 已接受的关键架构决策（约束实现） |
| [`docs/design/`](../design/) | 当前 MVP 详细设计 |

当愿景与 MVP 冲突时，按 [文档索引](../README.md) 的冲突裁决顺序处理：以 Accepted ADR 的
安全/一致性约束和 MVP scope 为准。

## 文档导航

| 文档 | 内容 |
| --- | --- |
| [architecture.md](./architecture.md) | 控制/数据面、数据模型、上传下载、选型与部署演进 |
| [roadmap.md](./roadmap.md) | 产品 Phase 1–4（区别于 MVP 内部 P0–P4） |
| [anti-patterns.md](./anti-patterns.md) | 明确避免的设计 |

## 已落地方向（MVP 相关 ADR）

下列决策已在 ADR 中接受，并约束当前实现；愿景中的其余能力不得借“架构完整”进入本轮 MUST：

- [ADR-0001](../adr/0001-modular-monolith-and-workers.md)：模块化单体与 Worker 演进
- [ADR-0002](../adr/0002-control-data-plane-separation.md)：控制面与数据面分离
- [ADR-0003](../adr/0003-postgresql-redis-outbox.md)：PostgreSQL 真相源与 Redis/Outbox 边界
- [ADR-0004](../adr/0004-immutable-whole-file-blobs.md)：不可变整文件 Blob
- [ADR-0005](../adr/0005-storage-provider-s3-seaweedfs.md)：StorageProvider 与 SeaweedFS

## 推荐初始落地栈

```text
React Web（后续）
+ Tauri/Rust Desktop（后续）
+ Go 模块化单体
+ PostgreSQL
+ Redis（后续，非 MVP 运行依赖）
+ Kafka/Redpanda（后续）
+ SeaweedFS
+ OpenSearch（后续）
```

当前 MVP 只要求 Go 控制面 + PostgreSQL + S3 兼容数据面（默认 SeaweedFS），详见
[MVP 范围](../mvp/scope.md)。
