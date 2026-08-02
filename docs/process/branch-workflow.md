# MVP 里程碑分支与工程门禁

> 状态：工程流程基线。配合 [MVP roadmap](../mvp/roadmap.md) 与 [ADR 索引](../adr/README.md) 使用。

## 1. 目标

用短里程碑分支推进本轮后端 MVP，并强制：

```text
目标 → 设计 / ADR → 实现 → 验收证据
```

禁止在没有设计门禁的情况下扩大 MUST，或把 [vision](../vision/) 中的后续能力夹带进本轮。

## 2. 分支命名

| 分支 | 用途 |
| --- | --- |
| `main` | 稳定基线；仅合入已完成门禁的里程碑 |
| `mvp/integrate` | MVP 集成分支；里程碑串行合入后于此验证 |
| `docs/product-vision` | 产品蓝图与流程文档（可先于代码） |
| `mvp/p0-baseline` | P0 设计基线与服务骨架 |
| `mvp/p1-namespace` | P1 租户 Namespace |
| `mvp/p2-upload` | P2 直传上传 |
| `mvp/p3-download-recycle` | P3 下载与回收 |
| `mvp/p4-integration` | P4 真实依赖集成与发布门槛 |

依赖链：

```text
docs/product-vision
  → mvp/p0-baseline
  → mvp/p1-namespace
  → mvp/p2-upload
  → mvp/p3-download-recycle
  → mvp/p4-integration
  → mvp/integrate → main
```

历史工作分支 `codex/mvp` 仅作过渡；新工作落到上表分支。

## 3. PR 门禁

每个里程碑至少包含两段（两个 PR，或同一 PR 内先文档 commit、后代码 commit）：

### Design gate

- 更新对应目标：`docs/mvp/scope.md` / `roadmap.md` / `acceptance.md` 或设计文档。
- 关键变更以 ADR `Proposed` 起草，评审后改为 `Accepted`。
- 本段**不**合入扩大范围的实现。

### Impl gate

- 代码与自动化测试。
- 更新 [`docs/mvp/implementation-log.md`](../mvp/implementation-log.md)：命令、环境摘要、结果。
- 未附证据的验收项保持「待验收」，不得写成已通过。

### 合并要求

- `go test ./...`、`go vet ./...`、`go build ./...` 在适用环境下通过（集成测试按门禁变量跳过或通过）。
- 不引入 [scope NOT NOW](../mvp/scope.md) 作为构建/启动/主路径依赖。
- 不提交 Secret、真实 Token、签名 URL 或生产凭据。

## 4. ADR 触发条件

出现以下任一变化时，必须新增或修订 ADR（旧 ADR 标 `Superseded`，不改写历史结论）：

- 安全边界（身份、租户隔离、生产模式防护）
- 一致性模型或事务边界
- 数据面路径（是否代理文件字节、签名模型）
- 上传/回收等状态机语义
- 存储选型或 StorageProvider 契约
- 身份模型（如 trusted-dev → OIDC）
- MVP MUST / SHOULD / NOT NOW 的实质调整

ADR 写「为什么」与稳定约束；DDL、接口字段、交付步骤分别进 design / mvp / OpenAPI。

## 5. 与产品愿景的边界

- [`docs/vision/`](../vision/) 描述 Phase 1–4 与 PB 级演进，**不扩大**本轮 MUST。
- P4 出口必须做范围回归：确认 Redis、Kafka、OpenSearch、分享、配额、Web/Tauri 等未成为
  MVP 运行依赖（除非经 ADR 与 scope 正式变更）。

## 6. 证据纪律

- 内存 fake 证明业务规则；PostgreSQL + SeaweedFS 证明真实协议与持久化。
- 验收结论只认 implementation-log 中的可复现证据。
- 编译成功或单次 happy-path 演示不足以宣布 MVP Done。
