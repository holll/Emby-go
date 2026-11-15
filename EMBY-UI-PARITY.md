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
- **筛选取值**：Emby 的筛选托盘可同时勾多个状态；我们沿用单值 `Filters`（后端目前只解析
  `IsPlayed`/`IsFavorite`），多选组合要动契约层语义，故只把「当前生效的筛选」气泡化。
- **布局与偏好**：不做 Emby 的六种布局、`usersettingsbuilder` 的 localStorage + 服务端 `CustomPrefs` 双写。
  我们的偏好只放 URL（可分享、可回退），不新增服务端用户设置存储。
- **图片比例**：Emby 按 `PrimaryImageAspectRatio` 自适应卡片；我们不返回该字段，卡片保持 2:3。

## 待评估（未承诺）

按性价比排序，尚未动手：视图切换与偏好持久化、计划任务/任务记录进度与停止、
日志历史回看（`/api/admin/logs/disk` 游标已实现、前端未接）、活动与告警流。

新增页面能力时，先在这里补「Emby 出处 + 我们的 file:line」，再动代码；不要为了像 Emby 而改
已批准的 API 契约或 NFO 真源语义。
