# P0 设计门禁：基线

- 分支：`mvp/p0-baseline`
- 状态：Accepted（设计基线已归档）
- 日期：2026-08-02

## 目标

固化 MVP 边界、契约、port 方向和可启动服务骨架。

## 设计产物

- [scope.md](../scope.md)、[roadmap.md](../roadmap.md)、[acceptance.md](../acceptance.md)
- [design/architecture.md](../../design/architecture.md) 等详细设计
- ADR-0001、0002、0003（Redis/Outbox 仅为演进边界，非本轮运行依赖）

## 实现范围

配置、health/ready、统一错误、请求 ID、优雅停止、迁移入口与 Compose 本地依赖说明。

## 出口

构建与静态检查可重复；AC-001～003 具备自动化或可复现证据路径。
