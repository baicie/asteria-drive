# MVP 里程碑设计门禁

每个 `mvp/pN-*` 分支在实现合入前应具备对应门禁记录。门禁确认范围与验收映射，不替代
[scope.md](../scope.md) 或 [acceptance.md](../acceptance.md)。

| 门禁 | 分支 | 主要 ADR / 设计 | 主要 AC |
| --- | --- | --- | --- |
| [p0-baseline.md](./p0-baseline.md) | `mvp/p0-baseline` | ADR-0001～0003，design/* | AC-001～003 |
| [p1-namespace.md](./p1-namespace.md) | `mvp/p1-namespace` | ADR-0007，data-model | AC-010～023 |
| [p2-upload.md](./p2-upload.md) | `mvp/p2-upload` | ADR-0004～0006、0008 | AC-024～037、071 |
| [p3-download-recycle.md](./p3-download-recycle.md) | `mvp/p3-download-recycle` | ADR-0009 | AC-040～053 |
| [p4-integration.md](./p4-integration.md) | `mvp/p4-integration` | testing-and-operations | AC-060～091 |

流程见 [branch-workflow.md](../../process/branch-workflow.md)。
