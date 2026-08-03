# ADR-0012: 租户级 Namespace 变更串行化与清理引用判定

- 状态：Accepted
- 日期：2026-08-03
- 范围：Namespace 拓扑、回收归属、恢复与 Blob 清理
- 修订：ADR-0009 的 PostgreSQL 并发实现边界

## Context

目录移动、创建文件、回收和恢复分别检查了目标节点的当前状态，但 PostgreSQL 默认的 `READ COMMITTED`
快照不能保证“检查父目录”与“写入子节点”之间不发生其他 Namespace 变更。只锁源节点也不能阻止两个
目录移动基于旧快照共同形成环。递归回收还可能在等待并发子项回收后，用旧成员集合覆盖已经持久化的
独立 `trashed_root_id`。

这些行为破坏树无环、活动节点必须位于活动父目录下、独立回收根不被接管三个不变量。仅依赖唯一索引
或 Go race detector 无法发现这种数据库事务交错。

## Decision

1. MVP 把 `tenant` 行作为该租户 Namespace 变更的事务互斥点。所有会创建或修改 `file_node`、改变
   `trashed_root_id`，或判定最后有效 Blob 引用的事务，先执行
   `SELECT ... FROM tenant ... FOR NO KEY UPDATE`。
2. 固定锁顺序为：tenant Namespace 锁、upload session（若有）、节点/目标父目录、Blob。S3 调用不在
   tenant 锁内执行，Multipart 数据流、签名和普通读取不获取该锁。
3. 创建目录、移动、上传元数据提交和恢复在持有 tenant 锁后仍锁定并复查目标父目录。目录移动从目标
   父目录向上遍历祖先并拒绝遇到源目录；递归查询使用 `UNION`，使历史损坏数据也不会无限递归。
4. 回收递归的 anchor、递归项和最终 `UPDATE` 都重新要求节点仍为 `active` 且没有其他
   `trashed_root_id`。即使以后放宽串行化范围，旧快照也不能覆盖独立回收根。
5. `purging` 节点的版本已不再是可恢复的逻辑引用。清理 Blob 时只把 owner 仍非 `purging` 的版本视为
   有效外部引用；清理准备在 tenant 锁内执行并按 Blob ID 锁定候选，保证共享 Blob 的最后一个有效引用
   被清理时能够进入 `pending_delete`。

## Consequences

- 同一租户的 Namespace 写事务在 MVP 中线性化，优先保证树和回收归属正确；不同租户仍完全并行。
- 锁只覆盖短 PostgreSQL 事务，不覆盖客户端上传、对象完成、对象删除或下载，因此不串行化数据面。
- 大型单租户若出现可测量的 Namespace 写锁等待，应在 M2 以分层目录锁或全量 Serializable + 自动重试
  替换租户级互斥；替换前必须保留相同锁顺序、不变量和事务交错测试。
- `purging` 元数据作为不可恢复 tombstone 保留时，不会永久保护已无有效引用的共享对象。

## Rejected alternatives

- **只锁源节点和目标父目录**：两个跨子树移动可以锁住互不相交的节点集合，仍可能共同形成环。
- **只在递归 UPDATE 增加状态条件**：可以避免覆盖回收根，但不能阻止并发插入形成活动孤儿。
- **对整张 `file_node` 表加锁**：跨租户也会互相阻塞，不符合租户隔离和扩展方向。
- **立即依赖 Serializable 且不自动重试**：冲突会直接暴露为高频 503；MVP 尚无足够负载证据证明该取舍。

## Verification

- PostgreSQL 集成测试并发执行互为目标的目录移动，断言最多一个成功且递归查询可终止。
- 并发执行父目录回收与目录创建/文件提交/移动/恢复，断言不存在活动孤儿。
- 并发回收父子目录，断言独立回收根不被覆盖。
- 两个文件共享 Blob 并依次永久清理，断言最后一次清理会安排且完成对象删除。
