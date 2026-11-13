# Emby-go 项目速读（面向 Agent）

**读法：先读「项目模型」和「不要破坏的边界」，再按「任务导航」打开与当前任务相关的实现和测试。不需要通读源码。** 代码写法看 [AGENTS.md](<AGENTS.md>)，外部 HTTP 行为以 [API-CONTRACT.md](<API-CONTRACT.md>) 和契约测试为准，部署与使用看 [README.md](<README.md>)。

## 1. 项目模型

Emby-go 是一个面向 `.strm` + `.nfo` 媒体目录的轻量 Emby 兼容服务。`.strm` 指向远端 HTTP(S) 播放源，`.nfo` 是磁盘上的元数据真源；扫描把可浏览的数据同步进 SQLite。账号、授权、播放进度、收藏、任务配置等则由 SQLite 保存，不能靠重新扫描恢复。缺 NFO 的条目可进入待补录，不进入 Emby 播放列表。

```text
Emby 客户端 ── HTTP ──┐
浏览器管理后台 ─ fetch ─┴─ net/http.Server → Gin / server.App
                                           ├─ store → SQLite（索引、用户数据、任务配置）
                                           ├─ scanner / nfo → 媒体目录（STRM、NFO、图片）
                                           └─ scraper / scheduler / artwork 等业务模块
播放请求通过鉴权后 → 302 Location → 客户端直连真实媒体源
```

- **部署形态：**Go 单进程、单二进制、SQLite WAL；可选 Redis 缓存，未配置或不可用时可回退进程内缓存。Redis 不是多实例协调器。服务不转码，也不代理主要播放流量。
- **网络：**入站由 Gin 路由，Go `net/http.Server` 负责监听；出站客户端用标准库 `http.Client`，MetaTube、翻译、Webhook 等调用的出站策略入口见 [outbound.go](<internal/outbound/outbound.go>)。不要假定所有网络路径共用同一策略，修改时追到实际调用点。
- **前端：**原生 HTML/CSS/JS 和 `fetch()`，没有独立前端构建。资源通过 [web.go](<internal/server/web.go>) 的 `go:embed` 打进二进制；改完需重新构建并重启服务，再刷新页面。

## 2. 从入口读到任务

启动链： [main.go](<cmd/metatube/main.go>) → [配置加载](<internal/config/config.go>) → [server.New / App / routes](<internal/server/server.go>) → [store.Open](<internal/store/store.go>) 和 [数据库迁移](<internal/store/migrations.go>)。`server.App` 是 HTTP 适配、依赖组装与后台生命周期的交汇点；`store` 不反向依赖上层业务包。默认从路由表搜 URL，找到 handler，再沿调用追到实际读写点。

| 当前任务 | 首先打开 | 下一跳与测试线索 |
|---|---|---|
| Emby 接口、浏览/搜索、权限过滤 | [路由表](<internal/server/server.go>)、[API 契约](<API-CONTRACT.md>) | [列表和详情](<internal/server/library.go>)、[可见性](<internal/server/visibility.go>)；先看 [兼容测试](<internal/server/emby_compat_test.go>)、[契约测试](<internal/server/api_contract_test.go>)。 |
| 登录、会话、API 密钥、用户管理 | [鉴权与凭据范围](<internal/server/auth.go>)、[管理用户 handler](<internal/server/users_admin.go>) | [账户与会话存储](<internal/store/users.go>)、[管理凭据事务](<internal/store/admin_credentials.go>)；检查 [鉴权权威性](<internal/server/auth_authority_test.go>) 与 [密钥范围](<internal/server/api_key_scope_test.go>) 回归。 |
| 扫描、重建、增量指纹、索引清理 | [扫库入口 `scanLibraries`](<internal/server/admin.go>) | [扫描执行 `ScanWithOptions`](<internal/scanner/incremental.go>) → [扫描组提交/库存核对](<internal/store/scan_state.go>)；核对 [扫描状态测试](<internal/store/scan_state_test.go>)。 |
| 播放、续播、收藏、进度上报 | [播放信息与流端点](<internal/server/media.go>)、[用户数据 handler](<internal/server/userdata.go>) | [播放会话/进度事务 `RecordPlaybackEvent`](<internal/store/playback_sessions.go>)；注意影片可见性和播放会话绑定不是同一个检查。 |
| 刮削、翻译、人工编辑、NFO 写入 | [刮削入口](<internal/server/scrape.go>)、[翻译入口](<internal/server/translation.go>)、[编辑恢复](<internal/server/edit_recovery.go>) | [刮削应用](<internal/scraper/apply.go>)、[NFO 局部更新](<internal/nfo/update.go>)；先确认走预览、确认写入还是索引恢复，再看 [元数据锁测试](<internal/server/metadata_lock_test.go>)。 |
| 图片、海报缩略图、预热、预览图 | [图片与预热](<internal/server/media.go>)、[磁盘图片缓存](<internal/imagecache/disk.go>) | [图像任务](<internal/server/artwork.go>)、[视频缩略图](<internal/server/sprites.go>)；看 [预热测试](<internal/server/prewarm_test.go>) 与相关资源测试。 |
| 合集自动拼接封面 | [合集封面生成](<internal/server/collection_cover.go>)、[管理端点](<internal/server/collection_cover_admin.go>) | [四列拼接算法](<internal/collage/collage.go>)（[说明](<COLLECTION-COVER.md>)）、[生效记录](<internal/store/collection_cover.go>)；看 [封面测试](<internal/server/collection_cover_test.go>)。 |
| 计划任务、启动/关闭、运行状态 | [任务配置与分发](<internal/server/scheduled.go>)、[调度器](<internal/scheduler/scheduler.go>) | [App 的关闭链路](<internal/server/server.go>)、[进程退出](<cmd/metatube/main.go>)；看 [关闭测试](<internal/server/health_shutdown_test.go>)。 |
| 后台页面、请求或交互 | [后台 HTML](<internal/server/web/index.html>)、[页面与请求入口](<internal/server/web/app.js>) | [导航](<internal/server/web/navigation.js>)、[任务交互](<internal/server/web/task-runner.js>) 或对应功能脚本；新增资源需同时检查 HTML 引用、嵌入、ETag 与路由，见 [资源测试](<internal/server/web_assets_test.go>)。 |

## 3. 不要破坏的边界

- **外部契约：**Emby 兼容路由同时支持根路径与 `/emby`，并注册小写变体；后台管理面在 `/api/admin/*`。保留鉴权、中间件顺序、错误码、缓存头和兼容字段。API 密钥不能替代管理会话；流地址受鉴权，匿名图片是已明确的兼容例外。
- **文件与索引：**NFO 局部更新要保留未建模内容；改读—改—写流程前，先核对现有 NFO 写入互斥与原子替换。扫库允许部分成功，不能把不完整或过期清单当作可删除证据。涉及文件与 DB 的提交，不要把现有校验、提交、补偿/恢复拆散。
- **身份与缓存：**管理写入以当前请求凭据与数据库事务内的权限复核为准，不能用裸用户 ID 或 `UserByID` 快照替代。改缓存前核对用户、授权范围、索引版本等相关维度，避免将 A 用户可见的数据复用给 B 用户。
- **生命周期：**区分 HTTP 请求 context 和后台任务 context。退出先停止接纳/调度，HTTP drain 与后台等待共享 **15 秒**预算；不要先关数据库再等 worker，也不要绕过预算另做无界等待。
- **边界校验：**在 HTTP、配置、文件、远端响应等不可信输入处校验一次。不要为迁移测试恢复旧生产旁路。涉及出站 URL 时保留该调用原有的目标限制与重定向检查；不要把真实 token、带凭据 URL 或原始日志贴进示例和报告。

## 4. 从零验证

在仓库根目录运行。Go 版本以 [go.mod](<go.mod>) 为准；当前最低声明是 `1.27.0`。先用版本命令与内置测试确认环境，不需要启动服务、连接 Redis 或挂载真实媒体库：

```bash
# 只编译并打印版本，不读服务配置或启动监听。
go run ./cmd/metatube -version

# 轻量冒烟：兼容路由、错误信封、Web 资源。
go test ./internal/server -run '^(TestAPIContractCriticalRoutesRegistered|TestAPIContractErrorEnvelope|TestWebShellAssetsAreServed)$' -count=1

# 提交前基础门禁；低内存环境可加 -p 1，按需对改动路径加 -race。
go build ./...
go vet ./...
go test ./... -count=1
```

- **手动启动：**按 [配置示例](<config.example.yaml>) 使用临时配置、临时数据库和测试媒体目录，不复用生产数据。配置选择顺序是 `-c` → `EMBY_CONFIG` → `config.yaml`。监听端口按配置设置；默认监听所有网卡，验证时使用隔离环境。
- **前端回归：**用 `node --test internal/server/web*_test.cjs`；其中有真实浏览器场景，运行前检查本机浏览器依赖。Go 侧的 [Nov10 浏览器测试](<internal/server/nov10_browser_test.go>) 需 `NOV10_BROWSER=1`、Python Playwright 和 Chromium；未启用造成的跳过不能算浏览器验收通过。
- **CI 与发布：**核心门禁见 [CI 工作流](<.github/workflows/ci.yml>)；`v*` 标签会触发 [发布工作流](<.github/workflows/release.yml>)，不是普通测试操作。未经授权不要部署、打标签或推送。

接到任务时，先核对 `git status --short --branch`；只读对应链路和回归测试。交付时说明改了哪个边界、跑了哪些检查，以及哪些场景没有验证。
