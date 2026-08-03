# 产品实施路线（Phase 1–4）

> 状态：产品阶段规划。Phase 编号**不等于** MVP 内部里程碑 P0–P4。
> 当前后端 MVP 是 Phase 1「可用网盘」的收窄垂直切片，完成条件见
> [mvp/acceptance.md](../mvp/acceptance.md)。

## 编号对照

| 编号 | 含义 |
| --- | --- |
| Phase 1–4 | 产品能力阶段（本文） |
| MVP P0–P4 | 本轮后端实施里程碑，见 [mvp/roadmap.md](../mvp/roadmap.md) |
| M2 | MVP 之后的下一产品入口：生产身份与加固，再扩展分享/配额等 |

```text
产品 Phase 1（可用网盘）
  └── 当前后端 MVP = P0 → P1 → P2 → P3 → P4 → Definition of Done
产品 Phase 2+ / M2+
  └── 同步、协作、CDC、企业与多地域等（不在本轮 MUST）
```

## Phase 1：可用网盘

目标能力（完整 Phase 1 产品面）：

- 用户和组织（MVP 仅 trusted-dev 固定映射；正式身份属 M2）
- 文件夹和文件
- S3 Multipart、断点续传、下载和 Range
- 分享链接、回收站、配额（分享/配额属后续；回收站在 MVP MUST）
- PostgreSQL + SeaweedFS
- 整文件不可变对象

**当前后端 MVP 子集**：trusted-dev、Namespace、Multipart 直传、下载授权、回收/恢复/清理、
PG + SeaweedFS 验收。不含分享、配额、OIDC。

## Phase 2：同步和协作

- Tauri 桌面客户端、本地 SQLite 索引、文件变更监听
- 文件版本、冲突副本、内容 Hash 秒传
- 图片/视频/Office 预览、OpenSearch

## Phase 3：高性能增量同步

- FastCDC 内容定义分块、块级去重、Manifest
- 多端增量同步、小块 Pack 合并
- 大目录分片、PostgreSQL 多 Shard

## Phase 4：企业和多地域

- 企业 SSO、DLP、水印、审计导出、法律保留
- 冷热分层、跨地域复制、超大租户独立集群

## 愿景能力归属（防范围膨胀）

| 愿景能力 | 归属 |
| --- | --- |
| 模块化单体、控/数分离、整文件 Blob、S3 Multipart、SeaweedFS | 当前 MVP（已有 ADR） |
| OIDC、分享、配额、ACL、审计 | M2 / Phase 1 后半或 Phase 4 |
| Redis、Outbox+Kafka、OpenSearch、预览/扫描 Worker | M2+ |
| Tauri 同步、CDC 分块、多 Shard、多地域 | Phase 2–4 |

进入新 Phase 前，必须先完成上一阶段验收或明确延期记录；不得以愿景文档绕过 MVP Definition of Done。
