# Asteria Drive 后端 MVP 数据模型

状态：MVP 实现设计基线，含已交付 M2 身份模型注记；真实迁移与持久化证据见 `docs/mvp/implementation-log.md`
最后更新：2026-08-05

## 1. 范围

本文主体定义 [MVP scope](../mvp/scope.md) P0-P4 所需的 PostgreSQL 模型：租户、Namespace、不可变 Blob/版本、Multipart 上传会话和回收站。M2-1 后续已通过 `0002_identity_access.sql` 增加 OIDC 主体和租户成员；邀请、正式 ACL、分享、配额、审计和 Outbox 不属于原 MVP 模型。

PostgreSQL 是元数据真相源；对象存储只保存不可变字节。用户路径不能成为对象 Key，`ListObjects` 不能作为目录查询。

## 2. 通用约定

- ID 使用应用层生成的 UUID v4；数据库不依赖单机序列作为跨分片标识。
- 时间使用 `timestamptz` 并以 UTC 写入；业务测试注入 Clock。
- 所有业务表都带 `tenant_id`。Repository 不暴露不带租户条件的业务查询。
- 跨租户 ID 与不存在 ID 对调用方统一为 `404`。
- 状态值使用受约束文本；Schema 与 Go 常量同步测试。
- 乐观并发字段 `revision bigint` 从 1 开始，每次可见元数据更新加一。
- 文件正文、Bearer Token、预签名 URL、S3 Secret 和数据库 Secret 永不入库。

## 3. 关系概览

```text
tenant
  +-- file_node (directory/file tree)
  |     +-- file_version -- blob
  +-- upload_session -- upload_part

principal -- tenant_member -- tenant  (M2-1 identity extension)

file_node.trashed_root_id -> file_node.id
upload_session.committed_node_id -> file_node.id
```

一个文件节点在 MVP 中只有一个已提交版本，但仍从第一天保留 `file_version`，以保证不可变 Blob、覆盖演进和恢复语义不需要破坏性迁移。

## 4. 实体

### 4.1 `tenant`

```text
tenant
- id uuid PK
- display_name text
- root_node_id uuid UNIQUE, initially nullable
- created_at timestamptz
```

trusted-dev 配置中的租户在启动时幂等初始化。每个租户恰有一个根目录；初始化事务创建 tenant、根节点并回填 `root_node_id`。根目录不能重命名、移动、回收或清理。

### 4.2 `file_node`

```text
file_node
- id uuid PK
- tenant_id uuid NOT NULL
- parent_id uuid NULL
- kind text NOT NULL                 DIRECTORY | FILE
- display_name text NOT NULL
- normalized_name text NOT NULL
- current_version_id uuid NULL
- size_bytes bigint NOT NULL
- mime_type text NOT NULL
- status text NOT NULL               ACTIVE | TRASHED | PURGING
- trashed_root_id uuid NULL
- original_parent_id uuid NULL
- revision bigint NOT NULL
- created_at timestamptz NOT NULL
- updated_at timestamptz NOT NULL
- deleted_at timestamptz NULL
```

约束：

- 根目录 `parent_id IS NULL`，其他节点必须有同租户父目录。
- `FILE` 必须在提交事务结束时拥有 `current_version_id`；`DIRECTORY` 永远没有版本。
- 活动 Namespace 中 `(tenant_id, parent_id, normalized_name)` 唯一。
- 每个租户最多一个 `parent_id IS NULL` 根节点。
- 所有父子、版本和回收关联都使用包含 `tenant_id` 的复合外键或等价触发/事务约束，不能形成跨租户关系。

目录列表索引：

```text
(tenant_id, parent_id, normalized_name, id)
WHERE status = 'ACTIVE' AND trashed_root_id IS NULL
```

名称规则：

1. 输入必须是合法 UTF-8，经 NFC 规格化；显示名保留规格化后的原大小写。
2. `normalized_name` 使用 Unicode 小写后的 NFC 值，MVP 采用大小写不敏感冲突语义。
3. 拒绝空名、`.`、`..`、`/`、反斜杠、NUL、控制字符和超过 255 UTF-8 bytes 的名称。
4. 不去除普通首尾空格来偷偷改名；带首尾空格直接拒绝。

### 4.3 `blob`

```text
blob
- id uuid PK
- tenant_id uuid NOT NULL
- bucket text NOT NULL
- object_key text NOT NULL
- size_bytes bigint NOT NULL
- mime_type text NOT NULL
- checksum_algorithm text NOT NULL DEFAULT ''
- checksum_value text NOT NULL DEFAULT ''
- checksum_status text NOT NULL       VERIFIED | DECLARED | UNAVAILABLE
- status text NOT NULL                AVAILABLE | PENDING_DELETE | DELETED
- reference_count bigint NOT NULL
- created_at timestamptz NOT NULL
- deleted_at timestamptz NULL
```

- `(bucket, object_key)` 全局唯一。
- 对象 Key 形如 `blobs/{tenant-id}/{blob-id}`，不包含文件名或用户路径。
- Blob 一旦 `AVAILABLE`，对象定位、大小和校验字段不可变。
- `reference_count` 只是加速字段；删除前仍必须从版本事实重新确认引用。
- Multipart ETag 不得被描述为内容 MD5。

### 4.4 `file_version`

```text
file_version
- id uuid PK
- tenant_id uuid NOT NULL
- node_id uuid NOT NULL
- blob_id uuid NOT NULL
- size_bytes bigint NOT NULL
- mime_type text NOT NULL
- checksum_algorithm text NOT NULL DEFAULT ''
- checksum_value text NOT NULL DEFAULT ''
- created_by uuid NOT NULL
- created_at timestamptz NOT NULL
```

版本行不可更新。MVP 不公开历史版本 API，也不支持同名覆盖；未来覆盖只能创建新 Blob/版本并原子更新节点指针。

### 4.5 `upload_session`

```text
upload_session
- id uuid PK
- tenant_id uuid NOT NULL
- principal_id uuid NOT NULL
- parent_id uuid NOT NULL
- display_name text NOT NULL
- normalized_name text NOT NULL
- expected_size bigint NOT NULL
- mime_type text NOT NULL
- declared_checksum_algorithm text NOT NULL DEFAULT ''
- declared_checksum_value text NOT NULL DEFAULT ''
- bucket text NOT NULL
- object_key text NOT NULL
- storage_upload_id text NOT NULL
- status text NOT NULL
- completion_digest text NOT NULL DEFAULT ''
- committed_node_id uuid NULL
- failure_code text NOT NULL DEFAULT ''
- part_size bigint NOT NULL
- expires_at timestamptz NOT NULL
- revision bigint NOT NULL
- created_at timestamptz NOT NULL
- updated_at timestamptz NOT NULL
```

状态：

```text
CREATED -> UPLOADING -> COMPLETING -> OBJECT_COMPLETED -> COMMITTED
    |          |             |              |
    +----------+-------------+--------------+-> ABORTED | EXPIRED | FAILED
```

- `(tenant_id, id)` 是所有会话操作的查找键。
- `storage_upload_id`、Bucket 和 Key 只由服务端/StorageProvider 产生。
- 一个会话最多有一个 `committed_node_id`。
- 完成清单正规化后计算 `completion_digest`；相同 digest 重试返回原结果，不同 digest 返回 `409 idempotency_conflict`。
- `expected_size` 必须在 `(0, configured_max_file_size]`；完成后使用对象 `HEAD` 校验实际大小。

### 4.6 `upload_part`

```text
upload_part
- tenant_id uuid NOT NULL
- upload_session_id uuid NOT NULL
- part_number integer NOT NULL
- etag text NOT NULL
- checksum_algorithm text NOT NULL DEFAULT ''
- checksum_value text NOT NULL DEFAULT ''
- size_bytes bigint NOT NULL DEFAULT 0
- created_at timestamptz NOT NULL
PK (tenant_id, upload_session_id, part_number)
```

当前迁移在可选 Checksum、completion digest 和 failure code 上使用空字符串而不是 SQL `NULL`，与 Go 领域类型的零值保持一致；`committed_node_id` 等关系型缺失值仍使用 `NULL`。

Part Number 范围 1..10000。完成请求必须按 Part Number 严格递增且无重复；重复写同一 Part 只有内容完全一致时才是幂等成功。

## 5. 核心事务

### 5.1 初始化租户

在一个事务中插入租户与根目录，回填根节点。重复启动返回原根目录，不创建第二个根。

### 5.2 创建目录

验证父目录同租户且活动，规格化名称，插入 `DIRECTORY`。唯一索引负责裁决并发同名创建，数据库冲突映射为 `409 name_conflict`。

### 5.3 创建上传会话

1. 验证租户、父目录、名称、大小和会话上限。
2. StorageProvider 创建 Multipart，返回不透明 Upload ID。
3. 插入 `CREATED` 会话；若插入失败，尽力 Abort 刚创建的 Multipart。
4. 未提交会话不会创建 `file_node`，因此 Namespace 永远看不到半成品。

M2 配额必须在步骤 1/3 的数据库事务中预留，不能在完成时才做并发不安全检查。

### 5.4 完成上传

1. 条件更新并锁定会话为 `COMPLETING`，冻结 completion digest。
2. 调用 S3 Complete；超时结果未知时先 `HEAD`，不能盲目创建第二个对象。
3. 校验对象大小和可用 Checksum，记录 `OBJECT_COMPLETED`。
4. 单个 PostgreSQL 事务创建 Blob、FILE Node、Version，更新 Node 当前版本，并把会话改为 `COMMITTED`。
5. 同一会话重试先查询提交结果并返回同一节点。

对象完成而数据库或可重试依赖错误导致事务未决时保留 `OBJECT_COMPLETED`，维护任务可用 `HEAD` 后重放步骤 4。
父目录不可用或名称冲突属于确定性 Namespace 接纳失败，事务把会话改为 `FAILED` 后再精确删除对象；详见
[ADR-0013](../adr/0013-completed-upload-reconciliation.md)。M2 的配额结转和 Outbox 必须加入步骤 4 的同一事务。

### 5.5 移动与重命名

锁定节点和目标父目录，拒绝根目录、非活动节点以及目录移动到自身/后代。只更新 `parent_id`、名称、`revision` 与时间，不访问 StorageProvider。

### 5.6 回收、恢复与清理

- 回收根项时只捕获当时仍活动且没有其他回收归属的后代，把这些节点的 `trashed_root_id` 设为根项 ID；
  根项记录 `original_parent_id` 和 `deleted_at`。已经独立回收的后代保持自己的回收根，详见
  [ADR-0009](../adr/0009-independent-nested-recycle-roots.md)。
- 普通查询和下载统一过滤 `trashed_root_id IS NULL`。
- 恢复锁定回收根和目标父目录；原父不存在或名称冲突返回 `restore_conflict`，不自动改名。成功后只清空
  `trashed_root_id` 等于该根 ID 的节点，不接管其他独立回收根。
- 永久清理先把子树改为 `PURGING`，再枚举版本/Blob。只有重新确认无引用时才标记 Blob `PENDING_DELETE` 并调用精确 Key 删除。
- S3 Delete 失败时节点保持不可见并可重试；成功后标记 Blob `DELETED`。任何 GC 都不得依赖无界 Bucket List 作为热路径。

## 6. 查询与并发

目录和回收站使用 Keyset：

```sql
WHERE tenant_id = $1
  AND parent_id = $2
  AND (normalized_name, id) > ($3, $4)
ORDER BY normalized_name, id
LIMIT $5;
```

Cursor 包含版本、查询作用域、最后的 `normalized_name/id`，使用 HMAC 验签；篡改、跨父目录复用或版本不支持均返回 `invalid_cursor`。禁止深 `OFFSET`。

并发规则：

- 改变 Namespace、回收归属或有效 Blob 引用判定的事务先获取 tenant Namespace 锁，固定锁顺序见
  [ADR-0012](../adr/0012-tenant-namespace-mutation-serialization.md)。
- 唯一约束是名称冲突最终裁决者。
- 会话完成使用行锁和条件状态转换；数据库事务结果未知时先读回状态。
- `revision` 用于 PATCH/恢复的乐观并发；冲突不静默覆盖。
- 清理与恢复通过状态条件和行锁互斥，删除策略宁可暂时泄漏对象也不能误删。

## 7. MVP 不变量

1. 任意业务查询和关联都不能跨 `tenant_id`。
2. 活动同目录规格化名称唯一。
3. 未 `COMMITTED` 会话不产生可见文件。
4. 一个会话最多提交一个节点、版本和 Blob。
5. `COMMITTED` 文件一定引用 `AVAILABLE` Blob；文件和版本大小与对象 `HEAD` 一致。
6. 路径变化不改变对象 Key，不复制 Blob。
7. 回收根及后代对普通查询和下载不可见。
8. Blob 在最后一个版本引用消失前不得删除。
9. PostgreSQL 和 S3 的失败窗口能通过持久状态幂等收敛。

## 8. M2 扩展点与已交付身份扩展

M2-1 已通过独立迁移增加 `principal` 和 `tenant_member`，由 `(issuer, subject)` 唯一标识外部主体，并以
`(tenant_id, principal_id)` 主键保存角色和成员状态。后续邀请、正式 ACL、`quota_account`、
`quota_ledger`、`share_link`、`audit_log` 和 `event_outbox` 必须继续携带租户约束。配额预留/结转与
Outbox 写入加入上传提交事务；分享只引用节点/版本，不复制 Blob。M2 设计不得改变本页的对象不可变、
直传、租户隔离和幂等提交不变量。
