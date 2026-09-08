# MetaTube Media Server — 开发计划

> 定位：**兼容 Emby API 协议的第三方媒体库软件**。元数据真源为 NFO——本期**只读取现成 NFO**，
> 不内置刮削器/翻译/插件运行时（标准刮削器插件规范列为 roadmap）。
> **媒体源只支持 `http(s)` 的 `.strm`**；`ed2k` 等非 HTTP 内容视为**不兼容**，在扫库/记录页提示，不进 Emby。
> Web 提供**管理后台**（非影院浏览端），浏览海报墙通过 Yamby 等 Emby 客户端完成。
> 支持规模 7000 → 1 万部。原则：不背历史包袱，大胆重构。
> **技术选型：Go + Gin + SQLite(modernc) + 原生 JS 嵌入单二进制。**

## 1. 定位与边界（已确认）

| 维度 | 结论 |
|---|---|
| 产品 | Emby API 兼容的第三方媒体库服务端 |
| 浏览入口 | **Yamby 等 Emby 客户端**（协议只对齐 Emby，不做 Jellyfin）；验收以 §7 接口级自测为准，不依赖 GUI |
| Web 界面 | **管理后台**：设置 / 媒体库 / 扫描 / 影片记录（含未刮/**不兼容提示**）/ 手动补录 / 任务 / 接口调试 |
| 用户体系 | 本期**单用户**（现有管理员账号映射为唯一 Emby 用户） |
| 元数据来源 | **只读现成 NFO**（标题/原名/剧情/年份/评分/导演/演员/系列/制作商/标签等）；编辑/手动录入写 NFO 后同步 DB 索引 |
| 媒体源支持 | `.strm` 内容**只支持 `http/https`**；扫库时解析归类，`ed2k` 及其它 scheme → **incompatible（不兼容）**：**不进 Emby items**，在扫库/记录页醒目提示（数量+列表+原因），可换 http 源后“重读源/重扫”入库 |
| 未刮影片 | 仅 http(s) 且无 NFO → 登记 pending；Web 手动录入 NFO + 上传封面/背景 → success 进入 Emby |
| 播放 | **302 直拉**：PlaybackInfo 返回本服务流地址 → `GET /videos/{itemId}/stream` 实时读 `.strm` → http(s) 则 **302 Location=真实地址** 客户端直拉；服务端不转发字节、不转码。实测遇防盗链/UA/Range 问题 → 同源**转发兜底**（§5.2） |
| 失败反馈 | 播放遇到死链/上游 403/404 → 明确错误不白屏不崩溃（302 模式尽力透传，转发兜底可原样回上游状态码）；非 http 源根本不会进 Emby |
| 未知接口探针 | 路由匹配不上的请求（Emby 各类客户端会发五花八门的请求）→ **NoRoute 记录 method/path/query/请求体**，供 Web 调试页查看，驱动补端点 |
| 续播/进度 | 最小 UserData：`position_ticks / play_count / played / last_played_at`，Item 回填支持“继续播放” |
| 播放带宽 | 直拉模式不占本服务带宽，适配万部规模 |
| 主题 | 深色影院风服务于 Emby 客户端；Web 后台工具性但同套深色令牌 |
| 收藏/多用户/插件规范/翻译 | roadmap |

## 2. 数据模型（重构后）

```
libraries(id, name, path UNIQUE, type, enabled, …)

movies(                                          -- 文件 + 元数据一体
  id INTEGER PK AUTOINCREMENT,                   -- 稳定 ID == Emby ItemId
  library_id INTEGER,
  source_path TEXT UNIQUE,                       -- 指向 .strm（唯一，播放取址源）
  file_size, file_mtime,                         -- .strm 自身 size/mtime，变更检测
  source_protocol,                               -- http/https / ed2k / other
  source_container,                              -- URL 路径后缀推断 mp4/mkv/ts/…
  number,
  status,            -- success(NFO已入库，Emby可见) / pending(http无NFO) /
                     -- incompatible(非http源) / manual(手动录入,视为success族)
  nfo_path,                                      -- 同目录 *.nfo 路径（真源）
  output_dir,                                    -- NFO/图片所在目录
  title, original_title, plot, year, premiered, rating,
  director, series, maker, label,
  collection,                                     -- NFO <set><name>：所属合集（BoxSet）
  genres/tags/studios JSON, title_translated,
  has_poster/has_fanart INTEGER(或按文件存在判定),
  runtime_seconds,           -- NFO 有则填（续播百分比）
  created_at, updated_at)
actors(name PK, avatar_url, updated_at)
movie_actors(movie_id, actor_name, PK(…))

userdata(                                        -- 单用户；多用户加 user_id
  movie_id INTEGER PK REFERENCES movies(id),
  position_ticks INTEGER DEFAULT 0,
  play_count INTEGER DEFAULT 0,
  played INTEGER DEFAULT 0,
  last_played_at TEXT)

api_probe(                                       -- 未知接口探针（环形/上限截断）
  id INTEGER PK AUTOINCREMENT,
  method TEXT, path TEXT, query TEXT, body_preview TEXT,
  created_at TEXT)
```

- 对外只暴露 `status` 属 success/manual 的影片；incompatible/pending/failed 仅 Web 后台可见
- **合集（BoxSet）**：由 NFO `<set><name>` 聚合。Views 追加一个 `CollectionType=boxsets` 的「合集」媒体库（Id="boxsets"），其子项为 BoxSet（id 形如 `boxset:<base64>`）；某合集成员 = `collection=<该合集名>` 的可见影片；**合集范围内的类型（Genre/Tag/Studio/Person）列表只返回该上下文内数量≥1 的项**
- **媒体库外部 id**：媒体库对 Emby 层暴露 `libraryIDBase(1e12)+内部库id`（影片 id 是小自增，二者全局不冲突，对齐 Emby 唯一 item id）。库封面取子项**宽图 fanart 优先**（4.9 auto_poster 观感），无则回退海报；Views 补 `PrimaryImageAspectRatio`
- `source_protocol/source_container` 扫描时解析缓存；**播放时实时读 .strm 取最终 URL**，避免外部刷新链接后用陈旧地址
- 本期无 provider/meta_providers 等刮削字段（roadmap 插件规范再加）
- `kv` 表存**失效版本号**（`lib:{id}:version`、`g:version`）：任何对该库 movies 的写（扫描/编辑/手动录入/删除）都 bump；缓存键带版本号即可整体失效，无需逐条删除

## 3. 技术选型

| 项 | 选型 | 理由 |
|---|---|---|
| 语言/框架 | Go 1.25 + **gin-gonic/gin** | 用户意向；v1 已验证，生态成熟 |
| 数据库 | SQLite（`modernc.org/sqlite` 纯 Go，CGo-free） | 沿用 v1；万部规模单文件足够 |
| 数据访问 | 手写 SQL（database/sql） | 轻量可控，避免 ORM 心智负担 |
| 缓存 | **可选 Redis**（`github.com/redis/go-redis/v9`），共用 `internal/cache` 接口 | 默认 `memory`(进程内 LRU) 零外部依赖；改 `driver=redis` 即启用 |
| 缓存一致性 | 单写者 bump 版本号；**缓存只作加速，真源 = SQLite/NFO** | 换缓存后端不失失效语义 |
| 配置 | YAML（`gopkg.in/yaml.v3`） | 沿用 |
| Web 前端 | `go:embed` 内嵌原生 HTML/JS/CSS | 单二进制，无构建链 |
| HTTP 客户端 | `net/http`（自定义 Transport，区分直连/代理） | 302 为主；转发兜底按需 |
| 并发/任务 | goroutine + channel + `golang.org/x/sync/errgroup` | 自研调度，无需外部队列 |
| 日志 | `log/slog`（结构化 + 请求日志中间件） | 标准库 |
| 测试 | `testing` + `httptest`；接口级断言脚本/`go test` 集成 | §7 验收依赖它 |
| 部署 | 单二进制 + 单端口（沿用 18080） | 简单 |

> 缓存定位：**只加速不存真**。SQLite 是事实源，NFO 是元数据真源。单机单用户默认 `memory` 驱动即可满足万部；
> 何时值得切 Redis：跨重启保留会话/token 与任务队列、需要多实例水平扩展、实测热点查询/慢网络挂载读 strm 变瓶颈。
> Redis 非本期必需，按可选项融入。

**模块结构（伪代码级目录）**

```
cmd/metatube/main.go         # 装配：config → store → services → gin 路由 → listen
internal/config              # YAML + DB 设置合并
internal/store               # sqlite 连接、schema、movies/userdata/api_probe DAO
internal/cache               # 缓存抽象 Get/Set/Del/SetNX/TTL + KeyBuilder；memory LRU 默认，Redis adapter(可选)
internal/scanner             # 扫库：strm 分类、NFO 解析、变更检测、状态机
internal/nfo                 # MovieMeta 模型：Read/Write（NFO 真源契约）
internal/auth                # 管理登录(会话) + Emby access token 签发与校验
internal/media               # Emby API 处理器：Views/Items/Images/PlaybackInfo/stream/UserData
internal/admin               # Web 管理 API + go:embed 前端
internal/probe               # 未知接口记录器（NoRoute 捕获）
internal/scheduler           # 扫描/重建索引等后台任务与任务日志
```

## 4. 关键流程伪代码

### 4.1 扫描（strm 分类 + NFO 同步）

```
func Scan(lib) ScanResult:
  files := walk(lib.Path, ext==".strm")
  for f in files:
    proto, _ := readStrmFirstLine(f)          // 读首行 URL → scheme
    rec := store.UpsertCandidate(f, proto)    // 登记/更新，mtime 变才重处理
    switch:
      proto ∈ {http,https}:
        if meta := nfo.Read(siblingNfo(f)):   // 同目录 *.nfo
          upsertMovie(rec, meta); rec.status = success
        else: rec.status = pending            // 待手动补录
      default:                                // ed2k / other
        rec.status = incompatible             // 不进 Emby；扫库页提示
        rec.note = "不兼容源: " + proto
  return stats{success, pending, incompatible, failed}
```

### 4.2 NFO→DB upsert

```
func upsertMovie(rec, meta):
  tx:
    row := movies.upsert(
      source_path, library_id, number=nfo.number? 或文件名解析,
      title/original_title/plot/year/…,
      genresJSON/tagsJSON/studiosJSON, title_translated,
      runtime_seconds, has_poster=存在(poster.jpg), has_fanart=…)
    actors.diff(meta.actors) → actor.upsert + movie_actors.replace
  // 写侧：SaveNFO 后同样调用 upsertMovie（编辑/手动录入共用）
```

### 4.3 手动录入（无 NFO → 生成）

```
POST /api/admin/items/manual {source_path, number, title, …, source_url?, fields…}:
  if source_url != "" && !isHTTP(source_url): 400 "仅支持 http(s) 源"
  if source_url != "": writeStrm(source_path, source_url)   // 生成/覆写
  meta := nfo.FromFields(...)
  nfo.Save(siblingNfo(source_path), meta)       // 写 NFO
  存图（上传/URL → poster/fanart.jpg）
  upsertMovie(rec, meta); rec.status = success  // 即时进索引，Emby 端可见
```

### 4.4 Emby Items 查询映射

```
func Items(uid, q):                             // GET /Users/{uid}/Items
  rows := store.Search(q, filters)              // SQL：仅 status∈{success,manual}
    // 条件：IncludeItemTypes=Movie / ParentId=library → view / SortBy / Years
    //       Genres / SearchTerm(LIKE title|original_title|number)
    //       StartIndex/Limit / 支持 Filters(IsUnplayed…) 最小集
  return { Items: rows.map(toEmbyItem), TotalRecordCount }
toEmbyItem(row):
  return { Id, Name=title, Type:"Movie", Container=source_container,
           ImageTags:{Primary/Backdrop: 有图? "primary":省略},
           ProductionYear, PremiereDate, Genres, Studios, CommunityRating,
           RunTimeTicks, UserData: loadUserData(row.id) }
```

### 4.5 播放：PlaybackInfo + stream 302

```
POST /Items/{itemId}/PlaybackInfo:              // 仅对 Emby 可见影片
  row := store.Get(itemId)                      // source_protocol 必为 http(s)
  return { MediaSources:[{
    Id, Name, Path:"/videos/{itemId}/stream",
    Container: row.source_container, IsRemote:false, Type:"Default",
    SupportsDirectStream:true, SupportsDirectPlay:true, SupportsTranscoding:false,
    RunTimeTicks }] }

GET|HEAD /videos/{itemId}/stream[.{ext}]:       // 吞掉可选扩展名
  u := readStrmFirstLine(row.source_path)       // 实时读，URL 为 http(s)
  if !u: 404 "strm 缺失"
  if !isHTTP(u): 415 { error: "不支持的源" }     // 防御，正常不会发生
  http.Redirect(w, r, u, 302)                   // 客户端携带 Range/Headers 原样去上游
  // 可选转发兜底：若配置，则用自定义 Transport 转发（带 UA/Referer/Cookie），
  //   处理 Range，并把上游 403/404 原样回给客户端（死链可见）
```

### 4.6 进度/续播（UserData）

```
on Sessions/Playing(Start):  if resumePos>0: 存为起点; else 清零
on Sessions/Playing/Progress: store.SetPosition(itemId, PositionTicks)
on Sessions/Playing/Stopped:  store.SetPosition(itemId, PositionTicks); play_count++
on POST|DELETE PlayedItems:   store.SetPlayed(itemId, true/false)
loadUserData(id): { PlaybackPositionTicks, PlayedPercentage=pos/runtime,
                    PlayCount, Played, LastPlayedDate }
```

### 4.7 未知接口探针（NoRoute）

```
r.NoRoute(func(c):
  p := c.Request.URL.Path
  if p 属于 Emby 前缀 (/Users /Items /Videos /System /Sessions …) 或非 /api:
    body := readLimit(c.Request.Body, 4KB)      // 防放大
    probe.Append(method, path, query, body)     // 写 api_probe（上限截断）
    c.JSON(404, gin.H{})                        // Emby 风格 404，不抛异常
  else:                                          // /api 未知
    c.JSON(404, {"error":"not found"})
)
// Web 调试页: GET /api/admin/probe 最近 N 条（清空按钮）
```

### 4.8 缓存层（可选 Redis，只加速不存真）

```
// cache 接口：默认 memory(LRU)，driver=redis 时切 go-redis
type Cache interface { Get(k)/Set(k,v,ttl)/Del(k)/Incr(k) }

// —— 读侧：缓存 Emby Items 分页 / 详情 / strm 分类，miss 则落 SQLite 回填 ——
func EmbyItems(libId, q) page:
  ver := store.KVGet("lib:{libId}:version")          // 失效版本
  key := "emby:items:{libId}:{ver}:" + hash(q)
  if hit := cache.Get(key): return hit
  page := store.Search(q)                              // SQL 真源
  cache.Set(key, page, TTL_items=5~60s)                // 带版本，扫描即整页失效
  return page

func ItemDetail(id):
  if hit := cache.Get("movie:{id}"): return hit
  row := store.Get(id); cache.Set("movie:{id}", row, TTL_detail=1h)
  // 写侧 upsertMovie / deleteMovie 时同步 Del("movie:{id}")，保持强一致

// —— strm 分类：只缓存 protocol/container 这种稳定结果 ——
func StrmMeta(path) (proto, container):
  if hit := cache.Get("strm:"+sha(path)); hit 且 stat(path).mtime==hit.mtime:
    return hit                                   // 存 mtime，命中后比对，变则重读
  proto, container, mtime := parseStrm(path)
  cache.Set("strm:"+sha(path), {proto,container,mtime}, TTL=0 不过期)
  // 注意：最终 URL 不缓存（要实时新鲜）；慢挂载仍可开短 TTL(30s) 缓存首行整条
  // “重读源/重扫” = parseStrm + Del("strm:…") 强制刷新

// —— 写侧统一失效（单写者，bump 即可） ——
func bump(libId): store.KVIncr("lib:{libId}:version"); store.KVIncr("g:version")
// 任意写路径(扫描 upsert/手动录入/编辑/删除/reindex)末尾调用 bump + Del 相关 movie:{id}

// —— 会话/token（可选迁 Redis，带 TTL，重启不丢） ——
tok 存 key "sess:{token}" TTL=7d（memory 驱动则退化为进程内 map）
// —— 未来任务队列（Redis Streams/BLPOP）—— 本期仍用进程内 channel，不加。
```

## 5. 播放链路（本期核心子设计）

### 5.1 源取址与分类

- 扫描/重扫：对每个 `.strm` 读首行 → 归 `http/https`（有效）与 `ed2k/其它`（**incompatible**）→ URL path 后缀推 container → 写 `source_protocol/source_container`
- **入库门槛 = http(s) + NFO**；ed2k 不进 Emby，扫库页提示“不兼容源”，数量与列表可见
- 播放取址：`/videos/{itemId}/stream` 每次**实时读 .strm** 首行，取最新 URL；读不到/空 → 按失效处理
- 刷新链路：外部工具更新 strm（新 token 链接）→ 无需重扫即生效；“重读源”可主动重解析协议列

### 5.2 Emby 播放对话（契约校准 + 字段定稿）

**P2.0 契约校准**：以 Emby 公开 API 行为为准定稿字段；若有 Yamby/真实 Emby 抓包仅用于校对可选字段。内置请求日志 + 探针辅助，**验收不依赖 GUI（§7 自测）**。

- `POST /Items/{itemId}/PlaybackInfo`（兼容 GET）：返回单 MediaSource（Path 指向 `/videos/{id}/stream`，Container=source_container 或空，DirectStream/DirectPlay=true，Transcoding=false）
- `GET|HEAD /videos/{itemId}/stream[.{container}]`：路由吞掉可选扩展名（客户端可能请求 `stream.mkv?Static=true&MediaSourceId=…`）；http(s) → **302**；非 http/读取失败 → 415/404 + JSON
  - 302 客户端原样带 Range 到上游；上游支持 Range 则拖拽可用
  - 若实录发现需特殊 UA/Referer/Cookie 或上游无视 Range → **同源转发兜底**：转发字节、按需带头、透传上游 403/404，逐源开关
- 进度/续播：`Sessions/Playing`、`/Progress`、`/Stopped` 解析 PositionTicks 落库；`PlayedItems` 标记；Item 回填 `UserData{PlaybackPositionTicks, PlayedPercentage, PlayCount, Played, LastPlayedDate}`

### 5.3 安全与边界

- 仅 `http/https` 源入库；重定向只到 http(s)；不跟链、302 模式不服务端拉取
- 不做转码/HLS/外挂字幕/倍速；m3u8 上游不保证起播
- 播放不占本机带宽与转码 CPU，是万部规模前提

## 6. 分阶段

### P0 · 数据层 + 扫库分类 + NFO 索引同步
- 新 schema（movies/actors/movie_actors + userdata + api_probe）；扫描登记并打 library_id
- **strm 分类**：http/https → success/pending；ed2k/其它 → **incompatible**（扫库结果含不兼容统计与列表）
- **NFO→DB upsert**：读现成 NFO 进索引；`POST /api/admin/reindex` 全量重建幂等；迁移工具（旧库 → 重扫重建）
- 保留：libraries/settings/auth、图片端点、登录

### P1 · 检索 API + 手动补录后端
- `GET /api/admin/items`：SQL 检索（view/sort/filter/search/page），仅 success/manual；支持按源协议筛
- 手动录入：字段 + **源地址（仅 http(s)，校验）** + 封面/背景上传或 URL → 写 strm/NFO/图 → success 即时入 Emby
- 编辑已刮元数据：写回 NFO 并 upsert；演员/详情/related 基础端点
- **缓存层 `internal/cache`**：memory 默认 + Redis adapter 可选（`driver=redis`）；Items 分页/详情/strm 分类走读缓存，写侧 bump 版本失效（§4.8）

### P2 · Emby 协议兼容层（核心，API 自测验收）
- 路由总纲：`/api/*` → admin；Emby 前缀 + 静态/播放 → media；**NoRoute 落入探针（§4.7）**
- 认证：`POST /Users/AuthenticateByName`、`GET /Users/Me`、access token
- 系统：`/System/Info/Public`、`/System/Info`、`/System/Configuration`
- 视图/浏览墙：`/Users/{id}/Views`（每库一个）；`/Users/{id}/Items`（ParentId、IncludeItemTypes、SortBy、Years、Genres、SearchTerm、StartIndex/Limit、Filters 最小集）映射 P1 检索；Item 带 UserData
- 详情/图片：`/Users/{id}/Items/{itemId}`、`/Items/{id}/Images/Primary|Backdrop`
- 播放/进度：PlaybackInfo、`/videos/{itemId}/stream`（302 + 可选转发兜底）、Sessions/Playing{Progress,Stopped}、PlayedItems（§5.2）
- 验收 = §7 全量自测通过，不依赖 GUI；Yamby 仅可选冒烟

### P3 · Web 管理后台
- 深色工具型 UI（同套深色令牌）
- 页面：总览/媒体库管理/影片记录/设置（库、认证、重建索引）/任务日志/**接口调试（探针列表）**
- **扫库/记录页不兼容提示**：incompatible 统计卡片 + 列表（来源/原因/操作：换源后重读）
- 影片列表网格+状态筛选，卡片标可播放（http 可播 vs 不兼容）；单部操作：编辑 / 手动录入 / 重读源 / 删除

### P4 · 打磨
- 未刮/不兼容引导、错误/空状态、移动端触感、万部性能、a11y/reduced-motion

### Roadmap（不在本期）
- **标准刮削器插件规范**（核心零内置刮削器）+ 自动翻译管线
- ed2k / 非 HTTP 网关（如需“整库可播”另行立项）
- 转码/倍速/外挂字幕/m3u8 增强播放；多源/画质切换
- 收藏、多用户与权限；观看统计、向量相关推荐
- （可选）Jellyfin 兼容子集

## 7. 验收基准（接口级自测，不依赖 GUI）

验收方式：**自动化接口测试**（`go test` 集成或断言脚本）对**真实运行服务** + **临时 fixture** 逐组断言；开发/Agent 跑通即通过。Yamby 等客户端仅可选人工冒烟，不设门槛。

**7.1 fixture**
- 临时库样本：
  - A：`http-strm` + NFO + poster/fanart → success
  - B：`ed2k-strm` + NFO → **incompatible**
  - C：`http-strm` 无 NFO → pending
- 可控上游：本地支持 Range 的静态文件 HTTP 服务充当 A 真实地址（断言不依赖外网）
- 单用户账号 + access token

**7.2 Emby API 断言组**
- 认证与系统：AuthenticateByName 返回 token；/Users/Me；/System/Info{/Public,/Configuration} 200 且关键字段非空
- 视图/浏览：Views 每库一个；Items 分页/排序/搜索/过滤与 P1 检索一致；**只返回 A（success）**，B/C 不在
- 详情/图片：Items/{id} 带 UserData；Primary/Backdrop 200（本地图）；不存在 id → 404
- 播放：A PlaybackInfo 返回可播 MediaSource；stream **302** 且 Location=可控上游、`Range: bytes=0-` 后对 Location 得 **206** 且字节一致、HEAD 预检通过
- 进度/续播：Start→Progress→Stopped 后 Items/{id} 的 PlaybackPositionTicks/PlayCount 持久化；PlayedItems 翻转 Played
- 错误路径：未认证、坏 id、非 http（防御性 415）等结构化错误，**不 5xx、不崩溃**

**7.3 Web 管理 API 断言组**
- 扫描：A→success、B→**incompatible（出现在不兼容列表）**、C→pending；重扫幂等
- 手动录入：为 C 提交字段 + http 源 + 封面 → 生成 strm/NFO/图，C 变 success 且**即刻出现在 Emby Items**；提交 ed2k 源被 400 拒
- 编辑写回：改标题后 NFO 文件变化且索引更新；reindex 幂等；未登录管理 API 被拒

**7.4 未知接口探针断言组**
- 向服务发一个未注册的 Emby 风格请求（如 `GET /Users/{uid}/GroupingOptions`、未知 POST + body）→ 返回 404 不崩溃
- 稍后 GET 探针记录能看到该 method/path/body

**7.5 可选人工冒烟（不设门槛）**
- 环境若有 Yamby：登录 → 浏览墙 → 点开 A 起播 → 退出重进续播，作额外确认

## 8. 执行顺序
P0 → P1 → P2（Emby 核心尽早可用，P2.0 契约校准先行）→ P3（Web 管理）→ P4。
