# 对外 API 契约（v2 起）

> 范围：本服务对 Emby 客户端与脚本暴露的 HTTP 接口。v2 的目标是**只增不改**：
> 已发布的路径、状态码、JSON 字段语义不破坏；新增字段/端点/可选参数不视为破坏性变更。
> 设计边界见 [开发计划](<dev-plan.md>)，历史实施与部署验收见 [施工台账](<PLAN-STATUS.md>)。
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
| 428 | `SCHEDULED_PRECONDITION_REQUIRED` | 条件更新缺前置 |
| 412 | `SCHEDULED_CONFIG_CHANGED` | 计划任务并发修改 |
| 401 | `AUTH_INVALID` | 凭据缺失/无效/过期 |
| 403 | `ADMIN_REQUIRED` | 非管理员访问管理端点 |
| 403 | `SETUP_CODE_INVALID` | 初始化验证码错误 |
| 403 | `INIT_SOURCE_DENIED` | 初始化来源不在白名单 |
| 404 | `NOT_FOUND` / `TASK_NOT_FOUND` | 资源或任务不存在 |
| 409 | `CONFLICT` | 通用状态冲突（资源被占用/重复操作） |
| 409 | `SCAN_BUSY` | 扫描进行中 |
| 409 | `PLAYBACK_SESSION_MOVIE_CONFLICT` | 同一用户的 PlaySessionId 已绑定其他影片；start/progress/stopped 均拒绝且零写入，不重绑会话 |
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
| 500 | `ITEM_DATA_UNAVAILABLE` | 列表/详情读取用户数据或演员失败，宁可整体失败也不回填空数据（`internal/server/item_associations.go`） |

自批次 74 起，`/api/*` 的 400/404/409/428/500/502/503 都由统一 helper 补通用 `code`（`INVALID_REQUEST` / `NOT_FOUND` / `CONFLICT` / `PRECONDITION_REQUIRED` / `INTERNAL` / `UPSTREAM_UNAVAILABLE` / `UNAVAILABLE`），端点有更精确语义时保留专用 `code`（如 `SCAN_BUSY`）。

自批次 76 起 Emby 兼容端点的错误也统一带 `code`（详情/列表读失败 500、播放上报 400、各类 404 均由 `apiError` 输出）。
唯一例外：图片端点的未知 kind 返回 `404` **空 body**（浏览器 `<img>` 不需要 JSON），契约测试 `TestEmbyCompat` 固定该行为。

## 4. 稳定字段（Emby 浏览主链路）

| 端点组 | 方法/路径 | 契约要点 |
|---|---|---|
| 认证 | `POST /Users/AuthenticateByName` | 返回 `AccessToken`、`User.Id/Name`；失败 401 `AUTH_INVALID` |
| 系统 | `GET /System/Info/Public`、`GET /System/Ping` | 无需鉴权；`ServerName`/`Id`/`Version` 必在 |
| 用户 | `GET /Users/Me`、`GET /Users/Public` | `/Users/Me` 返回当前认证用户；`/Users/Public` 恒返回空数组（不公开用户列表） |
| 视图 | `GET /Users/:uid/Views` | 库 `Id/Name/CollectionType` |
| 列表 | `GET /Users/:uid/Items`、`GET /Items` | `Items`、`TotalRecordCount`；排序/筛选参数为可选增量 |
| 详情 | `GET /Items/:id`、`GET /Users/:uid/Items/:id` | `Id/Name/Type/ImageTags/UserData/MediaSources` |
| 所属合集 | `GET /Items/:id/Collections`、`GET /Users/:uid/Items/:id/Collections` | 返回包含该影片的 `BoxSet` 列表（`Items`/`TotalRecordCount`）；无合集为空数组 |
| 图片 | `GET /Items/:id/Images/:kind[/:index]` | 匿名；ETag/缓存头有效；`CacheTag` 变化即换图；带 `maxWidth`/`maxHeight` 时按档位**向下**对齐（响应永不超过请求上限），同一输入只编码一次 |
| 播放信息 | `GET|POST /Items/:id/PlaybackInfo` | 单 `MediaSources`，`DirectPlay/DirectStream=true`、`Transcoding=false` |
| 播放上报 | `POST /Sessions/Playing`、`/Progress`、`/Stopped` | 204；计数由会话+序号保证幂等 |
| 续播 | `GET /Users/:uid/Items/Resume` | `UserData.PlaybackPositionTicks` 有效 |
| 用户数据 | `POST|DELETE /Users/:uid/PlayedItems/:id`、`FavoriteItems/:id`、`Items/:id/Rating` | 返回更新后的 `UserData`；评分用查询参数 `Likes=true|false`；`UserData.Likes` 为 bool，未评分时该字段不出现 |
| 流/转发 | `GET /videos/:id/stream`、`GET /Videos/:id/proxy` | 302 或转发；失败 502 `UPSTREAM_UNAVAILABLE` |

自批次 91 起，影片/列表项在 `Collection` 非空时输出 `CollectionIds: ["boxset:<base64url(name)>"]`（Emby 客户端据此渲染“所属合集”）。

管理端点（`/api/admin/*`）是后台契约，供本仓库 Web 前端与脚本使用；清单见 `internal/server/server.go` 的 `routes()`，不在 Emby 兼容承诺内。

### 4.1 多用户管理面（批次 86 起）

- `GET /api/admin/users` → `{"users":[{id,name,is_admin,created_at,last_login_at,library_ids}],"total":n}`；不返回密码哈希。
- `POST /api/admin/users` → 201；`PUT /api/admin/users/:id`、`PUT /api/admin/users/:id/libraries`、`DELETE /api/admin/users/:id`（删除当前账户 400）。
- `GET /api/admin/login-events?user_id=&limit=` → 登录审计（失败登录 `user_id=0`，不归因具体账户）。
- `GET /api/admin/playback-events?user_id=&limit=` → 播放事件（含 `movie_title`/`user_name` 快照；用户删除后快照仍在）。
- `POST /api/auth/password`（需登录）→ 204，改密后撤销该账户全部会话；改密前已通过密码验证但尚未签发的登录请求也不能再产生有效会话。
- `POST /api/auth/logout`（需登录）→ 204，仅撤销当前登录 token；其他设备的登录会话及 API 密钥不受影响。Emby 兼容入口 `POST /Sessions/Logout` 保留。
- 可见性（批次 87 起强制）：媒体库按用户授权（`user_libraries`，默认无授权）；未授权库的列表为空、详情/PlaybackInfo/流/代理/播放 404；管理员不受限。Suggestions 与实体聚合也必须限定可见库，实体缓存按授权范围隔离；Images JSON 清单、AdditionalParts、Similar 源影片及个人状态写入遵守同样的对象授权。Emby 数据只认本人；仅图片字节端点保持匿名（已批准兼容边界）。

### 4.2 独立翻译（管理面）

- `POST /api/admin/translate/run` → 202，返回进度快照（含 `task_id`、`running`、`kind`、`total/done/success/skipped/failed`）。202 仅表示任务已接收，非翻译或索引刷新已成功。
- 请求：`movie_ids` 为最多 1000 个正数影片 ID（去重）；或用 `library_id` 选择单库，省略/0 表示全库。两种范围互斥；显式空数组/null 不代表全库。`fields` 省略默认 `["title"]`，只允许 `title`、`plot`，显式空/null 或未知字段为 400。`limit` 为 0（不限）或 1–100000。
- `GET /api/admin/translate/status` → 当前/最近一次翻译进度；可用 `?task_id=正整数` 限定任务，不匹配最近任务返回 404，非法 ID 返回 400。`POST /api/admin/translate/cancel` → 200 请求取消，可带 `{"task_id":正整数}`；必须匹配当前运行任务，否则 409 且不取消其他任务。空请求体或 `{}` 兼容取消当前任务。两者均要求管理员，响应不缓存。并发翻译或其他 NFO 写任务占用时启动返回 409，不隐式排队。
- 复用既有翻译服务配置，不需要 MetaTube；不重新刮削或下载图片。标题输入为 NFO `originaltitle`，简介输入为当前 `plot`；保留原始标题，尊重字段锁，只局部提交所选字段，提交前检测相关文本及锁是否变化。无有效源/缺 NFO 的影片跳过，服务失败不写回；NFO 已保存但索引失败单独报告，取消不撤销已完成项。
- 应用内计划任务新增 `type: "translate"`，`params` 支持 `library_id`、`fields`、`limit`，不支持持久化 `movie_ids`。定时及立即运行与手动入口共享执行边界；原有 ETag 条件修改与未知参数保留契约不变。任务定义持久化，执行状态仅在进程内，重启不恢复、不补跑。

### 4.3 安全运维与多目标通知（R1/R3/R4/R5）

以下端点均要求管理员会话或管理 API 密钥；错误信封见 §3，凭据入口超限统一 `429 AUTH_RATE_LIMITED` + 正整数 `Retry-After`。

| 端点 | 契约要点 |
|---|---|
| `POST /Users/AuthenticateByName`（及 `/emby` 别名）、`POST /api/auth/initialize`、`POST /api/auth/password` | 共用进程内防爆破 guard：IP/全局令牌桶、bcrypt 槽、IP/账号/组合失败预留；成功仅在凭据提交后清账号与组合历史，不清 IP 历史。凭据请求体上限 8192 字节（超出 `413 AUTH_BODY_TOO_LARGE`）。 |
| `GET /api/admin/security/auth-limits` | 只读快照：`backend:"memory"`、配置上限、计数（`rejected_*`、`overflow` 等）与 IP/账号失败键摘要；不返回密码/token。 |
| `POST /api/admin/security/unlock` | body `{target_type:"account"\|"ip",value,reason,current_password}`；需当前管理员密码再认证。成功 `200 {"unlocked":true}`；再认证失败 `401 AUTH_REVERIFY_FAILED`。 |
| `GET /api/admin/logs/disk?kind=app&limit=200&after=<cursor>` | 独立磁盘 app 日志页数据源：`kind` 仅 `app`；`after`/`before` 互斥；`limit` 1–500；响应含 `items[{id,time,level,message}]`、`next_after`/`next_before`/`has_more`/`reset`/`gap`，`message` 已脱敏；非法游标 `400`、目录不可用 `503 LOGS_UNAVAILABLE`。旧 ring 接口 `GET /api/admin/logs` 保持不变。 |
| `POST /api/admin/apikeys/:key/rotate` | `:key` 为稳定 ID `ak_<32hex>`；必须 `If-Match: "ak-<stable_id>-<version>"`（缺失 428、格式非法 400、过期/并发 412）；body `{current_password,reason}`；成功 200 一次返回新明文 `key`（`Cache-Control: no-store`），旧 key 立即失效；`401`/`403`/`404`/`429`/`503` 分类见实现。 |
| `GET/PUT /api/admin/image-cache/settings`、`POST /api/admin/image-cache/trim` | 缩略图变体目录（`image_cache`）的运行期容量上限：`GET` 返回 `{dir,enabled,max_bytes,used_bytes,files,max_allowed_bytes,config_etag}`（`max_bytes=0` 表示不限制，`used_bytes`/`files` 为变体目录快照，不含临时文件）。`PUT` body `{max_bytes}`（0–64 GiB），必须 `If-Match: "ic1-<max_bytes>"`（缺 428、过期 412 `IMAGE_CACHE_SETTINGS_CHANGED`）；保存立即生效并按新上限清理，配置禁用时返回 `409 IMAGE_CACHE_DISABLED`。`trim` 立即清理过期/超限变体，返回 `{removed,freed_bytes,files,used_bytes,config_etag}`。变体是纯派生数据，清理不影响任何接口语义。 |
| `GET/PUT /api/admin/webhook/targets` | v2 多目标整集合：`GET` 返回 `version:2`、`targets[{id,name,enabled,url_configured,events,format,timeout_seconds,allow_private}]`、`config_etag`、`multi_target`，永不回显 URL。`PUT` 必须 `If-Match`（缺 428、过期 412）；body `{targets:[...],confirm_delete,confirm_empty_targets}`，按目标 ID 合并秘密（空白 URL 保留、`clear_url` 清除），新目标由服务端生成 `wh_<16hex>` ID；最多 10 个目标、整体 ≤64 KiB；清空需显式确认。v2 形成后旧 `PUT /api/admin/webhook/settings` 返回 `409 WEBHOOK_MULTI_TARGET_CONFIG`，旧 `GET` 标 `multi_target:true`。 |

### 4.4 全量与增量扫描

- `POST /api/admin/tasks/scan?library_id=1&mode=incremental` 继续返回 202 与任务 ID；202 仅表示已接受。省略 `mode` 兼容为 `full`，只接受 `full` / `incremental`。省略或 `library_id=0` 表示全部库，非法或负数 ID 返回 400。旧同步扫描入口使用相同模式解析与执行流程；`reindex` 保持强制全量。
- `Idempotency-Key` 在参数合法化后处理：摘要包含操作、库、模式和影响执行的选项。同 key 同摘要返回原任务；同 key 异摘要返回 `409 IDEMPOTENCY_CONFLICT`，包括并发注册时的锁内检查。重放不绕过非法参数检查。
- 手动扫描可显式提供 `allow_empty_removal=true|false|1|0`；其他值返回 400。已有索引的库突然枚举为空时默认阻止清理，只有明确确认才可继续。确认不绕过读取、取消、库配置、根目录或根标识健康检查。
- 可选高级参数 `root_marker_path` / `root_marker_sha256` 必须成对提供：前者为库根下的相对文件路径，后者为预期内容的 64 位十六进制 SHA-256。标识文件只读且不自动创建；必须在解析符号链接后仍位于库根内，内容最多 64 KiB。缺失、越界、不匹配或扫描中变化使清单不完整，禁止清理。未配置标识时，根目录身份和空库检查不能识别所有远程挂载部分掉线。
- 任务失败仍保留已提交组的部分 `result`。结果附加 `mode`、`groups_total/groups_done`、`unchanged_groups`、`added/updated/deleted`、`index_changed`、`incomplete`、`cleanup_performed`、`unreadable_nfo` 等字段；`cleanup_performed=true` 表示完成清理检查，不代表必有删除。后续失败不撤销已提交组；有业务提交仍使缓存失效，无变化扫描不推进内容版本。
- `unreadable_nfo` 统计「NFO 文件存在但解析失败」的条目数（非 `.strm` 读失败）。这类条目**不是**删除指令：已索引的行保留原元数据、演员与可见状态，只跟随文件刷新文件派生列，失败原因写入该影片的 `last_scrape_error`（前缀 `[NFO 不可读]`）；从未索引过的源仍按 pending 入库并带同样原因。NFO 修好后下一次扫描自动恢复并清除该备注。
- 应用内 `scan` 计划支持 `params.mode` 及上述成对根标识参数；旧计划缺省为 full。未知参数与 ETag 条件修改继续保留；计划中的 `allow_empty_removal` 不隐式授权空库删除。前端不创建或启用额外计划；任务记录仍为进程内数据，重启后不恢复运行任务。

### 4.5 MetaTube 手动识别（刮削预览）

`GET /api/admin/items/:id/scrape/preview` 保留原自动识别行为，新增可选查询参数：

| 参数组合 | 行为 |
|---|---|
| 不传 `q` 和 `metatube_id` | 按影片现有元数据自动识别，兼容旧调用 |
| `q=ABP-123` | 按用户输入搜索，不回退到现有影片番号 |
| `q=ABP-123&metatube_id=Provider1` | 限定 MetaTube 数据源搜索 |
| `metatube_id=Provider1:movie-id` | 以数据源网页 ID 直接查询详情，不将 ID 当作番号搜索 |
| `q=https://provider.example/movie/...` | 将完整 URL 交给 MetaTube 解析，支持范围取决于 MetaTube 数据源 |

- 参数值两端空白会被去除；可选空字段应省略。显式空值、重复参数、非法数据源/ID 或非法 URL 返回 `400 INVALID_REQUEST`，不会静默改用自动识别。
- 响应结构保持不变：`query`、`expected_number`、`recommended`、`candidates`；未找到时返回空候选，不视为写入成功。
- 预览和候选详情检查不修改数据库/NFO、不下载图片、不触发翻译；仍须通过原确认接口显式确认后才写入。
- 自动预览/批量刮削优先复用影片已有的 MetaTube 身份；兼容官方 `Provider:escaped-ID[:Position[:Update]]` 与旧版 `metatube:Provider:ID`。有效身份定位失败不会回退其他影片；显式手动输入不受已有身份影响。
- 新刮削身份使用官方 `Provider:escaped-ID` 格式保存到 NFO `uniqueid type="metatube"`。读取兼容顶层 `metatubeid`，非空 uniqueid 优先；已有身份受只补缺失/覆盖及 ProviderIds 字段锁约束。
- Emby 影片 DTO 在保留原 `ProviderIds.metatube` 值的同时，新增官方大小写 `ProviderIds.MetaTube`（有效旧版身份转换为官方格式；已有官方格式保留可选后缀）。
- 后缀语义：`Position` 只影响图片裁切，`Update` 只影响精准查询是否强制刷新上游（`Update=true` 对应 `lazy=false`）；两者都不进入影片身份，也不参与番号匹配。省略或 `false` 保持缓存优先。`query` 与 `ProviderIds.MetaTube` 原样保留后缀文本，不做二次解码或重新格式化。
- 预览候选新增可选字段 `update`（布尔）：只有「精准复用已存标记」的候选可能为 `true`，表示后续 `inspect`/`confirm` 应带回同一选项；省略即缓存优先，调用方不得为普通搜索候选补该字段。
- `POST /api/admin/items/:id/scrape/inspect` 与 `POST /api/admin/items/:id/scrape` 的请求体新增可选 `update`（布尔）与 `position`（数字）：`update` 语义同上；`position` 是主海报裁切位置，取值 `[0,1]`，越界、非数字或非有限值返回 `400 INVALID_REQUEST`，省略则沿用上游默认（由上游按番号决定位置并做人脸检测）。
- 预览候选新增可选 `position`（数字）：同样只有「精准复用已存标记」的候选可能携带，调用方须在 `inspect` 与确认请求里带回同一值，预览主海报与确认后落盘的才是同一张图；普通搜索候选不得补该字段。
- `inspect` 响应里 `poster` 与 `images[].preview` 中的主海报地址会带上 `&pos=`，与确认写入时使用的裁切位置一致。
- 单条确认 `POST /api/admin/items/:id/scrape` 的元数据写入成功后仍返回 `200`、`status="success"` 及既有 `provider/number/title/images/warnings`。图片下载尽力而为，`warnings` 非空表示部分图片未保存；新增可选 `refresh_error` 字符串，仅在 **NFO 已保存但索引刷新失败**时出现，内容为脱敏恢复提示（重新扫描影片或媒体库），不暴露底层 SQL/路径。这些提示不代表 NFO 未写入，调用方不得自动重试确认写入；管理端须持续展示图片警告与索引恢复提示，不能仅用成功 toast 掩盖部分成功。正常结果省略 `refresh_error`，其余失败状态保持原契约。
- 图片代理 `GET /api/admin/scrape/image` 新增可选 `ratio`、`pos`：只对 `kind=primary` 生效，`ratio∈[0.01,100]`、`pos∈[0,1]`；越界、非数字、非有限值，或 `kind` 为 `thumb`/`backdrop` 却携带这两个参数时返回 `400`。两参数进入服务端缓存 key（换位置不会命中旧图），缺省时不下发任何裁切参数、保持旧行为。裁切在上游完成，本服务只转发；`thumb`/`backdrop` 的上游裁切比例固定，故不接受这两个参数。
- 图片代理下载准入（D-4/A）：沿用匿名图片边界，字节缓存命中直接返回；缓存未命中时最多 4 路活动请求，名额满立即返回 `503`，不排队、不自动重试、不设置成功缓存头。该准入同时包围剧照列表解析和图片下载；已获名额的上游失败或取消仍返回 `502`，退出时释放名额。饱和时已取消的缓存未命中请求同样返回 `503`，不保证优先报告取消。单张下载仍为 8MB 上限；4 路不代表进程总内存或所有请求数有界。
- `inspect` 响应新增可选字段 `stills`：`[{"index":0,"preview":"/api/admin/scrape/image?kind=still&provider=..&id=..&index=0&version=<64hex>"}]`。`index` 是服务端对上游剧照列表归一化（去空白、去重、最多 24 条）后的下标；新 `preview` URL 携带有序列表的 SHA-256 `version`，两者共同绑定本次只读候选。调用方应原样使用 `preview`，不能去掉版本再拼接链接；不回显上游图片地址。无剧照时字段省略或为空数组。
- 图片代理新增 `kind=still`：必须携带非负整数 `index`，缺省/负数/非数字返回 `400`；下标越界返回 `404`。可选 `version` 非空时必须为 64 位小写十六进制且仅支持 still，否则 `400`。服务端按 `(provider,id,version)` 保存有界进程内列表快照、按 `(kind,provider,id,index,version)` 隔离图片字节，浏览器 URL 同步隔离；列表快照过期/被淘汰时仅在重新解析出的列表版本相同才重建，不同则 `404`，绝不把旧下标套用到新列表；上游查询失败仍为 `502`。不带版本的历史链接维持原按影片解析及缓存行为，不承诺跨次 inspect 稳定。版本只绑定地址列表，不保证同一远端 URL 的字节永不变化。服务端不接受调用方传任意图片地址，固定 16:9 居中裁切，携带 `ratio`/`pos` 返回 `400`；图片代理仍为只读预览，不写 NFO/数据库/图片文件；另行显式确认写入见 §4.7。

### 4.6 管理端单条详情（只读派生字段）

`GET /api/admin/items/:id/detail` 保持原有字段，新增只读派生字段 `metatube_id`：

```json
{"saved": "metatube:FANZA:abc00123", "canonical": "FANZA:abc00123", "valid": true}
```

- `saved` 是已扫描索引里的 `provider_id` 原值（可能带历史 `metatube:` 前缀）；无值为 `""`。
- `canonical` 是可直接回填搜索的官方格式 `Provider:escaped-ID[:Position[:Update]]`；无法解析时为 `""`。
- `valid=false` 表示已存内容不是合法身份：调用方只能作文本展示并提示无法精准复用，不得回填搜索或据此发起精准查询。
- 派生自索引，不读盘、不写入；外部修改 NFO 后须重读/重扫才会反映。

### 4.7 多背景图磁盘索引（MT-03 分阶段施工）

- 已批准布局为片目录直接子目录 `extrafanart/`，文件名严格为 `fanartN.jpg`（十进制正整数、无前导零）。扫描只收集该目录直接包含的普通文件，按数字序（1、2、10）保存绝对路径；不跟随目录或文件符号链接。原有 poster/fanart/landscape 图片选择保持不变。
- 迁移 17 新增 `movies.extra_backdrops TEXT NOT NULL DEFAULT '[]'`，内容是有序 JSON 路径数组；空集合写 `[]`。索引可由整库/增量扫描及单片重扫/重建恢复，删除全部图片清空数组；这是磁盘派生索引，不是 NFO 影片身份或客户端提交的远端 URL。
- 管理端详情 `movie` 增量增加 `ExtraBackdrops`（当前索引的路径集合，与既有 `PosterPath` 等内部字段同口径）；缺图或损坏 JSON 按空集合展示，坏 JSON 可通过现有数据库诊断定位。路径不是客户端取图 URL。客户端多图访问按下列顺序契约提供。
- 客户端背景序列为非空 `BackdropPath` 在首、随后按 `ExtraBackdrops` 顺序追加；没有主背景路径时额外图从 0 开始。影片 DTO 的 `BackdropImageTags` 按此序列生成，`ImageTags.Backdrop` 为首项；没有图片时仍返回空数组/对象。图片清单 `GET /Items/:id/Images`（原鉴权和对象范围控制不变）按此序列返回 `Backdrop` 条目及连续 `ImageIndex`。
- 匿名 `GET /Items/:id/Images/Backdrop/:index`（以及既有 `Fanart` 别名与路由大小写/`/emby` 兼容）按该序列取图；不带下标等价 0。显式下标非整数、负数或整数溢出返回 400，合法但越界返回 404。序列不按磁盘存在性过滤，缺失文件保持原下标，普通原图请求返回 404，不回退别图；后续扫描重建才更新序列。其它图片类型与库/合集/实体封面仍沿用既有语义，本轮不改变其下标行为。
- 每项复用既有 `serveImage` 的 ETag、公开缓存、304 与缩略图路径；不同文件的变体按真实路径隔离。已有路径 tag 缓存（正缓存 5 分钟、负缓存 30 秒、tag 取 mtime）并不因扫描统一失效，同路径图片替换/删除可能短期沿用旧 tag、304 或变体；不承诺扫描后立即刷新该类字节。未新增列表快照或跨扫描永久下标保证。
- 手工片缺失 NFO、已索引片 NFO 不可读时，刷新上述磁盘索引必须保留已有元数据、演员与用户数据。有效 NFO 仍按原规则刷新元数据并保留 manual 分类，不能误解为手工片永不接受 NFO 更新。
- 备份阶段新增 `backup.Options.ExtraBackdrops`（零值 false，保持已有调用行为），独立于 `NFORoot`；命令行 `embybackup --extra-backdrops` 默认 true，可显式关闭。开启时按库遍历物理文件，只复制上述规范 `extrafanart/fanartN.jpg` 普通文件到产物 `extra-backdrops/lib-<ID>/<库内相对路径>`，不依赖数据库索引、也不跟随子目录或文件符号链接；结果 `ExtraBackdropCount` 单独计数。离线/已移除库跳过，在线库遍历/复制失败不发布半份备份，沿用输出边界校验、暂存发布、锁与保留规则。旧数据库不要求有 `extra_backdrops` 列，但开启文件备份要求有库路径表。
- 恢复仍为离线手工操作：将 `extra-backdrops/lib-<ID>/` 内容按原相对路径放回对应媒体库，数据库快照保留有序索引；迁移根目录后须重新扫描恢复绝对路径。不包含 STRM、原三张图片或任意其它 JPG；数据库与文件不是跨介质事务快照，并发修改/离线库可能导致不一致，不能仅恢复数据库就声称恢复图片。未增加自动恢复、覆盖冲突处理或文件级历史版本。
- 确认写图增量输入：`POST /api/admin/items/:id/scrape` 可带 `stills: {"version":"<64位小写hex>","indices":[2,0]}`，版本从本次 inspect 的剧照 preview URL 获取；下标 1–24 个、互异、每项在 `[0,23]` 内。不提供/为 null 保持原确认行为，不写额外图；空数组、非法版本/下标返回 400，不能用空选择删除旧图。客户端不提供图片 URL。
- 服务端在既有 NFO 写入门内重新取详情，按相同归一化列表核对摘要；版本不符返回 409，超出当前列表下标返回 400，上游查询失败 502。这些前置失败不下载任何图片、不写 NFO/影片/文件，也不更改 scrape_error。Update 仍决定重新查询时的 lazy，并非保证远端源站最新；版本绑定 URL 列表，不绑定远端字节。
- 通过后按提交下标顺序对应 `extrafanart/fanart1.jpg…`，经 MetaTube backdrop 16:9 居中路径下载并编码为真实 JPEG，沿用既有解码限制；不写入 Position/角标。只在 DownloadImages 开启时保存，关闭时仍校验选择版本但不下载额外图，沿用元数据成功语义。overwrite=false 跳过已有非空目标，true 可替换；不会删除未涉及的旧尾图，不承诺最终集合恰等于本次选择。
- 各图使用同目录唯一临时文件与既有目标提交锁，成功编码才 rename；单张下载/编码/提交失败保留该目标旧文件，继续其它图与 NFO，沿用 200+warnings/refresh_error。images 计入本次真正写入的额外图，重扫重建后客户端按磁盘数字序列举；不会自动重试确认。取消沿用既有不回滚已提交文件的边界，非跨图片/NFO/数据库原子事务。浏览器验收与实际上游/客户端验收单列，不能把 API 写入等同于全部端到端验收。
- 管理端剧照选择默认全不选；原生复选框按勾选先后组成上述 indices，取消后顺序压紧、重选排末，并标出目标 fanartN.jpg；清空只清界面选择，不删除旧图。不选时确认请求省略 stills；版本缺失/非法的旧预览只读展示，可重新读取详情获取当前版本。新搜索或成功读取候选/详情后清空选择，读取失败保留上一份可见候选及选项并显示失败提示，不静默换片。
- 确认在途冻结本次搜索、候选、覆盖与剧照选择，阻止重复提交；关闭仍可用但不代表取消服务端写入，迟到回包不污染新会话。409 尚无专用 code 区分版本冲突与 NFO 门忙，界面只提示未完成并提供重新读取详情、重新选择后显式确认，不猜错误原因、不自动重试。200 的图片 warnings/索引 refresh_error 继续使用持久的已保存结果面板，移除写入入口，仅“完成”关闭。

### 4.8 自动字幕/版本标签归属（MT-04，D-2/A）

- 迁移 18 新增 `movies.auto_marks TEXT NOT NULL DEFAULT '[]'`，记录本服务上次实际拥有的生成标签。空集合保存为 `[]`；现有行不根据 NFO 标签追认历史归属。管理端原始 `movie` 对象增量包含只读 `auto_marks`（Go字段名 `AutoMarks`），不是允许调用方提交归属的请求字段。
- 普通扫描、单片重扫、索引重建与普通影片 upsert 保留已有归属，不从字幕文件或 NFO 的同名文字推断归属；新建数据库重扫只能恢复 NFO 元数据，不能恢复此记录。数据库备份恢复可以保留记录，单独 NFO 快照不能。
- 自动刮削在既有 NFO 路径锁内合并标签：覆盖模式仅撤销上次记录的生成标签，保留其它原标签，再合并当前上游标签及生成标签。当前生成文字若已是未归属旧标签或本次上游标签，不将其认领为程序标签；已记录的同名文字按归属记录处理，无法识别外部工具是否后来赋予相同文字人工含义。
- 补缺失模式已有标签时、Tags 字段锁或全局锁生效时，标签保持不变，不扩张归属；只保留原归属与当前实际标签的交集。手工 NFO 编辑仍走原编辑契约，不自动认领或猜测生成标签。文件名显式 `-C/-U/-UC` 仍优先于同主名外挂中文字幕推断，不新增字幕内容探测。
- 独立影片刮削（批量任务或单条确认）之间同样独占既有 NFO 通道；已占用时单条确认返回 409，不排队、不自动重试。单条确认取得通道后重新读取影片归属快照，持有到 NFO、归属记录与索引刷新结束；保留探测等既有同 kind 引用计数，不把此变更扩展为所有任务的新并发政策。
- NFO 提交成功后、索引刷新前记录新的归属；NFO 写入失败不更新归属。两者不是联合事务：若 NFO 已保存而归属记录失败，且未触发后述取消错误，单条确认仍为 `200/status="success"`，新增可选脱敏 `auto_marks_error`，与图片 `warnings`、索引 `refresh_error` 独立出现。提示必须说明已保存及需人工核对，不能用自动重试或“仅重扫即可恢复归属”掩盖不一致；正常成功省略该字段，不暴露 SQL 或本地路径。
- 管理端对上述任一部分成功保留持久的已保存结果面板，仅“完成”关闭，不自动再次确认写入。归属失败单独展示并转义动态文案，不合并成图片失败或索引失败；关闭界面不撤销已经提交的 NFO/图片/数据库变更。
- 取消边界维持原行为：NFO 提交后仍先尝试记录归属，再检查取消；已取消时单条确认走 502、批量该条记失败，不再更新成功状态或触发后续重扫。若归属持久化失败与取消同时发生，取消错误会优先于上述成功响应/批量人工核对提示，归属失败仍记录服务日志；不能把错误响应当作零写入或自动重新刮削的依据。底层 Apply/RunMovies 不自行提供跨文件和数据库互斥，独占保证限于上述服务端生产入口。

### 4.8.1 独立本地字幕标记整理入口（MT-04）

- 已批准首批：管理员显式选择单个十进制DB影片ID；服务端解析现有主片STRM/登记NFO/库，不接收路径、标签、归属、provider或其它影片ID。没有MetaTube配置也可使用；不联网查资料、探测媒体、下载图片、重命名文件、全库清理或新建缺失NFO，不更新人工分类或最近刮削状态。只检查主片，不汇总AdditionalParts字幕。
- `POST /api/admin/items/:id/subtitle-marks/preview`：单一JSON对象，允许 `overwrite` 布尔值（省略为false）。`POST /api/admin/items/:id/subtitle-marks`：同模式及必填 `version`（预览返回的64位SHA-256 hex）。请求上限4096字节，拒绝null/类型错误/额外字段/多个对象；无参数绑定到其它旧入口。确认不接受客户端提交预览标签。
- 预览200：`movie_id,title,library_name,main_file,version,overwrite,evidence:{code,tag,source,matches},before,after,previous_owned,owned,protected,changed,image_note`；数组为空时返回 `[]`，不返回null。`source` 为filename/subtitle/none，matches只给匹配字幕basename，显式C/U/UC优先。before/after为去除首尾空白的逻辑标签，previous_owned为当前实际归属，owned为拟提交归属；changed包括标签字节或归属变更。protected表示标签受字段锁/全局锁/补缺失已有标签保护；始终展示“未重下载海报，旧图角标可能未同步”。预览只读，不保存票据或改变索引/版本。
- version绑定影片/库/主片/分段路径、播放地址、登记NFO原文摘要、实际归属、字幕证据及模式。确认复用关停登记和管理员代际准入，在既有独占subtitle_marks通道内重读影片/归属并重新预览，变化409要求重新预览且显式确认，不排队/自动重试。最终NFO原文摘要比较、锁判断、标签合并和写入在同一既有路径锁内；目录与文件不能成为跨文件系统事务，不承诺外部工具绝无观察后变化。
- 认证/管理员策略沿用现有路由组；非法请求400，影片不存在404，比较冲突/通道占用409，主片/NFO/字幕目录不可判定或NFO写入失败422，关停/索引库不可用503。确认写入前取消503且不写；缺失/损坏NFO或目录不可读不是可撤销标签的依据。没有显式后缀时目录枚举错误失败，不修改旧附加刮削启发式的错误语义。
- 已完成NFO提交（包括确认原文无需重写）后先尝试持久化实际归属，再检查取消，最后用既有非破坏性单片执行器刷新索引；不调用人工编辑后处理、不清理其它缺失片。200返回 `status:success|partial_success|unchanged,saved,indexed,ownership_saved,warnings`。saved表示当前NFO已确认保存，不保证本次一定改写；unchanged不改写NFO或更新数据库版本，已有标签索引不一致时仍刷新。indexed及无变化快捷判断仅核对本批标签（按同一首尾空白规范化语义），不是NFO全部元数据同步证明；仅外部标题等变化仍须显式重扫，不用此入口代替通用元数据同步。
- 归属失败为200/partial_success、ownership_saved=false及独立脱敏auto_marks_error，须人工核对，不声称重扫恢复归属；索引失败为indexed=false及refresh_error，提示显式重扫。NFO已提交后取消仍200/partial_success，canceled=true与refresh_error表明后续索引未执行，保留已提交文件/归属，不回滚、不重复确认；不同于旧刮削取消502，此项不修改旧契约。返回文案不包含底层SQL/本地路径。
- 界面覆盖模式改变必须重新预览；确认在途冻结重复入口。已保存结果独立展示归属/索引问题，仅完成关闭；409清除旧预览而非盲重试。确认网络错误不能解释为未写入，显示结果未知并清除旧预览，先人工核对；关闭界面不取消或回滚。所有动态文案转义。此为仓库实现契约，不等于真实媒体操作或M1正式发布授权。

### 4.9 NFO / STRM 文件读取边界（N-01 子项）

- 按 D-5 推荐值采用二进制字节单位：NFO 全文最多 `8 << 20`（8MiB），STRM 全文最多 `1 << 20`（1MiB），精确等于上限可读；不是只限制 STRM 首个 URL 行。实际读取最多探测上限加 1 字节，不依赖预先 Stat，超限不将截断内容送入 XML/URL 解析或摘要。此为单次输入读取边界，不是总内存、总文件数或耗时保证。
- 扫描超限按 WARN + 跳过处理，不中断其它健康分组；整库 full/incremental 保留该组原影片、演员、用户数据、归属与旧扫描基线，不提交新基线、不运行本库缺失清理。结果 `skipped` 增加跳过组的 STRM 成员数（不把 NFO 另算一条），`incomplete=true`、`cleanup_performed=false`，单纯超限不增加 `failed`、不返回整库读取失败；真实 I/O 错误、取消、库身份变化仍沿用既有错误行为。下一次显式扫描在文件恢复合规后正常更新并清理，无自动重试。
- RescanOne/ReindexOne 对超限同样不覆盖原记录、不将该项列入已刷新源；有 CD1 的栈必须包含所有从属 STRM 以及所读共享 NFO 的边界检查。没有 CD1 时原有独立条目仍逐项处理；不将已完成的其它条目当作回滚。扫描为跳过，不代表文件已成功索引或已通过发布验收。
- NFO 修改、演员头像更新、翻译读/提交、预览以及编辑恢复摘要均通过同一有界原文读取入口。超限时该 NFO 不写入、不留下临时文件，保留原文件锁与原子写政策；不另外扩大纯写入 API 的输出限制。已读取字节的内部 XML 解析不重复校验文件大小。各 HTTP 调用沿用原错误映射，未新增状态码；拒绝某个 NFO 不保证其它先前提交的图片/文件没有写入，既有部分提交与取消边界保持。
- NFO/STRM 文件入口只交付普通文件句柄：预先 Stat 跟随文件链接并拒绝稳定的 FIFO/设备/目录，实际打开后再检查句柄类型；Unix 以非阻塞方式打开，避免 Stat 后路径被换成无 writer FIFO 时等待。非 Unix 保留静态与句柄检查，但不承诺同等的替换竞态防阻塞。非普通对象通过明确类型错误返回，不将流内容送入 XML/URL 解析；扫描按上述超限相同的跳过/保留/不清理政策处理，原有真实 I/O 错误政策不变。
- 保留普通目标文件 symlink（含库外）读取兼容；NFO 改写仍在原路径 rename，替换链接而非写穿目标。不新增根目录沙箱、链接解析、inode 锁或全局文件类型政策。该边界不是一致性快照，也不是恶意目录替换、设备 driver 副作用或挂载/NFS/FUSE I/O 的硬超时保证；根标记、图片、备份与其它读取入口未在本次一并改写。
- 流式文件备份不是 NFO 解析入口，仍沿用备份规则；本子项不限制媒体信息 JSON、配置 YAML 或任意其它格式。ffprobe 独立执行边界见 §4.10，不能与文件读取的剩余限制互相替代。

### 4.10 ffprobe 执行边界（N-01 本地已验收子项）

- `probe.Run` 每次调用的执行预算最多 60 秒，名额等待也计入；调用者更短的正预算保留，非正值采用 60 秒。父 context 的更短期限/取消优先。`Verify` 仍用最多 15 秒预算（调用者可更短），与 Run 共享**单进程内**两个子进程名额；等待可取消，不新增 HTTP 503 或自动重试。不是跨进程/跨实例限额，也不改变 NFO 门闩政策。
- stdout 最多 `4 << 20`（4MiB）、stderr 最多 `64 << 10`（64KiB），精确等于可读；任一超限使该次调用失败，不解析截断结果、不返回成功的 Raw。超限、启动失败、退出错误、取消等退出路径都释放进程名额，超限输出不会写入 NFO/mediainfo 缓存。
- Unix 为新进程组，取消、超时或输出超限终止同组子进程，直接子进程正常退出后也清理仍留在同组的后代；非 Unix 只保证直接子进程终止。继承管道有最多额外 1 秒的 WaitDelay 收尾预算，不把该收尾伪称已包含于 60/15 秒，也不保证 OS 不可中断 I/O、主动脱离进程组的后代或远端服务硬实时停止。
- YAML 保留历史兼容语义（不因旧配置新增启动拒绝）：`probe_timeout_seconds <= 0` 默认 60，正值上限 60；`probe_concurrency <= 0` 默认 2，正值上限 2，1 仍为单 worker。共享子进程名额覆盖批量与单条/校验，不只约束批量 worker 数。更高旧值仍加载、按有效上限执行。
- HTTP/批量保留现有失败与取消映射：单条所有目标失败仍 502，有其它成功目标仍 200 并列 failures；批量失败仍用 `FFPROBE_FAILED` 计数/诊断，取消不假称成功。单次执行失败不写本目标的输出，但不撤销其它已成功目标；不承诺整个请求零写入。本阶段的文件媒体 JSON 读取、总内存、输入流量、升级/回退矩阵不在此范围。实现、验证证据及运行/交叉编译覆盖限制见台账 §39。
- 单条探测的执行错误在进入全失败响应或部分成功 `failures` 前复用既有日志脱敏，去除源URL的userinfo和query凭据，保留可用错误诊断。不把错误脱敏扩展为对合法播放地址的改写：缺失ffprobe或媒体探测失败不阻断既有浏览和302播放，302仍携带客户端取源所需的原始URL。默认信任STRM、ffprobe直接访问URL，不构成出站隔离；真实执行及普通门禁回归见[台账§63](<PLAN-STATUS.md#63-n-01-可信strm探测错误脱敏与播放契约2026-11-0506>)。

### 4.11 平铺部署与 Web 自升级（U-01）

四个管理端点，落在既有 `requireAuth + requireAdmin + auditAdminAction` 组内；页面打开时的检查与升级进度轮询都是普通管理请求，不单独发凭据：

- `GET /api/admin/update`：返回最近一次检查结果，进程内缓存 10 分钟；页面打开自动检查走这里。
- `POST /api/admin/update/check`：强制重新查询，绕过缓存（仍受上游超时约束）。
- `POST /api/admin/update/apply`：body `{"version":"vX.Y.Z-abcdef0"}`。服务端强制重新检查，`version` 必须等于当前 Release 标签，否则 409；客户端不能指定下载源或安装路径。成功返回 202 `{"version":...}`，后台下载、校验并原子替换 `/opt/emby-go/emby-go`，随后发出正常关闭信号。
- `GET /api/admin/update/status`：`{"phase":"idle|downloading|verifying|installing|restarting|failed","target":"...","message":"...","updated_at":"...","downloaded_bytes":N,"total_bytes":N}`。`failed` 是终态直到下一次 apply；`message` 为失败原因。`downloaded_bytes`/`total_bytes` 只在 `downloading` 阶段出现，`total_bytes` 来自 Release 资产大小；页面据此显示进度条，缺失时不推算百分比。

检查结果字段 `{"current","latest","available","notes","published_at","checked_at"}`。检查失败（网络、上游 JSON、无匹配资产）返回 502 `{"error":"检查更新失败：..."}`，不会伪装成「已是最新」。Release 必须同时提供 `emby-go-<goos>-<goarch>` 与 `SHA256SUMS`，缺失时检查即失败并在错误里列出缺少的资产，不降级为无校验安装。`available` 只比较 `vX.Y.Z` 三元组，构建后缀 `-commit` 不参与比较：同一 Release 不会重复提示，而无法解析成本地版本（如 `dev`）会按「落后于 Release」提示更新。

版本核对：进程执行新程序的 `-version` 并与目标标签比较；页面在服务恢复后读取公开 `GET /api/version`，仅当实际版本等于目标或等于「目标 + `-commit` 后缀」才显示成功。没有回滚端点或备份目录：替换前失败时旧程序仍在运行，替换后启动失败由运维按部署文档处置。升级复用既有有界关闭与 `Restart=always`，不新增独立守护进程、sudo 代理、通用任务队列或自动回退。

部署可选设置环境变量 `EMBY_GO_RELEASE_MIRROR`（例如 `https://mirror.example.com/github.com`），把资产下载改写到镜像；它只替换下载地址，资产名、`SHA256SUMS` 校验、`-version` 核对与原子替换都不变。镜像必须是 `https://`，否则检查阶段返回 502 并在错误中说明镜像地址无效；未设置时直接使用 GitHub Release 资产地址。

### 4.12 扫描任务中心（N-02）

任务摘要持久化在 `task_records`，迁移 19。不是新队列：提交仍复用既有异步扫描执行器，首版不可取消、进程重启不恢复、不补跑；重启时未完成任务只标记中断。

- `POST /api/admin/tasks/scan`：语义不变（202 仅表示已接受）。202 之前先写入摘要行，写入失败直接返回错误，不会出现「已受理但查不到任务」。
- `GET /api/admin/tasks`：`{"items":[{"id","type","status","started_at","ended_at?","error?","result?"}],"running":bool,"history_error":bool}`。items 是进程内实时任务与最近 100 条持久摘要按 `id` 降序合并，同一 id 以内存状态为准；`result` 是执行结果摘要（扫描/探测/备份各自的 JSON 对象），`result_json` 永不暴露；`history_error=true` 表示摘要读取失败但实时列表仍返回。
- `GET /api/admin/tasks/{id}`：内存命中返回异步扫描 DTO（`task_id` 等既有字段）；内存已丢失时回落摘要，返回同一 DTO 形状的只读视图（含 `library_id`、`created_at`、`finished_at`、`error`，有结果时含 `result`）。都不存在返回 404。
- 留存：摘要最多 500 条，超出按 id 淘汰最旧；启动时 `queued`/`running` 标为 `interrupted`（`error` 说明进程重启），任务 id 从摘要最大 id 继续递增，避免重启后 id 重叠。
- 幂等：`Idempotency-Key` 的窗口与重放行为不变；重放仍返回原任务 id，不新建执行。

### 4.13 诊断证据（N-03）

- `GET /api/admin/diagnostics`：只读聚合，不触发 ffprobe、不访问外部服务、不修改任何状态。字段：
  - `app`：`version`/`commit`/`build_time`/`started_at`/`uptime_seconds`/`go_version`/`os`/`arch`。
  - `store`：`path`/`size_bytes`/`schema_version`/`movies{success,pending,manual}`/`libraries`/`task_records`/`scheduled_tasks{total,enabled}`。
  - `runtime`：`scan_running`，`probe{running,total,done,failed,started_at,finished_at,error}`，`scrape{running,kind,total,done,failed,started_at}`。
  - `scheduler`：数组，条目为 `id`/`name`/`type`/`cron`/`enabled`/`last_run_at`/`last_status`/`last_message`，启用任务附 `next_run`（服务器本地时区计算后的 RFC3339）。
  - `webhook{targets,enabled_targets,multi_target}`、`image_cache{enabled,dir,files,bytes,max_bytes}`、`ffprobe{path,error}`。
- 读取数据库结构版本/文件信息失败时返回 500，不返回部分推测值；单个运行中任务的状态是读取瞬间快照，不做一致性等待。
- 诊断页只展示证据与跳转到既有动作入口，不提供「一键修复」、远程探测或自动变更范围。

### 4.14 Webhook 投递历史（N-04）

- 记录点：业务事件在 `send` 返回后按目标逐行写入；管理员测试消息（任意 draft 地址）不记录。写入 best-effort，失败只记日志，不回滚业务结果、不触发重发。
- 字段白名单：`event`/`item_id`/`target_id`/`status`/`error_code`/`created_at`。不存接收地址、请求体、响应体或响应头。
- `status`：`sent`（接收端已确认）/`failed`/`skipped_budget`；`error_code` 为稳定分类。
- `GET /api/admin/webhook/deliveries?limit=`：`limit` 为 1–500 的整数，默认 100；返回 `{"items":[{"id","event","item_id","target_id","status","error_code","created_at"}],"retention":{"max_rows":1000,"days":7}}`，按 `id` 倒序。
- `DELETE /api/admin/webhook/deliveries`：清空历史，返回 `{"cleared":n}`；只删除历史记录，不重发、不回滚业务、不影响目标配置。
- 留存：最多 1000 条且 7 天，任一超限即淘汰；不建 outbox、不自动重试。非管理员访问返回 403。

### 4.15 固定范围备份（N-05）

- `GET /api/admin/backup`：`{"dir","retention","running","last"}`。`dir` 固定为数据库同级的 `backups/`；`retention` 恒为 7；`last` 为最近一条 `type=backup` 的任务摘要（`task_id`/`status`/`started_at`/`finished_at`/`error`/`result`），没有记录时为 `null`。只读，不访问媒体盘。
- `POST /api/admin/backup/run`：受理返回 202 `{"task_id"}`，后台执行一次固定范围备份。已有备份在进行时返回 409；服务关停登记期返回 503。
- 固定范围：SQLite 一致性快照（`VACUUM INTO`）+ 媒体库 NFO + 规范 extrafanart 目录，保留 7 份；不提供网页恢复、任意路径、匿名下载或删除指定备份。
- 结果元数据：`result{dir,nfo_count,extra_backdrop_count,pruned,finished_at}`，`dir` 为该次备份的时间戳子目录。快照含凭据材料（密码哈希/令牌/API Key）与媒体信息，因此目录由进程以 `0700` 建立、不回显内容、不经 Web 提供下载；取回请用服务器文件工具。
- 落库复用 `task_records`（`type=backup`），失败原因写入任务摘要；单飞：进程内 atomic 标记 + `internal/backup` 输出目录锁（跨进程）。任务不自动重试，失败后由管理员重新发起。非管理员访问返回 403。

### 4.16 Web 管理界面仅管理员登录

- `/admin` 管理台与 `/` 登录页只接受 `Policy.IsAdministrator=true` 的账户；密码正确的普通用户不会获得管理台入口，登录页提示“该账户不是管理员，Web 管理界面仅对管理员开放”，且不写入 `emby_token`。
- 已有普通用户 token 直接访问 `/admin` 时，前端清除本机凭据并回到登录页；后端管理端点继续由 `requireAdmin` 返回 403，不依赖前端拦截。
- `POST /Users/AuthenticateByName` 的响应结构不变（仍返回 Emby 兼容的 `User.Policy`），普通用户 token 仍可用于 Emby 客户端播放；本限制只作用于 Web 管理界面。
- 登录页与管理台的判定都取同一份 `/Users/Me` 策略，不新增端点、不新增字段。

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

## 相似作品排序（`GET /Items/{id}/Similar`）

候选先由 `movie_features` 倒排召回（跳过全库 >10% 的高频特征），再按可解释层级排序，数字越小越相关：

| 层级 | 条件 |
|---|---|
| 0 | 同系列：NFO `<set>` 优先，退回 `<series>`（特征标记或字段比对） |
| 1 | 同番号前缀，≥3 个字母（如 `ZOZO-226` / `ZOZO-225`） |
| 2 | 共同演员 ≥ 2 |
| 3 | 共同演员 1，且厂牌/片商/工作室相同 |
| 4 | 共同演员 1 |
| 5 | 仅类型/标签/厂商/导演特征重合，特征分 ≥ 10 |

同层内依次按：共同演员数降序 → 特征分降序 → 年份差升序 → 番号前缀 → 标题。
全库恒定的官方分级不参与打分；年份、番号前缀只作同层细分。结果只含当前账户有权限的媒体库。
