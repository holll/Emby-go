# PLAN-STATUS — 施工状态与放行台账

> 本文件保留历史决策、施工证据与部署验收，不再重复维护完整设计；当前边界见 [设计与后续计划](<dev-plan.md>)，使用说明见 [README](<README.md>)。
> **阅读顺序**：§0–§8 是早期批次快照，§9 起是后续实施及计划；最新 MetaTube 范围裁剪见 §20。旧条目的“未做”、Redis 必选、单用户、兼容回退等描述不代表当前实现；后续批次会覆盖早期状态。历史数据不重新包装成当前测试结果。
> **重要更新**：批次 84–90 已引入多用户与用户隔离；95 移除旧管理员认证回退；96 将 Redis 降为可选；97 摘要化会话。§10 的旧回退/待定项以这些后续结果为准。
> **完成 = 当批本地实现 + 本地回归通过**，不代表生产放行。部署仍须按 §3 回填 A01–A15 和相关开放条目；不删安全验收定义或故障证据。
> 详细历史记录与部分研究资料不随仓库发布，不能假设外部读者拥有临时脚本或截图。

文档整理：2026-10-23（Asia/Shanghai）；下列基线日期与提交号保持为历史。
基线提交：`1b93a06`（40 批 checkpoint）；批次 41 `3499029`；批次 42 起见 git log

## 0. 本地基线（2026-10-17 实测）

| 项 | 结果 |
|---|---|
| `go build ./...` | 通过（Go 1.27.2，go.mod 声明 1.27.0） |
| `go vet ./...` | 通过 |
| `go test ./...` | 16 包全绿；456 个 Test 函数（server 约 40s / store 约 17s）；3 个实时验收用例未设 env 时跳过 |
| `node --test internal/server/web_test.cjs` | 88/88 通过 |
| 测试形态 | httptest + 内存库为主；真实浏览器（Playwright 三引擎）与真实二进制冒烟已做；仍无真实 Emby 客户端、生产库、真机 |
| CI | go / vuln / web 三 job；run `39263674365` 全绿（go/vuln/web）；门禁含 gofmt、build、vet、test、覆盖率 ≥70%、三包 race、govulncheck、前端 VM |
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

**生产部署验收未执行**；自动子集与真实二进制冒烟已用假 Redis+合成媒体本地跑通（见 §3.1），不等于部署验收。验收定义与断言见下方表格；执行时在此逐项回填：命令、环境、原始输出路径、执行人、日期。未回填即视为未通过。

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
| A13 浏览器/a11y | 自动（Playwright 三引擎巡检，批次 60） | D12 矩阵（Chromium/Firefox/WebKit） | 7 页面 ×3 引擎 0 console error/0 axe/0 移动溢出；键盘流程 `keyboard.cjs` |
| A14 入口/资源 | 自动（413/401/健康 + 登录限流 429）本地通过；慢头/大 header 压测未做 | 压测工具、代理拓扑 | `limits_test.go`、acceptance 输出；慢头依赖部署侧代理 |
| A15 性能/语义 | 自动（批次 57 固定参数） | D12 阈值、同参数前后采样 | 5000 项扫描 11.0s；列表 Limit=50 p95 17.2ms、Limit=200 p95 49.7ms；详情 p95 7.1ms；RSS 47.5MB；基准脚本未随仓库保留 |

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
- A12 UI 冒烟（批次 59，Playwright/Chromium 156 headless，1280×800 + 390×844）：登录 → 媒体墙（100 卡）→ 搜索「Perf Movie 42」命中 → 详情抽屉打开且首焦点在抽屉内 → 连按 8 次 Tab 焦点仍在抽屉（焦点圈）→ Esc 关闭后焦点回到卡片标题按钮；控制台 0 error；脚本 `docs/acceptance/ui.cjs`。
- A12 跨浏览器页面巡检（批次 60，Chromium/Firefox/WebKit × 7 页：总览/媒体库/媒体墙/计划任务/接口探针/刮削/设置）：三浏览器全部 0 控制台错误、移动视口横向溢出 0px；纯键盘流程（批次 60）：autofocus 账号 → Tab 密码 → Enter 提交 → Tab 到「媒体墙」Enter → Tab 到标题按钮 Enter → 详情抽屉显示对应影片，全程无鼠标、0 error。
- A12 功能级断言（批次 61，`docs/acceptance/func.cjs`）：设置页创建 API key→用该 key 调 `/api/admin/settings` 200→UI 删除→同 key 立即 401；计划任务新建→列表出现→删除；详情「编辑元数据」改标题→保存后抽屉关闭、墙面卡片标题更新、重开详情仍为新标题（NFO 已写）；快捷播放按钮打开播放器浮层（标题=影片名）并可关闭；全程 0 error。
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

早期“下一步”中 FE-07/FE-15、图片预算、索引及候选分批已在后续批次落实，不再作为待施工队列重复列出。当前候选方向见 [后续计划](<dev-plan.md#4-后续工作>)。

仍需保留的部署门禁：按 §3 补充 HTTP 级时钟、提交后杀进程、只读卷、任务崩溃、长流与 503 窗口、真实客户端、真机/读屏、慢头/大 header、生产库和备份恢复演练。按实际网络配置初始化保护、可信代理与 TLS；未回填不得宣称生产可用。

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

2026-11-03 清理（本批次）：

- 删除已完成的一次性方案产物：`docs/2026-10-29-go-review.md`（评审报告，施工状态已完成）、`docs/2026-10-29-modernize.patch`（已被落地改写，无法原样回滚）。
- 删除 `docs/screenshots/`（16 张，2026-10-23~25 批次 UI 快照）：界面此后已变（紧凑布局、webhook 多目标、任务面板抽共用状态机），图片不再代表当前界面；A12/A13 的断言结论仍以正文记录为准。
- 验收脚本 `docs/acceptance/*.cjs` 去掉 `/tmp/pw` 硬编码：playwright 走正常解析（`PW_DIR` 可覆盖）、axe-core 走 `require.resolve`（`AXE_PATH` 可覆盖）、截图目录默认 `os.tmpdir()/emby-acceptance-*`（`SHOT_DIR` 可覆盖）、实例地址统一 `UI_BASE`（默认 `http://127.0.0.1:18100`）、账号 `UI_USER`/`UI_PASS`；`pages.cjs` 页面清单补齐为当前 11 页导航。
- `docs/` 整个目录在 `.gitignore` 里（`/docs/`），所以上文引用的 `docs/acceptance/*.cjs`、`docs/emby-study/jellyfin-reading-notes.md` 与出处 JSON 只存在于本地验收环境，仓库读者看不到；这些路径继续用于本机复跑，正文断言与结论不依赖它们是否入库。

## 8. v2 施工台账（v1.5.0 之后）

v1 台账冻结在批次 72（tag `v1.5.0`）。变更规则统一见 [API 契约](<API-CONTRACT.md#5-变更规则v2>)，当前设计见 [开发计划](<dev-plan.md>)。

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

2.1/2.2 完成：批次 90–104（P0 会话隔离、全量 schema 校验与备份加固、旧 API 删除、Redis 可选、会话摘要化、安全头/审计、部署样例、前端 P1-7/8/9/10、调度器防重入、覆盖率门禁、管理端指标、索引与 ANALYZE、会话 TTL）。
仍未做（2.3 剩余大项）：前端虚拟滚动与模块化（P1-11）、server 抽 service 的依赖倒置、axe/读屏自动门禁、多实例调度去重与缓存共享、生产/真机人工验收。

| 批次 | 主题 | 状态 | 边界 |
|---|---|---|---|
| 104 | 管理端指标：`GET /api/admin/metrics`（管理员），返回 uptime/goroutines/堆内存/GC/影片/用户/媒体库/缓存后端/readiness；JSON 稳定、无外部依赖 | ✅ 本地完成 | 无 Prometheus 格式与持久化时间序列；请求延迟直方图/metrics 鉴权限流未做 |
| 103 | CI 覆盖率门禁：`go test -coverprofile` + `go tool cover -func` 总语句覆盖率 <70% 失败（当前 73.2%） | ✅ 本地完成（门禁命令本地通过） | 只卡总覆盖率，未设包级下限；axe/读屏仍无自动门禁 |
| 102 | 调度器层防重入：`Scheduler` 增 `running` 表，同一任务到点/立即执行永不并发；重叠触发记 `skipped`，`RunNow` 忙时返回 `ErrBusy`；不同任务互不阻塞（P2-10） | ✅ 本地完成 | 仍不跨进程/多实例去重；没有持久化运行中间态，重启时执行中的任务没有恢复记录 |
| 101 | 刮削候选并发：会话内 `inspectSeq` 取号，慢回包让位给更晚点击；关闭浮层/换片后回包不写状态（P1-10） | ✅ 本地完成（VM 用例） | 未在真实慢网络下用 Metatube 双候选复现；候选按钮未做 loading 禁用 |
| 100 | 前端 P1：路由切换自动收起播放器/刮削/详情浮层（P1-7）；卡片 `:focus-within` 显示操作与播放层（P1-8）；状态/库/排序筛选整页重绘后还原焦点到触发控件（P1-9） | ✅ 本地完成（Playwright 实测） | 仅还原焦点，不恢复滚动位置；`focus-within` 依赖现代浏览器；虚拟滚动（P1-11）仍未做 |
| 99 | 部署样例：`deploy/Dockerfile`（多阶段、distroless 非 root、`-X main.version`）、`deploy/docker-compose.yml`（Redis 可选 profile）、`deploy/emby-go.service`（30s 停止宽限 + 加固）；README 增部署段 | ✅ 本地完成 | 未在 CI 构建镜像（无 Docker job）；未做 compose/systemd 实机演练 |
| 98 | 安全响应头（nosniff/DENY/no-referrer/Permissions-Policy）+ CSP `Report-Only`（script-src self、img/media 放行 http(s)/blob）；迁移 13 `admin_audit_events` 记录管理员 POST/PUT/PATCH/DELETE（GET 不记），90 天/5 万条裁剪，`GET /api/admin/audit-events` 只读查询 | ✅ 本地完成 | CSP 尚无 report 收集端，违规只进浏览器控制台；审计无 UI；审计在响应后同步写库（失败仅日志） |
| 97 | 登录会话摘要化：迁移 12 把 `access_tokens.token` 原地改写为 SHA-256 摘要（无需 DDL，旧会话升级后仍有效），读写/撤销/过期全部按摘要匹配；`isTokenHash` 识别 64 位 hex 防重复摘要 | ✅ 本地完成 | 迁移后回退旧二进制将无法识别会话（需重新登录）；摘要无盐但输入为 192 位随机值 |
| 96 | Redis 降为可选：未配置 `redis_addr` 或连接失败时回退进程内缓存（容量 4096 条）并告警；`adminSettings` 返回真实 `cache`/`redis_online`（不再写死 redis/true）；README/config.example 与部署说明同步 | ✅ 本地完成 | 内存兜底无跨实例共享、重启失效；多实例仍需外部缓存；缓存故障切换无指标 |
| 95 | 删除硬编码 user 1 的用户数据 API（`Data/DataFor/SavePlayback/SetPlayed/SetFavorite/SetLikes/SetHideFromResume`），改 `DataUser/DataForUser/SavePlaybackForUser` 与 `SetIndexed*ForUser`；`authOK` 账户缺失按 401 fail-closed，移除 `administrators` 旧行回退；新增删除用户 token 失效与旧口令拒绝测试 | ✅ 本地完成 | 旧 `SaveAccessToken`/`CreateAPIKey` 仍是 user 1 包装（服务端已走 ForUser）；回退旧二进制仍读 administrators |
| 94 | 全量 schema 校验：参考库跑完迁移 1–11 后逐表比对列/类型/默认值/主键/外键/唯一/CHECK，索引按语义签名（列序/排序/唯一/部分谓词）；备份 CLI 加固：只读 DSN + busy_timeout、暂存目录成功后才发布、`PRAGMA integrity_check` 失败即删、NFO 落盘 fsync | ✅ 本地完成 | 崩溃残留 `.tmp` 目录不自动清理；备份保留期外的异地副本仍靠部署侧 |
| 93 | `userdata(movie_id)`、`login_events(user_id,created_at)` 索引（迁移 11，末尾 ANALYZE）；周期性 ANALYZE（启动一次 + 每 7 天，随进程退出 join）；图片缓存 TTL 5 分钟→24 小时（配合 Cloudflare） | ✅ 本地完成 | 真实 20k 删库基准与索引写放大未测；ANALYZE 周期是固定 7 天，不感知写入量 |

## 12. 全栈复审修复（2026-10-23，本地）

范围为本轮确认的 17 项问题及 Similar 源对象授权遗漏，不覆盖旧报告全部开放项；保留前两轮重构和既有 NFO 路径改动，未提交、未部署。

| 范围 | 实施 | 回归证据 |
|---|---|---|
| 身份与并发（3 项） | migration 14：账户 ID 不复用、会话签发校验凭据代际；改密/撤销原子推进代际；最后管理员在 SQLite 写锁内检查。成功登录审计在签发提交后记录，自助改密同时校验不可变账户 ID。 | [Store 身份测试](<internal/store/identity_test.go>)、[HTTP 身份测试](<internal/server/auth_identity_test.go>)、[改密身份交错回归](<internal/server/users_password_identity_test.go>) |
| 库授权（5 项 + Similar） | Suggestions、实体聚合/详情、Images JSON、AdditionalParts、个人状态写入和 Similar 源对象限定可见库；聚合在分页前过滤，缓存绑定账户与授权快照；缺身份/删除/旧管理员角色/读库故障失败关闭。匿名图片字节边界不变。 | [对象授权与缓存回归](<internal/server/object_scope_test.go>)、[实体查询回归](<internal/store/entity_scope_test.go>) |
| 备份边界（1 项） | 写入及清理前拒绝源数据库/媒体库与输出互相包含；关闭 NFO 复制同样保护媒体库；统一实际规范路径处理符号链接与 `..`。 | [输出边界回归](<internal/backup/output_boundary_test.go>)、[清理边界回归](<internal/backup/output_cleanup_boundary_test.go>) |
| 前端（8 项） | 管理按钮遮挡、旧身份响应、当前会话退出、密钥明文生命周期、扫描终态刷新、隐藏字段占位、日志刷新快照及刮削 inspect 失败恢复。 | [前端 VM 与静态 Chromium 回归](<internal/server/web_test.cjs>) |

统一验证：`go test -count=1 -timeout=15m -coverprofile=… ./...` 全部通过（server 128.689s、store 55.838s），总语句覆盖率 **75.9%**，通过 70% 门禁；`go vet ./...`、`go build ./...`、本轮 Go 文件格式与 `git diff --check` 通过。`node --test internal/server/web*_test.cjs` **144/144** 通过，app/login 语法检查通过。全量 `go test -race -count=1 -timeout=30m ./...` **全部通过**（server 928.409s、store 518.849s），本轮本地统一回归完成。

真实浏览器覆盖限缓存 Chromium 的宽/窄静态 DOM/CSS；不代表真实服务、客户端播放、Firefox/Safari、读屏或生产迁移验收。

升级前备份，schema 15（含 API 密钥 stable_id）不支持直接降级旧二进制；回退须停服恢复升级前数据库，详见 [备份与恢复](<README.md#备份与恢复d11>)。历史已删除且无留存引用的用户 ID 无法追溯。备份目录检查不固定 inode，仍要求备份期间不由外部替换目录/符号链接或变更库路径；SQLite 与 NFO 仍非联合快照。

## 13. 独立翻译与手动计划任务

已实现从现有 NFO 独立翻译，不重新刮削或获取图片，原刮削内自动翻译保持不变。复用现有翻译服务配置及服务端缓存，不新增应用内翻译缓存或持久失败队列。

- **入口**：详情页“仅翻译”、管理员媒体墙多选、独立翻译页的按库/全库运行，以及 `translate` 类型计划任务。新建翻译计划默认禁用自动调度，仍可手动立即执行；未替用户创建或启用任何定时计划。
- **字段**：默认仅标题，以 NFO `originaltitle` 为源并保留它；简介需显式选择，以当前 `plot` 为源，界面提示重复翻译风险。尊重字段锁、保留未知 XML；缺原文或缺 NFO 跳过，不创建 NFO。
- **并发与落盘**：网络请求不持文件锁；提交时检查相关源字段、目标字段与锁，冲突不覆盖。与扫描、探测、刮削及编辑共用应用内 NFO 互斥门。写前取消不落盘；写后局部重建索引不因取消而省略，索引失败独立报告。重试即使文本未改变也刷新索引，修复前次刷新失败。
- **API 与观察**：管理员专用 `/api/admin/translate/run`、`/status`、`/cancel`；显式空选片不退化全库，IDs 与显式库参数互斥。精确任务 ID、页面代际及身份校验防止旧响应刷新或取消新任务；计划未知参数与 ETag 往返保留。

回归入口：

- [NFO 快照与提交](<internal/nfo/translation_test.go>)：字段/锁 CAS、未知节点、取消与冲突。
- [独立翻译核心](<internal/scraper/translation_test.go>)：服务失败保留、原文与字段锁、真实索引刷新失败后的 nochange 修复，以及旧 CD1 与共享 NFO 并存路径。
- [翻译 API](<internal/server/translation_test.go>)、[计划任务](<internal/server/scheduled_translation_test.go>)：权限、范围、参数、互斥、取消、索引错误和计划往返。
- [Web 回归](<internal/server/web_test.cjs>)：确认范围、选片与上限、精确任务取消、退避及跨身份/页面隔离、nochange 完成刷新和禁调度计划的手动执行。

冻结后验收：全量 `go test -count=1 -timeout=15m -coverprofile=/tmp/emby-translation-coverage.out ./...` 通过，总语句覆盖率 **76.5%**；`go vet ./...`、`go build ./...`、Go 格式和 `git diff --check` 通过。NFO/scraper 全包 race、补充交叉测试 race，以及 server 的 `Test(Translation|ScheduledTranslation)` 定向 race 均通过。本轮没有重新运行全项目 race；§12 的完整 race 是上一轮快照证据。

最终 Web **154/154** 通过、0 跳过，app/login 语法检查及冻结后 Go 内嵌构建通过。缓存 Chromium 在 1280/500px 下验证选片框与管理按钮命中、隐藏字段和键盘聚焦；仅为静态 DOM/CSS 与 VM 回归，不代表真实翻译网关/业务服务端到端验收。未访问生产翻译服务，未提交、推送或部署。

## 14. 安全运维六项（R1–R6）

按安全运维六项方案施工。六项共享一条前提：默认配置收紧、失败关闭、敏感信息不落调试/审计/容器日志，单进程内存态重启后清零。

- **R1 防爆破**：新增 [auth_guard.go](<internal/server/auth_guard.go>)，登录/初始化/改密/`/Users/AuthenticateByName` 统一走 `AdmitRequest → AttachAccount → Finish` 三步预留；IP 令牌桶 10/1rps、全局 40/20rps、bcrypt 并发槽默认 4（有界等 100ms）、凭据 body 8 KiB（含 chunked 超限 413）、账号与组合失败窗口、账号冷却上限 5 分钟、键表满转 overflow 桶；成功只在凭据提交后清账号/组合额度，不清 IP 攻击历史。429 保持 `AUTH_RATE_LIMITED` 且 `Retry-After` 为正整数。[配置](<internal/config/config.go>) 增加 `security.auth`；管理员可查 [auth-limits](<internal/server/auth_guard_handlers.go>) 并以交互会话 + 密码再认证 [unlock](<internal/server/auth_guard_handlers.go>)。[debug raw 日志](<internal/server/requestlog_debug.go>) 与 [审计路径](<internal/server/admin_audit.go>) 对认证/改密/密钥/解锁路由整段脱敏。
- **R2 扫描身份**：扫描进度写入 `instance_id/run_id/task_id`（[server.go](<internal/server/server.go>)、[admin.go](<internal/server/admin.go>)）；前端只按运行身份或提交的 task 归并，首读历史终态不弹、观察到终态只提示一次、失败可关闭、换页/换账号/服务重启 ID 重用都不误刷新。
- **R3 多 Webhook**：`webhook.config.v2` 多目标（≤10、整集合 ETag、按目标 ID 合并 URL 秘密、缺/过期 If-Match 428/412、旧单目标 PUT 409）；[dispatcher](<internal/webhook/dispatcher.go>) 单事件 4 并发/5 秒预算、`skipped_budget` 不算成功、聚合 `partial`；`X-Webhook-Status: partial` 在通用 API 层统一提示，业务成功不回滚。[设置组件](<internal/server/web/webhook-settings.js>) 重写为多目标编辑器，前置删除订阅与清空确认。
- **R4 磁盘日志**：写入层 [sanitizer](<internal/logging/sanitize.go>) 覆盖消息/属性/嵌套分组，出站 `?[REDACTED]`、Bearer/密码/token 统一替换；[disklogs](<internal/logging/disklogs.go>) 用受限根、严格日期文件名、HMAC 游标、1 MiB 扫描/64 KiB 行/512 KiB 响应上限，半行与 gap/reset 显式返回，旧非结构化行只显示隐藏占位符；管理员 `/api/admin/logs/disk` 与独立[运行日志页](<internal/server/web/app.js>)，增量轮询、去重、1000 行上限、暂停/加载更早/滚动锚点、内容全转义。
- **R5 密钥轮转**：[migration 15](<internal/store/api_key_rotation.go>) 增加 stable_id/version/rotated_at 并回填唯一 ID；对外 `ak_` 稳定 ID，`POST /api/admin/apikeys/{id}/rotate` 需 If-Match、密码再认证与 actor 代际校验，key/key_hash 同事务替换，旧 key 立即失效；UI 显示版本/最近轮转并提供轮转表单。
- **R6 探测进度**：[probe.go](<internal/server/probe.go>) 批量启动输出 0/N，每个处理完成文件输出一条 `媒体信息探测[n/N]`（task_id/item_id/编号/CD/状态/safe_error），计数与进度行在 `probeProgressEmit` 内串行，单条同步探测复用同一完成函数输出 1/1。

施工期统一验证记录：

- `go build ./...`、`go vet ./...`、`gofmt`、`git diff --check` 通过。
- `node --test internal/server/web*_test.cjs` **156/156** 通过；`node --check` app/login/webhook-settings 通过。
- 全量 Go 测试与覆盖率见下方“六项冻结验证”。

### 六项冻结验证（2026-10-23 本地）

- `go test -count=1 -timeout=15m -coverprofile=/tmp/emby-ops-coverage-final.out ./...` 全部通过，`go tool cover -func` 总语句覆盖率 **75.8%**（高于项目 70% 门禁）；`go build ./...`、`go vet ./...`、`gofmt -l`、`git diff --check` 通过。
- `node --test internal/server/web*_test.cjs` **156/156** 通过（web_test 137 + web_progress 19）；`node --check` app.js/login.js/webhook-settings.js 通过。
- 新增/更新的定向测试覆盖：防爆破并发预留/键表饱和/冷却/429 形状/chunked 超限、扫描运行身份与前端状态机、Webhook 多目标 ETag/秘密合并/预算与 partial、磁盘日志游标/半行/截断/脱敏、密钥轮转与 migration 15、探测严格进度顺序与单次计数。
- 本轮未重跑全项目 `-race`，§12 的 race 结果仍是上一轮快照；未连接真实 Webhook 接收端、未用真实生产库执行 migration 15、未做真实浏览器/真实磁盘日志链路的人工验收。这些边界不影响本地单元/集成结论，但不能替代上线前演练。

**施工边界**：未提交、未推送、未部署；工作树中早于本轮的身份/授权修复、独立翻译与文档调整保持原样，未回滚或改写。

## 15. 增量扫描收敛（2026-10-23，本地）

本轮依据增量扫描计划，在工作树已有部分实现上补齐正确性、接入与验收；不引入文件监视器，不覆盖既有六项运维、独立翻译及取证资料。

- 两种模式都完整枚举。旧 API、旧计划默认 `full`；界面提供 `incremental`。原始 STRM/NFO 内容摘要、图片 stat、CD 成员与索引证据决定跳过；未变化组不解析 NFO、不改业务行或基线、不推进版本。
- 迁移基线为 **schema 16**，不重写既有迁移 checksum。组索引、演员/特征、扫描基线与版本同事务提交；writer 预留后检查库配置和取消，失败丢弃暂存结果。其他索引写入口在自身事务内失效重叠基线。
- 完整健康清单才允许末尾缺失清理；失败、取消、不稳定、库配置变化及根标识异常均抑制清理。已有记录的空库默认受保护；只有手动 HTTP 显式确认可放行，计划不自动执行该确认。可选相对根标识与 SHA256 必须成对，根内只读，不创建标识文件。
- 保留仍在盘的 manual 主片和人工 part。缺失/无效 NFO 不清人工元数据、演员及特征，但不能冻结来源和分段结构；非 manual 继续原有 pending 规则。只有健康完整清单证明真正消失后才清理，拆组过程不提前删除另一组的 ID/创建时间/观看数据。
- HTTP 在幂等重放前严格解析参数；同 key 异规范摘要返回 409，同 key 同参数并发得到同一任务。计划保留未知参数和 ETag；失败结果保留已提交部分成果，缓存按真实索引变化失效。前端以本次 task result 为准，保持身份与页面隔离，安全未完成不自动隐藏。

### 回归与性能入口

- [事务与失效回归](<internal/store/scan_state_test.go>)：失败注入/真实 COMMIT 失败、ID 与版本回滚、跨入口失效、大批次、零变化业务与状态触发器计数。
- [扫描正确性](<internal/scanner/incremental_acceptance_test.go>)、[保留策略](<internal/scanner/incremental_retention_test.go>)、[manual 缺 NFO 的 CD 结构与零写回归](<internal/scanner/incremental_manual_parts_test.go>)、[完整清单](<internal/scanner/incremental_inventory_test.go>)、[根标识](<internal/scanner/root_marker_test.go>)。
- [HTTP 与幂等回归](<internal/server/incremental_scan_test.go>)；前端 `node --test internal/server/web*_test.cjs`。
- [可重复性能基准](<internal/scanner/incremental_benchmark_test.go>)：200 组，本地临时目录，`-benchtime=1x`，仅单次对照而非稳定倍数承诺。

| 200 组场景 | full → incremental，毫秒 | 业务行 DML，full → incremental | 增量组重建 / 版本推进 |
|---|---:|---:|---:|
| 普通组，0% 变化 | 907.46 → 57.68 | 800 → 0 | 0 / 0 |
| 普通组，1% 变化 | 881.01 → 60.60 | 800 → 8 | 2 / 2 |
| 普通组，10% 变化 | 910.16 → 187.11 | 800 → 80 | 20 / 20 |
| 每组双 CD，0% 变化 | 2306.14 → 347.98 | 800 → 0 | 0 / 0 |
| 每组双 CD，1% 变化 | 2171.56 → 251.16 | 800 → 8 | 2 / 2 |
| 每组双 CD，10% 变化 | 1364.34 → 347.68 | 800 → 80 | 20 / 20 |

无变化场景扫描状态行 DML 也为 0。`benchmem` 的 B/op、allocs/op 是总分配指标，不是峰值内存；枚举/摘要/解析/提交分段耗时及真实解析次数尚未单独仪表化。严格 CD 成员复核仍逐组 ReadDir，大目录存在复杂度开销，不用可漏变化的目录时间戳缓存换取速度。

### 统一冻结验证

- 冻结后 `go test -count=1 -timeout=20m -coverprofile=/tmp/emby-incremental-coverage-final.out ./...` 全部通过；总语句覆盖率 **76.7%**，高于项目 70% 门禁。server 146.288s、store 72.313s、scanner 10.213s。
- `go vet ./...`、`go build ./...`、`gofmt -l internal/store internal/scanner internal/server` 与 `git diff --check` 通过。
- `node --test internal/server/web*_test.cjs` **163/163** 通过，无失败/跳过；app/login 语法检查通过。真实 Chromium 的局部静态 CSS/命中测试通过，不等于真实服务 E2E。
- 冻结后 `go test -race -count=1 -timeout=40m ./...` **全包通过**：server 1085.388s、store 665.555s、scanner 167.566s；退出码 0。此前目录 stat 缓存的失败促成撤回优化，不冒充通过；这里记录的是撤回及 manual CD 修复后的独立完整重跑。

**边界**：没有真实 SMB/NFS 故障演练、生产库迁移或真实服务浏览器 E2E；图片同大小/mtime 覆写仍是 stat 指纹残余。根标识与复验不是文件系统和 SQLite 的联合快照，无法排除所有外部并发改盘/挂载异常。未提交、推送或部署。

---

## 16. 前端功能布局与移动操作（2026-10-23，本地实施完成）

### 实现范围

- 保留暗色管理台和原生 JS/CSS，不改后端业务权限、任务身份隔离、计划参数/ETag 或危险确认。导航按职责分组；手机改为按钮开启的抽屉菜单，而不是十个入口横向滚动。
- 设置分成账户安全、用户与授权、服务连接等六类，每次只显示一类；切类不销毁输入，身份切换重置。Webhook 使用不含 URL/凭据的折叠摘要，已有目标默认收起、新目标展开；原保存、冲突和删除确认保留。
- 计划任务先显示现有列表，新建表单按需展开；刮削先显示执行与进度，连接配置收起。媒体墙主筛选与维护分层，详情的简介/演员与维护工具分开；独立翻译仍是独立功能。
- `navigation.js` 单独处理移动菜单、Esc/Tab 焦点循环、主内容 inert、关闭后焦点还原、跨断点复位及遮罩互斥；已接入显式静态路由、go:embed、ETag/no-cache。手机操作目标按至少 44 CSS px 和 8px 间距设计，兼顾 safe-area、窄屏表单/表格和详情抽屉。

### 验证证据

- 冻结后 `node --test internal/server/web*_test.cjs` **171/171 通过**，无失败/跳过；app/navigation/webhook-settings/login 语法检查和 `git diff --check` 通过。
- 新增 `internal/server/web_navigation_test.cjs` 四组独立导航状态测试；`internal/server/web_layout_test.cjs` 使用现有 Chromium、内置 CDP 与模拟 API，在 **1280、768、390、360 CSS px** 下运行完整页面脚本，覆盖七页面、六设置分类、展开表单/详情、选片与管理按钮实际命中及触控尺寸、菜单/焦点和文档横向溢出。测试启用减少动画偏好以稳定几何测量，不以隐藏 checkbox 代替可见标签命中。
- `TestNavigationEmbeddedAsset` 验证导航资源 200、JS 类型、no-cache/ETag、304 及加载顺序。全量 `go test -count=1 -timeout=20m ./...` 通过（server 106.511s、store 57.611s、scanner 9.858s）；`go vet ./...`、`go build ./...` 通过。
- 本轮不重跑完整 race；上一节完整 race 结果属于增量扫描批次，不能冒认为本轮结果。

**验证边界**：Chromium 使用临时文件和模拟数据，不启动业务服务、不访问生产；不等于真实 API/网关 E2E、手机真机、Safari/Firefox 或读屏验收。截图已生成，但当前模型不支持图像输入，未作人工视觉审图。部署需重新构建包含嵌入资源的二进制并刷新浏览器；本轮未提交、推送或部署，既有工作区改动与取证资料保留。

---

## 17. NFO 不可读保护与契约补登记（2026-10-24，本地实施完成）

### 实现范围

- 扫描不再把「NFO 存在但解析失败」当成清空元数据的指令。增量路径在 `buildRecords` 记录候选文件与解析错误并通过 `ScanGroupMovie.KeepIndexedMetadata` 透传；`commitScanGroup` 在人工保护分支之前走保留分支：只更新 `file_size/file_mtime/source_protocol/source_container/additional_parts/updated_at`（复用 `updateManualScanStructureTx` 的差异谓词），元数据、演员、特征与 `status` 原样保留，`last_scrape_error` 写入 `[NFO 不可读] <path>: <err>`（截断 300 字符）。全量/单文件路径同样区分 `os.ErrNotExist`（无 NFO，仍按 pending）与其他读取错误（保留，`IndexMovieUnreadableNFO`）；从未索引过的源仍是 pending 入库并带同前缀原因，用户能看到并修。
- 恢复是自动的：NFO 可解析后正常索引并清除 `[NFO 不可读]` 前缀的备注（增量在提交事务内，全量在读后确认再写，且只清本前缀，不动刮削失败/待确认）。
- 扫描结果新增 `unreadable_nfo`，前端在扫描摘要里显示「NFO 不可读 N（已保留原元数据）」；「重读源」单文件入口在同一情形下返回 `nfo_unreadable: true` 与 `warning`，避免操作者以为文件被读到了；`API-CONTRACT.md` §4.4 登记字段与语义。
- `API-CONTRACT.md` §3 补登 `500 ITEM_DATA_UNAVAILABLE`（`internal/server/item_associations.go`），消除 code 表漂移。
- `dev-plan.md` §3.1 增加对应硬约束；`docs/emby-study/jellyfin-reading-notes.md` §8 标注第 1/3 项已落地、第 2 项（目录级 `movie.nfo` 兼容）经确认不做。

### 验证证据

- 新增 `internal/scanner/unreadable_nfo_test.go`：好 NFO → 破坏 → 增量与全量各扫一遍 → 断言标题/简介/`status=success`/可见性保持、`unreadable_nfo=1`、备注带前缀；修好 NFO 再扫 → 标题刷新、备注清空；另测「新源 + 坏 NFO」入库为 pending。新增 `internal/server/image_conditional_test.go`：原图与缩略图的 `Cache-Control` 不得含 `immutable`（保留 `max-age` + `must-revalidate`），`If-None-Match` 单值/多值/`W/`/`*` 必须 304，不匹配必须 200。
- `go build ./...`、`go vet ./...`、`gofmt -l internal/`（无输出）、`git diff --check` 通过；`go test -count=1 ./...` 全包通过（server 115.084s、store 64.666s，最后一次完整运行）；`node --test internal/server/web*_test.cjs` **171/171** 通过。扫描进度快照（`scanStatus`）同步暴露 `unreadable_nfo`，前端摘要据此显示。
- 本轮未重跑 race 与覆盖率，不冒认既有结果。

**验证边界**：未做真实挂载/NFS 故障、真实 Emby 客户端或生产库验证；「保留」只覆盖元数据与可见性，不保证 NFO 目录级候选兼容（明确不做）。本轮未提交、推送或部署；工作区既有改动（API-CONTRACT/PLAN-STATUS/README/dev-plan 的历史改动、docs/* 删除、reports/）保持原样。

---

## 18. 缩略图磁盘变体缓存与尺寸档位（2026-10-24，本地实施完成）

### 实现范围

- 先量化再动手：`internal/imageutil.Thumbnail` 全链路（webp 解码 + 盒式缩放 + 编码）在本机 2 核实测 —— 1000×1500 源缩到宽 200 为 **70ms**（解码 44–60、缩放 15、编码 ~15），缩到宽 400 为 **122–152ms**；纯 Go 的 `deepteams/webp` 解码是主要成本。一页 100 部海报墙（前端同时要 200 与 400 宽）约 **19s CPU**，2 核加 `thumbSem` 4 并发约 8–10s 出齐；命中内存、命中 304、无尺寸参数直出原图时基本零成本，所以问题集中在「首次/重启后重算」与「并发重复算」。
- 原实现只有进程内 LRU（512 条、TTL 30 分钟）：一页就能把缓存顶掉，重启即全部重算。新增 `internal/imagecache`：`Disk` 以 sha256 前 16 字节做两级分片（`<2hex>/<32hex>.webp`），`Put` 走同目录临时文件 + `rename` 原子写，`Get` 命中时按 1 小时节流刷新 mtime 作为「最近使用」的近似，`Trim` 先删 TTL 过期、再按 mtime 由旧到新淘汰到 80% 水位，`MaybeTrim` 后台执行且十分钟节流 + 单飞。缓存不可用（目录建不出来）时降级为纯内存缓存并告警，图片接口行为不变。
- `internal/server/media.go` 的命中顺序改为 进程内 LRU → 磁盘变体 → 同 key 合并生成 → 信号量 → 复查（内存、磁盘）→ 生成 → 内存 + 落盘；`beginThumbFlight/finishThumbFlight` 实现 singleflight（follower 等 `done` 或请求 ctx 取消），避免 `<img>` 与 `srcset` 同时开时把同一份 CPU 工作排两遍。生成函数抽成 `App.thumbGenerate`，测试据此统计真实编码次数。
- 尺寸按档位**向下**取整（`imageEdgeBuckets`，195 → 180）：变体集合可枚举、彼此更易复用，且响应永远不超过请求的 `maxWidth/maxHeight`（不破坏客户端的布局语义）；`<32` 保留原值。
- 配置 `image_cache{enabled, dir, max_bytes, ttl_hours}`，默认启用、目录取 `db_path` 同级 `image-cache/`、上限 2GiB、保留 168 小时；`-1` 表示不限/不过期；`config.Load` 与 `server.New` 两处都套默认值（测试直接构造 `config.Config` 会绕过 `Load`）。启动时清理一次，之后由写入侧触发。
- 文档：`config.example.yaml` 增 `image_cache` 段（含成本说明）、`API-CONTRACT.md` §4 图片行补「按档位向下对齐、同一输入只编码一次」、`.gitignore` 增 `/image-cache/`。

### 验证证据

- 新增 `internal/imagecache/disk_test.go`：读写回环且不留临时文件、key 稳定且不同输入不撞文件、按容量淘汰最旧（1500 字节上限删到剩最新一份）、TTL 过期在 `Trim` 时删除、禁用/降级实例为纯空操作。
- 新增 `internal/server/image_disk_cache_test.go`：首次请求生成并落盘（1 份变体）；`imgThumb.Clear()` 后同 URL 仍 200 且**零新增生成**（走磁盘）；`maxWidth=195` 与 `190` 都落在 180 档、响应 180×90 且字节完全相同、不新增变体；8 个并发同 URL 只生成 1 次（计数 hook，生成窗口内人为 sleep 30ms）；改写源图 mtime 后新增 1 份变体；`Cache-Control` 仍含 `max-age` 且不含 `immutable`。新增 `internal/config/config_test.go` 两例：默认值/显式关闭/`-1` 语义、YAML 覆盖生效。
- `go build ./...`、`go vet ./...`、`gofmt -l internal/`（无输出）、`git diff --check` 通过；`go test -count=1 ./...` 全包通过（server 120.556s、store 67.818s、scanner 8.310s、imagecache 0.010s，最后一次完整运行）；`node --test internal/server/web*_test.cjs` **171/171** 通过。
- `go test -race -count=1 ./internal/imagecache/` 与 `go test -race -count=1 ./internal/server/ -run TestImage` 均通过（后者 29.655s），覆盖 singleflight 与磁盘读写的并发路径。

**验证边界**：未在真实大目录（十万级变体）上测 `Trim` 耗时与 IO 抖动；未做多实例共享同一缓存目录的压测（singleflight 只在进程内生效，多实例会各生成一次，靠 rename 原子性保证不读到半文件）；未用真实 Emby 客户端验证 180 档缩略图的观感；容量/TTL 的淘汰策略以 mtime 近似 LRU，长时间只读不写的变体可能被淘汰后重新生成。本轮未提交、推送或部署；工作区既有改动（API-CONTRACT/PLAN-STATUS/README/dev-plan 的历史改动、docs/* 删除、reports/）保持原样。

---

## 19. 图片缓存进入设置页：在线容量上限与清理（2026-10-25，本地实施完成）

### 实现范围

- 设置页新增「图片缓存」分类（`internal/server/web/image-cache-settings.js`）：展示缓存目录、启用状态、当前占用（字节 + 变体数）、容量上限，提供「保存上限」与「立即清理」。上限输入以 GiB 为单位（0 = 不限制），前端换算成字节提交，不引入第二套单位语义。
- 后端 `internal/server/image_cache_settings.go`：`GET /api/admin/image-cache/settings` 返回 `{dir,enabled,max_bytes,used_bytes,files,max_allowed_bytes,config_etag}`；`PUT` 接受 `{max_bytes}`（0–64 GiB）并沿用日志期限设置的并发约定——缺 `If-Match` 428、不匹配 412 `IMAGE_CACHE_SETTINGS_CHANGED`、配置禁用时 409 `IMAGE_CACHE_DISABLED`；`POST /api/admin/image-cache/trim` 立即清理。
- 持久化走既有的 `settings` KV（键 `image_cache.max_bytes`）：配置文件仍是部署默认值，设置页保存的值优先，重启后仍然生效；坏值在启动时降级为「沿用配置文件」并告警，不让服务起不来。
- `internal/imagecache` 增 `Stats()`（只统计 `.webp`，排除临时文件）与 `SetMaxBytes()`/`MaxBytes()`；`maxBytes`/`ttl` 的读取改到 `trimMu` 下，避免设置页写入与后台清理并发读写同一字段。
- 保存后立刻按新上限清理（最多等 2 秒，超出部分交给后台，响应不装成「已清完」）；`Trim` 的 80% 水位计算改为 `maxBytes*80/100`，修掉小上限（<100 字节）时整数除法把水位算成 0、误删整目录的问题。
- 文档：`API-CONTRACT.md` §4.3 增端点行；`config.example.yaml` 注明 `max_bytes` 可被管理页在线覆盖。

### 验证证据

- 新增 `internal/server/image_cache_settings_test.go`：未认证 401、非管理员 403；GET 快照字段与 `no-store`；11 组非法 body 全部 400；缺 `If-Match` 428、过期 `If-Match` 412；保存后 DB 持久化、重启保留、`0` 立即落到 `Disk`；写入 3 份变体后 GET 统计 3/3000 字节，上限调到 2048 后同步清理到 ≤1 份；禁用配置下 GET `enabled:false` 且 PUT 409；坏值设置不阻断启动并回落 2 GiB；静态资源可服务、ETag 命中 304、`/admin` 引用该脚本。
- `internal/imagecache/disk_test.go` 增 `TestDiskStatsAndSetMaxBytes`：临时文件不计入占用、上限 6 字节时按水位删到 1 份、`0`/负数归一成不限制、禁用实例与 nil 接收者均为空操作。
- 新增 `internal/server/web_image_cache_test.cjs`（3 个用例）：体积格式化与 GiB 往返、渲染占用/保存换算成字节并带 `If-Match`、412 时保留草稿且禁止重提、禁用配置锁死表单、清理按钮走 POST 并回显释放量。
- `go build ./...`、`go vet ./...`、`gofmt -l internal/`（无输出）、`git diff --check` 通过；`go test -count=1 ./...` 与 `node --test internal/server/web*_test.cjs` 全绿（详见提交前的最终运行）。

**验证边界**：`used_bytes`/`files` 是即时 walk 目录的结果，十万级变体时单次读取会慢一个量级（前端只在打开/保存/清理后各读一次）；上限只约束本进程的清理策略，多实例共享同一目录时各自看到同一个上限但清理时机不协调；设置页不能开启配置里已经关闭的缓存（`enabled` 只能改配置文件）。

---

## 20. MetaTube 能力对齐施工计划（2026-11-03）

### 范围决策

用户在插件能力对照后明确裁剪本阶段范围；本节覆盖先前“优先补来源过滤”等建议。设计边界、依赖及逐项验收见 [方案 §4.1](<dev-plan.md#41-metatube-能力对齐方案2026-11-03-范围更新>)，本节只维护施工状态。

| 项目 | 当前决策 | 施工边界 |
|---|---|---|
| 预告片刮削与生成 | 暂缓 | 不接入预告片详情、不生成预告片 STRM、不增加任务；已有 NFO 预告片读取不移除 |
| 自定义替换表 | 暂缓 | 不开发标题、演员、类型替换表及配置界面 |
| 标题与 Tagline 模板 | 暂缓 | 保留现有固定标题规则，不引入模板引擎 |
| 全局来源筛选与排序 | 暂缓 | 不增加来源白名单及优先级；已有手动指定单一来源继续保留 |
| 翻译扩展 | 当前够用，维持现状 | 现有服务和开关继续维护，不新增翻译引擎、不改现有翻译状态/缓存策略 |

“暂缓”不是已完成，也不是永久取消；重新启动需另行排期。本轮仅更新计划，不启动实现、不改变运行中的服务。

### 已有基线（不是本轮新增）

- 四种影片手动识别已在前序实现，搜索与预览只读，确认后才进入写入流程。
- 官方 `Provider:movie-id` 标记已支持写入、导入、精准复用及历史 `metatube:` 前缀兼容；只补缺失、锁字段及旧 API 键兼容保留。
- Position/Update 目前仅解析兼容，完整裁切/强制刷新语义仍属于下表待施工项，不能算已验收。
- 演员头像、文件名版本标记/角标和现有翻译继续作为回归基线，不重复立项。
- 前序本地验证记录：身份适配后 `go test ./...` 与前端 260 项测试通过，并执行了相关身份路径 race；这是前序运行记录，不是本次文档更新重新跑出的结果，也不代表生产验收。

### 保留施工队列

状态图例：○ 待施工 · ◐ 施工中 · ✅ 本地实施与回归完成。当前没有条目进入施工中。

| ID | 施工项 | 状态 | 前置与交付门槛 |
|---|---|---|---|
| MT-01 | Position/Update 完整语义 | ○ | 结构化后缀兼容旧解析调用方；验证 Update→lazy 参数及精准失败不转搜；不改变只读预览和写入锁 |
| MT-02 | 图片 ratio/pos/auto 与缓存隔离 | ○ | 依赖 MT-01；核对上游参数范围、保留旧默认请求、真实图片验证裁切效果 |
| MT-03 | 剧照与多图片候选 | ○ | 依赖 MT-02；先定图片角色与持久化命名；选择确认后写入，不默认全量下载 |
| MT-04 | 中文字幕整理增强 | ○ | 复用现有标记和写入保护；明确人工与程序标签归属、外置字幕识别和任务入口，不默认整库改写 |
| MT-05 | 演员独立资料与精准身份 | ○ | 先定持久真源及重建协议；人工选择、同名防误配、头像兼容和关系回归 |
| MT-06 | AVBASE 姓名转换建议 | ○ | MT-05 身份边界明确后实施；默认关闭、人工确认，不把相似度 0.8 当作安全自动覆盖保证 |

按 MT-01 → MT-02 → MT-03 → MT-04 → MT-05 → MT-06 建议顺序推进；不把暂缓的来源筛选、替换表、模板或预告片当成前置。系列自动合集与外链另行评估，插件自身自动更新不迁移为本项目功能。

### 后续验收与本轮验证边界

- 各施工项须交付实现、针对性测试、契约/使用说明更新；涉及 NFO、图片落盘和索引更新时继续报告部分成功并支持恢复，不用文件回滚伪装联合事务。
- 功能批次按方案 §5 执行 build/vet/Go/前端回归，并发路径按需 race；图片和选择 UI 补真实图片与浏览器验证。真实 MetaTube/客户端或生产环境未验证时单列边界。
- 本轮仅修改本台账与方案，检查文档差异及 `git diff --check`；未重跑功能测试、未提交、未推送、未重建或部署。既有未提交代码和历史文档改动保留。
