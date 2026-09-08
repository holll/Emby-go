# Emby 3.5.2.0 源码分模块研究报告

> 目的：为 Emby-go（Emby API 兼容的电影型媒体服务）**查漏补缺**建立基线。
> 对象：`src/Emby-3.5.2.0`（Emby 2018 年最后开源版，C#/.NET + ServiceStack）。
> 方法：按模块精读 `MediaBrowser.Api` 控制器的 `[Route]` 与方法行为；`MediaBrowser.Model` 为二进制引用，DTO 字段依控制器赋值与客户端实测推断。
> 状态图例（对 Emby-go）：✅=已实现且对齐；◑=已实现但简化/未对齐；❌=缺失（值得考虑）；—=电影库不相关/可不做。

---

## 0. 重要跨版本结论（先说清）

3.5.2 开源树里 **没有** 这些 4.x 常见接口（属当时未随源码发布的私有 DLL / 后置 WebSocket）：

- `GET|POST /Items/{Id}/PlaybackInfo`、`/Videos/{id}/stream[.{container}]`
- `POST /Sessions/Playing`、`/Playing/Progress`、`/Playing/Stopped`（3.5.2 播放上报走 WebSocket `PlaybackStart/Progress/Stopped` 消息）
- `POST|DELETE /Users/{UserId}/PlayedItems/{Id}`、`POST /Users/{UserId}/Items/{Id}/UserData`
- `GET /Users/Me`
- `/Items/Filters2` 路由在 3.5.2 存在但 handler 只填 `Genres`；`/Items/Filters` 旧版更全。

Emby-go 面向 Yamby / iPlay 等现代客户端，**上述“4.x 才有”的接口已按 4.9.5.0 实现并校准**（PlaybackInfo/stream/进度上报/UserData）。本报告对 3.5.2 的研究主要用于补 4.x 也保留的浏览/元数据/管理接口缺口。

---

## 1. 核心浏览 / 用户模块（UserService、UserLibrary、Suggestions、Videos、Library、Movies）

### 1.1 认证与用户
| 端点 | 说明 | 状态 |
|---|---|---|
| `POST /Users/AuthenticateByName` | body `Username/Pw`(明文)或 `Password`(SHA1)；返回 `AuthenticationResult{User,AccessToken,ServerId,SessionInfo}` | ✅（已按 4.9 契约） |
| `GET /Users/Public` | 登录屏公开用户 | ✅（单用户返回 `[]`） |
| `GET /Users`、`GET /Users/{Id}`、`/Users/New|Policy|Configuration|Password` 等 | 多用户管理 | —（单用户，不做） |
| `GET /Users/Me` | 3.5.2 无；4.x 有 | ✅ |

### 1.2 媒体库主查询（Emby-go 核心已覆盖）
| 端点 | 说明 | 状态 |
|---|---|---|
| `GET /Users/{uid}/Items`、`GET /Items` | 主查询；query：`ParentId/Recursive/IncludeItemTypes/ExcludeItemTypes/Filters(IsPlayed/IsFavorite/IsResumable)/Genres|GenreIds/Years/Studios|StudioIds/PersonIds/SortBy/SortOrder/StartIndex/Limit/Ids/SearchTerm/Fields/EnableImages/EnableUserData` | ✅ 大部；**待补**：`ExcludeItemTypes`、`Ids`、`CollapseBoxSetItems`、`EnableImages/EnableUserData` 语义、`IsResumable` Filter |
| `GET /Users/{uid}/Views` | 媒体库视图列表；固定补 `PrimaryImageAspectRatio`/`DisplayPreferencesId` | ✅（+合集视图）；PrimaryImageAspectRatio 未补 |
| `GET /Users/{uid}/Items/Latest` | 最近新增，返回 **BaseItemDto[]**，支持 `GroupItems` | ✅ |
| `GET /Users/{uid}/Items/Resume` | 可续播，按 `DatePlayed` 倒序，尊重 `HidePlayedInLatest` 类排除 | ✅（按 position_ticks 判断；`IsResumable`/完整播放过滤可再对齐） |
| `GET /Users/{uid}/Suggestions` | 随机推荐流 | ✅（当前=最近入库，可换随机/推荐策略） |
| `GET /Users/{uid}/Items/{Id}` | 单项详情 BaseItemDto | ✅ |
| `GET /Users/{uid}/Items/Root` | 用户根目录 DTO | ❌（返回一个“Media Folders”根 DTO 即可，成本低） |
| `GET /Users/{uid}/GroupingOptions` | 可分组视图 `SpecialViewOption[]` | ❌（Yamby 不依赖；Web 老客户端可能用） |
| `GET /Users/{uid}/Items/{Id}/SpecialFeatures|LocalTrailers|Intros` | 花絮/预告片/片头 | ◑ 仅探针/空；对纯 strm 无花絮数据，返回空数组即可闭环 |
| `POST|DELETE /Users/{uid}/FavoriteItems/{Id}` | 收藏，返回 UserItemDataDto | ✅ |
| `POST|DELETE /Users/{uid}/Items/{Id}/Rating` | 赞/踩 | ✅ |
| `POST/DELETE /Users/{uid}/PlayedItems/{Id}` | 标记已看 | ✅（4.x 端点） |
| `POST /Users/{uid}/Items/{Id}/UserData` | 直接写 UserData | ❌（低成本补：按 UserItemDataDto body 合并落库） |

### 1.3 分类（by-name）浏览
| 端点 | 说明 | 状态 |
|---|---|---|
| `GET /Genres`、`/Genres/{Name}` | Genre 列表/单点（DTO 带 `MovieCount` 等） | ✅ 列表（虚拟 `genre:<b64>` id）；❌ `{Name}` 单点 by-name 路由 |
| `GET /Persons[/{Name}]`、`/Studios[/{Name}]`、`/Years[/{Year}]` | 演员/片商/年份分类 | ◑ 仅经 `Items?IncludeItemTypes=Person|Studio` 虚拟化支持；❌ by-name 单点 + `/Years` |
| `GET /Users/{uid}/Items?IncludeItemTypes=Genre|Person|...` | 分类聚合 | ✅（含合集作用域、0 匹配隐藏） |

### 1.4 Suggestions / 相似 / 电影页
| 端点 | 说明 | 状态 |
|---|---|---|
| `GET /Movies/Recommendations` | 推荐分类块 `RecommendationDto[]`（SimilarToRecentlyPlayed / SimilarToLikedItem / HasActorFromRecentlyPlayed…） | ❌（可作为首页增强；当前只有 Suggestions） |
| `GET /Items/{Id}/Similar`、`/Movies/{Id}/Similar` | 相似影片 | ✅（Go 侧自研 Genre/Tag/Studio/People 加权） |
| `GET /Videos/{Id}/AdditionalParts` | 多分段/CD 兄弟 | ✅（返回空结构） |

### 1.5 Library / 管理（Emby-go 用自研 /api/admin，非必合）
| 端点 | 说明 | 状态 |
|---|---|---|
| `GET /Items/Counts` | 各类别计数 | ✅ |
| `GET /Library/MediaFolders`、`/Library/VirtualFolders` | 媒体库/虚拟文件夹列表 | —（管理侧已走 /api/admin） |
| `POST /Library/Refresh` | 触发全库扫描 | —（对应 /api/admin/scan） |
| `POST /Items/{Id}/Refresh` | 单条异步刷新 | ◑ 可映射到“重读源” |
| `GET /Items/{Id}/Ancestors`、`/File`、`/Download`、`/ThemeSongs|Videos` | 杂项 | —/❌（Ancestors 低成本可返回父级链） |
| `POST /Collections`、`/Collections/{Id}/Items` | BoxSet 增删维护 | ◑ 当前合集只读聚合（NFO `<set>`），维护接口可后置 |

---

## 2. 系统 / 配置 / 图像 / 会话模块

### 2.1 握手层（客户端识别、必做，均 ✅）
- `GET /System/Info/Public` — 免认证 `PublicSystemInfo`（Version/ServerName/Id/…）✅
- `GET|POST /System/Ping` — 免认证返回服务器名 ✅
- `GET /System/Info` — 登录后完整 `SystemInfo` ✅（已补 OS/地址等）
- `GET /System/Endpoint` → `{IsLocal,IsInNetwork}` ✅

### 2.2 配置 / 环境 / 向导 / 品牌（管理侧；Web 后台可自行覆盖）
| 端点 | 说明 | 状态 |
|---|---|---|
| `GET|POST /System/Configuration`、`/System/Configuration/{Key}` | 完整 ServerConfiguration 读/写 | ◑ GET 仅最小对象；POST 未实现（管理后台不强依赖） |
| `GET /Environment/Drives`、`/DirectoryContents`、`/ValidatePath`、`/ParentPath` | **选库/建库路径**核心 | —（管理 UI 用 /api/admin；若未来做“服务端目录浏览”需补） |
| `POST /Startup/*`、`GET /Branding/*`、`/Localization/*` | 首启向导/品牌/多语言 | — |
| `GET|POST /DisplayPreferences/{Id}?UserId&Client` | 视图模式/排序/记忆索引 | ◑ 已提供 GET/POST（POST 未真正按 body 存储）；**建议落库真存**，Yamby/Web 保存排序偏好会用到 |
| `GET /Items/Counts` | — | ✅ |

### 2.3 图像（浏览核心，✅ 大部分；差异点要修）
- `GET /Items/{Id}/Images` → `ImageInfo[]`（`ImageType/ImageIndex/ImageTag/Path/Width/Height/Size`）✅
- `GET|HEAD /Items/{Id}/Images/{Type}[/{Index}]`：query `MaxWidth/MaxHeight/Quality/Tag/Format(webp)/AddPlayedIndicator/PercentPlayed…` ✅（参数当前基本忽略；**Tag 一年缓存头、Format/宽高缩放未做**）
- `POST|DELETE /Items/{Id}/Images/{Type}` — **body 是 base64 字符串**（非 multipart）◑（管理后台走 multipart 上传 → webp；Emby 端 base64 协议未实现）
- 名字类图片 `/Genres/{Name}/Images`、`/Persons/...`、`/Studios/...`、`/Users/{Id}/Images` ❌（按需补 `/Genres|Persons|Studios/{Name}/Images/{Type}` 可让客户端头像/图集闭环）
- `/Images/Ratings/...`、`/Images/MediaInfo/...` 内置图标（分级/媒体图标）— 客户端缺图回退用；可后置
- `/Items/{Id}/RemoteImages*`（刮削远端图）—（无刮削器，不做）

### 2.4 会话 / 设备 / 进度（4.x 形态已实现；3.5.2 差异点说明）
| 端点 | 说明 | 状态 |
|---|---|---|
| `GET /Sessions` | 在线会话；含 `NowPlayingItem/PlayState`（“谁在看/正在播放”） | ❌（低成本返回空/最小会话列表即可，也可为将来“正在播放”卡片铺路） |
| `POST /Sessions/Capabilities`、`/Capabilities/Full`、`/Logout` | 客户端能力上报/登出 | ✅（返回 204） |
| `POST /Sessions/Playing[/Progress|Stopped]` | 播放上报（4.x REST；3.5.2 走 WS） | ✅ |
| `POST /Sessions/{Id}/Playing|Command|Message` 等 | 服务器→客户端遥控 | — |
| `/Devices`、`/Auth/Keys` | 设备管理/API Key | —/❌（可返回空列表占位） |

> 播放进度若想支持“多端正在播放/遥控”，3.5.2 依赖 WebSocket；Emby-go 走 4.x REST 上报，已够单用户续播需求。

---

## 3. 元数据 / 搜索 / 过滤 / 相似 / 字幕模块

### 3.1 搜索（❌ 最值得补）
`GET /Search/Hints?SearchTerm&UserId&Limit&ParentId&IncludeMedia/People/Genres/Studios/Artists&IncludeItemTypes`
→ `SearchHintResult { TotalRecordCount, SearchHints: SearchHint[] }`
`SearchHint` 关键键：`Name/Id(=ItemId)/Type/MediaType/MatchedTerm/ProductionYear/RunTimeTicks/PrimaryImageTag/PrimaryImageAspectRatio/ThumbImageTag/BackdropImageTag/IsFolder`。

现状：Emby-go 返回 `[]`（与实测 4.9 服务器一致）。**但 iPlay 不用它、Yamby 可能用**；SearchHint 是旧 Web/部分客户端的搜索入口。建议：实现真正命中（标题/番号/原名/女优），ImageTag 用与 Items 一致的 tag。注意 4.9 真机对 /Search/Hints 返回 `[]` 的现象，可能与服务器配置/客户端调用方式有关，落地前再抓一次 Yamby 的实际调用再定形。

### 3.2 过滤（❌ 缺 `/Items/Filters`、`/Items/Filters2`）
- `GET /Items/Filters?UserId&ParentId&IncludeItemTypes&Recursive` → `QueryFiltersLegacy{Years:int[], Genres:string[], Tags:string[], OfficialRatings:string[]}`（不含 Studios；按父节点 Recursive 聚合内存 Distinct）。
- `GET /Items/Filters2` → `QueryFilters{Genres: NameGuidPair[]}`（3.5.2 handler 仅填 Genres）。
- 客户端过滤抽屉据此渲染年份/类型/标签 pill。
- **建议**：Emby-go 实现 `/Items/Filters` 输出可过滤维度（含 **Studios/Person**，因为 AV 片商/女优是核心维度；3.5.2 不给 Studio 列表是缺憾），配合已有 Items 查询的 `Genres/Years/Studios/PersonIds` 形成闭环，且**遵循“只出现当前上下文数量≥1 的项”**（与合集范围逻辑一致）。

### 3.3 相似推荐算法（可复用骨架）
3.5.2 `SimilarItemsHelper.GetSimiliarityScore` 权重：
- 共同官方分级 +10；每共同 Genre +10；每共同 Tag +10；每共同 Studio +3
- 共同人物：Director +5 / Actor +3 / Composer 3 / GuestStar 3 / Writer 2 / 其他 1
- 年代差 <10 年 +2；<5 年再 +2；总分>2 才进候选，排除自身，降序
- Movie 型实际走库层 `SimilarTo`；音乐侧才用静态打分。

现状：Emby-go `overlapScore` 只算 Genre/Tag/Studio 相同数。建议升级为上面权重版（把 People 分角色计入、年份邻近加分），与 3.5.2 语义一致，也更好服务 AV“同片商/同女优推荐”。

### 3.4 元数据编辑闭环（管理/刮削用；❌ 可后置）
| 端点 | 说明 | 状态 |
|---|---|---|
| `POST /Items/{Id}` | 全量 BaseItemDto 写回（Name/Genres/Tags/Studios/Taglines/PremiereDate/OfficialRating/People/ProviderIds…） | ◑ 管理后台编辑仅 title/plot/year；完整元数据写回可补（NFO 已能保存这些字段） |
| `POST /Items/{Id}/Refresh` | 单条异步刷新 | ◑ 映射“重读源/重建” |
| `GET /Items/{ItemId}/MetadataEditor` | 编辑器选项 | — |
| `POST /Items/RemoteSearch/{Type}` + `Apply/{Id}` | 远程刮削识别/落库 | —（本期无刮削） |

### 3.5 电视 / 字幕 / 非电影类
| 模块 | 端点概览 | 状态 |
|---|---|---|
| TvShows | `/Shows/NextUp|Upcoming`、`/Shows/{Id}/Seasons|Episodes` | ◑ NextUp 已空实现；剧集结构电影库不做（除非 AV 合辑用） |
| Subtitles | `/Videos/{Id}/{MediaSourceId}/Subtitles/{Index}/Stream.{Format}`、`subtitles.m3u8` | —/❌（strm 外挂字幕基本不用；如需转发内嵌字幕文本可补） |
| Channel / LiveTv / Music / Games / Playlist | `/Channels*`、`/LiveTv*`、`/Albums|Artists|InstantMix`、`/Games/SystemSummaries`、`/Playlists*` | — |

---

## 4. Emby-go 覆盖总览与差距清单

### 已实现（浏览主链路，无需动）
认证、`/Users/Me`、Views(+合集)、Items/Latest/Resume/Suggestions、Items 主查询（含类型/合集过滤、排序、分页）、Item 详情、Image 清单/取图、Similar、PlaybackInfo、stream(302)、播放进度上报、收藏/评分/已看/隐藏续播、System Info/Ping/Endpoint、DisplayPreferences(GET)、Sessions Capabilities/Logout/Ping、Search/Hints(空占位)。

### 值得优先补（按性价比）
1. `/Search/Hints` 真命中（标题/番号/原名/女优），返回 SearchHint 结构 —— 兼容旧 Web/部分客户端搜索。
2. `/Items/Filters`（+兼容 Filters2）：产出 Years/Genres/Tags/Studios/OfficialRatings(可加 Persons) 过滤维度，且只含数量≥1。
3. `POST /Users/{uid}/Items/{Id}/UserData` + `POST /Users/{uid}/Items/{Id}/Rating` 既有（已做）；真正要补的是 UserData 直接写接口与 `/Users/{uid}/Items/Root`。
4. by-name 端点：`/Genres/{Name}`、`/Persons/{Name}`、`/Studios/{Name}`、`/Years[/{Year}]` 及其 `/Images/{Type}`（图集/头像闭环）。
5. `/DisplayPreferences` POST 真正按 body 落库（可存 kv/JSON）。
6. `GET /Sessions` 返回最小会话列表（配合正在播放展示）。

### 中/远期（管理、闭环）
- `/Items/Filters` Studio/Person 维度、`/Movies/Recommendations`、Similar 加权升级、`POST /Items/{Id}` 完整元数据写回、`/Library/VirtualFolders|MediaFolders` 只读占位、`/Environment/*` 目录浏览（若做 Web 端建库）。
- Images：Tag 一年缓存头、`AddPlayedIndicator/PercentPlayed`、`Format` 协商、by-name 图片。

### 明确不做
多用户 CRUD、频道/LiveTV/音乐/游戏/播放列表、字幕 HLS、远程刮削、插件/包/新闻、重启/关机、设备/API-Key 管理。

---

## 5. 复用素材速查

- **相似度权重**：Genre+10/Tag+10/Studio+3/OfficialRating+10；People: Director5/Actor3/Writer2；年代 <10 年 +2、<5 年再 +2；>2 分入选。
- **Filters 口径**：旧版 = 纯字符串数组聚合（Years/Genres/Tags/OfficialRatings），Filter2 = Genres NameGuidPair；聚合都要在“当前 ParentId 上下文 Recursive”内做。
- **3.5.2 播放上报是 WebSocket**，REST 是 4.x 形态；Emby-go 已正确选 4.x。
- **Views 固定补 `PrimaryImageAspectRatio`、`DisplayPreferencesId`**（Emby-go 已补 DisplayPreferencesId，AspectRatio 可后续读图补）。
