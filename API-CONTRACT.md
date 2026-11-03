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
