# Web 传输可靠性 + 跨刷新恢复：本地验收证据（2026-11-02）

- 基线 `origin/main`：`fb0e5e09836a3b9518d651aaee7e63c4e7291b6a`
- 增量提交：`7c17ef3`（恢复实现）、`29e0833`（gofmt 修正）
- 分支：`claude/web-transfer-reliability-8ace5b`（叠加在 `feat/web-transfer-reliability` / #96 之上）
- Draft PR：[#97](https://github.com/baicie/asteria-drive/pull/97)
- CI run：[41474905270](https://github.com/baicie/asteria-drive/actions/runs/41474905270)（CI）、
  [41474905291](https://github.com/baicie/asteria-drive/actions/runs/41474905291)（Security）
- 边界：未合入 `main`、未打 tag、未发布 Release；未操作 staging 主机、数据卷或部署/回滚
  Workflow。以下证据只覆盖本地与隔离 CI 环境。

## 命令与结果

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go test ./... -count=1` | 全部包通过 |
| `go test -race ./... -count=1` | 全部包通过 |
| 集成套件（真实 PostgreSQL 17.5 + SeaweedFS 3.85） | 22/22 清单用例，`tools/verify-integration-report` 输出 `verified 22 required integration tests across 3 packages` |
| `npm run typecheck` | 通过 |
| `npm run test:unit` | 10 文件 / 163 用例通过 |
| `npm run build` | 通过 |
| `npm run test:e2e`（无 live 环境变量） | 19 passed，2 skipped（live-stack 按设计跳过） |
| `npm run test:e2e --grep "live stack"`（真实栈） | 2 passed |

## 真实栈恢复用例

`web/e2e/live-stack.spec.ts` 的第二个用例：

1. 通过真实 API 在根目录创建 multipart 会话；
2. 只用真实签名 URL 上传第 1 个分片；
3. 断言 `GET /api/v1/uploads/{id}/parts` 返回 `complete: true`、仅 `[1]`、
   且带 `Cache-Control: no-store`；
4. 把非敏感句柄写入 `sessionStorage` 后重新加载页面；
5. 断言页面出现“检测到未完成的上传”横幅；
6. 在页面内重建带原始 `lastModified` 的 `File` 并触发文件选择
   （`setInputFiles` 会改写 `lastModified`，被身份校验正确拒绝）；
7. 断言“已恢复并完成”提示出现、目录中文件大小等于原字节数、句柄已清除。

该用例同时验证了一个实现缺陷的修复：目录刷新会先 `clearNotice()`，所以恢复成功的提示
必须在刷新**之后**发出，否则用户看不到任何确认。

## CI 结果（run 41474905270）

`CI / quality`、`CI / race`、`CI / integration`、`CI / api-contract`、`CI / web`、
`CI / web-live` 全部 pass；`Security / codeql`、`Security / codeql-web`、
`Security / dependency-review`、`Security / govulncheck` 全部 pass。

`CI / web-live` 是本次新增的 job：固定版本 Compose 依赖 + 真实控制面 + Chromium，
只跑 `live stack` 用例，并以 `if: always()` 上传脱敏后的 `web-live-evidence`。它**尚未**
成为 required check。

## 首次推送失败与修正

首次推送（run `41474678713`）的 `CI / quality` 在 “Check formatting” 失败：新增的
`web-live` 测试夹具在 `gofmt` 下需要对齐 `actions:` 字段。已用 `29e0833` 修正并重跑，
上述 run 为修正后的结果。本地 `gofmt -l` 在 Windows 上因 `core.autocrlf` 会列出所有
文件，因此本地未发现该问题；后续核对改为对仓库内 LF 内容运行 `gofmt`。

## 未覆盖 / 风险

- 恢复是单条待恢复句柄，不是上传队列；多标签页互斥未实现，只拒绝单标签页内重复恢复。
- `ListParts` 在 SeaweedFS 上未验证超过 1000 个分片的聚合。
- `CI / web-live` 只跑过一次，稳定性与成为 required check 需要远端设置和多次运行证据。
- 未做真实浏览器自动化覆盖“刷新后点击继续”之外的 UI 分支（例如探测失败后的静默降级）。
