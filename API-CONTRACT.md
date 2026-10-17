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
| 400 | `IDEMPOTENCY_KEY_TOO_LONG` | 幂等键超过 128 字符 |
| 400 | `SCHEDULED_PRECONDITION_REQUIRED` | 条件更新缺前置 |
| 400 | `SCHEDULED_CONFIG_CHANGED` | 计划任务并发修改 |
| 401 | `AUTH_INVALID` | 凭据缺失/无效/过期 |
| 403 | `SETUP_CODE_INVALID` | 初始化验证码错误 |
| 403 | `INIT_SOURCE_DENIED` | 初始化来源不在白名单 |
| 404 | `NOT_FOUND` / `TASK_NOT_FOUND` | 资源或任务不存在 |
| 409 | `SCAN_BUSY` | 扫描进行中 |
| 409 | `NFO_CHANGED` | 手工编辑与磁盘 NFO 冲突 |
| 413 | `BODY_TOO_LARGE` | 请求体超过端点预算 |
| 429 | `AUTH_RATE_LIMITED` | 登录/初始化失败过多 |
| 429 | `TASK_CAPACITY` | 异步任务表已满 |
| 503 | `AUTH_UNAVAILABLE` | 凭据存储不可用（可重试） |
| 502 | `UPSTREAM_UNAVAILABLE` | 播放源/上游不可用 |

管理面旧端点仍有只带 `error` 的 400/404/500；批次 74 会补齐 `code`（纯增量），在此之前客户端应对缺失 `code` 做兜底。

## 4. 稳定字段（Emby 浏览主链路）

| 端点组 | 方法/路径 | 契约要点 |
|---|---|---|
| 认证 | `POST /Users/AuthenticateByName` | 返回 `AccessToken`、`User.Id/Name`；失败 401 `AUTH_INVALID` |
| 系统 | `GET /System/Info/Public`、`GET /System/Ping` | 无需鉴权；`ServerName`/`Id`/`Version` 必在 |
| 用户 | `GET /Users/Me`、`GET /Users/Public` | 单用户映射到管理员 |
| 视图 | `GET /Users/:uid/Views` | 库 `Id/Name/CollectionType` |
| 列表 | `GET /Users/:uid/Items`、`GET /Items` | `Items`、`TotalRecordCount`；排序/筛选参数为可选增量 |
| 详情 | `GET /Items/:id`、`GET /Users/:uid/Items/:id` | `Id/Name/Type/ImageTags/UserData/MediaSources` |
| 图片 | `GET /Items/:id/Images/:kind[/:index]` | 匿名；ETag/缓存头有效；`CacheTag` 变化即换图 |
| 播放信息 | `GET|POST /Items/:id/PlaybackInfo` | 单 `MediaSources`，`DirectPlay/DirectStream=true`、`Transcoding=false` |
| 播放上报 | `POST /Sessions/Playing`、`/Progress`、`/Stopped` | 204；计数由会话+序号保证幂等 |
| 续播 | `GET /Users/:uid/Items/Resume` | `UserData.PlaybackPositionTicks` 有效 |
| 用户数据 | `POST|DELETE /Users/:uid/PlayedItems/:id`、`FavoriteItems/:id`、`Items/:id/Rating` | 返回更新后的 `UserData` |
| 流/转发 | `GET /videos/:id/stream`、`GET /Videos/:id/proxy` | 302 或转发；失败 502 `UPSTREAM_UNAVAILABLE` |

管理端点（`/api/admin/*`）是后台契约，供本仓库 Web 前端与脚本使用；清单见 `internal/server/server.go` 的 `routes()`，不在 Emby 兼容承诺内。

## 5. 变更规则（v2）

- **允许（增量）**：新增端点、新增 JSON 字段、新增可选查询参数/请求头、新增 `code`。
- **禁止（v2 内）**：删除/改名已发布字段、改变状态码或 `code` 语义、改变认证载体要求、改变 302/转发的可用前提。
- **弃用**：先标记并在 README/本文件登记，至少一个 minor 版本后移除；移除视为破坏性变更。
- **破坏性变更**：升 v3，并在 v2 最后一个 minor 提供迁移说明与双写/双读窗口。
- JSON 字段名大小写、时间格式、ticks 单位（100ns）不得变更。

## 6. 门禁

- 契约测试：`go test ./internal/server -run TestAPIContract -count=1`。
- 关键路由清单在 `api_contract_test.go` 中硬编码；新增/删除路由必须同批更新测试与本文件。
- 契约测试与 CI（`.github/workflows/ci.yml`）同批跑；未通过不得打 tag。
