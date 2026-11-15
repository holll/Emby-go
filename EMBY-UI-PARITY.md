# Emby 页面与交互对照

这份文档记录我们从 Emby 源码里学到、并已经采纳到原生控制台的页面规则，以及**有意不照搬**的部分。
它不构成对 Emby 官方客户端的兼容承诺：对外 HTTP 行为仍以 [API 契约](<API-CONTRACT.md>) 为准，
本文件只约束前端页面与交互。

## 参考源码

- Emby 3.5.2 Web 控制台：`/root/src/reference/emby-3.5.2.0/MediaBrowser.WebDashboard/dashboard-ui/`
  （`itemdetails.html`、`scripts/itemdetailpage.js`、`scripts/dashboardpage.js`、`list/list.js`、`components/`…）
- 官方 REST 快照：`/root/src/EmbyRestApiDocs`（用于确认 DTO 字段，例如 `UserData.PlaybackPositionTicks`）
- `/root/src/reference/emby-last-public-master` 同目录树里多数 JS 是 0 字节空文件，**不可用**；不要拿它当依据。

Emby 前端是压缩过的 AMD 模块，引用时给到页面/脚本级位置即可，不逐行抄写。

## 已采纳的规则

| 规则 | Emby 出处 | 我们的实现 |
| --- | --- | --- |
| 有播放进度时主按钮变「继续播放」，并另给「从头播放」；续播从 `PlaybackPositionTicks` 起播 | `itemdetails.html` + `scripts/itemdetailpage.js`（Play/Resume 并存，Resume 仅进度 > 0） | `internal/server/web/emby.js` 详情按钮区 + `app.js` `openPlayer(item, { startTicks })` |
| 播放器叠在详情之上，关闭后用户状态（进度/已看）回到最新 | Emby 详情页不销毁，靠 `UserDataChanged` 刷新按钮 | 详情抽屉不关；`closePlayer` 触发 `onClosed` → 重读 `/Items/{id}/UserData` |
| 排序拆成「字段 + 方向」两个维度，方向可回显 | `sortmenu.template.html`（SortBy/SortOrder 两个下拉） | 媒体墙 `#item-sort` + `#item-order`，方向写进 URL `order` |
| 卡片「已看」是圆底对勾，进度条细条贴底 | `librarybrowser` 卡片模板（`.cardIndicators` + `.itemProgressBar`） | `wallCard` 的 `wall-badge played`（`icon('check')`）+ `.wall-progress` 4px |
| 概览显示正在播放的会话（谁、在播什么、播到哪） | `dashboard.html` 会话区 | `sessionsPanel()` + `GET /Sessions`（10 秒轮询，切页自停） |
| 详情背景图回退：Backdrop → Thumb → Primary | `itemdetails.html` 背景链 | `emby.js` `backdropKind` |
| 卡片操作收进悬浮菜单，右键唤起同一份菜单 | `librarybrowser` 卡片模板（hover FAB、右键菜单） | `app.js` `openCardMenu`：`[data-more]` 按钮与 `#wall` 右键共用一份菜单，支持方向键/Esc/焦点归还 |
| 概览显示最近播放活动（用户、影片、次数、UTC 时间），入口到完整记录 | `dashboard.html`「用户活动」limit 7 | `app.js activityPanel` + `#overview-activity`，数据走 `/api/admin/playback-events` |
| 媒体墙布局引擎：海报/海报卡片/缩略图/缩略图卡片/列表，偏好存浏览器本地、不进 URL | `librarybrowser.js showLayoutMenu`（List/Poster/PosterCard/Thumb/ThumbCard） | `app.js WALL_LAYOUT_TEXT` + `wallCard(item, layout)` + `repaintWall` + `#item-layout`，localStorage `emby_wall_layout` |
| 筛选按「播放状态」「收藏」两个维度同时生效，URL 与气泡可回显 | `filtermenu`/`filterdialog` 多选，`Filters` 逗号列表（AND） | `app.js` 的 `status`/`favorite` 参数 + `wallEmbyQuery` 拼 `Filters=IsFavorite,IsPlayed` |
| 概览显示运行中的任务（类型、已运行时长、查看跳转） | `dashboard.html` 的「运行中任务」区 | `app.js tasksPanel` + `#overview-tasks`，数据走 `/api/admin/tasks` 的实时并集 |
| 运行日志提供「历史」模式：按游标分页读磁盘 app 日志，暂停实时尾随，可返回实时 | `log.html`（日志文件列表，新窗口打开） | `log-stream.js` 的 `#dl-history` + `loadHistory`，走 `/api/admin/logs/disk` |
| 计划任务列表就地显示运行态与已运行时长，有取消端点的类型可直接停止 | `scheduledtaskspage.js`（Running 进度条/百分比、Idle 显示上次结果） | `pageScheduled` 的 `data-scheduled-state/elapsed` + 5 秒轮询；停止复用 `/probe/media/cancel`、`/scrape/cancel` |
| 计划任务页支持「表格 / 卡片」两种布局，偏好存浏览器、不进 URL | `scheduledtaskspage.js` 本身就是卡片列表 | `pageScheduled` 的 `#sched-layout` + `scheduledRow/scheduledCard`，localStorage `emby_scheduled_layout` |
| 当前筛选显示成一排可移除气泡 | `filtermenu` 的已应用筛选 chip 行 | 媒体墙 `#wall-active-filters`：状态/媒体库/搜索各一枚，点 × 只清自己那一维并把焦点还给对应控件 |

## 有意保留的差异

- **详情形态**：Emby 是整页 `itemdetails.html`，我们是 560px 侧抽屉。抽屉与既有浮层栈、焦点归还、
  键盘操作（`web_layout_test.cjs` 的详情外壳用例）绑定，改整页收益低、回归面大。
- **视频信息只读**：Emby 的音轨/字幕下拉需要 `MediaSources` + 可播判断；我们按 API 契约只展示
  `MediaStreams`，不提供切换。字幕 `Stream.Format` 等字段仍受探测模型限制（见
  [EMBY-COMPAT-AUDIT.md](<EMBY-COMPAT-AUDIT.md>) §0.2）。
- **元数据编辑**：Emby 是独立弹窗 + 左树，且 lock 语义与我们相反（Emby 勾选 = 允许刷新，我们勾选 = 锁定）。
  语义已按本项目 NFO 真源模型固定，不追随 Emby。
- **列表加载**：Emby 库页是显式 `1-100 of N` 分页；我们用无限滚动。接口侧 `StartIndex/Limit`
  已支持，需要时可加「页码」而不改 DTO。
- **活动流**：只聚合播放记录摘要（同用户同影片合并），没有 Emby 的告警、新闻、设备活动流；
  首页保持只读，时间按 UTC 原文显示，完整记录在防火墙页。
- **布局引擎**：只做 Emby 的五个视图（Poster/PosterCard/Thumb/ThumbCard/List），不复制六种布局之外的
  自适应密度；偏好只存浏览器 localStorage（换设备不同步），排序/筛选仍走 URL 以便分享。
- **运行中任务**：Emby 靠 websocket 增量推进度；这里按 10 秒轮询刷新，进度只显示已运行时长，
  不伪造百分比（真实进度仍在任务详情/扫描快照里）。首页保持只读，停止入口留在计划任务页与任务中心。
- **日志历史**：只对 `app.log` 开放（`requests.log` 可能含敏感 URL，磁盘接口本身也不暴露它），
  历史与实时是两种互斥视图，进入历史会断开 SSE，避免两个来源抢同一个 pre。
- **任务取消**：扫库/重建索引没有中途停止语义（部分结果会落库），翻译/雪碧图在各自页面停止；
  计划任务页只对探测、刮削提供「停止」，避免给出一个「点了不一定停」的按钮。
- **筛选多选**：把 Emby 的多选托盘收敛成「播放状态 + 收藏」两个正交维度，底层仍是单次
  `Filters` 逗号列表（AND）；不做「已看 + 未看」这种互斥组合，也不开放任意 ItemFilter。
- **计划任务布局**：Emby 只有卡片一种形态，我们保留表格（便于横向比对 cron 与状态）再加卡片，
  两种视图共用同一组 `data-scheduled-*` 锚点，所以运行态、停止和轮询不需要写两遍。
- **布局与偏好**：不做 `usersettingsbuilder` 的 localStorage + 服务端 `CustomPrefs` 双写。
  我们的偏好只放 URL（可分享、可回退），不新增服务端用户设置存储。
- **图片比例**：Emby 按 `PrimaryImageAspectRatio` 自适应卡片；我们不返回该字段，卡片保持 2:3。

## 待评估（未承诺）

暂无未动手项。活动流只做「最近播放」摘要；Emby 的告警/新闻/设备活动流、TV 录制等不在本项目范围内，
管理写操作走审计事件接口，不混进首页。

新增页面能力时，先在这里补「Emby 出处 + 我们的 file:line」，再动代码；不要为了像 Emby 而改
已批准的 API 契约或 NFO 真源语义。
