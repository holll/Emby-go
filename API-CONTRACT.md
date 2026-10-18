# 对外 API 契约（v2 起）

> 范围：本服务对 Emby 客户端与脚本暴露的 HTTP 接口。v2 的目标是**只增不改**：
> 已发布的路径、状态码、JSON 字段语义不破坏；新增字段/端点/可选参数不视为破坏性变更。
> 方案见 [dev-plan.md](dev-plan.md) §10，进度见 [PLAN-STATUS.md](PLAN-STATUS.md) §8。
> 本文件由契约测试 `internal/server/api_contract_test.go` 守门；改路由/错误信封必须同批更新本文件。

## 1. 基路径

- Emby 兼容端点同时注册在两处：根路径（`/Users/...`）与 `/emby` 前缀（`/emby/Users/...`）。
- 管理端点固定在 `/api/admin/*`；初始化/健康检查/登录状态在 `/api/*` 与 `/healthz`。
- 未注册路径统一 404 `{"error":"not found","code":"NOT_FOUND"}`，且带 `Cache-Control: no-store`。

## 2. 认证

| 载体 | 用途 |
|---|---|
| `X-Emby-Token` / `X-MediaBrowser-Token` 请求头 | Emby 客户端会话 token |
| `X-Emby-Authorization` / `Authorization` 中的 `Token=` | 兼容部分客户端的鉴权头 |
| `api_key` / `ApiKey` / `X-Emby-Token` 查询参数 | 脚本/播放 URL 携带 |
| 管理 API 密钥 | 等价管理员全权（D02，已具名接受风险） |

- 图片端点（`/Items/:id/Images/*`、刮削图片代理）保持匿名，以兼容浏览器 `<img>` 与客户端图片缓存。
- 流与转发端点（`/videos/:id/stream`、`/Videos/:id/proxy`）必须鉴权：短时签名或 token 由服务端生成。

## 3. 错误信封

所有错误响应为 JSON：

```json
{"error": "human readable", "code": "STABLE_CODE", "retryable": true}
```

- `error` 是人类可读文本，措辞可随版本微调；**`code` 是稳定契约字段**，客户端应按 `code` 分支。
- `retryable` 仅在可重试错误（503/429）出现。
- 错误响应不带缓存（`Cache-Control: no-store`）。

| HTTP | code | 语义 |
|---|---|---|
| 400 | `INVALID_REQUEST` | 通用参数/请求体不合法（批次 74 覆盖 `/api/*`，批次 76 覆盖 Emby 兼容端点） |
| 400 | `IDEMPOTENCY_KEY_TOO_LONG` | 幂等键超过 128 字符 |
| 400 | `SCHEDULED_PRECONDITION_REQUIRED` | 条件更新缺前置 |
| 400 | `SCHEDULED_CONFIG_CHANGED` | 计划任务并发修改 |
| 401 | `AUTH_INVALID` |
| 403 | `ADMIN_REQUIRED`（非管理员访问 `/api/admin/*`）、`SETUP_CODE_INVALID`/`INIT_SOURCE_DENIED`（初始化） | 凭据缺失/无效/过期 |
| 403 | `SETUP_CODE_INVALID` | 初始化验证码错误 |
| 403 | `INIT_SOURCE_DENIED` | 初始化来源不在白名单 |
| 404 | `NOT_FOUND` / `TASK_NOT_FOUND` | 资源或任务不存在 |
| 409 | `CONFLICT` | 通用状态冲突（资源被占用/重复操作） |
| 409 | `SCAN_BUSY` | 扫描进行中 |
| 409 | `NFO_CHANGED` | 手工编辑与磁盘 NFO 冲突 |
| 413 | `BODY_TOO_LARGE` | 请求体超过端点预算 |
| 429 | `AUTH_RATE_LIMITED` | 登录/初始化失败过多 |
| 429 | `TASK_CAPACITY` | 异步任务表已满 |
| 503 | `AUTH_UNAVAILABLE` | 凭据存储不可用（可重试） |
| 503 | `UNAVAILABLE` | 通用服务不可用（可重试） |
| 502 | `UPSTREAM_UNAVAILABLE` | 播放源/上游不可用 |
| 428 | `PRECONDITION_REQUIRED` | 实例尚未初始化 |
| 415 | `UNSUPPORTED_MEDIA_TYPE` | 上传/请求类型不支持 |
| 500 | `INTERNAL` | 未归类内部错误（不回显内部细节） |

自批次 74 起，`/api/*` 的 400/404/409/428/500/502/503 都由统一 helper 补通用 `code`（`INVALID_REQUEST` / `NOT_FOUND` / `CONFLICT` / `PRECONDITION_REQUIRED` / `INTERNAL` / `UPSTREAM_UNAVAILABLE` / `UNAVAILABLE`），端点有更精确语义时保留专用 `code`（如 `SCAN_BUSY`）。

自批次 76 起 Emby 兼容端点的错误也统一带 `code`（详情/列表读失败 500、播放上报 400、各类 404 均由 `apiError` 输出）。
唯一例外：图片端点的未知 kind 返回 `404` **空 body**（浏览器 `<img>` 不需要 JSON），契约测试 `TestEmbyCompat` 固定该行为。

## 4. 稳定字段（Emby 浏览主链路）

| 端点组 | 方法/路径 | 契约要点 |
|---|---|---|
| 认证 | `POST /Users/AuthenticateByName` | 返回 `AccessToken`、`User.Id/Name`；失败 401 `AUTH_INVALID` |
| 系统 | `GET /System/Info/Public`、`GET /System/Ping` | 无需鉴权；`ServerName`/`Id`/`Version` 必在 |
| 用户 | `GET /Users/Me`、`GET /Users/Public` | 单用户映射到管理员；`/Users/Public` 恒返回空数组（不公开用户列表） |
| 视图 | `GET /Users/:uid/Views` | 库 `Id/Name/CollectionType` |
| 列表 | `GET /Users/:uid/Items`、`GET /Items` | `Items`、`TotalRecordCount`；排序/筛选参数为可选增量 |
| 详情 | `GET /Items/:id`、`GET /Users/:uid/Items/:id` | `Id/Name/Type/ImageTags/UserData/MediaSources` |
| 图片 | `GET /Items/:id/Images/:kind[/:index]` | 匿名；ETag/缓存头有效；`CacheTag` 变化即换图 |
| 播放信息 | `GET|POST /Items/:id/PlaybackInfo` | 单 `MediaSources`，`DirectPlay/DirectStream=true`、`Transcoding=false` |
| 播放上报 | `POST /Sessions/Playing`、`/Progress`、`/Stopped` | 204；计数由会话+序号保证幂等 |
| 续播 | `GET /Users/:uid/Items/Resume` | `UserData.PlaybackPositionTicks` 有效 |
| 用户数据 | `POST|DELETE /Users/:uid/PlayedItems/:id`、`FavoriteItems/:id`、`Items/:id/Rating` | 返回更新后的 `UserData`；评分用查询参数 `Likes=true|false`；`UserData.Likes` 为 bool，未评分时该字段不出现 |
| 流/转发 | `GET /videos/:id/stream`、`GET /Videos/:id/proxy` | 302 或转发；失败 502 `UPSTREAM_UNAVAILABLE` |

管理端点（`/api/admin/*`）是后台契约，供本仓库 Web 前端与脚本使用；清单见 `internal/server/server.go` 的 `routes()`，不在 Emby 兼容承诺内。

### 4.1 多用户管理面（批次 86 起）

- `GET /api/admin/users` → `{"users":[{id,name,is_admin,created_at,last_login_at,library_ids}],"total":n}`；不返回密码哈希。
- `POST /api/admin/users` → 201；`PUT /api/admin/users/:id`、`PUT /api/admin/users/:id/libraries`、`DELETE /api/admin/users/:id`（删除当前账户 400）。
- `GET /api/admin/login-events?user_id=&limit=` → 登录审计（失败登录 `user_id=0`，不归因具体账户）。
- `GET /api/admin/playback-events?user_id=&limit=` → 播放事件（含 `movie_title`/`user_name` 快照；用户删除后快照仍在）。
- `POST /api/auth/password`（需登录）→ 204，改密后撤销该账户全部会话。
- 可见性（批次 87 起强制）：媒体库按用户授权（`user_libraries`，默认无授权）；未授权库的列表为空、详情/PlaybackInfo/流/代理/播放 404；管理员不受限。Emby 数据只认本人；图片端点保持匿名（已批准兼容边界）。

## 5. 变更规则（v2）

- **允许（增量）**：新增端点、新增 JSON 字段、新增可选查询参数/请求头、新增 `code`。
- **禁止（v2 内）**：删除/改名已发布字段、改变状态码或 `code` 语义、改变认证载体要求、改变 302/转发的可用前提。
- **弃用**：先登记到下方 §5.1，运行时对旧端点回 `Deprecation`/`Sunset`/`Link` 头；实现至少保留到 `Sunset` 日期，移除视为破坏性变更（升 v3）。

### 5.1 弃用登记表

| 端点 | 替代 | 登记版本 | Sunset | 现状 |
|---|---|---|---|---|
| `POST /api/admin/scan`（同步扫描） | `POST /api/admin/tasks/scan`（202 + 任务轮询） | v2.0.0 | 2027-04-01 | 可用；响应带弃用头；Web 前端已迁移到异步入口 |

弃用头（RFC 8594 / RFC 8288）：

```http
Deprecation: true
Sunset: Thu, 01 Apr 2027 00:00:00 GMT
Link: </api/admin/tasks/scan>; rel="successor-version"
Warning: 299 - "deprecated endpoint; use POST /api/admin/tasks/scan"
```

规则：登记项必须有可用的替代路径与 `Sunset`；登记表在 `internal/server/deprecation.go`，契约测试核对路径真实存在且响应确实带弃用头。
- **破坏性变更**：升 v3，并在 v2 最后一个 minor 提供迁移说明与双写/双读窗口。
- JSON 字段名大小写、时间格式、ticks 单位（100ns）不得变更。

## 6. 门禁

- 契约测试：`go test ./internal/server -run 'TestAPIContract|TestEmbyCompat' -count=1`。
- 关键路由清单在 `api_contract_test.go` 中硬编码；新增/删除路由必须同批更新测试与本文件。
- 契约测试与 CI（`.github/workflows/ci.yml`）同批跑；未通过不得打 tag。
