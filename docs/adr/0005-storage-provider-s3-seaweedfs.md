# ADR-0005: StorageProvider、S3-compatible 接口与 SeaweedFS 默认部署

- 状态：Accepted
- 日期：2026-08-02
- 范围：对象存储抽象与默认后端

> 实施注记（2026-08-02）：仓库已经包含 `StorageProvider`、内存 fake、S3-compatible adapter 和
> SeaweedFS Compose 环境；本地 SeaweedFS 的 Multipart、签名全量/Range 下载、删除以及完整 HTTP +
> PostgreSQL + SeaweedFS 端到端契约测试已通过。其他提供者兼容性仍需分别验证。

## Context

Asteria Drive 需要支持私有部署和公有云，同时避免自行实现副本、纠删码、磁盘恢复和对象一致性。若业务模块散落调用某个厂商 SDK，签名、Multipart、Checksum 和错误语义会渗透到核心逻辑，使后端迁移和测试困难。不同 S3-compatible 实现也并非行为完全相同，抽象不能只靠“接口名称一致”的假设。

## Decision

在核心业务与对象存储 SDK 之间定义窄而明确的 `StorageProvider` 端口。它覆盖 MVP 必需能力：

- 创建、签发分片、查询/校验、完成和中止 Multipart Upload；
- 签发受限上传或下载 URL；
- 获取对象元数据、大小与可用 Checksum；
- 幂等删除对象；
- 暴露经过验证的能力差异，而不是伪装所有后端完全一致。

核心只传递服务端生成的 Bucket/Key、期限和策略，不依赖厂商响应类型。不能把 Multipart ETag 默认当作文件内容 Hash；完整性判断优先使用显式 Checksum，并由适配器规范化错误类别和重试提示。

首个生产级适配器面向 S3-compatible API。自托管参考部署默认使用 SeaweedFS 的 S3 Gateway；公有云可使用 AWS S3 或经兼容性测试的 OSS/COS，超大私有云可后续评估 Ceph RGW。本 ADR 只选择接口与默认部署方向；是否已集成和通过兼容性验证应以实施记录为准。

本地文件系统或内存适配器只可用于单元测试和受限开发环境，不作为生产对象存储。每个生产适配器必须通过统一契约测试和真实后端集成测试。

S3 checksum request headers 是显式能力，不由“S3-compatible”名称推断。SeaweedFS 3.85 对带
`x-amz-checksum-sha256` 的预签名 PUT 存在签名不兼容，因此参考配置关闭该能力，客户端摘要保留为
`declared`。只有契约测试证明支持的后端才启用 `ASTERIA_S3_CHECKSUM_HEADERS`；存储端返回并校验完整摘要后
才能记录为 `verified`。

## Consequences

- 网盘核心专注权限、Namespace、版本和配额，不承担底层分布式存储实现。
- S3-compatible 后端可替换，厂商 SDK、凭证和特殊错误被限制在适配器内。
- 最小公共接口可能无法直接利用全部厂商特性；能力协商和扩展接口需要克制。
- SeaweedFS 的部署、升级、容量与故障域仍需专门运维，不因接口抽象而消失。
- “兼容 S3”不是验收结论，Multipart、签名、Checksum、Range、删除和一致性都需要测试。

## Rejected alternatives

- **自研分布式对象存储**：偏离网盘产品层目标，可靠性和运维成本不可接受。
- **业务代码直接调用单一厂商 SDK**：供应商语义扩散，后续迁移和测试成本高。
- **以 POSIX/FUSE 作为终端传输热路径**：不利于客户端直传、预签名和 CDN；POSIX 可保留给内部处理 Worker。
- **把本地文件系统作为生产默认**：缺少所需的扩展、故障恢复和多实例一致性能力。

## Follow-up or triggers

- 在实现前冻结 MVP 端口、错误分类、Checksum 规则和契约测试矩阵。
- 建立 SeaweedFS S3 Gateway 的可重复开发环境，并验证 Multipart 中止、重复完成、Range、CORS 和生命周期行为。
- 引入非 S3 后端或厂商独占能力时，先证明业务收益，再通过新 ADR 扩展端口，避免向核心泄露 SDK。
