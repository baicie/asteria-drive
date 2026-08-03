# P1 设计门禁：租户 Namespace

- 分支：`mvp/p1-namespace`
- 状态：Accepted
- 日期：2026-08-02

## 目标

以 PostgreSQL 为真相源，提供严格租户隔离的目录/文件元数据能力。

## 设计产物

- ADR-0007 身份与租户边界
- data-model 中 `file_node`、Cursor、名称 NFC 规格化
- `GET /api/v1/tenant` 只读发现

## 实现范围

`internal/auth`、Metadata Repository（PostgreSQL + memory fake）、目录创建/读取/列表/移动重命名。

## 出口

401、跨租户 404、名称冲突 409、非法移动、稳定 Cursor 分页有自动化证据。
