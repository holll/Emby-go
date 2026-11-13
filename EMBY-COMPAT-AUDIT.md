# Emby 兼容接口审查报告

> 审查基准：`/root/src/EmbyRestApiDocs`（官方 RestAPI 快照，70 services / 548 endpoints / 329 models，`meta.source = https://dev.emby.media/reference/RestAPI.html`）
> 被审对象：本仓库 `internal/server/server.go` 的 `embyRoutes()` + `registerStreamRoutes()`
> 日期：2026-11-13 ｜ 结论全部经代码读取 + httptest 实测双向核对

## 0. 结论摘要

| 维度 | 结果 |
|---|---|
| 路由覆盖率 | 我方 63 条路由，文档 548 个端点中 **53 个被覆盖（9.7%）** |
| 我方路由无文档对应 | 9 条（多为 Emby 4.x 已移除或文档漏收，非缺陷） |
| 越权差异 | **0 处**（50 个可比对的命中端点，文档全部标 `user`，我方均为「任意已认证用户」） |
| 响应结构差异 | **8 处**确认（见 §4） |
| 状态码差异 | **5 个端点**：我方 204 / 文档 200 |
| 真实客户端高风险缺口 | `GET /Sessions`、`/Auth/Keys`、`PlayingItems` 旧族、`/Shows/{Id}/Seasons\|Episodes`、字幕 `Stream.{Format}` 族、**全部 HEAD 变体** |
| 参数覆盖度 | 图片端点 5/16 参数；`/Items` 约 25/99 参数 |

**总体判断**：现有实现是围绕「电影 + 合集」的**窄面深度兼容**，不是全量 Emby 兼容。已实现的端点形状基本对，主要风险集中在 ① 全部 `HEAD` 请求返回 404；② 会话/用户管理类端点整族缺失；③ 若干 DTO 缺字段（客户端一般不据此崩溃，但会降级或显示为空）。

### 0.1 修复进展（2026-11-13，按用户指令「严格按文档修改」）

§4 的 8 处 A 类差异已全部按文档修完，§6 的 P0/P1 未动（属功能新增，不在本次授权范围）。

| 项 | 状态 | 落点 |
|---|---|---|
| 4.1 图片与 `/System/Ping` 的 HEAD | ✅ 已修 | `registerEmby` 内按 `embyNeedsHead` 派生 HEAD |
| 4.2 `AuthenticationResult.SessionInfo` | ✅ 已修 | `auth.go` `sessionInfoFor` |
| 4.3 `PublicSystemInfo` 七字段 | ✅ 已修 | `system.go` `publicInfo` |
| 4.4 `PlaybackInfoResponse.ErrorCode` | ✅ 已修 | `media.go` `playback` |
| 4.5 `UserItemDataDto` 三项字段 | ✅ 已修（`Likes` 与 `Rating` 并存） | `userdata.go` `userDataDTO`/`userDataJSON` |
| 4.6 `SystemInfo` 补字段 | ✅ 已修（无实现的用零值） | `system.go` `info` |
| 4.7 204 → 200 | ✅ 已按文档改（仅 Emby 端点；自有 `/api/auth/logout` 仍 204） | `system.go` `noContent`、`auth.go` `embyLogout`、`media.go` `playing` |
| 4.8 图片 `?tag=` | ⚠️ 部分修（改返回强缓存头，不做 304） | `media.go` `serveImage` |

附带对齐：图片端点缺省画质 82 → 90（文档 ImageService 写「默认 90」），预热档位同步改 90。

**仍然保留的差异**（有意，不是遗漏）：`?tag=` 不返回 304（强缓存不等于 304，首个请求必须拿到字节）；`UserData.Likes` 保留（自有前端读它）；`POST /Users/{uid}/Items/{id}/Rating` 继续用 `?Likes=`（文档 UserLibraryService #18 就是 `Likes` 必填，不是 4.x 的 `{"Rating":n}`——这一点审计报告 §4.5 的推测有误，以文档为准已更正）。

### 0.2 缺口补齐（2026-11-13，第二批）

§6 的 P0/P1 中「能复用既有能力」的部分已补齐，共 **30 条新端点**，路由总数 63 → 93（`embyRoutes()` 89 条 + 4 条流路由）。实测命中文档端点数从 53 升到 **79/548（14.4%）**。

| §6 缺口 | 状态 | 落点 |
|---|---|---|
| P0 `GET /Sessions` | ✅ 已补 | `emby_extra.go` `sessions` + `store.ActivePlaybackSessions` |
| P0 `GET\|POST\|DELETE /Auth/Keys` | ✅ 已补（含 `/Auth/Keys/{Key}/Delete` 兼容写法，管理员 403 校验） | `emby_extra.go` `apiKeys`/`createAPIKey`/`deleteAPIKey`/`embyAdminOnly` |
| P0 `POST /Users/{uid}/Items/{id}/UserData` | ✅ 已补（GET 一并补上） | `userdata.go` `itemUserData` |
| P0 `PlayingItems` 旧族 4 条 | ✅ 已补（与 `/Sessions/Playing*` 共用 `applyPlaybackEvent`） | `media.go` `playingItems` |
| P1 `/Shows/{Id}/Seasons\|Episodes\|Upcoming` | ✅ 已补（空集合） | `emby_extra.go` + `embyRoutes` |
| P1 `/Items/{Id}/ThemeMedia\|ThemeSongs\|ThemeVideos` | ✅ 已补（空集合，三段/单段形状按文档） | `emby_extra.go` `emptyThemeMediaAll`/`emptyThemeMedia` |
| P1 `/Users/{uid}/Items/{id}/Intros\|LocalTrailers\|SpecialFeatures` | ✅ 已补（空集合） | `embyRoutes` |
| P1 `/Users/{uid}/Items/Root`、`/Library/MediaFolders` | ✅ 已补 | `library.go` `rootItem`/`mediaFolders` |
| P1 `/Genres/{Name}`、`/Tags`、`/Studios`、`/Persons` | ✅ 已补（复用 `EntityNamesVisible`） | `library.go` `genreByName`/`tagList`/`studioList`/`personList` |
| P1 `/Years`、`/OfficialRatings` | ✅ 已补（新标量列聚合 `EntityScalarNamesVisible`） | `emby_extra.go` `scalarEntityList` |
| 额外 `/Items/{Id}/Ancestors`、`/Movies/{Id}/Similar` | ✅ 已补 | `library.go` `ancestors`；`similar` 复用 |

**仍然不做的**（本次确认，不是遗漏）：

| 缺口 | 原因 |
|---|---|
| 字幕 `GET /Videos/{Id}/{MediaSourceId}/Subtitles/{Index}/Stream.{Format}` 全族 | `internal/probe` 的 `SubtitleInfo` 只有 `Embedded bool`，**不保存外挂字幕文件路径**，物理上无法对外提供。要做必须先改探测层。 |
| `/Users/{Id}/Images/{Type}` 用户头像族 | 无用户头像存储（`internal/avatar` 只服务演员头像）。 |
| 图片**长形式** `GET /Items/{Id}/Images/{Type}/{Index}/{Tag}/{Format}/{MaxWidth}/{MaxHeight}/{PercentPlayed}/{UnPlayedCount}` | 短形式 + query 参数已能覆盖客户端实际用法；长形式需改路由表结构。 |
| Live TV（75）、DLNA（22）、音乐、插件、Sync、播放列表 | 不在本项目「电影 + 合集」范围内。 |
| Emby 管理面（Library maintenance 27 等） | 管理面走自有 `/api/admin/*`，不重复实现。 |

回归测试：`internal/server/emby_doc_endpoints_test.go`（7 个用例，逐条断言文档形状与状态码）。

---

## 1. 审查方法与证据等级

三层证据，报告中逐条标注来源：

| 标记 | 含义 |
|---|---|
| `[文档]` | 来自 `/root/src/EmbyRestApiDocs` 的 `index.json` 或对应 md 表格 |
| `[代码]` | 读本仓库源码确认（给出 `file:line`） |
| `[实测]` | 用 `httptest` 起真实 `newApp`，登录后实打请求读响应 |

**重要前提**：参考文档自我声明「不代表已逐接口实机验证」，且 `meta.conventions.required` 明写「未知/unspecified 不等于可选」。因此本报告**严格区分三类结论**：
- **A 类｜实现错误**：我方能调到该端点，但形状/状态码与文档不符 → 可修
- **B 类｜实现缺失**：文档有、我方 404 → 是否补齐是产品决策
- **C 类｜文档自身问题**：文档内部不一致或漏收 → 不构成我方缺陷

---

## 2. 路由覆盖率

方法：把我方路由的 `:id` 占位符与文档的 `{Id}` 同时归一为通配段，逐段大小写不敏感比对（文档路径大小写敏感但 Emby 客户端实际两种都用，我方 `registerEmby` 也同时注册 PascalCase 与小写两份）。

| 分类 | 覆盖 / 文档 | 说明 |
|---|---|---|
| Library browsing | 19 / 121 | 影片列表、详情、相似、合集、Genres、NextUp |
| Playback and synchronization | 11 / 70 | `/Sessions/Playing*`、PlaybackInfo、PlayedItems、Favorite、Rating |
| System and administration | 6 / 78 | Info/Public/Ping/Configuration/Endpoint |
| Users and authentication | 6 / 45 | 登录、`/Users/Public`、`/Users/{Id}`、`/Users/Me` |
| Streaming protocols | 6 / 34 | `/Videos/{Id}/stream` 及其 `.ext`、`index.bif` |
| Images and subtitles | 5 / 76 | 仅条目图片短形式；字幕与 HEAD/上传全缺 |
| Library maintenance | 0 / 27 | 媒体库增删改、Refresh、扫描上报全缺 |
| Live TV | 0 / 75 | 定位外（本项目无直播电视） |
| DLNA | 0 / 22 | 定位外 |

### 2.1 我方有、文档无同路径的 9 条（C 类，非缺陷）

| 路由 | 判断 |
|---|---|
| `GET /Users/Me` | `[实测]` 200 正常返回 UserDto。文档无 `/Users/Me`，但 `[文档]00-基础/05-常用接口速查表.md` 与本项目其它客户端均需要「当前用户」，属文档漏收 |
| `GET /Items/{id}` | `[文档]` 端点表里只有 `DELETE /Items/{Id}`，但 `[文档]05-常用接口速查表.md §3` 明确列了 `GET /Items/{Id}` = 条目详情 → **文档自身不一致**，我方实现正确 |
| `GET /Items/{id}/Images/Thumbnail`、`.../Thumbnail/:index` | 文档用 `{Type}` 占位符，我方把 `Thumbnail` 当具体 Type 注册（sprite 预览专用）。语义等价，路径匹配脚本会判「无对应」 |
| `GET /Items/{id}/Collections`、`/Users/:uid/Items/:id/Collections` | Emby 4.x 关系端点，文档未收 |
| `GET /Users/:uid/Items/:id/PlaybackInfo`、`POST` 同路径 | 文档只收无 UserId 前缀的形式；真实 Emby 两种都有（`server.go:848` 注释记录了 Yamby 走带前缀形式） |
| `GET /Episode/:id/IntroSkipperSegments`、`GET /MediaSegments/:id` | 返回空数组的桩（`a.emptyList`），Emby 4.8+ 的片头/片尾跳过插件端点，文档未收 |
| `GET /System/Ext/ServerDomains` | 本项目自有扩展（`a.cfg.ServerDomains`），非 Emby 标准 |

### 2.2 鉴权差异（0 处越权）

- `[实测]` 50 个可比对端点，文档 `auth` 全部为 `user`；我方为 `auth=true`（`requireAuth` 通过即可，不额外要求管理员）→ **不存在「文档要求管理员而我方放开」的越权**。
- `[代码]` `server.go:836` 表中 `auth=false` 的只有 7 条：`/Items/:id/Images/:kind[/:index]`（图片流）、`/System/Info/Public`、`/System/Ping`（GET/POST）、`/Users/Public`、`/Users/AuthenticateByName`。
- 其中图片两条**匿名**是刻意的兼容例外（浏览器 `<img>`/`<video>` 无法带自定义头），已在 `API-CONTRACT.md §2` 明示；文档标 `user`。这是唯一一处「我方比文档更宽松」，属已知设计取舍，不是疏漏。

---

## 3. 已实现端点的逐条核对

### 3.1 正确（形状一致）

| 端点 | 核对结果 |
|---|---|
| `GET /Items/Counts` | `[实测]` 14 个字段全出（Album/Artist/Book/BoxSet/Episode/Game/GameSystem/Item/Movie/MusicVideo/Program/Series/Song/Trailer）；与 `[文档] ItemCounts` 一致。语义偏窄（本服务只有影片与合集，其余恒 0） |
| `GET /Users/{UserId}/Views` | `[代码]` `library.go:946` 返回 `{Items, TotalRecordCount}`；`[文档]` `QueryResult_BaseItemDto` → 一致（缺 `StartIndex`，文档模型只有两个字段） |
| `GET /Users/{UserId}/Items/Latest` | `[代码]` `library.go:1189` 返回裸数组；`[文档]` `BaseItemDto[]` → 一致 |
| `GET /Users/{UserId}/Items`、`GET /Items` | `[代码]` 返回 `QueryResult` 结构，分页/排序/过滤主链路正确（参数覆盖另见 §5） |
| `GET /Genres` | `[代码]` 返回 `{Items:[{Name,Id}],TotalRecordCount}`，`entityId = kind:base64url(name)` 稳定可回查 |
| `GET /System/Ping` | `[代码]` `system.go:88` 纯文本 `"Emby Server"`，与真实 Emby 一致（文档只写 200） |
| 用户数据写端点族（Favorite/Rating/Played/HideFromResume，含 `/Delete` 变体） | `[代码]` `userdata.go` 全族齐备，先 `validUser` + `Movie` + `IsVisible` + `canViewMovie` 再写，404 语义正确 |
| `POST /Users/AuthenticateByName` | `[实测]` 200，接受 JSON 与 form；失败 401 / 未初始化 428 / 限流 429 — 与 `[文档]00-基础/02-认证与鉴权.md` 的分档一致（`SessionInfo` 缺失见 §4.2） |

### 3.2 我方多给（无害）

`movieImageInfo`（`media.go`）在 `ImageInfo` 里输出 `ImageTag`，代码注释已记录「3.5.2 契约，真机 4.9 列表无此键，多给无害」。多字段不影响解析，保留合理。

---

## 4. A 类：确认的结构 / 状态码差异（可修）

### 4.1 `HEAD` 请求全部 404 —— 影响面最大

`[实测]`：

```
HEAD /System/Ping            -> 404
HEAD /Items/1/Images/Primary -> 404
```

`[文档]` 定义了 18 个图片 `HEAD` 变体（`HEAD /Items/{Id}/Images/{Type}` 等，params 与 GET 完全一致）与 `HEAD /System/Ping`。
`[代码]` `registerEmby` 只注册 `route.method` 单一方法（`server.go:919` `g.Handle(route.method, ...)`），Gin 不做 GET→HEAD 自动映射；`registerStreamRoutes` 单独补了 `/Videos/:id/stream` 的 HEAD，图片与 `/System/Ping` 没有。

**后果**：客户端用 HEAD 探测图片是否存在（速查表 §4 明列「`HEAD /Items/{Id}/Images/{Type}` 探测图片是否存在（省流量）」）会拿到 404，进而认为「无图」，海报墙退化为占位块。

**修法（最小）**：在 `registerEmby` 里对 `GET` 且路径含 `/Images/` 或 `/System/Ping` 的路由补注册同 handler 的 `HEAD`。约 3 行。

### 4.2 `POST /Users/AuthenticateByName` 缺 `SessionInfo`

`[实测]` 响应键：`[AccessToken, User, ServerId]`
`[文档]` `AuthenticationResult` = `{User, SessionInfo, AccessToken, ServerId}`

缺 `SessionInfo`（含 `PlayState`/`AdditionalUsers`/`Capabilities`/`DeviceName`/`RemoteEndPoint`/`Id`/`UserId`/`UserName`/`Client`/`LastActivityDate`/`ApplicationVersion`）。多数客户端只读 `AccessToken`，但依赖 `SessionInfo.Id` 做 `/Sessions/{Id}/...` 的客户端会直接失败。

### 4.3 `PublicSystemInfo` 缺 `LocalAddress` / `WanAddress`，多 `ProductName`

`[实测]` `[Id, LocalAddresses, ProductName, RemoteAddresses, ServerName, Version]`
`[文档]` `[Id, LocalAddress, LocalAddresses, WanAddress, RemoteAddresses, ServerName, Version]`

`LocalAddress`（单值）被不少客户端用来在登录页拼 `http://<LocalAddress>:<port>`；缺它会导致局域网直连探测失败。`[代码]` `system.go` 的 `info`（`/System/Info`）里有 `LocalAddress`/`WanAddress`，只是 `publicInfo` 没带上——属同一份数据漏填。

### 4.4 `PlaybackInfoResponse` 缺 `ErrorCode`

`[实测]` 键：`[MediaSources, PlaySessionId]`
`[文档]` 三字段：`MediaSources` / `PlaySessionId` / `ErrorCode`

`ErrorCode` 为空时可省略，但客户端若按「无 ErrorCode = 成功」判，则当前行为等价；风险低。

### 4.5 `UserItemDataDto` 缺 `Rating` / `UnplayedItemCount` / `Key`

`[实测]` 条目详情里的 `UserData` = `{IsFavorite, LastPlayedDate, PlayCount, PlaybackPositionTicks, Played, PlayedPercentage}`
`[文档]` 还有 `Rating`（number double）、`UnplayedItemCount`、`Key`、`ItemId`、`ServerId`。

`[代码]` `userdata.go:17` 的 `userDataDTO` 恒出 6 字段；`ItemId`/`ServerId` 只在 `userDataJSON` 包装时补。用户评分（`Rating`）我方用 3.x 语义的 `Likes`（bool，仅非 0 时出现）——**文档 4.x 已改为 `Rating` 数值**。这是版本代际差异：写端点 `POST .../Rating` 的入参是 `?Likes=true|false`，与 4.x 的 body `{"Rating":8.5}` 不兼容。

那条「`rating` 只认 `?Likes`，否则 400」的实现（`userdata.go:102`）会让 4.x 客户端评分失败。这是**已实现端点里唯一可能造成功能失败**的一条。

### 4.6 `SystemInfo` 缺 10+ 字段

`[实测]` 21 个键；`[文档]` 30+。缺：`ProgramDataPath`、`LogPath`、`CachePath`、`TranscodingTempPath`、`ItemsByNamePath`、`InternalMetadataPath`、`WebSocketPortNumber`、`HasUpdateAvailable`、`CanLaunchWebBrowser`、`PackageName`、`CompletedInstallations`、`SupportsLocalPortConfiguration`、`SupportsWakeServer`、`HasImageEnhancers`。

客户端主要读 `Version`/`Id`/`ServerName`/`OperatingSystem`，这些都在。风险低。

### 4.7 状态码：5 个端点我方 204，文档 200

| 端点 | 我方 | 文档 |
|---|---|---|
| `POST /Sessions/Playing` | 204 | 200 |
| `POST /Sessions/Playing/Progress` | 204 | 200 |
| `POST /Sessions/Playing/Stopped` | 204 | 200 |
| `POST /Sessions/Capabilities`、`/Capabilities/Full` | 204 | 200 |
| `POST /Sessions/Playing/Ping` | 204 | 200 |
| `POST /Sessions/Logout` | 204 | 200 |

`[实证]` `POST /Sessions/Playing -> 204`。
`[代码]` `media.go` 的 `playing` 末尾 `c.Status(http.StatusNoContent)`；`system.go:91` `noContent`。

**注意这里是我方自己的契约冲突**：`API-CONTRACT.md` 已把 204 写成契约，而 Emby 文档写 200。两者都是 2xx，绝大多数客户端按 `status < 300` 判成功。**建议：保持 204 并在此记录差异**，不要为对齐文档改动已被测试覆盖的契约（会动 6 个端点 + 相关 web 测试）。

### 4.8 图片 `tag` 参数被忽略，缓存协商失效

`[实测]` `GET /Items/1/Images/Primary?tag=bogus` → **200（全量图片字节）**
`[文档]` `Tag` 参数：「传入条目对象中的缓存标记（Tag），以便服务端返回**强缓存响应头**」

`[代码]` `imageResizeParams`（`media.go:429`）只读 `maxWidth/width`、`maxHeight/height`、`quality`，完全不看 `Tag`；`serveImage` 用 `posterTag(path)` 自己生成 ETag。
**后果**：客户端传 `tag` 想命中 304 不会命中——但因为 ETag 本身就基于同一 mtime，客户端若用 If-None-Match 仍可 304。属「参数被忽略但结果不坏」，风险低。

---

## 5. 参数覆盖度

### 5.1 图片端点：5 / 16

`[文档]` `GET /Items/{Id}/Images/{Type}` 的 16 个参数：`MaxWidth / MaxHeight / Width / Height / Quality / Tag / CropWhitespace / EnableImageEnhancers / Format / BackgroundColor / ForegroundLayer / AutoOrient / KeepAnimation / Index / Type / Id`
`[代码]` 我方只认 `maxWidth/width`、`maxHeight/height`、`quality`（全部大小写不敏感），其余**静默忽略**（不报错，直接忽略）。

风险排序：
1. `Format`（jpg/png/original）—— 客户端若硬要求 png 会拿到 webp；现代客户端都支持 webp，风险中低
2. `Index`（多背景图取第 N 张）—— 我方用路径参数 `/{kind}/{index}` 承接，query 形式被忽略
3. 其余（裁白、增强、背景色、前景层、动画、自动转正）都是「锦上添花」，忽略不致命

### 5.2 列表端点：约 25 / 99

`[文档]` `GET /Items`、`GET /Users/{UserId}/Items` 各有 99 个 query 参数。
`[代码]` `parseMovieQueryOptions`（`library.go:1316`）实际生效：`Filters`(IsPlayed/IsUnplayed/IsFavorite)、`SortBy`(默认 SortName)、`SortOrder`、`Genres/Tags/Studios/Person`(+Ids)、`SearchTerm`、`Years`、`Ids`、`ExcludeItemTypes`、`ImageTypes`、`HasTmdbId`、`StartIndex`、`Limit`、`ParentId`、`IncludeItemTypes`、`Fields`、`EnableImageTypes`、`ImageTypeLimit`、`EnableUserData`、`EnableTotalRecordCount` —— 约 25 个。
其余（`MinCommunityRating`、`HasSubtitles`、`IsHD`、`MinPremiereDate`、`ExcludeItemIds`、`AdjacentTo`、`ArtistType`…）**静默忽略**。

这是合理的窄面选择：**静默忽略优于报错**（客户端多传参数不会 400）。唯一风险是「筛选器 UI 显示某个过滤器但结果不生效」——属功能降级，不是崩溃。

---

## 6. B 类：高价值缺口（真实客户端会打、我方 404）

按「客户端会不会因此功能坏掉」排序：

### P0｜会让某个功能整个失效

| 端点 | 影响 |
|---|---|
| `GET /Sessions` | `[实测]` 404。客户端「正在播放」「远程控制」「多端同步」页面的数据源 |
| `GET /Auth/Keys`、`POST /Auth/Keys`、`DELETE /Auth/Keys/{Key}` | `[实测]` 404。第三方客户端做 API Key 登录时会先探测 |
| `POST /Users/{UserId}/Items/{ItemId}/UserData` | `[实测]` 404。整块「读写用户数据」的通用端点；我方只有细粒度 `/PlayedItems`、`/FavoriteItems`、`/Rating` 三条，**4.x 客户端不一定认得细粒度** |
| `POST /Users/{UserId}/PlayingItems/{Id}` 及 `/Progress`、`/Delete`、`DELETE` 同路径 | `[实测]` 404。旧式会话上报（Emby 3.x / 部分 4.x 客户端仍用）。我方只实现了 `/Sessions/Playing*` 新式 |
| 全部图片 `HEAD` | 见 §4.1 |

### P1｜会让某个页面空白

| 端点 | 影响 |
|---|---|
| `GET /Shows/{Id}/Seasons`、`GET /Shows/{Id}/Episodes`、`GET /Shows/Upcoming` | `[实测]` `GET /Shows/1/Seasons -> 404`。本项目无剧集，但客户端进「剧集」Tab 会报错而非显示空列表 |
| `GET /Items/{Id}/ThemeMedia`、`/ThemeSongs`、`/ThemeVideos` | `[实测]` `GET /Items/1/ThemeMedia -> 404`。详情页「主题曲/主题视频」区块 |
| `GET /Users/{UserId}/Items/{Id}/Intros`、`/LocalTrailers`、`/SpecialFeatures` | 详情页「花絮/预告」区块 |
| `GET /Users/{UserId}/Items/Root` | 部分客户端的首页根节点 |
| `GET /Library/MediaFolders` | `[文档]` 媒体库结构的主要读取口（我方用 `Views` 替代） |
| `GET /Genres/{Name}` | 我方只有 `GET /Genres` 列表 |
| `GET /Artists`、`GET /Persons`、`GET /Studios`、`GET /Tags`、`GET /Years`、`GET /OfficialRatings` | 筛选器数据源。我方用 `/Genres` + 实体虚拟 id 覆盖了部分，但缺独立端点 |
| `GET /Videos/{Id}/{MediaSourceId}/Subtitles/{Index}/Stream.{Format}` 全族 | 外挂字幕下载。我方 `MediaStreams` 里 `SupportsExternalStream=false`、`IsExternal` 按 NFO 如实标记，**等于明确告诉客户端「不要来取」**——这是自洽的降级，不是 bug |
| `GET /Users/{Id}/Images/{Type}` 用户头像族 | 头像显示 |

### P2｜管理面 / 定位外
`POST /Users/New`、`POST /Users/{Id}/Policy`、`POST /Users/{Id}/Password`、`GET /Users/Query`、`GET /System/Logs/*`、`POST /System/Restart|Shutdown`、`GET /System/ReleaseNotes`、`GET /System/WakeOnLanInfo`、`POST /Library/Refresh`、`POST /Library/Media/Updated`、`GET /Items/{Id}/Ancestors`、`GET /Items/{Id}/File|Download`、`POST /Collections`、`POST|DELETE /Collections/{Id}/Items`、全部 Live TV / DLNA / 音乐 / 插件 / 同步 / 播放列表。

本项目管理面已用自有 `/api/admin/*` 覆盖同类能力（`API-CONTRACT.md §4` 已声明 `/api/admin/*` 不在 Emby 兼容承诺内），不必重复造 Emby 的管理端点。

---

## 7. 文档自身的问题（C 类，不计为我方缺陷）

1. `GET /Items/{Id}`：`index.json` 端点表只有 `DELETE`，但 `05-常用接口速查表.md §3` 明确列 `GET /Items/{Id}` = 条目详情 → 文档内部不一致，我方实现正确。
2. `GET /Users/Me`：文档完全未收，但速查表与真实客户端普遍使用。
3. `GET /Users/{UserId}/Items/{ItemId}/UserData`：文档**只有 POST，没有 GET**。Emby 真实服务器两个都有；我方两个都没有。
4. `GET /Items/{Id}/Images/{Type}/{Index}/{Tag}/{Format}/...` 长形式：文档收了但 `params` 计数（16）与路径占位符数（9）不一致。
5. `POST /Users/{UserId}/PlayingItems/{Id}` 与 `POST /Sessions/Playing` 并存：文档同时收两套语义重复的上报路径，未说明哪个是废弃的。

---

## 8. 建议的修复顺序（按性价比）

| 优先 | 动作 | 成本 |
|---|---|---|
| 1 | 给图片端点与 `/System/Ping` 补 `HEAD`（`registerEmby` 里加一次同 handler 注册） | ~3 行，风险 0 |
| 2 | `publicInfo` 补 `LocalAddress`/`WanAddress`（数据现成，`info` 里就有） | ~2 行 |
| 3 | `userDataDTO` 补 `Rating`/`UnplayedItemCount`/`Key`；`rating` 同时接受 4.x 的 `{"Rating":n}` body | ~20 行，需先定「`Likes` 3.x 语义是否保留」 |
| 4 | `GET /Sessions` 返回本服务实例的会话数组（数据源是现有 token 表） | 中；需明确「会话」在本项目里对应什么 |
| 5 | `POST /Users/{UserId}/Items/{ItemId}/UserData` 通用写端点（复用现有 `userdata.go` 的四个细粒度实现） | 中 |
| 6 | 图片 `Format`、`Index` query 参数支持 | 中低 |
| — | `204 → 200` | **不建议**：会动已固化的契约与 6 个端点的测试，收益仅为「与文档字面一致」 |
| — | 补齐 Live TV / DLNA / 音乐 / 插件 | 定位外，不做 |

---

## 9. 明确的「不做」清单与本报告不宣称的事

**不宣称**：本报告不代表当前实现与「某个具体版本的 Emby 服务器」等价。参考文档是官方 RestAPI 快照，其自身声明「不代表已逐接口实机验证」；本报告只做了「文档 ↔ 我方代码/实测」的双向核对，**没有对真实 Emby 服务器发起对照请求**。

**明确不做**（与项目定位一致，见 `README.md` 范围与不做 / `API-CONTRACT.md`）：
- Live TV / DLNA / 音乐库 / 插件系统 / 同步（Sync）/ 播放列表
- Emby 的管理端点（`/System/Restart`、`/Library/Refresh`、`POST /Users/New` 等），本项目管理面走 `/api/admin/*`
- 转码与 HLS（`/Videos/{Id}/master.m3u8` 等），本服务只做上游直链 302
- 图片上传/删除（管理面走 `/api/admin/items/:id/images/:kind`）

---

## 10. 复现方式

```bash
# 覆盖率统计（参数名归一化后逐段比对）
python3 - <<'PY'
import json, re, collections
d = json.load(open('/root/src/EmbyRestApiDocs/index.json'))
# ... 见审查过程脚本；核心是把 {Id} 与 :id 都归一为通配段后比对
PY

# 我方路由表导出
grep -n '{"GET"\|{"POST"\|{"DELETE"\|{"HEAD"' internal/server/server.go
```

实测证据均用 `newApp(config.Config{DBPath: t.TempDir()}, cache.NewMemory(64))` + `httptest.NewServer` 复现：初始化管理员 → 登录取 token → 建库扫描 → 打目标端点。