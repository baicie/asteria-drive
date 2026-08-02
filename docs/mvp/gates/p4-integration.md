# P4 设计门禁：真实集成与发布门槛

- 分支：`mvp/p4-integration`
- 状态：Accepted（设计）；实现/验收 **通过**（见 implementation-log 与 evidence）
- 日期：2026-08-02

## 目标

在 PostgreSQL + SeaweedFS 上复现主路径，并满足发布门槛；不引入 vision 中的 Redis/Kafka 等 NOT NOW 依赖。

## 设计产物

- [testing-and-operations.md](../../design/testing-and-operations.md)
- OpenAPI 与注册路由一致
- scope §9 暂定 SLO 测量方法

## 实现 / 验证范围

- HTTP → PG + SeaweedFS 端到端与重启持久性
- 跨租户、竞态、跨系统故障注入
- trusted-dev 生产模式拒绝、日志 Secret 扫描
- 暂定 SLO 与 100 并发会话基线
- 范围回归（P4-07）

## 出口

适用 MUST 验收项有证据；Definition of Done 可签核；仍标注非公网生产就绪。
