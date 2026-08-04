# Asteria Drive 后端 MVP REST API

状态：实现契约基线  
版本：`v1`  
最后更新：2026-08-02

机器可读契约：[OpenAPI 3.1](../openapi.yaml)

## 1. 范围

本 API 是网盘控制面，处理 OIDC/OAuth2 或 trusted-dev 身份、租户 Namespace、上传协调、下载授权和回收站。文件正文通过短期 S3 URL 在客户端与对象存储之间传输，绝不经过这些路由。

成员邀请/删除、正式 ACL、分享、配额、历史版本 API、同步和预览属于后续 M2 阶段。本轮已落地 M2-1 的
Resource Server 验证、成员解析和基础 RBAC，以及 M2-2 的成员列表、角色和 active/suspended 状态更新；不保留未实现的占位路由。

所有业务路由以 `/api/v1` 开头。`/healthz`、`/readyz` 是无版本探针。

## 2. HTTP 约定

- JSON 请求必须使用 `Content-Type: application/json`；无 Body 的请求除外。
- JSON 响应使用 `application/json; charset=utf-8`。
- 请求解码拒绝未知字段、多个顶层值和超出路由上限的 Body。
- 时间为 UTC RFC 3339；ID 为 UUID 字符串；大小为 bytes。
- 服务端在响应设置 `X-Request-ID`。合法入站 ID 可透传，否则生成新 ID。
- 成功资源直接放在 `data`；分页附带 `page.next_cursor`。
- 预签名 URL 是 Bearer 凭证，不得出现在日志、Trace 或错误中。

示例成功：

```json
{
  "data": {
    "id": "17da10b8-70d1-4f83-8d37-e127d110ef75",
    "kind": "directory"
  }
}
```

## 3. 身份与租户

除探针外都要求：

```http
Authorization: Bearer <oidc-access-token-or-trusted-dev-token>
```

`trusted-dev` Token 由服务端配置固定映射到 `tenant_id` 和 `principal_id`，并忽略 `X-Tenant-ID`。OIDC 请求必须携带 UUID 格式的 `X-Tenant-ID`；服务端验证 JWT 的 issuer、签名、audience、`exp`/`nbf` 和 subject，再用 `(issuer, subject)` 查询 PostgreSQL `principal` 与 `tenant_member`。租户和角色不信任 JWT 自声明。缺失/无效 Token 返回 `401 unauthenticated`；OIDC 缺失或非法租户选择器返回 `400 invalid_request`；未加入租户、suspended 成员或权限不足返回 `403 forbidden`。跨租户资源 ID 与不存在 ID 仍统一返回 `404 not_found`。

`trusted-dev` 只允许 `ASTERIA_ENV=development`。production 必须配置 `ASTERIA_AUTH_MODE=oidc`、PostgreSQL 和 S3，并在监听端口前完成校验。

## 4. 错误契约

```json
{
  "error": {
    "code": "name_conflict",
    "message": "an active item with this name already exists",
    "request_id": "req_2f5f..."
  }
}
```

外部 `message` 稳定且不含 SQL、Bucket、Object Key、供应商响应或栈。主要错误：

| HTTP | code | 含义 |
| --- | --- | --- |
| 400 | `invalid_request` | JSON、字段、名称、Part 或状态输入非法 |
| 400 | `invalid_cursor` | Cursor 篡改、版本或作用域错误 |
| 401 | `unauthenticated` | 缺失或无效 Bearer Token |
| 403 | `forbidden` | 未加入租户、成员已 suspended 或角色缺少路由权限 |
| 404 | `not_found` | 资源不存在、跨租户或当前状态不可见 |
| 409 | `name_conflict` | 活动同目录规格化名称冲突 |
| 409 | `invalid_state` | 上传/回收状态不允许此操作 |
| 409 | `idempotency_conflict` | 同一完成会话收到不同清单 |
| 409 | `restore_conflict` | 原父目录不可用或名称被占用 |
| 412 | `revision_mismatch` | `If-Match` 与当前 revision 不一致 |
| 413 | `request_too_large` | JSON Body 超出 1 MiB 路由上限 |
| 415 | `unsupported_media_type` | 需要 JSON 但 Content-Type 不符 |
| 503 | `dependency_unavailable` | 必需 PostgreSQL/S3 控制面不可用 |
| 500 | `internal_error` | 未分类内部错误 |

## 5. 资源表示

### 5.1 Node

```json
{
  "id": "17da10b8-70d1-4f83-8d37-e127d110ef75",
  "parent_id": "7ce1b22b-dd38-4037-b337-f67f5ab84042",
  "kind": "file",
  "name": "report.pdf",
  "size": 1048576,
  "mime_type": "application/pdf",
  "revision": 1,
  "created_at": "2026-08-02T10:00:00Z",
  "updated_at": "2026-08-02T10:00:00Z"
}
```

内部 `normalized_name`、Blob ID、Bucket、Object Key、storage upload ID 和租户 ID 不返回。

### 5.2 Upload

```json
{
  "id": "55f5efc1-51e8-43be-9408-e4b6bcda92e0",
  "parent_id": "7ce1b22b-dd38-4037-b337-f67f5ab84042",
  "name": "report.pdf",
  "expected_size": 1048576,
  "mime_type": "application/pdf",
  "status": "created",
  "part_size": 8388608,
  "expires_at": "2026-08-03T10:00:00Z",
  "committed_file_id": null
}
```

状态对外使用小写：`created`、`uploading`、`completing`、`object_completed`、`committed`、`aborted`、`expired`、`failed`。

## 6. 探针

### `GET /healthz`

不访问依赖。`200`：

```json
{"service":"asteria-server","status":"ok"}
```

### `GET /readyz`

检查 Repository；真实 S3 模式还检查轻量控制面可达性。全部就绪返回 `200 status=ready`，否则 `503 status=not_ready`，不返回依赖地址或凭据。

## 7. 当前租户

### `GET /api/v1/tenant`

返回 Bearer Token 固定映射的租户和客户端后续操作所需的根目录 ID。该接口不接受 `tenant_id`、
`X-Tenant-ID` 或任何租户选择参数：

```json
{
  "data": {
    "id": "11111111-1111-4111-8111-111111111111",
    "display_name": "Local Asteria",
    "root_directory_id": "7ce1b22b-dd38-4037-b337-f67f5ab84042",
    "created_at": "2026-08-02T10:00:00Z"
  }
}
```

租户在服务启动时由受控 Token 配置初始化；MVP 不提供租户创建、枚举或切换 API。

## 8. Namespace

### `POST /api/v1/directories`

```json
{"parent_id":"7ce1b22b-dd38-4037-b337-f67f5ab84042","name":"Projects"}
```

创建目录，返回 `201`、`Location: /api/v1/directories/{id}`、Node 和 `ETag: \"<revision>\"`。并发规格化重名只有一个成功，其余 `409 name_conflict`。`Idempotency-Key` 为 SHOULD；实现后相同键/同请求重放结果，不同请求返回冲突。

### `GET /api/v1/directories/{id}`

仅返回活动目录；文件 ID、回收根/后代、跨租户均为 `404`。

### `GET /api/v1/directories/{id}/children?limit=50&cursor=...`

列出直接活动子项。默认 50，最大 200。排序键为 `(normalized_name,id)`；Cursor HMAC 绑定 tenant、父目录、版本和最后一项。

```json
{
  "data": [{"id":"...","kind":"directory","name":"Projects","revision":1}],
  "page": {"next_cursor": null}
}
```

### `GET /api/v1/files/{id}`

返回已提交、活动文件 Node。未提交、目录、回收/清理中或跨租户均为 `404`。

### `PATCH /api/v1/nodes/{id}`

至少提供一个字段：

```json
{"name":"Archive","parent_id":"4ff06e49-20c5-4f08-b578-46c26a163493"}
```

要求 `If-Match: \"<revision>\"`。根目录不可修改；目录不能移到自身或后代；目标父必须是活动同租户目录。成功 `200` 返回新 Node/ETag。该操作不调用 StorageProvider。

## 9. 上传

### `POST /api/v1/uploads`

```json
{
  "parent_id": "7ce1b22b-dd38-4037-b337-f67f5ab84042",
  "name": "report.pdf",
  "size": 1048576,
  "mime_type": "application/pdf",
  "checksum": {"algorithm":"sha256","value":"47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="}
}
```

`checksum` 可省略。服务端验证名称、父目录、大小上限和同名冲突，生成不透明对象 Key并创建 Multipart。返回 `201` Upload；不返回 Bucket/Key/长期凭据。`Idempotency-Key` 为 SHOULD。

### `GET /api/v1/uploads/{id}`

返回当前租户的会话与最终文件 ID。过期会话可以返回 `expired`；跨租户/不存在为 `404`。

### `POST /api/v1/uploads/{id}/parts/sign`

```json
{"part_number":1,"checksum":{"algorithm":"sha256","value":"47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="}}
```

Part Number 为 1..10000。只允许非过期 `created/uploading` 会话；首次签名可把状态推进为 `uploading`。返回：

```json
{
  "data": {
    "part_number": 1,
    "method": "PUT",
    "url": "https://s3.example/...signed...",
    "required_headers": {},
    "expires_at": "2026-08-02T10:15:00Z"
  }
}
```

签名限定会话保存的 Key、Upload ID、Part Number、方法和服务端 TTL。客户端不能提交 Endpoint/Bucket/Key/TTL。

### `POST /api/v1/uploads/{id}/complete`

```json
{
  "parts": [
    {"part_number":1,"etag":"\"storage-etag\"","checksum":{"algorithm":"sha256","value":"47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="}}
  ]
}
```

Part 必须严格递增、唯一、数量有界。服务端对正规化清单计算 digest：

- 首次成功返回 `201`，同时返回 committed Upload 与 File Node。
- 相同清单重试返回 `200` 和同一结果。
- 不同清单重试返回 `409 idempotency_conflict`。

完成对象后必须 `HEAD`/provider result 校验实际大小和可用 Checksum，再在一个 PostgreSQL 事务中创建 Blob、Node、Version 并提交会话。对象成功但可重试事务失败时保留 `object_completed`，重试不再次创建业务结果；父目录不可用或名称冲突会持久化为 `failed` 并精确清理对象。

SHA-256 值使用标准 Base64 编码的 32-byte 摘要。后端返回并验证完整摘要时记录 `verified`；不支持 checksum request/response 能力时保留客户端值并记录 `declared`。`ASTERIA_S3_CHECKSUM_HEADERS` 只能在真实后端契约测试通过后开启，SeaweedFS 3.85 参考环境保持关闭。

### `DELETE /api/v1/uploads/{id}`

幂等取消未完成会话并尽力 Abort Multipart；对已经持久化为 `failed` 且对象曾完成的会话，重复取消会重试精确对象 Delete。首次和重复成功都返回 `204`。`completing`、`object_completed` 和已提交会话不被公开取消，返回 `409 invalid_state`。过期维护任务使用同一领域操作。

## 10. 下载授权

### `POST /api/v1/files/{id}/download-authorizations`

无 Body 或 `{}`。只为活动已提交文件的当前版本签发短期 GET URL。

```json
{
  "data": {
    "method": "GET",
    "url": "https://s3.example/...signed...",
    "expires_at": "2026-08-02T10:15:00Z"
  }
}
```

`Content-Disposition` 使用安全编码的显示名。Range 由对象存储处理；API 不代理正文。目录、回收/清理中、未提交和跨租户资源返回 `404`。

## 11. 回收站

### `DELETE /api/v1/nodes/{id}`

要求 `If-Match`，把文件或目录根项移入回收站，返回 `204`。目录后代随根项对普通查询/下载不可见。根目录不可回收。已回收根的重复请求可返回 `204`；跨租户/不存在为 `404`。

### `GET /api/v1/recycle-bin?limit=50&cursor=...`

列出当前租户的回收根项，不重复列出后代。返回 Node、`original_parent_id`、`deleted_at` 和下一 Cursor。

### `POST /api/v1/recycle-bin/{id}/restore`

可选 Body `{}`，要求 `If-Match`。恢复原父/名称；原父不存在、不活动或名称冲突返回 `409 restore_conflict`，
不覆盖、不自动改名。成功 `200` 返回 Node/ETag；成功后该 ID 已不再是回收站项，重复调用恢复路由返回
`404 not_found`，调用方可用已知 ID 读取恢复后的目录或文件。

### `DELETE /api/v1/recycle-bin/{id}`

要求 `If-Match`，同步执行引用安全的永久清理；全部对象删除与元数据收尾成功后返回 `204`。节点先进入不可见 `purging`；对象删除失败返回依赖错误且可用相同 revision 重试，不会把节点恢复为可见。仍被版本引用的 Blob 不得调用 Delete。异步 GC 接受语义不属于本轮公开契约。

## 12. 幂等、并发与限制

- 上传完成以会话 ID + completion digest 强制幂等，是 MUST。
- 路径和 Body 中格式非法的 UUID 返回 `400 invalid_request`；合法但不存在或跨租户的 UUID 返回 `404 not_found`。
- 目录/上传创建的 `Idempotency-Key` 是 SHOULD；若尚未实现，客户端只可在明确未收到成功时重新读取/重试并处理 `name_conflict`。
- PATCH、回收、恢复和清理使用 revision/`If-Match`；格式为带引号的十进制 revision。
- 默认 JSON Body 1 MiB；完成清单可单独配置但最多 10000 Part。
- 默认文件上限 50 GiB、会话 TTL 24 小时、签名 TTL 15 分钟；服务端可下调，客户端不能提高。自动回收
  保留策略尚未进入本轮公开 HTTP 契约。
- 服务端配置 HTTP 超时；外部依赖错误分类为可重试/不可重试，响应不暴露供应商详情。

## 13. 路由总表

| 方法 | 路径 | 成功 |
| --- | --- | --- |
| GET | `/healthz` | 200 |
| GET | `/readyz` | 200/503 |
| GET | `/api/v1/tenant` | 200 |
| GET | `/api/v1/tenant/members` | 200 |
| PATCH | `/api/v1/tenant/members/{principal_id}` | 200 |
| POST | `/api/v1/directories` | 201 |
| GET | `/api/v1/directories/{id}` | 200 |
| GET | `/api/v1/directories/{id}/children` | 200 |
| GET | `/api/v1/files/{id}` | 200 |
| PATCH | `/api/v1/nodes/{id}` | 200 |
| DELETE | `/api/v1/nodes/{id}` | 204 |
| POST | `/api/v1/uploads` | 201 |
| GET | `/api/v1/uploads/{id}` | 200 |
| POST | `/api/v1/uploads/{id}/parts/sign` | 200 |
| POST | `/api/v1/uploads/{id}/complete` | 200/201 |
| DELETE | `/api/v1/uploads/{id}` | 204 |
| POST | `/api/v1/files/{id}/download-authorizations` | 201 |
| GET | `/api/v1/recycle-bin` | 200 |
| POST | `/api/v1/recycle-bin/{id}/restore` | 200 |
| DELETE | `/api/v1/recycle-bin/{id}` | 204 |

OpenAPI 必须与本表和示例同步；路由测试比较注册路由与契约，防止文档漂移。
