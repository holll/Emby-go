# PLAN-STATUS — 施工状态与放行台账

> 唯一进度台账。方案见 [dev-plan.md](dev-plan.md)，逐批细节见 `docs/implementation-progress-2026-10-16.md`（内部记录，不入库）。
> 状态口径：**完成 = 本地实现 + 本地回归通过**；不代表生产放行。生产放行必须同时满足 A01–A15 全过（部署环境回填）、D01–D12 已批并落地、相关条目关闭。

最后更新：2026-10-18（Asia/Shanghai）
基线提交：`1b93a06`（40 批 checkpoint）；批次 41 `3499029`；批次 42 起见 git log

## 0. 本地基线（2026-10-17 实测）

| 项 | 结果 |
|---|---|
| `go build ./...` | 通过（Go 1.27.2，go.mod 声明 1.27.0） |
| `go vet ./...` | 通过 |
| `go test ./...` | 16 包全绿；447 个 Test 函数（server 38.2s / store 16.2s）；3 个实时验收用例未设 env 时跳过 |
| `node --test internal/server/web_test.cjs` | 86/86 通过 |
| 测试形态 | httptest + 内存库为主；真实浏览器（Playwright 三引擎）与真实二进制冒烟已做；仍无真实 Emby 客户端、生产库、真机 |
| CI | go / vuln / web 三 job；批次 89 的 push 已触发，最近一次全绿见仓库 Actions；门禁含 gofmt、build、vet、test、三包 race、govulncheck、前端 VM |
| 生产放行 | **否**。部署级人工验收（A01/A04/A06/A08/A10/A11/A12/A14）未执行；D01–D12 已全部批准并落地，公网门禁另需按 D04 配置 `setup_code`/来源白名单/可信代理与 TLS |

## 1. 审查 35 项状态

图例：✅ 本地完成 · ◐ 部分 · ○ 未开始

| 编号 | 问题 | 状态 | 批次 | 剩余边界（摘要） |
|---|---|---|---|---|
| BE-01 | 会话无有效期、Logout 不撤销 | ◐ | 2,3,64 | 已实现 expires_at（迁移 3，旧行回填 created_at+30 天）、30 天滑动续期、撤销当前会话与撤销全部会话（管理 API+设置页）；剩余：无 revoked_at 撤销清单、在途请求/长流/已发直链不追撤、无强杀/掉电演练 |
| BE-02 | 播放 key 与管理会话无权限隔离 | ✅ | 48 | D02 具名接受：API key 即管理员凭据，UI/README 明确标注；无 scope 执行本身；多用户版本需重开 |
| BE-03 | 日志脱敏不完整 | ◐ | 16,28,32 | 仅请求诊断/debug/NoRoute/密钥事件、认证入口、启动自检；上游 URL/错误、媒体 path、子进程输出、历史日志未审计 |
| BE-04 | 退出未等后台任务闭环 | ◐ | 3, 49 | 已实现 SIGTERM/SIGINT → readiness 立即失败、scheduler.Stop、探测/刮削取消、workerWG 有界 join、HTTP Shutdown 与后台 drain 共享 15s 预算、超时强制退出且不先关 DB；剩余：长流不撤销（D09 设计）、部署侧 systemd/存活探针/30s 宽限与真实客户端 drain 未演练 |
| BE-05 | 请求预算与登录限流 | ◐ | 41 | 读超时、body 413、登录/初始化 429、不信任代理头已做；初始化验证码/来源白名单/可信代理已由 D04（批次 65）落地；残余：chunked 超限文案仍为 400、限流单进程内存态、代理后 IP 共享、TLS 由部署侧承担 |
| BE-06 | Redis 错误不可见 | ◐ | 2 | 仅认证不再读写正缓存；Cache 接口无 context/error；Redis 必选政策未改 |
| BE-07 | 长同步扫描与进程内任务状态 | ◐ | 19,22,23,33,37-39, 50 | 批次 50 完成扫描异步任务表（`POST /api/admin/tasks/scan` → 202+task_id，`GET /api/admin/tasks/:id`，Idempotency-Key 1h 重放，容量 200，满容只淘汰已完成，重启即丢）；剩余：旧同步 `/scan` 保留至验收集、探测/刮削状态未并入统一任务表、无异步取消端点、UI 未迁移到 task_id 轮询 |
| BE-08 | NoRoute 去重 map 无界 | ◐ | 16,20,21,47 | 128 上限 + 6h TTL + LRU 淘汰 + 写库令牌桶（burst 60、平均 1 QPS）；清空探针已重置 seen/epoch；跨进程不共享；真实唯一路径洪水未压测 |
| BE-09 | 出站信任边界与响应头预算 | ◐ | 42 | 新增 internal/outbound：协议白名单、DNS 解析+拨号地址校验并钉 IP、重定向 ≤3 跳且逐跳复检、响应头 512 KiB、拨号/握手/响应头/连接预算；元数据/翻译/播放代理已接入。残余：代理模式只能校验请求目标、无法钉住代理实际连接地址；私网例外按主机名而非完整可信源策略；ffprobe 子进程出站未纳入；无真实 NAS/签名 URL/HEAD-Range/并发压测；D04 已批（批次 65） |
| BE-10 | App/handler 编排过重 | ○ | — | 仅抽公共 helper；Auth/Task/Library/PlaybackService 未抽；属 R3 |
| DB-01 | 迁移吞错且缺版本台账 | ◐ | 1 | 台账/校验和/同事务 DDL/未知结构拒绝已实现；支持版本仅 1，无真实历史 fixture；空间不足/硬退出/WAL 备份/恢复演练未做，**不得标完成** |
| DB-02 | 播放计数读改写竞争 | ◐ | 51 | 批次 51 完成：迁移 2 建 `playback_sessions`/`playback_events`（会话键=PlaySessionId，缺失回退 `item:<id>`），`last_event_seq` 单调序号；`RecordPlaybackEvent` 单事务写会话+事件+userdata，stopped 仅 `stopped=0→1` 原子 +1，重复 Stopped/重放/重启后重放幂等，停止后迟到 Progress 忽略；事件表保留最近 2 万条。剩余：无 PlaySessionId 的旧客户端需先发 Playing 重开才能再计一次；无观看时长统计与按会话查询 API；`SavePlayback` 仍被旧测试/夹具使用 |
| DB-03 | 业务提交与缓存版本分离 | ◐ | 9-15,24,25,27,34 | 已覆盖 metadata/删除/清理/manual/相似/图片/userdata/设置/played/头像；播放进度、Stopped/心跳、扫描检查点未合并；跨进程版本镜像未做 |
| DB-04 | JSON 筛选与包含搜索 | ◐ | 81 | 已量化不引入 FTS5/关系表：5k 库 SQL 层 1–3ms；50k 行（复制行）标题 `%term%` 14.8ms、COUNT 18.4ms、深页 4.0ms；Go 层 `BenchmarkSearch*` 3.8–6.5ms/次（5k，含 100 行解码）；结论 `%term%`/JSON/演员 EXISTS 原语义保留，50k 真实库或延迟超预算再评估 FTS5 trigram/feature 表 |
| DB-05 | COUNT/宽投影/深 OFFSET | ◐ | 80,83 | 批次 80 建部分索引 `idx_movies_visible_title`/`idx_movies_visible_library_title` 并在迁移 6 末尾 `ANALYZE`；实测 5,000 部库：全局首页 1.57→0.05ms、深页 OFFSET 2000 3.08→0.16ms、单库 0.05ms，`EXPLAIN` 无临时排序；COUNT 仍走 `idx_movies_status`；轻量投影、keyset/深 OFFSET 上限未做 |；批次 83 复测 50k 行：宽投影 200 行 3.44ms vs 窄投影 0.32ms，`OFFSET 20000` 8.13ms，`COUNT(*)` 10.2ms；Go 层 `BenchmarkSearchAdminWidePage200` 3.19ms/615KB/13k allocs。结论：列表 DTO 含 Overview 等宽字段，做投影会改客户端可见契约；深 OFFSET 有复合索引 0.16–8ms，加硬上限会破坏深分页语义，暂不引入；保留基准防回退 |
| DB-06 | 演员反向索引缺口 | ◐ | 80 | 批次 80 建 `idx_movie_actors_actor_movie(actor_name,movie_id)`；种子实验（200 演员 / 9,950 关系）`ActorsMissingAvatar` 9.01→0.96ms；`MoviesByActor` 计划不变（movies 驱动，2.2ms），仍靠主键探测；未在真实刮削库测写放大 |
| DB-07 | context 与锁/池/WAL 观测 | ○ | — | QueryContext/BeginTx、DB.Stats、busy/WAL 指标均无；单写队列未决 |
| DB-08 | 逐特征 SQL 与分块 IN | ◐ | 29,30,31 | DataFor/ActorsFor 已分块；逐来源特征判定与 MoviesByIDs 未统一 |
| DB-09 | 影片与演员分事务提交 | ✅ | 10-13 | 影片+演员+特征+双库版本同事务；NFO 与 SQLite 非联合事务 |
| DB-10 | 候选全量加载/回填/JSON 诊断 | ◐ | 82 | 批次 82：刮削候选改 200 部 id 游标分批（`MoviesForScrapeBatch`），跨批共享熔断/进度，峰值内存不再随库大小增长；特征回填暴露 `done/total/running`，Close 先取消再等待；`CorruptedJSON` 定位损坏 JSON（影片+列），`GET /api/admin/recovery` 增 `corrupt_json_total/corrupt_json`、`settings` 增 `features_backfill`。边界：单条手动路径仍传切片、损坏列表无 UI 修复动作、回填无重试计数/进度持久化 |
| FE-01 | 计划探测“全部”退化 | ✅ | 5 | 前后端须同批发布；真实计划触发/浏览器未验 |
| FE-02 | 字段不能清空却报成功 | ✅ | 7 | 多管理员无修订条件；NFO gate 非跨进程 CAS；R3 持久意图未做 |
| FE-03 | 卡片键盘事件截获按钮 | ◐ | 17 | 仅事件目标即卡片本身才处理；role=button 仍嵌套按钮；读屏/焦点未验 |
| FE-04 | 遮罩焦点生命周期缺失 | ◐ | 批次 46 | 统一遮罩栈：z-index 顺序决定 Esc、Tab 在顶层遮罩循环、打开入栈/关闭恢复触发焦点、遮罩容器与 dialog 角色/tabindex；inert 与真实读屏未做 |
| FE-05 | 网络错误被当登出/任务结束 | ◐ | 2,19,22,23,26 | 401 才清凭据；三条 GET 已单飞+代次+退避；POST/设置/登录无预算；无持久 task_id |
| FE-06 | 小字对比度不足 | ◐ | 18 | --dim 38%→60%，静态断言 6.31–7.37:1；非渲染器，未覆盖渐变/图片/像素采样 |
| FE-07 | 全幅播放层覆盖动作 | ◐ | 78 | Chromium 1280×800 + 390×844 触屏模拟：播放层打开时卡片命中区 `elementFromPoint` 落在层内、点 stage 不关闭、背景点击/触摸关闭、关闭后命中区恢复、详情播放同样覆盖；`overlayTrapTab` 改受控循环（Tab/Shift+Tab 不再落回 body）；真机触屏未验 |
| FE-08 | 后置同选择器覆盖移动规则 | ○ | — | 基础/断点顺序未调整；小屏计算样式未测；R3 |
| FE-09 | 登录重 render 丢输入 | ◐ | 批次 44 | 保留节点，错误/忙碌只做局部更新；失败聚焦密码；初始化密码策略提示。真实浏览器、密码管理器、读屏未验 |
| FE-10 | 筛选缺标签、开关空 label | ◐ | 45 | 筛选/排序/媒体库控件补可访问名称、筛选与库 Tab 补 aria-pressed、编辑展开补 aria-expanded、计划任务开关补名称；a11y tree 与真实读屏未验 |
| FE-11 | 每页 100 追加 DOM | ◐ | 79 | 30 页实测（5000 部库）：3,100 卡 / 74,533 DOM 节点 / JS 堆 9.5MB / 总加载 29.9s，无控制台错误；结论暂不回收/窗口化（5,000 卡预估 ~12 万节点），5k 以上再评估「加载更多」；图片预算：墙上限 360→`srcset` 200/400，详情海报 240（1x 120）、背景 1080（1x 540），详情海报加 `width/height` 防 CLS；真机与低端机未测 |
| FE-12 | boot 串行、概览拉整页 | ◐ | 22,23 | 已去 boot 裸 probe/scan 首读；聚合计数、按需加载、CWV 未做；R4 |
| FE-13 | NFO 落盘后读库/重扫失败 | ◐ | 4,13,26,33,36 | saved/indexed/warnings 已实现；无持久操作意图/启动对账（R3）；文件与 DB 非联合事务 |
| FE-14 | 编辑重建 params 丢参数 | ◐ | 6,8 | 条件更新已做；toggle/delete 仍非条件；未知扩展键不保留；无草稿持久化 |
| FE-15 | 忙碌不一致、cron 校验乱序 | ◐ | 77 | 单次提交（请求期间按钮 disabled、重复提交忽略）；cron 校验 latest-wins（序号+表达式复核，旧响应不覆盖）；校验接口返回 `time_zone`，UI 明示按服务器时区执行；真实触屏/跨时区设备走查未做 |

统计：✅ 4 · ◐ 21 · ○ 10（BE-05 批次 41、FE-10 批次 45 转为 ◐）。

## 2. 决策台账 D01–D12（2026-10-18 全部批准）

已批准项按建议默认值执行；实施细节见 §1 与 §6，历史提案已清理（§2.1）。

| ID | 待定项 | 卡住 |
|---|---|---|
| D01 | ✅ 已批（2026-10-18）：会话 TTL 30 天滑动（迁移 3 增 `expires_at`）；logout 撤销当前 token；管理页可撤销全部会话；API key 不设过期但可撤销；升级时旧 token 统一补 30 天期限，不追溯吊销 | BE-01 完整化（批次 64 已落地） |
| D02 | ✅ 已批（2026-10-17）：API 密钥=管理员全权凭据，具名接受风险；未来多用户时非管理员不得建 key；图片保持匿名。不实现 scope 隔离 | BE-02 以风险接受关闭 |
| D03 | ✅ 已批（2026-10-18）：密钥摘要使用 HMAC-SHA256 + 服务端 secret（迁移 3 保留旧明文列一个版本，双读窗口后可删）；管理面只展示前缀；旧 fixture 同步更新 | DB-01 放行、key 摘要迁移 |
| D04 | ✅ 已批（2026-10-18）：单实例单写、Redis 必选、反代终止 TLS、默认不信任 XFF、初始化仅回环/显式 CIDR、预算沿用批次 41/42；公网仍需 A03/A14 + D01 落地 | 已落地（批次 65）：`setup_code`、`initialize_allow_from`、`trusted_proxies`、README 公网门禁；**未把“仅回环”设为默认**（LAN 初始化仍可用，启动时警告） |
| D05 | ✅ 已批（2026-10-18）：仅审计本仓库/本机历史日志与备份；发现明文凭据即轮换并记录；不追溯第三方代理留存 | ✅ 已落地（批次 68，审计记录见 §2.2）：未发现真实凭据，上线前轮换 checklist 已列 |
| D06 | ✅ 已批（2026-10-18）：版本域含 userdata/库/图片指纹；缓存键带版本；缺版本视为脏；读己之写窗口 ≤ 500ms | ✅ 已落地（批次 67）：全部可见变更推进 `g:version`，版本化键即时换域；无版本键不命中；单进程读己之写即时 |
| D07 | ✅ 已批（2026-10-17）：新建播放会话/事件表与序号协议；DB-02 已按此落地（批次 51）：会话内永久去重、停止后心跳忽略、旧 `/Sessions/Playing*` 协议映射同一写链 | DB-02 完成（批次 51） |
| D08 | ✅ 已批（2026-10-18）：NFO 为人工编辑真源；索引失败给恢复入口与 7 天提示；锁字段阻止自动覆盖；备份含 NFO 与数据库 | ✅ 已落地（批次 69）：迁移 5 `edit_failures`，设置页「索引恢复」列表+超 7 天提示；成功重索引自动清除；NFO 真源/锁字段此前已实现；备份含 NFO 随 D11 |
| D09 | ✅ 已批（2026-10-17）：SIGTERM 即 readiness=false，15s 总预算，长流不撤销，部署宽限 30s | BE-04（★） |
| D10 | ✅ 已批（2026-10-17）：进程内任务表、202+task_id、1h 幂等、容量 200、重启不恢复、旧 /scan 保留至验收集通过 | BE-07、R3 |
| D11 | ✅ 已批（2026-10-18）：每日自动备份数据库+NFO、保留 7 份；RPO 24h/RTO 1h；恢复后需重置管理员密码（凭据不入备份） | ✅ 已落地（批次 70）：`cmd/embybackup` 一致性快照+NFO+保留清理；README 恢复步骤；定时执行由部署者配置 |
| D12 | ✅ 已批（2026-10-18）：浏览器矩阵 Chromium/Firefox/WebKit + iOS Safari/Android Chrome 抽测；性能基线 p95 列表 ≤ 300ms、详情 ≤ 150ms、扫描 5000 项 ≤ 60s；验收人=项目所有者 | ✅ 已落地（批次 71）：矩阵与阈值已固化并有 A13/A15 证据；真机抽测仍属部署验收 |

### 2.1 提案存档

D01–D12 已于 2026-10-17/18 全部批准并落地，原提案正文与上表批准结果重复，已清理；执行细节见 §1 与 §6。

### 2.2 D05 历史凭据审计记录（2026-10-18，批次 68）

范围：本仓库全部提交历史与当前工作区的配置/数据/日志文件（D05 默认值：仅审计本仓库与本机历史日志、备份；不追溯第三方代理留存）。

结果：
- 当前工作区无 `config.yaml`、无本地 `*.db`/`*.log`；被忽略的只有 `docs/`（内部文档，已知无凭据）。
- 提交历史中 `config.yaml` 曾进入仓库（`ed8470c`/`0013478`），两个版本均只含 `redis_password: ""`，无真实口令；随后 `b5e2b48`/`9fb4076` 已移除并改为 `config.example.yaml`。
- 当前跟踪文件中无数据库、日志、cookie、密钥文件；`config.example.yaml` 的 `redis_password`/`setup_code` 均为空占位。
- 历史 diff 里出现的 `api_key=secret`、`private-password-canary` 等全部是测试夹具字符串，不是真实凭据。
- `/tmp/acc`、`/tmp/perf` 验收实例已不存在；即使存在也只含合成账号（`admin/acceptance-pass-123`）与合成媒体数据。

处置：无泄露证据，但按「已泄露/待轮换」口径执行上线前 checklist——部署时重新生成管理员密码与 API 密钥、用 `DELETE /api/admin/access-tokens` 撤销全部会话、确认请求日志 90 天且不含凭据、不把日志投递第三方。无代码变更。

## 3. 验收 A01–A15

**生产部署验收未执行**；自动子集与真实二进制冒烟已用假 Redis+合成媒体本地跑通（见 §3.1），不等于部署验收。验收定义与断言见 `docs/architecture-database-frontend-review-2026-10-16.md` §9；执行时在此逐项回填：命令、环境、原始输出路径、执行人、日期。未回填即视为未通过。

批次 83 后在当前构建（`0056730`）复跑真实二进制：`TestAcceptanceLive`、`TestAcceptanceConcurrentPlayback`、`TestAcceptanceScanTaskAPI`、`TestAcceptanceManualEdit`、`TestAcceptanceSessionRevoke` 全部 PASS（假 Redis + 合成媒体）。

自动子集（批次 52 新增，`internal/server/acceptance_test.go`）：

```bash
ACCEPTANCE_BASE_URL=https://<host> ACCEPTANCE_USER=admin ACCEPTANCE_PASS=... \
  go test ./internal/server -run TestAcceptanceLive -v
```

覆盖：A03 未认证 401 + 登录；A05 双会话各计一次、重放/迟到心跳不改计数（库为空时跳过）；A11 `/api/health` 200；A14 Content-Length 超限 413。

| 项 | 方式 | 前置/环境 | 证据 |
|---|---|---|---|
| A01 认证 | 自动（撤销 `TestAcceptanceSessionRevoke`+过期单测）；HTTP 级可控时钟仍待补 | D01 已落地（批次 64） | 单测/验收名：`TestAccessTokenExpiryValidation`、`TestAcceptanceSessionRevoke`；部署级人工项未做 |
| A02 旧缓存 | 自动（旧二进制对照，批次 58） | 旧提交 `9c4fcb0` worktree | `docs/..` LEAD 记录：删 token 后旧二进制 200 / 当前 401；Redis 故障注入仍未做 |
| A03 迁移/UI/权限 | 自动子集（401/登录）**本地通过**；人工（旧库升级、key UI）待做 | 旧版本 DB fixture、浏览器 | acceptance 输出 + 截图 |
| A04 原子/版本 | 自动（store 用例）+ 人工未做（提交后杀进程） | 可注入失败的 commit 屏障 | 测试：`TestUserSettingsAtomicAPI`、`TestAdminDeleteAtomicFailureAndRetry`、`TestSaveMediaInfoAtomic`；杀进程演练待部署 |
| A05 播放 | 自动（acceptance + 单测）**本地真实二进制通过**（双会话/重放/迟到心跳） | 已初始化实例、≥1 部影片 | acceptance 输出 |
| A06 文件/索引 | 人工（NFO 失败/部分成功） | 可写失败目录（只读卷） | 日志 + 文件快照 |
| A07 扫描 | 自动（scanner 回归）+ 人工（断源/删除前取消） | 可断源目录、SIGTERM 时机 | 测试名 + 日志 |
| A08 迁移 | 自动（migrations_test 篡改/漂移）+ 人工（只读卷） | 每个支持版本 fixture | 测试名 + 输出 |
| A09 恢复/回退 | 人工（批次 56 本机演练） | T0 备份 + 当前二进制 + 假 Redis | 记录：SIGTERM 52ms；备份 172K；删库后 initialized=false；还原 1.2s 可服务；标题/PlayCount/PositionTicks/NFO 保留 |
| A10 任务 | 自动（`scan_tasks_test.go` 6 组）+ 人工 kill -9 未做 | 真实进程 kill -9 | 单测/实跑：202/幂等/409/容量/1h 过期；崩溃不恢复语义未演练 |
| A11 退出 | 单测 + 自动（health）；**本地 SIGTERM 实跑通过**；503 窗口/长流/超时路径待演练 | 部署侧 30s 宽限 | 测试名 + 演练记录 |
| A12 跨层兼容 | 自动（契约测试 + Playwright 页面/axe）+ 人工旧客户端未做 | 旧版 Emby 客户端 | `contract_test.go`；批次 59 axe 0 violations；真实旧客户端抓包待做 |
| A13 浏览器/a11y | 自动（Playwright 三引擎巡检，批次 60） | D12 矩阵（Chromium/Firefox/WebKit） | 7 页面 ×3 引擎 0 console error/0 axe/0 移动溢出；键盘流程 `keyboard.cjs`；截图 `docs/screenshots/` |
| A14 入口/资源 | 自动（413/401/健康 + 登录限流 429）本地通过；慢头/大 header 压测未做 | 压测工具、代理拓扑 | `limits_test.go`、acceptance 输出；慢头依赖部署侧代理 |
| A15 性能/语义 | 自动（批次 57 固定参数） | D12 阈值、同参数前后采样 | 5000 项扫描 11.0s；列表 Limit=50 p95 17.2ms、Limit=200 p95 49.7ms；详情 p95 7.1ms；RSS 47.5MB；`docs/acceptance/bench.cjs` |

> 自动化只证明协议/计数层；D12 已批并落地，A13/A15 的自动部分已有证据，真机与部署验收仍需执行。

### 3.1 本地真实二进制冒烟（2026-10-17，批次 53）

环境：本机 Go 1.27.2；`cmd/metatube` 编译后以临时 `config.yaml`（端口 18099、临时 SQLite）启动；Redis 由**仅验收用**的 RESP 假服务替代（支持 HELLO3/CLIENT/PING/GET/SET/DEL/SCAN，未入库）；媒体库放合成 `.strm`+`.nfo`。

结果：

- `ACCEPTANCE_BASE_URL=http://127.0.0.1:18099 … go test ./internal/server -run TestAcceptanceLive -v` → **PASS**（0.09s）；服务端日志可见 401/413/登录/两次 Playing+Stopped/迟到 Progress 的实际请求序列。
- A03 未认证 401、A14 1 MiB+ → 413、A11 `/api/health` 200 均在真实 HTTP 栈上验证。
- A05：两个 PlaySessionId 同位置各计一次 → `PlayCount=2`；重放 Stopped 与迟到 Progress 后仍为 2（真实二进制+SQLite，非 httptest）。
- A11 退出：`kill -TERM` 后日志「收到退出信号：开始有界关闭 budget=15s」→「已退出」，进程正常退出（未触发 15s 强制路径）；readiness 503 窗口需并发压测复核。
- A06 子集（批次 55）：8 个会话对同一影片并发 `Stopped`（各自 PlaySessionId）→ 全部 204，`PlayCount` 精确 +8；真实 SQLite 写事务无丢更新/500。
- A10 子集（批次 55）：`POST /api/admin/tasks/scan?library_id=1` + `Idempotency-Key` → 202+`task_id=3`；轮询状态到 `succeeded`；同 key 重放 → 202、`replayed=true`、同一 `task_id`。
- A07 子集（批次 56）：`PATCH /api/admin/items/1` `{"title":"Acceptance Edit …"}` → 200 `saved:true`；`a.nfo` 实际写入新标题；再触发扫描并等待终态后详情标题仍为人工值（未被旧 NFO 回写）。
- A09 恢复演练（批次 56，本机）：SIGTERM 52ms 退出 → 备份 172K（DB+NFO+config）→ 删除 DB 重启得 `{"initialized":false}`（确认丢失）→ 还原后重启 1.2s：`initialized:true`、登录成功、标题/`PlayCount=12`/`PositionTicks=300000000` 全部保留、NFO 一致。
- A12 UI 冒烟（批次 59，Playwright/Chromium 156 headless，1280×800 + 390×844）：登录 → 媒体墙（100 卡）→ 搜索「Perf Movie 42」命中 → 详情抽屉打开且首焦点在抽屉内 → 连按 8 次 Tab 焦点仍在抽屉（焦点圈）→ Esc 关闭后焦点回到卡片标题按钮；控制台 0 error；截图 `docs/screenshots/01-login..05-mobile.png`，脚本 `docs/acceptance/ui.cjs`。
- A12 跨浏览器页面巡检（批次 60，Chromium/Firefox/WebKit × 7 页：总览/媒体库/媒体墙/计划任务/接口探针/刮削/设置）：三浏览器全部 0 控制台错误、移动视口横向溢出 0px；Chromium 逐页截图 `docs/screenshots/chromium-*.png`。纯键盘流程（批次 60）：autofocus 账号 → Tab 密码 → Enter 提交 → Tab 到「媒体墙」Enter → Tab 到标题按钮 Enter → 详情抽屉显示对应影片，全程无鼠标、0 error。
- A12 功能级断言（批次 61，`docs/acceptance/func.cjs`）：设置页创建 API key→用该 key 调 `/api/admin/settings` 200→UI 删除→同 key 立即 401；计划任务新建→列表出现→删除；详情「编辑元数据」改标题→保存后抽屉关闭、墙面卡片标题更新、重开详情仍为新标题（NFO 已写）；快捷播放按钮打开播放器浮层（标题=影片名）并可关闭；全程 0 error。截图 `docs/screenshots/func-detail-edited.png`。
- 依赖漏洞扫描（批次 63）：`govulncheck ./...` 升级前命中 9 个调用链（x/net v0.25.0 ×8、x/text v0.15.0 ×1）；升级 x/net 0.60.0 + x/text 0.42.0（连带 x/crypto 0.57.0、x/sys 0.48.0）后 **调用链 0 命中**；CI 新增 `vuln` job 作为门禁。首次 CI 实跑显示 Go 1.26.0 标准库本身有漏洞（annotations 12 条指向 `http.Header.Set/Get/Del`、`http.Client.Do`），据此 go.mod 上调 `go 1.27.0`。
- A15 混合负载（批次 62，`docs/acceptance/mixed.cjs`）：5000 项全库扫描进行中，12 个并发 worker（列表×5/详情/并发播放停止×2）持续 12s：共 5930 请求、**0 错误**、494 rps、p95 55ms、p99 94ms、max 594ms；扫描最终 success=5000。
- A13 可访问性（批次 59/60，axe-core WCAG 2.1 A/AA）：修复 2 个 serious（100 节点 `nested-interactive`、100 节点 `label-content-name-mismatch`）与 1 个 critical（刮削页 `<select id="sc-lib">` 缺可访问名，补 `aria-label="媒体库"`）；复跑 **Chromium/Firefox/WebKit × 7 页全部 0 violations**；卡片改为「非交互容器 + 独立标题按钮」，内部按钮保持原生语义；脚本 `docs/acceptance/axe.cjs`、`pages.cjs`。
- A02 旧正缓存回归（批次 58，本机双二进制对照）：旧提交 `9c4fcb0` 登录→删 `access_tokens` 全部行→重启→旧 token 仍 **200**（Redis 正缓存在重启后继续授权）；当前二进制同流程→**401**。证明 BE-06 取消认证正缓存后的失效路径生效。
- A15 性能冒烟（批次 57，本机，合成 5000 部）：同步扫描 5000 项 **11.0s**（success=5000）；keep-alive 10 并发：列表 `Limit=50` p50 3.4ms/p95 **17.2ms**/p99 66ms、1639 rps；`Limit=200` p95 49.7ms；详情 p50 3.2ms/p95 **7.1ms**/p99 12.7ms；进程 RSS 47.5MB、DB 1.6MB；0 错误。均远低于 D12 建议阈值（列表 p95<300ms、详情 p95<150ms、内存<300MB）。

未覆盖：生产 Redis 语义/网络、真实 Emby 客户端、TLS/反代、真实媒体文件、更大并发与压测（8 并发只覆盖计数写链）、Go 1.25 工具链、任务表重启丢失语义的演练、异地备份传输与损坏库修复、真实网络与多客户端混合负载。以上仍属「未执行」。

## 4. 阶段 R0–R4

| 阶段 | 判断 | 说明 |
|---|---|---|
| R0 基线 | ◐ | 测试/vet/race 基线与 CI 已建并在 GitHub 实跑通过（run 39086840621）；A09 本机备份恢复演练通过（批次 56）；生产拓扑、观测、异地备份未做 |
| R1 认证 | ◐ | BE-01 会话期限/撤销全部（批次 64）、BE-02 风险接受关闭、D03 key 摘要迁移（批次 66）已落地；BE-03/06 仍局部；A01/A02 自动部分已过，其余部署级项未执行 |
| R1-D 部署门禁 | ◐ | BE-05 批次 41、BE-09 批次 42 已建默认收紧；D04 已批并落地初始化验证码/来源白名单/可信代理（批次 65）；D05 历史凭据审计完成（批次 68，上线前轮换为部署动作）；TLS 仍由部署侧承担；未过则不得公网暴露 |
| R1-N 正确性 | ◐（最大进展） | DB-09、DB-03 多写路径、DB-02 播放会话/事件（批次 51）、FE-01/02/13 阶段 1/14 子项 |
| R2 生命周期 | ◐ | BE-08 批次 47 主体、BE-04 批次 49、FE-03/05/06 局部、FE-09 批次 44、FE-10 批次 45、FE-04 批次 46；FE-07/15 未做 |
| R3 任务与边界 | ◐ | 批次 50 建立扫描异步任务表（202/task_id/1h 幂等/容量 200，进程内不恢复）；探测/刮削仍用各自状态端点；无持久任务/NFO 操作意图，重启不补跑 |
| R4 量化优化 | ◐ | 批次 79–83 收口：FE-11 基线+图片预算、DB-05/06 索引、DB-04 搜索量化（暂不 FTS5）、DB-10 候选分批/回填/JSON 定位、DB-05 投影与深分页量化（维持语义）；剩余：真实 50k 库、冷缓存、索引写放大与 ANALYZE 漂移；A15 自动基线已执行（批次 57/62），固定硬件与真机复测未做 |

## 5. 下一步队列（按依赖）

批次 41–72 已全部完成，逐批主题见 §6。剩余工作都依赖部署环境或属于 R4：

1. **部署验收**：A01 HTTP 级可控时钟、A04 提交后杀进程、A06/A08 只读卷、A10 kill -9、A11 长流与 503 窗口、A12 旧客户端抓包、A13 iOS/Android 真机、A14 慢头/大 header 压测——需可登录部署。
2. **前端独立项**：FE-07（触屏/elementFromPoint 复核）、FE-15（cron 校验与 latest-wins），并补 FE-04/FE-09/FE-10 的真实读屏验证。
3. **R4 性能项**：DB-04/05/06/10、FE-11（分页 DOM/图片 srcset）。
4. **生产放行**：按 D04 配置 `setup_code`/来源白名单/可信代理与 TLS；完成后按 §3 回填，未回填不得宣称生产可用。

## 6. 批次映射（1–72）

| 批次 | 主题 |
|---|---|
| 1 | 启动迁移台账与不再吞错（DB-01/R0） |
| 2–3 | 凭据真源鉴权、401/503 分类、Logout 撤销（BE-01/BE-06） |
| 4 | NFO 部分成功与人工恢复（FE-13 阶段 1） |
| 5–8 | 计划探测 all、编辑保留参数、显式清空、config_etag（FE-01/02/14） |
| 9–15 | 事务版本、单项原子索引、删除/标记/相似/收藏版本绑定（DB-03/DB-09） |
| 16 | 请求诊断/debug/NoRoute 隐私收敛（BE-03/BE-08） |
| 17–18 | 键盘事件来源隔离、低强调文字对比度（FE-03/FE-06） |
| 19–23 | 刮削/探测/扫描进度串行观察、故障退避、代际隔离（BE-07/BE-08/FE-05/FE-12） |
| 24–27 | 设置快照、图片路径、探测版本失败、手动 played（DB-03 局部） |
| 28 | 认证公开入口错误脱敏（BE-03） |
| 29–31 | 批量读取去重分块、关联缺失、详情错误分类（DB-08） |
| 32 | 启动自检日志隐私（BE-03） |
| 33–39 | 刮削/头像任务生命周期、多 worker、取消与熔断（BE-07 局部） |
| 40 | 列表主查询固定错误、相似源 404/500 分类 |
| 41 | HTTP 读超时、请求体 413、登录/初始化 429、不信任代理头（BE-05 部分） |
| 42 | 出站协议白名单、DNS/拨号地址校验并钉 IP、逐跳重定向策略、响应头/拨号/握手/连接预算（BE-09 部分） |
| 43 | CI 工作流：build/vet/test、认证与出站 race、前端 VM 测试（R0 基线） |
| 44 | 登录错误/忙碌不再整段重渲染，保留输入与焦点稳定，补密码策略提示（FE-09 部分） |
| 45 | 筛选/排序/媒体库可访问名称、筛选与库 Tab aria-pressed、编辑展开 aria-expanded、计划任务开关名称（FE-10 部分） |
| 46 | 遮罩统一焦点栈：Esc 按 z-index、Tab 循环、关闭恢复焦点、dialog/tabindex 语义（FE-04 部分） |
| 47 | 诊断去重 TTL/LRU 与写库令牌桶（BE-08 主体） |
| 48 | D02 决策落地：API 密钥管理员语义、UI/README 具名风险提示（BE-02 风险接受） |
| 49 | BE-04 退出协调：readiness、信号驱动有界关闭、worker join、15s 预算与强制退出路径 |
| 50 | BE-07 扫描异步化：202+task_id、1h 幂等、容量 200、状态查询；旧 /scan 保留 |
| 51 | DB-02 播放会话/事件表（迁移 2）：序列号、重复停止幂等、停止后迟到心跳忽略、原子计数 |
| 52 | 验收集骨架：验收矩阵回填格式、D04/D01/D06 提案、env 门控的实时实例自动验收（A03/A05/A11/A14） |
| 53 | 本地真实二进制冒烟：假 Redis+合成媒体跑通 TestAcceptanceLive（A03/A05/A11/A14）与 SIGTERM 退出 |
| 54 | CI 增 gofmt 门禁与 store race（播放并发）；补 D03/D05/D08/D11/D12 建议默认值 |
| 55 | 验收集扩 A06 并发计数（8 并发精确 +8）与 A10 扫描任务 202/轮询/幂等重放 |
| 56 | A07 人工编辑 NFO 往返（严格 PATCH→重扫不回写）与 A09 本机恢复演练 |
| 57 | A15 固定机器性能基线：5000 部扫描 11s、列表 p95 17ms、详情 p95 7ms、RSS 48MB |
| 58 | A02 升级会话不失效：旧版本创建 token，新版删除后旧二进制仍可用、新二进制拒绝 |
| 59 | A12/A13 无障碍修复：媒体卡嵌套交互与名称不匹配，axe 复跑 0 violations |
| 60 | A12 跨浏览器 3×7 页巡检 + 纯键盘流程；A13 修复刮削页 select 可访问名 |
| 61 | A12 功能级断言：API key 创建/使用/删除、计划任务、元数据编辑、播放器开合 |
| 62 | A15 扫描中混合负载：5930 请求 0 错误、p95 55ms、扫描 success=5000 |
| 63 | 依赖安全升级（x/net/x/text/x/crypto/x/sys、go 1.26）+ CI govulncheck 门禁 |
| 64 | D01 会话期限：迁移 3 `expires_at`（旧行回填 30 天）、滑动续期、撤销全部会话 API/设置页；`session_ttl_days` 配置 |
| 65 | D04 公网初始化门禁：`setup_code`（初始化验证码）、`initialize_allow_from`（来源 CIDR）、`trusted_proxies`（代理头信任）、启动警告与公网部署文档 |
| 66 | D03 API 密钥摘要化：迁移 4 `key_hash`/`key_prefix`，旧明文行改写；列表仅前缀，原文只在创建时返回一次 |
| 67 | D06 版本域验收：收藏/评分/已看/编辑/删除后版本推进且读己之写；无版本缓存键不命中 |
| 68 | D05 历史凭据审计：历史 `config.yaml` 无真实口令，工作区无本地库/日志，上线前轮换 checklist 入台账 |
| 69 | D08 索引失败恢复提示：迁移 5 `edit_failures`，恢复列表与超 7 天提示，成功重索引自动清除 |
| 70 | D11 备份与恢复：`cmd/embybackup` SQLite `VACUUM INTO` 快照 + NFO 副本 + 保留 N 份；README 恢复与轮换步骤 |
| 71 | D12 落地记录 + A01–A15 证据回填：自动项列测试名/数据，人工部署项显式保留为未执行 |
| 72 | CI 实跑闭环：vuln 门禁改用 `1.27.x` 补丁版，GitHub run `39052328722` 全绿 |
## 7. 文档漂移修复记录

2026-10-17 批次前修复（本次）：

| 位置 | 原文 | 修正 |
|---|---|---|
| README:5 | “不内置刮削器” | 改为可选内置 MetaTube 刮削/翻译 |
| README:7 | “零外部依赖（除可选 Redis）” | 改为 Redis 必选，缺失拒绝启动 |
| README:78 | “手动补录” | 改为编辑/刮削链（端点 2026-10-12 已移除） |
| README:155 | “不做刮削器” | 改为不做通用插件，内置适配器除外 |
| dev-plan §1–§3、§6、Roadmap | 手动补录/零刮削/可选 Redis/无 provider 字段 | 按实现校准，并新增 §9 施工治理 |

## 8. v2 施工台账（v1.5.0 之后）

v1 台账冻结在批次 72（tag `v1.5.0`）。v2 目标与规则见 [dev-plan.md](dev-plan.md) §10，契约正文见 [API-CONTRACT.md](API-CONTRACT.md)。

| 批次 | 主题 | 状态 | 边界 |
|---|---|---|---|
| 73 | 契约基线：`API-CONTRACT.md`、关键路由/错误信封契约测试、未知路由 `NOT_FOUND`、admin 组 `no-store` 先于鉴权 | ✅ 本地完成 | 管理面旧响应当时仍只带 `error`（已于批次 74 补齐） |
| 74 | 管理面错误码补齐（纯增量）：`/api/*` 统一 `apiError`，139 处旧响应补 `INVALID_REQUEST`/`NOT_FOUND`/`CONFLICT`/`PRECONDITION_REQUIRED`/`INTERNAL`/`UNAVAILABLE`；契约测试补 400 用例 | ✅ 本地完成 | Emby 兼容端点少数错误仍只带 `error`（批次 76 统一）；专用 `code`（`SCAN_BUSY` 等）保持不变 |
| 75 | 弃用机制：`internal/server/deprecation.go` 登记表 + RFC 8594/8288 弃用头；首个登记 `POST /api/admin/scan`（Sunset 2027-04-01）；Web 扫描迁移到 `POST /api/admin/tasks/scan` | ✅ 本地完成 | v2 内只登记不删除；旧同步入口仍可用；移除需 Sunset 且升 v3 |
| 76 | 协议抽检（不依赖抓包）：Emby 兼容端点 41 处错误补 `code`；`emby_compat_test.go` 覆盖 System/User/Views/Items 形状、分页边界、容忍未知参数、404/400 信封、图片 404 空 body；修复评分接受但不回显的问题（新增 `userDataDTO`，`Likes` 按 bool 回显） | ✅ 本地完成 | 旧客户端抓包与真机走查（A12 的抓包部分）仍需用户提供设备/部署环境 |
| 77 | 前端独立项（FE-15 部分）：计划任务单次提交、cron 校验 latest-wins、服务器时区明示 | ✅ 本地完成 | 真实触屏/跨时区设备走查仍待部署或真机 |
| 78 | FE-07 复核与焦点循环修复：Playwright `elementFromPoint`/触屏/背景关闭/命中区恢复；`overlayTrapTab` 改受控循环，修复播放器内 Tab 落到 body | ✅ 本地完成 | 真机触屏与真实读屏仍未验 |
| 79 | FE-11 30 页 DOM/内存基线 + 图片宽度预算：媒体墙/详情 `srcset` 与 `maxWidth`，详情海报尺寸属性；实测 3,100 卡 DOM 74,533、堆 9.5MB | ✅ 本地完成 | 真机、低端机与 5k 以上库未测；未做窗口化 |
| 80 | DB-05/06 二级索引：演员反向索引 + 可见影片标题部分索引，迁移 6 内置 `ANALYZE`；EXPLAIN/计时证据 | ✅ 本地完成 | 轻量投影/keyset 未做；写放大与大库冷缓存未测 |
| 81 | DB-04 搜索量化：新增 `BenchmarkSearch*` 固定 5k 基准（标题/类型/演员/深页）+ 50k 行扫描测量，结论暂不引入 FTS5/关系表 | ✅ 本地完成 | FTS5 语义回归、中文分词、写入放大与真实 50k 库未测 |
| 82 | DB-10：刮削候选游标分批（不再整库装载）、回填进度/取消、损坏 JSON 定位（store+recovery/settings API） | ✅ 本地完成 | 手动单条路径仍传切片；损坏 JSON 无 UI 修复；回填进度不持久化 |
| 83 | DB-05 列表宽投影/深分页量化：50k 宽/窄投影与 OFFSET 20000 计时、`BenchmarkSearchAdminWidePage200`；结论暂不投影/不设 OFFSET 硬上限 | ✅ 本地完成 | 真实 50k 库与冷缓存未测；JSON 编码体积未单独计量 |
| 84 | 多用户基座（迁移 7）：`users` 表、token/API key 绑定 `user_id`、按 `users` 登录与管理员判定、初始管理员回退与管理员 API 路径 | ✅ 本地完成 | 媒体库可见性未按用户限制；Emby 多用户仅同一账户 |
| 85 | 按用户隔离播放数据（迁移 8）：`userdata` 重建为 `(user_id,movie_id)`、`playback_sessions.user_id`、`login_events` 登录审计（成功归户、失败记 id=0）；Emby UI 数据只认本人 | ✅ 本地完成 | 管理员跨用户查看只有数据库层，尚无 API/UI |
| 86 | 多用户管理面：用户 CRUD（创建/改名/改密/删除/管理员标记）、媒体库授权表 `user_libraries`（默认无授权）、管理员只读审计 API（登录/播放记录）、自助改密并撤销会话；删除用户保留审计 | ✅ 本地完成 | 前端管理页待批次 88；审计表无独立清理任务（写入时按 90 天/5 万条裁剪） |
| 87 | 可见性强制：`viewerLibraries` 统一范围；`/Views`、`/Items`、搜索、续播、最新、计数按授权库过滤；详情/PlaybackInfo/流/代理/播放上报未授权库 404；详情与列表缓存键带用户与授权指纹（修掉跨用户缓存泄漏） | ✅ 本地完成 | 图片端点仍匿名（D02 已批准的兼容边界）；管理员只读审计，不代改 |

门禁：`go test ./internal/server -run TestAPIContract -count=1` 必须通过；路由或错误信封变更同批更新 `API-CONTRACT.md`。

## 9. 多用户施工（批次 84 起）

目标：在保留单管理员兼容的前提下引入普通用户；管理员可管理用户，普通用户只能读写自己的播放数据，且不能创建 API 密钥（D02 已批）。

| 批次 | 主题 | 状态 | 边界 |
|---|---|---|---|
| 84 | 用户模型基座：迁移 7 建 `users` 表并从 `administrators` 复制 id=1；`access_tokens`/`api_keys` 增 `user_id`；登录改为按用户校验、会话绑定用户；`/api/admin/*` 增加 `requireAdmin`（403 `ADMIN_REQUIRED`）；`/Users/{id}` 支持已存在用户 | ✅ 本地完成 | `userdata`/播放会话仍是单用户结构，普通用户暂时看到与管理员相同的播放数据；Emby 端点仍只认 `Users/1` 的数据；无用户管理 UI/API |
| 85 | 按用户隔离播放数据：迁移 8 重建 `userdata` 为 `(user_id, movie_id)`、播放会话带 `user_id`、新增 `login_events`；store 增加 `DataUser`/`DataForUser`/`WriteIndexed*ForUser`/`RecordPlaybackEvent.UserID`；服务端一律按当前登录用户读写 | ✅ 本地完成 | 旧行归 user 1；Emby `/Users/{uid}` 仅允许当前用户 id（管理员代看留批次 86）；未做跨用户列表 API |
| 86 | 管理面：用户 CRUD（创建/改名/改密/删除/管理员标记）、媒体库授权表 `user_libraries`、管理员只读审计 API（登录/播放记录）、自助改密撤销会话；删除用户保留审计 | ✅ 本地完成 | 可见性尚未在浏览/流/详情路径强制；前端页待 88 |
| 87 | 可见性强制：非管理员只能看到被授权的媒体库（默认无授权）；浏览/列表/搜索/续播/最新/计数按授权过滤；详情/PlaybackInfo/流/代理/播放 404；缓存键带用户与授权指纹 | ✅ 本地完成 | 图片端点保持匿名（D02 兼容边界）；Emby 跨用户读仍禁止 |
| 88 | Web 管理 UI：用户列表/创建/改密/删除/媒体库授权、登录与播放活动记录页；非管理员隐藏入口；普通用户设置页可自助改密 | ✅ 本地完成 | 真机与读屏复测 |
| 89 | 活动记录从设置页拆为独立侧边栏页面：新增 `activity` 导航/页面（登录+播放审计、刷新）；设置页不再请求审计 | ✅ 本地完成 | 与批次 88 同属 UI 调整，自动截图/真机待补 |
| 90 | 架构复审 P0：播放会话按用户隔离（迁移 10 重建 sessions 为 (user_id,session_key) 主键、events 增 user_id，修跨用户同 PlaySessionId 互相停止/计数） | ✅ 本地完成 | 真实多客户端撞 key 复现、生产迁移演练 |
| 91 | 生产反馈：影片 DTO 增 `CollectionIds`，并实现 `GET /Items/{id}/Collections`（及 `/Users/{uid}/...`）只返回该片所属合集；修客户端缺端点时退化为列出全部合集 | ✅ 本地完成 | 需部署生产后由客户端复测；真实客户端抓包未做 |

## 10. 多用户设计决策（批次 84；批次 86 补充）

- **账户**：`users(id INTEGER PRIMARY KEY, name UNIQUE, password_hash, is_admin, created_at, updated_at)`；id=1 为初始管理员，迁移从旧 `administrators` 复制，旧表保留以支持二进制回退。
- **会话**：`access_tokens.user_id` 默认 1；登录成功后会话绑定账户，`requireAuth` 把 `store.User` 放入请求上下文。
- **API 密钥**：`api_keys.user_id` 默认 1；`APIKeyUser` 解析所属账户；仅管理员可创建（D02）。旧密钥归管理员。
- **可见性（m03632）**：媒体库按用户分配，新用户默认**全部不可见**；管理员不受限。Emby 数据只认本人，管理员跨用户只读审计走网页 API。
- **审计（m03632）**：登录/播放记录保留 90 天且最多 5 万条；失败登录记 `user_id=0`（防枚举）并记 IP；删除用户保留审计（名字快照），个人播放数据随账户删除。
- **管理边界（m03632）**：管理员只读查看他人播放/登录记录，不代改；普通用户可自助改密，管理员也可重置。
- **管理员边界**：`/api/admin/*` 统一 `noStore → requireAuth → requireAdmin`；非管理员返回 403 `ADMIN_REQUIRED`，不再默认“任何 token 都是管理员”。
- **兼容**：users 行缺失且会话属于 id=1 时，回退旧 `administrators`（或空库夹具）识别为管理员，避免升级瞬间把旧管理员锁在门外；该回退在 85/86 完成后应删除。
- **未决**：普通用户是否可见全部媒体库/合集；管理员能否代看/代改他人播放数据；密码策略（沿用 ≥9 字符）；用户禁用（批次 87 实现为 `disabled` 列还是删除待定）。
| 92 | 生产反馈：相似作品推荐重排——
分层：同系列(0) > 同番号系列(1) > 共同演员≥2(2) > 演员1+同厂牌(3) > 演员1(4) > 仅类型/标签特征(5)；移除全库恒定的分级加分，年份/前缀只作同层细分；修复候选未按账户媒体库授权过滤、缓存键缺账户 | ✅ 本地完成 | 需部署生产后复测；层级阈值（演员数/特征分）可再按真实库调参 |

## 11. 架构复审落地（2.1 起）

| 批次 | 主题 | 状态 | 边界 |
|---|---|---|---|
| 98 | 安全响应头（nosniff/DENY/no-referrer/Permissions-Policy）+ CSP `Report-Only`（script-src self、img/media 放行 http(s)/blob）；迁移 13 `admin_audit_events` 记录管理员 POST/PUT/PATCH/DELETE（GET 不记），90 天/5 万条裁剪，`GET /api/admin/audit-events` 只读查询 | ✅ 本地完成 | CSP 尚无 report 收集端，违规只进浏览器控制台；审计无 UI；审计在响应后同步写库（失败仅日志） |
| 97 | 登录会话摘要化：迁移 12 把 `access_tokens.token` 原地改写为 SHA-256 摘要（无需 DDL，旧会话升级后仍有效），读写/撤销/过期全部按摘要匹配；`isTokenHash` 识别 64 位 hex 防重复摘要 | ✅ 本地完成 | 迁移后回退旧二进制将无法识别会话（需重新登录）；摘要无盐但输入为 192 位随机值 |
| 96 | Redis 降为可选：未配置 `redis_addr` 或连接失败时回退进程内缓存（容量 4096 条）并告警；`adminSettings` 返回真实 `cache`/`redis_online`（不再写死 redis/true）；README/config.example 与部署说明同步 | ✅ 本地完成 | 内存兜底无跨实例共享、重启失效；多实例仍需外部缓存；缓存故障切换无指标 |
| 95 | 删除硬编码 user 1 的用户数据 API（`Data/DataFor/SavePlayback/SetPlayed/SetFavorite/SetLikes/SetHideFromResume`），改 `DataUser/DataForUser/SavePlaybackForUser` 与 `SetIndexed*ForUser`；`authOK` 账户缺失按 401 fail-closed，移除 `administrators` 旧行回退；新增删除用户 token 失效与旧口令拒绝测试 | ✅ 本地完成 | 旧 `SaveAccessToken`/`CreateAPIKey` 仍是 user 1 包装（服务端已走 ForUser）；回退旧二进制仍读 administrators |
| 94 | 全量 schema 校验：参考库跑完迁移 1–11 后逐表比对列/类型/默认值/主键/外键/唯一/CHECK，索引按语义签名（列序/排序/唯一/部分谓词）；备份 CLI 加固：只读 DSN + busy_timeout、暂存目录成功后才发布、`PRAGMA integrity_check` 失败即删、NFO 落盘 fsync | ✅ 本地完成 | 崩溃残留 `.tmp` 目录不自动清理；备份保留期外的异地副本仍靠部署侧 |
| 93 | `userdata(movie_id)`、`login_events(user_id,created_at)` 索引（迁移 11，末尾 ANALYZE）；周期性 ANALYZE（启动一次 + 每 7 天，随进程退出 join）；图片缓存 TTL 5 分钟→24 小时（配合 Cloudflare） | ✅ 本地完成 | 真实 20k 删库基准与索引写放大未测；ANALYZE 周期是固定 7 天，不感知写入量 |
