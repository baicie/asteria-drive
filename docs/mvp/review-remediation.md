# PR #2 审查问题修复计划

状态：设计、实现与验证已完成；PR #2 已于 2026-08-03 合并到 `main`（merge commit `57fc1c1`）。
基线分支 `mvp/p4-integration`，修复分支 `codex/fix-p4-review-findings`。

## 问题与设计映射

| ID | 审查问题 | 设计决定 | 验证 |
| --- | --- | --- | --- |
| RF-01 | 并发目录移动形成环 | ADR-0012 tenant Namespace 锁 + 祖先检查 | PostgreSQL 并发移动测试 |
| RF-02 | 父回收与子写入形成活动孤儿 | ADR-0012 tenant Namespace 锁 + 父行复查 | 创建/移动/提交/恢复竞态测试 |
| RF-03 | 并发嵌套回收覆盖独立根 | ADR-0012 防御性状态条件 | 父子并发回收测试 |
| RF-04 | 未知 Complete 结果返回非重试 404 | ADR-0013 保持 `COMPLETING` + 可重试错误 | Service 故障注入 |
| RF-05 | `OBJECT_COMPLETED` 名称冲突无法清理 | ADR-0013 确定性 `FAILED` + Delete 重试 | 双会话同名完成测试 |
| RF-06 | 回收后完成重试失去幂等结果 | ADR-0013 从 session 读取 committed 结果 | 回收/清理后重放测试 |
| RF-07 | 共享 Blob 依次清理后泄漏 | ADR-0012 只统计非 `purging` owner | 共享 Blob 顺序清理测试 |
| RF-08 | 恢复唯一冲突错误码错误 | restore-aware 约束映射 | Repository 映射测试 |
| RF-09 | 非法 UUID 返回 500 | Service UUID 边界校验 + SQLSTATE 防御 | HTTP 400 测试 |
| RF-10 | `NoSuchBucket` 返回资源 404 | ADR-0013 依赖错误分类 | S3 adapter 单元测试 |

## 实施步骤

1. [x] 归档 ADR-0012、ADR-0013 和本问题矩阵。
2. [x] 实现 PostgreSQL Namespace 锁、锁顺序和递归防御条件。
3. [x] 实现完成失败收敛、committed 结果读取和失败对象清理重试。
4. [x] 修正 Blob 有效引用、restore/UUID/S3 错误映射。
5. [x] 补充单元、Repository、HTTP 和真实依赖并发测试。
6. [x] 执行格式、测试、静态检查、构建和 Linux race 门禁，并把结果追加到实施日志。

评审时任何未通过的 RF 项都会阻断合入；上述项目已在 PR #2 合并前全部关闭。
