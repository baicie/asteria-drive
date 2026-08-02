# Asteria Drive 后端 MVP 规划

> 文档状态：范围与设计基线；候选实现和验收进度见
> [implementation-log.md](./implementation-log.md)。
>
> 本目录描述的是目标行为，不代表仓库当前已经具备这些能力。只有
> [acceptance.md](./acceptance.md) 中的验收项全部通过并满足 Definition of Done，才能宣布 MVP 完成。

## 目标

本轮交付一个有意收窄、可本地运行和可集成验证的后端垂直切片：

```text
受信任开发令牌
  -> 租户上下文
  -> 目录与文件元数据
  -> 创建上传会话并签发直传 URL
  -> 客户端直传 S3 兼容对象存储
  -> 提交文件版本
  -> 下载授权
  -> 回收站、恢复与清理
```

业务服务只承担控制面职责，不代理普通文件字节。生产实现使用 PostgreSQL 保存元数据，
通过 S3 兼容适配器连接 SeaweedFS 或其他兼容服务；快速单元测试使用内存 fake。

## 文档导航

| 文档 | 作用 | 主要读者 |
| --- | --- | --- |
| [scope.md](./scope.md) | 规定 MVP 的 MUST、SHOULD、NOT NOW、API 边界、数据不变量和暂定 SLO | 产品、架构、研发 |
| [roadmap.md](./roadmap.md) | 将工作拆为 P0-P4，给出任务 ID、依赖、里程碑出口和风险 | 研发、项目负责人 |
| [gates/](./gates/) | 各里程碑设计门禁（目标 → 设计/ADR → 实现出口） | 研发、评审人 |
| [acceptance.md](./acceptance.md) | 给出可执行的验收矩阵、证据要求和全局 Definition of Done | 研发、测试、评审人 |
| [../design/](../design/) | 详细设计与协议说明；以实际归档内容为准 | 研发、评审人 |
| [../adr/](../adr/) | 关键架构决策及其取舍；以实际归档内容为准 | 架构、研发、运维 |
| [../openapi.yaml](../openapi.yaml) | 与已注册 HTTP 路由对应的 OpenAPI 3.1 契约 | 客户端、研发、测试 |
| [../vision/](../vision/) | 产品蓝图（不扩大本轮 MUST） | 产品、架构 |
| [../process/branch-workflow.md](../process/branch-workflow.md) | 里程碑分支与 PR 门禁 | 研发 |

建议阅读顺序：`scope.md` -> 相关 ADR/详细设计 -> `roadmap.md` -> `gates/` -> `acceptance.md`。
工程分支约定见 [branch-workflow.md](../process/branch-workflow.md)。

## 编号体系

- `P0` 至 `P4`：本次 MVP 内部实施里程碑，不等同于产品版本。
- `P<n>-<nn>`：路线图任务，例如 `P2-03`。
- `AC-<nnn>`：验收条目，例如 `AC-031`。
- `M2`：MVP 之后的产品阶段。OIDC/OAuth2 生产身份接入属于 M2。

## 完成边界

MVP 完成意味着：在隔离的开发或验收环境中，可以使用已配置的受信任令牌完成上述垂直切片，
并分别在 PostgreSQL + SeaweedFS/S3 兼容端点与内存 fake 上获得规定的验证结果。

MVP 完成不意味着：系统已经适合公网生产部署。受信任开发令牌不是最终身份方案；在 M2 完成
OIDC/OAuth2、正式主体与权限模型、安全评审和部署加固之前，服务不得被描述为生产就绪。

## 约束词

- **MUST**：MVP 发布门槛，缺一项即不能完成。
- **SHOULD**：有明确价值，时间允许应交付；延期必须记录原因和后续任务。
- **NOT NOW**：明确不进入本次 MVP，不能以“顺手实现”为由扩大范围。

## 变更规则

1. 修改 MUST、API 语义、数据一致性边界或安全边界时，必须同步更新范围、路线图和验收矩阵。
2. 改变关键架构取舍时，必须先新增或修订 ADR，再修改实施计划。
3. 每个完成声明必须链接到可复现的测试、构建、迁移或负载验证证据。
4. 尚未验证的目标一律标记为“待实现”或“待验收”，不得写成已支持能力。
