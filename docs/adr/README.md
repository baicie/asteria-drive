# Asteria Drive Architecture Decision Records

本目录归档 Asteria Drive 的关键架构决策。ADR 的 `Accepted` 表示团队已接受该方向，作为 MVP 设计和实现的约束；它不表示相应能力已经落地。实际完成度应以代码、自动化测试和 MVP 进度文档为准。

## Index

| ADR | 决策 | 状态 |
| --- | --- | --- |
| [0001](0001-modular-monolith-and-workers.md) | 模块化单体与 Worker 演进边界 | Accepted |
| [0002](0002-control-data-plane-separation.md) | 控制面、数据面分离与对象存储直传 | Accepted |
| [0003](0003-postgresql-redis-outbox.md) | PostgreSQL 真相源、Redis 边界与事件演进 | Accepted |
| [0004](0004-immutable-whole-file-blobs.md) | 不可变整文件 Blob、路径与 Key 解耦及内部版本 | Accepted |
| [0005](0005-storage-provider-s3-seaweedfs.md) | StorageProvider、S3-compatible 接口与 SeaweedFS 默认部署 | Accepted |
| [0006](0006-multipart-upload-state-machine.md) | Multipart 上传状态机、幂等与孤儿回收 | Accepted |
| [0007](0007-mvp-identity-tenancy-sharing.md) | MVP 身份认证与租户隔离边界 | Accepted |
| [0008](0008-mvp-staged-reliability-boundary.md) | MVP 创建幂等与维护任务的分阶段交付边界 | Accepted |
| [0009](0009-independent-nested-recycle-roots.md) | 嵌套回收项保持独立回收根 | Accepted |

## Conventions

- 新决策先以 `Proposed` 起草，经评审后改为 `Accepted`。
- 已接受决策若发生实质变化，以新 ADR 取代并将旧 ADR 标为 `Superseded`，不直接改写历史结论。
- 后续 ADR 也可以明确修订早期 ADR 的阶段性范围；被修订文档保留原文并链接到新决策。
- 实现可分阶段推进，但不得在没有新 ADR 的情况下破坏已记录的安全、一致性和数据边界。
- ADR 记录“为什么”和稳定约束；接口细节、DDL、交付步骤分别归档到设计与 MVP 文档。
