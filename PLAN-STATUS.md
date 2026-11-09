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

“暂缓”不是已完成，也不是永久取消；重新启动需另行排期。2026-11-03 那一轮只修订计划；2026-11-04 起按下方“施工记录”开始落地，暂缓项仍未启动。

### 已有基线（不是本轮新增）

- 四种影片手动识别已在前序实现，搜索与预览只读，确认后才进入写入流程。
- 官方 `Provider:movie-id` 标记已支持写入、导入、精准复用及历史 `metatube:` 前缀兼容；只补缺失、锁字段及旧 API 键兼容保留。
- Position/Update 现已解析为结构化选项并驱动精准查询的 `lazy`（见下方 2026-11-04 施工记录）；Position 的图片参数与实际裁切已在 MT-02 落地本地实现与回归，真实图片效果仍待上游实例验收。
- 演员头像、文件名版本标记/角标和现有翻译继续作为回归基线，不重复立项。
- 前序本地验证记录：身份适配后 `go test ./...` 与前端 260 项测试通过，并执行了相关身份路径 race；这是前序运行记录，不是本次文档更新重新跑出的结果，也不代表生产验收。

### 保留施工队列

状态图例：○ 待施工 · ◐ 施工中 · ✅ 本地实施与回归完成。当前 MT-01 为 ◐（结构化后缀与 Update 语义已交付，Position 写回政策待定），MT-01 管理可见性补充为 ✅，MT-02 为 ◐（参数边界、缓存隔离与 Position 串联已交付，真实照片复算 PASS，端到端待上游实例），MT-03 为 ◐（只读剧照候选已交付，写入待持久化布局冻结），MT-04 为 ◐（外置中文字幕判定已交付，标签撤销与任务入口待冻结）。

| ID | 施工项 | 状态 | 前置与交付门槛 |
|---|---|---|---|
| MT-01 | Position/Update 完整语义 | ◐ | 结构化后缀兼容旧解析调用方（已交付）；验证 Update→lazy 参数及精准失败不转搜（已交付）；Position 已随候选/请求传递并接入图片参数（MT-02 已交付），仅剩写回官方标记的政策待定 |
| MT-02 | 图片 ratio/pos/auto 与缓存隔离 | ◐ | 上游参数范围已核对并落地边界校验；裁剪参数进缓存 key；Position 串联到主海报下载与预览；真实照片离线复算裁切语义 PASS，端到端真实上游实例本机不具备 |
| MT-03 | 剧照与多图片候选 | ◐ | 只读候选链路已交付（`preview_images` → `stills[{index,preview}]` → `kind=still&index=`）；服务端重新解析来源、客户端只提交受限下标；写入路径仍待持久化布局冻结，不默认全量下载 |
| MT-04 | 中文字幕整理增强 | ◐ | 外置中文字幕识别已交付（文件名后缀优先、否则同目录同主名字幕语言段，只写既有 `C` 标签/角标）；剩程序标记归属与撤销（删字幕后已写 `<tag>` 不消失）、用户自定义分类、独立任务入口未定；未新增整库改写入口 |
| MT-05 | 演员独立资料与精准身份 | ○ | 先定持久真源及重建协议；人工选择、同名防误配、头像兼容和关系回归 |
| MT-06 | AVBASE 姓名转换建议 | ○ | MT-05 身份边界明确后实施；默认关闭、人工确认，不把相似度 0.8 当作安全自动覆盖保证 |

按 MT-01 → MT-02 → MT-03 → MT-04 → MT-05 → MT-06 建议顺序推进；不把暂缓的来源筛选、替换表、模板或预告片当成前置。系列自动合集与外链另行评估，插件自身自动更新不迁移为本项目功能。

### 后续验收与本轮验证边界

- 各施工项须交付实现、针对性测试、契约/使用说明更新；涉及 NFO、图片落盘和索引更新时继续报告部分成功并支持恢复，不用文件回滚伪装联合事务。
- 功能批次按方案 §5 执行 build/vet/Go/前端回归，并发路径按需 race；图片和选择 UI 补真实图片与浏览器验证。真实 MetaTube/客户端或生产环境未验证时单列边界。
- 2026-11-03 那一轮只改台账与方案并做 `git diff --check`；2026-11-04 施工轮已实跑 build/vet/go test/前端回归（见下方施工记录），仍未提交、未推送、未重建或部署。既有未提交代码与历史文档改动保留。

### 2026-11-04 施工记录：MT-01 后缀载体与 MT-01 管理可见性补充

**交付内容**

- `internal/metatube/provider_id.go`：新增 `ProviderID{Provider, ID, Position *float64, Update *bool}` 与 `ParseProviderIDValue`、`(ProviderID).Lazy()`、`(ProviderID).String()`；`ParseProviderID` 保留原签名并委托新解析，`FormatProviderID` 未变。身份与请求选项分离，后缀原文保留在 `String()`，不重复解码 ID。
- `internal/scraper/apply.go`：新增 `ParseStoredProviderID`（兼容历史 `metatube:` 前缀）与 `CanonicalProviderID`（仅当历史值本身即官方格式才可复用，否则判不可回填）；`SplitProviderID` 委托前者，3 返回值签名不变，既有调用方（`internal/server/library.go:1061` 等）无需改动。
- `internal/scraper/{scraper,manual,run}.go`：预览、手动精准查询、已存身份与批量精确复用统一按 `pid.Lazy()` 决定上游 `lazy`；`Candidate` 新增可选 `update`，`Query`/`ProviderIds.MetaTube` 保留后缀；`ScrapeOneMovie` 改为接收 `metatube.ProviderID`。
- `internal/server/scrape.go`：inspect 与 confirm 请求体新增可选 `update`，缺省保持旧行为（`lazy=true`）。
- `internal/server/detail.go:134-152`：管理端单条详情兼容新增只读派生字段 `metatube_id{saved,canonical,valid}`，由已扫描索引值派生，不读盘、不新增端点、不扩大普通用户可见字段。
- 前端 `internal/server/web/app.js`、`style.css`：详情抽屉只读展示「已保存 MetaTube ID」（可复制、不可解析时只作文本并提示）；刮削弹窗独立「已保存」区，仅有效标记回填搜索草稿，打开弹窗仍走无参自动精准复用；候选 `update` 透传 inspect 与确认请求。
- 契约：[API-CONTRACT.md](<API-CONTRACT.md>) §4.5 补充后缀/lazy 语义与 `update` 字段，新增 §4.6 描述 `metatube_id` 派生字段；确认端点笔误修正为 `POST /api/admin/items/:id/scrape`。

**本轮决策（执行 Lead，已实施、待用户批准）**

1. Update 消费点为每次精准 `MovieInfo`（预览/inspect/确认重取/批量），true→`lazy=false`，未设置或 false→`lazy=true`；不做"只强刷一次再回缓存"。理由：方案只给出这一确定语义，缺少上游内容一致性的退回依据。
2. Position 只做载体，不进入图片参数、不写回标记；裁切与缓存维度随 MT-02 冻结验收。
3. 历史 `metatube:` 前缀按不透明值处理，只有本身即官方格式才可展示/回填。

**验证（本轮实跑）**

- `gofmt -l internal/ cmd/` 无输出；`go build ./...` 通过；`go vet ./...` 无输出。
- `go test ./... -count=1` 全绿：metatube 0.611s、scraper 4.526s、server 85.080s、store 52.277s、scanner 6.185s、nfo 8.053s、artwork 9.408s，其余包 ok。
- `node --test internal/server/web*_test.cjs` → 263 pass / 0 fail。
- 新增用例：`TestProviderIDSuffixSemantics`、`TestPreviewManualRequests`（后缀与 lazy 列）、`TestStoredIdentityUpdateForcesRefresh`、`TestScrapeLookupOptionsCarryUpdate`、`TestAdminItemDetailMetatubeID`，以及 web 三条展示/回填/透传契约用例。
- 未做：真实 MetaTube 上游与真实浏览器走查；MT-01 的 Position 验收与写回政策仍待 MT-02 同批冻结。未提交、未推送、未部署。

### MT-02 前置：上游图片参数契约核对（2026-11-04）

核对对象为同机上游实现 `/root/github/holll/metatube-sdk-go`（metatube-sdk-go），用于把 MT-02 的参数范围固化在真实契约上，不臆造默认值。

- 端点：`GET /v1/images/{primary|thumb|backdrop}/:provider/:id`，查询参数 `url`、`ratio`、`pos`、`auto`、`badge`、`quality`（`route/image.go:31-38`）。
- 上游缺省值：`ratio=-1`（未设置哨兵）、`pos=-1`（手动位置关闭）、`quality=90`、`auto=false`（`route/image.go:64-68`）。
- 每种图片的服务端常量：primary `2/3`、thumb `16/9`、backdrop `0`（不裁切）（`constant/ratio.go:3-7`）。thumb 与 backdrop 分支直接使用常量，**忽略** ratio/pos（`route/image.go:100-112`）。
- primary 且未带 `url` 时的实际语义（`engine/image.go:41-56`）：`ratio<0` → 用 `2/3`；`pos<0` → 用 `1.0` 并允许按番号触发行人脸检测（`auto=RequiresFaceDetection(number)`）；`pos>=0` → 手动位置生效且强制 `auto=false`。
- 裁切实现：`ratio` 仅在 `[1e-2, 1e2]` 内裁切，越界原图返回；`pos` 是长边上的相对位置（0..1），非负时按中心对齐后夹紧（`imageutil/crop.go:14-36`）。
- `auto` 只在带 `url` 的分支被读取（`route/image.go:93-96`）；本服务现有代理不带 `url`，因此对外暴露 `auto` 会是**无效果参数**，按编码红线不引入，待出现 `url` 场景（MT-03 候选）再评估。

据此固化的 MT-02 施工口径：本服务只对 `kind=primary` 接受可选 `ratio`/`pos`，边界校验 `ratio ∈ [0.01, 100]`、`pos ∈ [0, 1]`，非有限值或越界返回 400；`thumb`/`backdrop` 携带这两个参数同样 400，不做静默忽略；缺省不发送参数，保持旧请求与旧结果；`quality`（预览 70、落盘按配置）与 `badge` 维持现状；任何改变结果的参数都必须进入服务端缓存 key 与浏览器请求 URL。Position 来自 MT-01 已解析的标记，仅作用于 primary，不写回标记（写回政策仍待批准）。

### 2026-11-04 施工记录：MT-02 图片裁切参数与缓存隔离（MT-02 ◐）

**交付内容（代码）**

- `internal/metatube/client.go`：新增 `ImageOptions{Quality, RemoteURL, Badge, Ratio, Position}`；`ImageURL(kind, provider, id, opts)` 用 `strconv.FormatFloat(v,'g',-1,64)` 下发 `ratio`/`pos`，零值不下发任何可选参数（旧 URL 逐字不变）。
- `internal/scraper/apply.go`：`ApplyOptions` 新增 `Position *float64`；只有 `primary` 目标取该位置（`thumb`/`backdrop` 上游裁切比例固定，传了也是空转）；`downloadImage` 增加 `position` 形参并转 `ImageOptions`。
- `internal/scraper/run.go`：已存身份的单条与批量路径把 `pid.Position` 传给 `ApplyOptions`；搜索路径传 `nil`（普通候选没有位置）。
- `internal/scraper/scraper.go`：`Candidate` 新增可选 `position`，`carryLookupOptions` 与 `update` 一并带回，保证预览与落盘是同一张图。
- `internal/artwork/artwork.go:290`：改调 `ImageOptions{Quality: 90}`，行为不变。
- `internal/server/scrape.go`：图片代理新增 `optionalFloatQuery`/`validRatio`/`validPosition`/`cropKey`；`ratio`/`pos` 越界、非有限值或 `kind != primary` 携带时 400；两者进入 `imgThumb` 缓存 key；inspect 与确认请求体新增可选 `position` 并做同一边界校验（400 `INVALID_REQUEST`）。
- `internal/server/scrape_inspect.go`：`buildInspect` 接受 position，`poster` 与 `images[].preview` 的主海报地址带 `&pos=`，宽幅图不带；新增 `targetPosition`。
- 前端 `internal/server/web/app.js`：候选 `position` 与 `update` 同路径透传到 inspect 与确认请求体，缺省不发字段（`JSON.stringify` 丢弃 `undefined`）。
- 契约：[API-CONTRACT.md](<API-CONTRACT.md>) §4.5 补 `position` 语义、候选字段、inspect 预览 URL 与图片代理 `ratio`/`pos` 边界。
- 落地图片的 CacheTag 由既有失效链路覆盖：确认写入后 `len(result.Images)>0` 即调 `invalidateImageTag`（`internal/server/scrape.go:614-616`），未新增缓存机制。

**本轮决策（执行 Lead，已实施、待用户批准）**

1. Position 只在写图与预览时消费，不写回 NFO 身份（写回政策未定，不写回不会制造错误身份）。
2. `auto` 不暴露（只在上游带 `url` 的分支生效，本服务代理从不带 `url`）；`quality`/`badge` 维持现状，不新增开关。
3. 非 primary 携带 `ratio`/`pos` 直接 400 而非静默忽略，避免让人误以为生效。

**验证（本轮实跑）**

- `go build ./...` 通过；`go vet ./...` 无输出；`gofmt -l internal/ cmd/` 无输出。
- 新增用例：`TestImageURL`（零值无查询串、`pos=0` 与 `ratio=0.66666` 下发）、`TestApplyPositionCropsOnlyPrimaryPoster`、`TestStoredIdentityPositionCarriedToPreviewAndApply`、`TestScrapeImageCropParams`（越界/NaN/非 primary 400、缓存 key 隔离、换位置重取）、`TestScrapeInspectPosterCarriesPosition`、`TestScrapeConfirmForwardsPosition`，以及 web 侧 `update`/`position` 透传断言。
- `go test ./... -count=1` 全绿（artwork 13.2s、metatube 0.6s、scraper 5.9s、server 105.5s、store 70.2s，其余 ok）；`node --test internal/server/web*_test.cjs` → 263 pass / 0 fail。
- **真实图片裁切验证（PASS）**：用上游 SDK 的 `imageutil.CropImagePosition` 对 1200x900 真实照片离线复算 `ratio=2/3`：`pos` 0.0/0.1 → 600x900 at x=0（夹紧）、0.5 → x=300、0.9/1.0 → x=600（夹紧），像素哈希随窗口移动而变化；`ratio=0.001`/`101` 返回原图不裁切（正是本服务在边界直接 400 的原因）。脚本与结论留痕在 `tools/cropverify/`（`/tools/` 被忽略，不进版本库）。
- 未做：端到端真实上游实例验收（`./metatube` 是本仓库 `cmd/metatube`，本机无 MetaTube 服务实例）；真实浏览器走查。未提交、未推送、未部署。

### 2026-11-04 施工记录：MT-03 只读剧照候选（MT-03 ◐）

**范围**：方案要求「未确定持久化布局前不开始多背景图写入实现」，本库现有多图布局仍是单图 `poster.webp`/`fanart.webp`/`landscape.webp`（`internal/scraper/apply.go:35`、`internal/server/scrape_inspect.go:73`），没有 extrafanart 约定。故本批只做**只读候选**：能看见剧照、能验证候选链路，不写 NFO/数据库/图片文件。

**交付内容（代码）**

- `internal/metatube/client.go`：`MovieInfo` 新增 `PreviewImages []string json:"preview_images"`（上游 `model/movie.go:88` 的字段）。
- `internal/server/scrape_inspect.go`：`scrapeStill{index,preview}`；`stillURLs` 归一化（去空白、去重、截断到 `maxScrapeStills=24`），`inspect` 与图片代理共用同一份口径；`scrapeStillURL` 生成 `kind=still&index=N`。
- `internal/server/scrape.go`：图片代理新增 `kind=still`，必须带非负整数 `index`（缺省/负数/非数字 400，越界 404），拒绝 `ratio`/`pos`；由 `scrapeStillURLs` 按 `(provider,id)` 重新解析上游元数据并缓存 10 分钟，图片字节按 `(provider,id,index)` 缓存；上游取图走 `primary + url=` 分支，固定 16:9 居中（`stillRatio`/`stillPosition`）。
- `internal/server/server.go`：`App.scrapeStills *cache.Memory`（`scrapeStillCacheSize=256`）。
- 前端 `internal/server/web/app.js`：刮削弹窗「图片」之后新增只读剧照网格（`esc` 地址、`loading="lazy"`、`#N` 序号），缺失/空数组统一提示「上游没有剧照」；`style.css` 补 `.scrape-stills` 自适应网格。
- 契约：[API-CONTRACT.md](<API-CONTRACT.md>) §4.5 补 `stills` 字段与 `kind=still` 的下标/400/404/16:9/只读语义。

**本轮决策（执行 Lead，已实施、待用户批准）**

1. 剧照候选对外只暴露归一化下标，代理按 `provider`/`id` 重新解析来源；不新增 `url` 参数、不新造授权体系。
2. 解析列表与图片字节分层缓存，避免把上游元数据接口当图片接口用；代价是匿名图片请求会触发一次上游元数据解析，属对既有匿名图片契约的最小扩展。**更正（同日核对）**：此前写的「4 路并发上限」并不存在——代码里没有独立闸门，实际只受单次请求 30s 总超时、响应头上限、8MB 下载上限与出站默认 16 条同主机连接上限约束（超出排队而非失败）；是否加闸门需先定阈值，未做。
3. 剧照固定 16:9 居中裁切；样张多为横图，主海报的 2/3 会裁得只剩中间一条。

**验证（本轮实跑）**

- `gofmt -l internal/ cmd/` 无输出；`go build ./...`、`go vet ./...` 通过。
- 新增用例：`TestScrapeInspectStills`（去空/去重/截断 24、下标连续、地址走代理）、`TestScrapeStillProxy`（缺 index/负数/非数字/带 ratio/pos → 400；越界 → 404；固定 16:9；同图命中缓存；换下标复用已解析列表，元数据只解析一次）、`TestMovieInfoAndLazyParam` 补 `preview_images` 解析断言；web 侧新增 2 个剧照渲染用例。
- `node --test internal/server/web*_test.cjs` → 265 pass / 0 fail。

**未做**：多背景图/剧照写入（待持久化布局与覆盖删除协议冻结）；真实上游实例端到端验收；真实浏览器走查。

---

### 2026-11-04 施工记录：MT-04 外置中文字幕判定（MT-04 ◐）

**范围界定**

方案要求「结合文件名及同目录外置字幕判断」，同时禁止把文件名判断说成字幕流内容检测、禁止为此新增全量媒体探测。本库此前只按文件名后缀（`-C/-U/-UC`）判定版本标记，同目录 `.srt`/`.ass` 等外置字幕完全不参与——即「有中文字幕但文件名没标 C」的片子拿不到中文字幕标签与角标。本批只补这一条判定链路，不碰写入保护、不新增整库任务入口。

**交付内容（代码）**

- `internal/scraper/mark.go`：新增 `subtitleExts`（`.srt/.ass/.ssa/.vtt/.sub`）、`chineseSubtitleTokens`（`zh/chi/zho/chs/cht/zh-cn/zh-tw/zh-hk/zh-hans/zh-hant/chinese/中文/中字/简体/繁体/简中/繁中`，小写比较）、`hasChineseSubtitle(sourcePath string) bool`（读同目录、只认主名 + `.` 之后的语言段 + 字幕扩展名；目录不可读按「没有」返回，不让它挡住刮削）、`versionMarkFor(sourcePath string) (versionMark, bool)`（文件名标记优先，否则按字幕推断 `C`）、`markByCode(code string) (versionMark, bool)`（`detectVersionMark` 与 `versionMarkFor` 共用的表查询，承接原内联循环）。
- `internal/scraper/apply.go:88-91`：标记来源由 `detectVersionMark` 换成 `versionMarkFor`，注释改为「文件名没有标记时再看同目录外挂字幕」。NFO 标签与主海报角标两条既有消费路径不变。
- 测试 `internal/scraper/mark_subtitle_test.go`：`TestVersionMarkForExternalSubtitle`（9 例：无字幕、`zh`、`简体`、大写扩展名、`en` 不算、裸 `movie.srt` 不算、主名不同不算、非字幕扩展名不算、文件名 `-U` 优先于 `zh` 字幕）、`TestHasChineseSubtitleMissingDir`、`TestApplyChineseSubtitleMark`（端到端：只有 `chs.srt` 时 NFO 写 `<tag>中文字幕</tag>` 且主海报 `badge=zimu.png`、`thumb` 不贴；只有 `en.srt` 时两者都没有）、`TestApplyChineseSubtitleMarkFollowsFileChanges`（删字幕后重刮：角标与「中文字幕」标签一起消失，标签走块级替换）。

**本轮决策（执行 Lead，已实施、待用户批准）**

1. 只按文件名判定语言，不读字幕内容、不做媒体探测；裸 `movie.srt` 语言未知，不当中文字幕。
2. 文件名后缀优先于目录推断：`-U`/`-C`/`-UC` 是人工显式标注，字幕文件只是补充信号。
3. 撤销沿用既有块级替换语义（`internal/nfo/update.go:76` 的 `listTags`）：只要本次算出的标签列表非空（真实刮削通常有 `label`），整段 `<tag>` 按新值重建，所以字幕增删会连带反映；同一段里的人工标签也会被替换，区分程序标记与人工标签仍需标记归属记录，属 MT-04 后续。端到端实测见下方「端到端上游实例验收」。
4. 不新增整库改写入口：判定只在既有刮削写入时计算，符合「默认不自动整库改写」。

**验证（本轮实跑）**

- 见 dev-plan §4.4 本轮证据行：`gofmt -l internal/ cmd/` 无输出，`go build ./...`/`go vet ./...` 通过，`go test ./... -count=1` 全绿，`node --test internal/server/web*_test.cjs` 全绿。
- 未做：真实浏览器/客户端走查；字幕增删的自动撤销；用户自定义分类与角标组合矩阵。未提交、未推送、未部署。

---

### 2026-11-04 端到端上游实例验收（MT-01/02/03/04）

**环境**

- 上游：`/root/github/holll/metatube-sdk-go` 源码 `go build ./cmd/server` 得到的真实 MetaTube 服务，`-port 18080 -token testtoken -dsn /tmp/mt.db -db-auto-migrate`（sqlite，进程内缓存）。
- 本服务：本仓库 `cmd/metatube` 构建产物，`port 18090`、`db_path /tmp/accept/db.sqlite`、库目录 `/tmp/accept/lib`（`SSIS-001.strm`），经 `/api/admin/scrape/settings` 指向上述上游。
- provider：`JavBus`（真实站点数据）。FANZA 在本机返回 `not-available-in-your-region`、FC2 返回 `incomplete metadata`，因此这两家未覆盖，不能据此声称 FANZA 链路已验证。
- 证据留档：`reports/metatube-e2e-2026-11-04/`（响应 JSON、NFO 快照、裁切图、sha 与复现命令；`/reports/` 已被 `.gitignore` 忽略）。

**结果（全部 PASS）**

| 批次 | 动作 | 观察 |
|---|---|---|
| MT-01 | `preview?metatube_id=JavBus:SSIS-001:0.3:true` | 响应 `query` 原样保留后缀；候选 `update=true`、`position=0.3`；上游日志出现 `lazy=false`；库目录未产生文件 |
| MT-02 | `inspect`（position 0.3） | `poster` 带 `pos=0.3`，`images` 仅 primary 带 pos；backdrop/thumb 不带 |
| MT-02 | 图片代理 `pos=0.0/0.1/0.3/0.7/0.9/1.0` | 均 358x538（2/3）；`0.0≡0.1`、`0.9≡1.0`（两端夹紧）；`0.3`/`0.7` 各自唯一字节；重复请求 0.0007–0.0009s（缓存命中），新 pos 约 0.03s（回源） |
| MT-03 | `inspect` 的 `stills` | 10 条，与上游 `preview_images` 数量一致；`index=0` 800x450、`index=1/3` 533x299（16:9）；重复 index 命中字节缓存，换 index 复用列表缓存（上游 `/v1/movies/` 未重复请求） |
| MT-02/01 | `POST /api/admin/items/1/scrape`（position 0.3 / 0.7 / 省略） | `200`，落盘 3 图 + NFO；NFO 身份 `JavBus:SSIS-001` 不含后缀；`poster.webp` 三种取值 sha 互不相同（`39bc4c85…`/`3c0ffb80…`/`3b09be85…`），均 358x538 |
| MT-04 | 加 `SSIS-001.zh.srt` 后重刮 | NFO 新增 `<tag>中文字幕</tag>`，海报字节变 `0da00698…`（贴 zimu 角标） |
| MT-04 | 删 `zh.srt`、换 `en.srt` 后重刮 | 中文字幕标签消失、海报回到 `39bc4c85…`（角标撤销）；印证块级替换语义与「人工标签会被连带替换」的缺口 |

**据此修正**

- 上一节把「删字幕不删标签」写成现状是错的：那条结论来自标签列表为空的分支（单元测试用了空 `label`）。真实刮削有 `label`，标签列表非空即整段重建，所以标签会随字幕增删变化。测试已改为带 `label` 的 `TestApplyChineseSubtitleMarkFollowsFileChanges`，文档同步更正。
- MT-02 的「真实图片验证」由离线复算升级为真实上游实例端到端 PASS；MT-03 只读链路同批验证。FANZA/FC2 未覆盖，仍属未验证边界。
- 仍未验证：真实 Emby 客户端走查、真实 NAS/签名 URL、生产库规模；写回政策与剧照持久化布局仍待冻结。

### 2026-11-04 管理页真实浏览器走查与静态资源缺陷修复

**走查方式**：Playwright 1.49 + 本机缓存 Chromium（`/root/.cache/ms-playwright/chromium-1248`），访问真实服务（18090）与真实上游（18080），影片 `SSIS-001` 已刮削为 `JavBus:SSIS-001`。脚本与截图留档在 `reports/browser-walkthrough-2026-11-04/`（`/reports/` 已忽略）。

| 走查项 | 观察 | 结果 |
|---|---|---|
| 媒体墙 → 详情抽屉 | 元数据区出现「MetaTube ID」行，值 `JavBus:SSIS-001` | PASS |
| 点「复制」 | `navigator.clipboard` 读到 `JavBus:SSIS-001` | PASS |
| 刮削弹窗 | 独立「已保存 MetaTube ID」区块，`#scrape-metatube-id` 回填 `JavBus:SSIS-001` | PASS |
| 打开弹窗的默认请求 | 首屏直接出候选，仍走无参自动精准复用（回填没有改成手动提交） | PASS |
| 剧照网格 | `.scrape-still` 10 条，滚动后 10 张全部 `naturalWidth > 0`（800x450 / 533x299） | PASS |
| 主海报预览 | 图片实际加载，358x538 | PASS |
| 页面错误 | `pageerror` / `console.error` 均为 0 | PASS |

**走查发现并修复的缺陷**：`/web/task-runner.js` 返回 404（`Content-Type: application/json`），控制台报 `global.TaskRunner is not a function`，计划任务页整体失效。根因是 `internal/server/web.go:14` 的 `//go:embed` 列表与 `internal/server/server.go` 的静态资源路由都漏了这个文件；node 侧测试读磁盘文件，覆盖不到该链路。

- 修复：两处补上 `web/task-runner.js`；新增 `internal/server/web_assets_test.go` 的 `TestWebShellAssetsAreServed`——解析 `index.html`/`login.html` 引用的每个 `/web/...` 并逐个断言 200、非空正文、正确 Content-Type。
- 验证：删掉路由后该测试报 `GET /web/task-runner.js = 404`，恢复后通过；重启服务后浏览器 `typeof window.TaskRunner === "function"`，计划任务页无报错。


### 2026-11-04 缓存 key 单射缺陷与图片失败保护（MT-02/MT-03 加固）

**发现的缺陷（已复现）**：图片代理的进程内缓存 key 用字符串拼接 `"scrapeimg:" + kind + ":" + provider + ":" + id + cropKey`，而 `provider`/`id` 直接来自查询参数、属不可信输入。`(provider="FANZA:abc", id="def")` 与 `(provider="FANZA", id="abc:def")` 拼出同一条 key，后者会直接命中前者的缓存字节，把别的影片的图当自己的返回。剧照列表缓存 `"scrapestills:" + provider + ":" + id` 同理。

- 复现证据：修复前用假上游跑两条身份请求，上游只收到 2 次 `/v1/images/...`（应 4 次），第二次身份完全没打上游。
- 修复：新增 `joinCacheKey(prefix string, parts ...string)`（`internal/server/scrape.go`），给每段加字节长度前缀，两处 key 都改用它；图片字节 key 仍追加 `:i=<index>`（纯数字，无歧义）。
- 回归测试：`internal/server/scrape_image_cache_key_test.go` 的 `TestScrapeImageCacheKeyDistinguishesIdentity` 同时覆盖图片字节与剧照列表两条缓存；`go test ./internal/server -run 'TestScrapeImageCacheKeyDistinguishesIdentity|TestScrapeImageCropParams|TestScrapeStillProxy|TestScrapeInspectStills'` 通过。

**补齐的验收缺口**：MT-02 验收要求「上游失败不覆盖已有图片」，此前只有 `TestApplyImageFailureIsNotFatal` 断言「不致命 + warn + NFO 落盘」，没有断言已有海报字节不变。新增 `internal/scraper/apply_image_preserve_test.go` 的 `TestApplyImageFailureKeepsExistingImage`：预置 `poster.webp`，上游图片接口全 500，覆盖式重刮后断言海报字节原样、3 条 warn、NFO 仍写入。核对代码路径：`downloadImage` 先下到内存、再写同目录临时文件、编码后取提交锁 rename，失败路径都不触碰目标文件。

**文档更正**：此前 dev-plan 决策 6 与本节 MT-03 决策 2 声称剧照解析有「4 路并发上限」，代码里并不存在这个闸门；已改为如实描述（30s 总超时、响应头上限、8MB 下载上限、出站默认 16 条同主机连接上限；超出的请求排队）。是否加独立闸门需先定阈值，未做。

**补充实测（同日）**：只补缺失模式（`Overwrite=false`，条目状态 success、NFO 已有 `<title>/<genre>/<tag>/<plot>`）下，外挂中文字幕既不会新增 `<tag>中文字幕</tag>`（补缺失只填空字段，既有标签原样保留），也不会覆盖人工标题、自定义分类与简介；但主海报仍按目录判定贴 `zimu.png` 角标。该行为由 `internal/scraper/mark_subtitle_fill_test.go` 的 `TestApplyFillOnlyKeepsCustomCategoryAndManualTags` 固定，避免以后把「补缺失」改成「合并标签」时无声漂移。

**验证**：`gofmt -l internal/ cmd/` 无输出；`go build ./...`、`go vet ./...` 通过。

## 21. 原生部署与管理能力方案合并（2026-11-03）

### 文档整合

已将[原生部署与管理能力演进技术方案](<docs/native-roadmap-design.md>)纳入[总施工计划 §4.2–4.3](<dev-plan.md#42-原生部署与管理能力演进合并于-2026-11-03>)，与[MetaTube 对齐方案](<docs/metatube-alignment-design.md>)统一排期；两份详细方案保留，交叉引用已补齐，不复制成第三份详细设计。本文记录计划状态，不宣称静态分析中的风险已经运行复现。

| ID | 批次 | 当前状态 | 放行条件摘要 |
|---|---|---|---|
| N-00 | 设计冻结 | ○ 待冻结 | 按批次确认阈值、出站机制、任务语义、秘密政策和迁移回退；不静默决定 |
| N-01 | M1 可靠性与原生交付 | ○ 待施工 | 播放绑定、文件读取、探测边界与原生升级回退，负向用例及演练通过 |
| N-02 | M2 扫描任务中心 MVP | ○ 待施工 | M1 放行、持久登记、旧契约兼容、崩溃/幂等/权限验收；无自动续跑 |
| N-03 | M3 诊断与恢复 | ○ 待施工 | 只读不联网，执行复核范围、路径、版本和锁；重探依赖 M1 |
| N-04 | M4 Webhook 投递历史 | ○ 待施工 | 有界、白名单净化、无秘密落盘、无隐式重发 |
| N-05 | M5 备份管理 | ○ 待施工 | 秘密政策先决策，固定范围、预算、权限及恢复演练通过 |

### 合并后的施工约束

- N-01 为首个正式发布公共门禁；只读任务界面可以并行，不据此标记 M1 完成。MT 项保持 §20 状态与原依赖，不等待整个原生线结束，但共同发布时不能绕过 M1。
- M1 后管理线按 M2 → M3 → M4 → M5 分批交付；探测和批量刮削接入任务中心单独验收。N-00 分批冻结，不让备份未决政策阻塞无关可靠性补丁。
- 旧同步扫描 Sunset 2027-04-01、既有状态和权限契约保持；新统一 DTO 不覆盖旧枚举。任务摘要持久化不代表执行恢复；备份含秘密政策尚未解决，不修改历史 D11 批准记录。
- 原生二进制与 Docker 支持退出是待实施目标，本轮没有清理部署入口、改变服务或宣布平台支持已验收。MetaTube 暂缓项、翻译维持现状继续有效。

### 本轮验证边界

本次只整合计划及交叉引用；检查文档链接、差异与 `git diff --check`。未运行功能测试、未修改业务代码、未提交、未推送或部署；此前未提交改动与历史记录保留。各功能批次仍须按各自方案执行同 SHA 验证并登记证据。

## 22. 技术方案评审意见修订（2026-11-04）

### 结论与修订范围

依据技术方案评审团的静态审阅，修订[原生方案](<docs/native-roadmap-design.md>)、[MetaTube 方案](<docs/metatube-alignment-design.md>)及[总计划](<dev-plan.md>)。三角色返回报告只表示职责响应覆盖，不表示运行验证；结论仍为“可有条件归档，尚未首发放行”。历史批准记录、暂缓能力与翻译现状不改。

| 评审关注 | 本轮方案落点 | 实施/证据状态与阻断范围 |
|---|---|---|
| 播放绑定及原生升级回退 | 原生 §3.1、§3.4：同事务拒绝/零写入；支持前版/平台/schema 与 DB/NFO 配套恢复、损失窗口 | ○ 待施工/待验收；N-01 首发门禁 |
| 文件字节与耗时承诺混淆 | 原生 §3.2：全入口 max+1、保留旧状态与抑制误清理；挂载支持范围或针对性隔离待批准，不声称 context 可打断内核 I/O | ○ 待冻结/施工/验收；N-01 首发门禁，既有关闭门禁不放宽 |
| ffprobe 可信链接与资源边界 | 原生 §3.3：2026-11-05用户决定默认信任STRM，取消实际出站隔离；保留输出/并发/时间/子进程退出与脱敏 | ◐ 资源边界见§39；隔离需求已撤销而非实现完成，见§50；其余N-01门禁不变 |
| 任务观察与幂等留存 | 原生 §4：全部入口单一观察、旧表投影、稳定身份、崩溃后不重跑；展示淘汰不删除窗口内映射 | ○ 待冻结/施工/验收；仅 N-02，扫描 MVP 仍不可取消 |
| 诊断、Webhook、备份 | 原生 §5–7：范围/版本复核、白名单有限观察、全部认证材料的秘密政策二选一 | ○ 待各批冻结/施工/验收；分别 N-03/04/05，不全阻塞 M1；历史 D11 不改 |
| 后缀选项与图片链路 | MetaTube MT-01/02：typed 身份/选项、预览/详情/确认/批量/图片贯穿、写回政策、服务端/浏览器/落地缓存 | ○ 待冻结/施工/验收；MT-01→02→03 依赖不变 |
| 候选来源、标记归属及演员真源 | MetaTube MT-03/04/05/06：服务端候选/写路径、多背景图重建、程序标记来源、稳定演员身份与人工确认 | ○ 待对应批冻结/施工/验收；不扩大匿名图片或管理员授权契约 |

### 决策与证据入口

- 分批负责人/批准人、决策日期/结果和阻断范围分别登记于[原生 §9.1](<docs/native-roadmap-design.md#91-分批决策登记2026-11-04-评审修订>)与[MetaTube §6](<docs/metatube-alignment-design.md#6-分批冻结与决策记录>)。当前仅责任角色，具名待指派；本轮不替负责人确定挂载范围、阈值、隔离机制、存储或备份政策。
- 后续逐用例按[证据模板](<docs/native-roadmap-design.md#81-发布证据登记模板>)登记批次、平台/前版/schema、fixture/动作/预期、源码与产物标识、命令/环境/时间、原始证据及 PASS/FAIL/SKIP/待执行，并列未覆盖项。只对实际完成矩阵验收的平台/版本作支持承诺。
- 当前全部功能仍待实施/验收，没有新增 PASS 运行证据。局部 mock、历史测试或评审报告不替代真实嵌入页面/API、受保护监听、图片及升级回退证据。

### 本轮验证边界

本轮仅修改以上四份 Markdown，校验差异、引用和文档一致性；未修改业务代码、未运行 build/vet/功能测试、未提交、未推送或部署。既有未提交代码与历史文档改动保留；N-00..05、MT-01..06 不因方案修订标记实现完成。

### MetaTube 已保存标记管理展示补充（2026-11-04）

- 已将用户要求加入[MetaTube 方案 MT-01 补充](<docs/metatube-alignment-design.md#mt-01-补充管理页展示已保存标记与搜索回填>)并同步[总计划](<dev-plan.md>)：媒体墙影片管理详情只读显示/复制已保存 MetaTube ID，刮削弹窗显示已存值并回填有效搜索条件；已保存状态与搜索草稿分离。
- 状态：○ 待施工/待验收。沿用索引身份、现有权限及解析兼容，不新建身份真源或直接编辑保存入口；不因打开/搜索写 DB/NFO。默认自动精准复用不变；外部 NFO 改动需重读/重扫，确认结果按部分成功语义刷新。
- 展示补充可独立交付，不作为 MT-02 的后缀依赖，也不代表 MT-01 后缀语义完成。本轮仅更新技术方案、总计划与台账，未修改功能或执行部署。

## 23. 待决事项决策请求单（2026-11-04，第 7 轮）

§20 的 MT-01（写回）、MT-03（写入）、MT-04（标记归属）与 §21 的 N-01（首发门禁）都卡在同一类前置条件上：方案要求先冻结，未经确认不擅自定数值或政策。为让这些批次能真正开工，下面把每项拆成「可选项 + 推荐 + 批准后的施工范围 + 代价」，批准后按 §20/§21 状态推进并回写证据。

### D-1 MT-03 多背景图落盘布局

**2026-11-04 本轮交互确认：用户选择 A，多背景图布局获准施工；实现与验收尚未完成，见 §29。** 以下现状和选项保留为决策依据。

现状事实（已核对）：图片落位只有 `poster.webp`/`fanart.webp`/`landscape.webp`（`internal/scraper/apply.go:28-35`）；DTO 的 `BackdropImageTags` 恒是 0 或 1 个元素（`internal/server/library.go:1087-1103`）；`GET /Items/:id/Images/:kind/:index` 路由存在（`internal/server/server.go:829`）但 `a.image` 完全不读 `:index`（`internal/server/media.go:51-79`）——即索引当前只被忽略，不会报错。

| 选项 | 内容 | 代价 |
|---|---|---|
| A（推荐） | 片目录 `extrafanart/` 存 `fanart1.jpg…`，DB 新增 `movies.extra_backdrops TEXT`（JSON 顺序），DTO 按序输出 `BackdropImageTags`，`Images/Backdrop/:index` 按索引取文件，缺文件跳过该索引；`Primary` 仍取 `poster.webp` | 一次加列迁移 + 图片端点分派 + 清理/备份纳入新目录 |
| B | 不改布局，只允许「选一张剧照覆盖 `fanart.webp`」 | 最小改动，但客户端仍只有 1 张背景图 |
| C | 剧照字节存 DB blob | 备份/迁移体积膨胀，不推荐 |

### D-2 MT-04 程序标记归属（决定能否只撤销自己写的标签）

现状事实（本请求单提出时）：`internal/nfo/update.go:76` 的 `listTags` 在标签列表非空时整段重建 `<tag>`，所以字幕增删会连带替换同一段人工标签；DB 已有增量迁移机制（`internal/store/migrations.go:95` 的 `ALTER TABLE ... ADD COLUMN`），加列属常规操作。

后续决策：用户已明确选择 A，并确认历史无归属标签不追认、同名冲突与补缺失/锁行为。执行与中断恢复进度见 §37；不再把 D-2 当作未批准阻断。

| 选项 | 内容 | 代价 |
|---|---|---|
| A（推荐） | 新增 `movies.auto_marks TEXT`（JSON 数组，记录程序上次写入的 tag），重刮时只移除该集合内的 tag 再按当前判定重写；人工标签不动 | 一次加列迁移 + 写入/撤销两条路径的测试 |
| B | 片目录旁挂 `.marks.json` | 不动 DB，但多一份需备份/清理的状态 |
| C | 维持现状（块级替换） | 零改动，但会误伤人工标签（已实测） |

### D-3 MT-01 Position 是否写回官方标记

**2026-11-04 本轮交互确认：用户选择 A，正式确定不写回 Position，身份只保存 `Provider:escaped-ID`；见 §29。**

| 选项 | 内容 | 代价 |
|---|---|---|
| A（推荐） | 不写回（现状）。身份串只含 `Provider:escaped-ID` | 零改动；重扫/迁移稳定 |
| B | 写回 `Provider:ID:Position` | 身份串携带渲染参数，重扫、备份导入导出、外部 NFO 编辑都要跟着兼容，且无法区分人工设置与自动写入 |

### D-4 图片代理的资源边界（本轮新发现）

**2026-11-04 持续施工第 12 轮用户明确选择 A：保留 4 路和单张 8MB，饱和立即 503，不排队。批准人：当前用户；执行人：Lead。实施及验证状态见 §36。** 以下为批准前事实与选项，保留作历史依据。

现状事实（持续施工第 3 轮复核纠正）：`GET /api/admin/scrape/image` 沿用匿名图片契约，单张下载上限 8MB，成功图整张读入内存。**当前源码已有容量 4 的 `scrapeSem` 下载通道**（`internal/server/server.go:397` 初始化，`internal/server/scrape.go:713-720` 等待/取消分支，`internal/server/scrape.go:840` 容量）；饱和请求等待，取消返回 502，无独立频率限制。此前「无闸门、N×8MB 在途」结论不符合当前源码，撤销。4 路只限制通道内活动下载，不代表总内存（含缓存、分配开销等）或等待请求数有界。

| 选项 | 内容 | 代价 |
|---|---|---|
| A（推荐，待批准） | 保留 4 路上限，饱和改为直接 `503` 不排队；单张仍 8MB | 改变既有等待行为和响应契约，需补饱和拒绝回归；不能宣称总内存 ≤32MB |
| B | 保持 4 路通道、饱和等待至取消的现状 | 零策略改动；等待请求数仍无单独上限 |
| C | 单张上限降到 2MB，通道政策另选 A/B | 高清主海报可能被判超限，需说明兼容影响 |

### D-5 N-01 四项首发门禁的阈值与机制

**2026-11-05：依用户睡前授权由 Lead 按推荐继续自主决策，文件读取子项采用 NFO 8MiB / STRM 1MiB，超限 WARN + 跳过并保留旧索引、禁用本库缺失清理，施工证据见 §38；ffprobe 采用 60 秒预算（含名额等待）、stdout 4MiB / stderr 64KiB、共享 2 个进程名额与 Unix 组终止，施工与待收集验收见 §39。升级/重启/回退矩阵仍未完成。** 下表保留最初建议与待定范围，不把授权或阈值登记等同于验证通过。

| 门禁 | 建议值 / 机制 | 题面 |
|---|---|---|
| STRM/NFO 有界读取 | NFO ≤ 8MB、STRM ≤ 1MB；超限跳过该文件并记 warn，不中断整库扫描 | 数值是否批准；超限算「跳过」还是「失败」 |
| ffprobe 边界 | 单次 60s、stdout ≤ 4MB、stderr ≤ 64KB、并发 ≤ 2，超时杀进程组 | 数值是否批准；并发 2 是否合适 |
| 播放会话绑定 | 已批准方案是同一 `(user_id,session_key)` 不得改绑另一 `movie_id`，在同一事务内冲突拒绝且零写入；影片存在性与库授权沿用 HTTP 入口既有检查 | 本项无需新增阈值决策，已按原生 §3.1 开始施工；此前把此项写成「item_id 属于当前库」不准确，以本行和 §24 为准 |
| 升级/重启/回退矩阵 | 保留上一版二进制；DB 迁移保持向前一版兼容；回退以 DB 备份 + NFO 快照为准 | 支持矩阵范围（仅 Linux/amd64？Docker？） |

### 待你回复的最小集

按推荐全部采用即回「按推荐执行」；只改个别项就指出编号与选项（例如「D-1 选 B，D-4 选 A」）。批准后我按 D-4 → D-2 → D-1 的顺序施工（先补资源边界，再做标记归属，最后多背景图），D-5 单独作为 N-01 批次。

另有一个界面小事：剧照网格当前每张图下方显示 `#0/#1/#2` 序号（对应受限下标）。若无产品价值可去掉，一行改动。

## 24. 持续施工：播放绑定与已有施工复核（2026-11-04）

**状态：N-01 播放绑定子项已实现并通过回归；整批 N-01 未完成，不代表首发验收通过。** 保留现有未提交施工，无部署、重启或提交。

### 已完成

- `internal/store/playback_sessions.go` 在既有写事务内，对同一 `(user_id,session_key)` 检查已绑定 `movie_id`；不一致返回稳定领域错误，早于会话、事件及用户播放状态的任何写入。不引入锁、重试层或改绑机制；不同用户仍可独立使用相同会话键。
- `internal/server/media.go` 三类播放事件统一映射 `409 / PLAYBACK_SESSION_MOVIE_CONFLICT`，失败不触发版本失效或 Webhook；更新 API 错误契约。
- 新增 store/API 回归：活动与已停止会话的 start/progress/stopped 冲突、会话/事件/userdata 全字段零变化、原影片仍可上报、跨用户隔离、并发首次上报仅一部绑定成功。
- 复核 MT 既有预览时修复 `internal/server/scrape_inspect.go` URL 拼接：provider/id/kind 用 `url.QueryEscape`，避免不透明 ID 中的 `+`、`&`、`#` 被解释为 URL 结构。新增主海报/剧照 URL 往返测试，不增加产品决策。

### 本轮实跑证据及边界

本地未提交工作树，Go 1.27.2；以下是当前源码回归，不替代支持平台、旧版本升级回退或真实上游部署验收。

| 命令 | 结果 | 备注 |
|---|---|---|
| `go test ./internal/store ./internal/server -run 'Test(Playback|RecordPlayback)' -count=1` | PASS | store 4.330s / server 0.758s；首轮跨用户夹具未建 owner，错误复用用户 ID，修正夹具后通过 |
| `go build ./... && go vet ./... && go test ./... -count=1` | PASS | 所有包通过，server 171.595s / store 109.352s；该全量运行开始于末次 URL 修复前 |
| `go test -race ./internal/store ./internal/server -run 'Test(Playback|RecordPlayback)' -count=1` | PASS | store 121.434s / server 25.508s |
| `node --test internal/server/web*_test.cjs` | PASS | 265 pass / 0 fail / 0 skip，52.354s；原始输出 `/tmp/emby-go-goal-web-tests.log` |
| `go test ./internal/server -run 'Test(ScrapePreviewURLPreservesOpaqueIdentity|ScrapeInspect|ScrapeStill|ScrapeImage|Playback)' -count=1` | PASS | 4.066s；覆盖末次 URL 修复 |
| `go build ./... && go vet ./... && git diff --check` | PASS | 末次代码修复后复跑 |

以上为本机命令结果；尚无本批平台/旧版本矩阵、真实客户端播放上报或生产发布证据，不标为整批 PASS。

### 复核发现，下一轮处理

- **单条刮削部分成功回报遗漏（待修）**：`internal/scraper/apply.go` 重扫失败已保存 `ApplyResult.RefreshError` 且不回滚 NFO，但 `internal/server/scrape.go` 单条确认响应丢弃此字段，仍只返回 success。需明确「文件已写、索引待恢复」并给出重新扫描提示，不伪装成全部失败、也不自动重试写入。
- **图片警告管理端不可见（待修）**：确认接口已返回 `warnings`，`internal/server/web/app.js` 却只提示标题/图片数并关闭弹窗。需连同索引恢复信息显示部分成功；后端允许图片尽力而为的契约不变。
- **剧照列表与缓存对应关系（待核实/设计）**：inspect 的新列表与图片代理重新解析及旧字节缓存可能不同序，当前没有写图选择，不能把列表下标当永久身份；MT-03 仍只读部分交付，不宣称多图落盘完成。
- MT-04 标签归属与撤销、匿名图片并发闸门、N-01 文件读取/ffprobe/回退支持矩阵等原有未决项仍待批准/施工，不由本轮静默决定。独立只读复核子任务收到阶段性报告后结束失败，无完整关闭报告；以上记录只采用主会话已核对源码或亲自实跑的证据，不声称独立完整验收。

## 25. 单条刮削部分成功可见性（2026-11-04，持续施工第 3 轮）

**状态：API、管理端和本地回归已完成；不等于发布或真实浏览器验收。** 本轮修 §24 已核对的回报遗漏，并复核已有图片通道的实际边界，不改变图片尽力而为、NFO 已提交不回滚的既有契约，不新增重试/队列或未批准的产品政策。

### 接口与恢复边界

- 单条确认保留 `200 / status=success` 与既有字段。`ApplyResult.RefreshError` 非空时增加可选 `refresh_error`，固定脱敏提示「NFO 已保存，索引刷新失败；请重新扫描该影片或媒体库。」；正常结果省略。底层路径和 SQL 只沿用既有日志，不新增到 API 响应。
- 图片 `warnings` 和索引 `refresh_error` 独立，不把已经落盘的 NFO 描述成未写入；恢复索引只需重新扫描，不自动重试确认或重复下载。
- 新增 `internal/server/scrape_confirm_partial_test.go`：真实本地 HTTP 服务贯穿 handler→MetaTube 客户端→NFO 写盘→SQLite 索引；仅远端响应和 SQL 故障为夹具。四种模式（正常、图片失败、索引失败、同时失败）验证 HTTP 形状、独立提示、脱敏、实际 NFO 内容/身份、失败索引版本不变、写入闸门释放及请求计数；去掉故障后单文件重扫恢复，NFO 字节不变且不重复上游请求。
- 提交前的上游查询失败仍为 502，无 `refresh_error`、无成功形状、无 NFO/索引写入；新增真实路由回归覆盖，避免把所有失败混成部分成功。
- 管理端 `internal/server/web/app.js` 的确认结果分支替换整个搜索/确认区，持久 `role=status` 面板分别列出 NFO 已保存、索引需重扫和图片警告；只保留“完成”关闭，焦点移至该按钮，刷新媒体墙不关面板。成功、失败与部分成功的迟到响应均服从页面/影片状态检查；正常成功仍 toast+关闭。动态标题、数量、错误和每条警告均转义。
- 新增 `internal/server/web_scrape_partial_success_test.cjs` 15 项运行时回归：正常/三种部分成功、Done 不重复 POST、旧按钮不可重提、页面跳转/关闭/换片的三类迟到结果、转义与当前详情会话隔离。子任务未正常返回收尾报告，Lead 已直接检查源码/夹具并独立重跑全部前端测试，不把其进度消息当作最终验收。
- 复核已有图片通道：`TestScrapeImageSaturatedGateCancellation` 用真实匿名路由验证容量 4、饱和取消返回 502、不向上游请求且不占用名额。更正 §23 D-4 与方案中的“无闸门”错误事实；未新增通道或改变等待/拒绝策略，不据此宣称全部内存、等待请求数有界。
- 更新 API 契约 §4.5，禁止客户端自动重复确认，不新增端点或破坏已有成功字段。

### 本轮已实跑

基线：`52836dd` + 现有未提交工作树，Go 1.27.2 / linux-amd64。本地源码结果不替代真实远端、浏览器人工走查或生产验收。

| 命令 | 结果 | 范围/原始输出 |
|---|---|---|
| `go test ./internal/server -run '^TestScrapeConfirmReportsPartialSuccess$' -count=1` | PASS，0.943s | 四种真实文件/索引模式 |
| `go build ./... && go vet ./... && go test ./... -count=1` | PASS | server 92.711s / store 58.836s；`/tmp/emby-go-round3-go-tests.log`；前端施工完成前运行 |
| `go test -race ./internal/server -run 'Test(ScrapeConfirmReportsPartialSuccess\|ScrapeIndexRefreshFailureIsNotSuccess\|ScrapeStaysRunningUntilIndexRefreshReturns)$' -count=1` | PASS，26.629s | 新单条与既有批量刷新路径 |
| `go test ./internal/server -run '^TestScrapeConfirm(ReportsPartialSuccess\|LookupFailureDoesNotCommit)$' -count=1` | PASS，1.131s | 提交前/后错误语义，含全部 4 种已提交结果 |
| `go test ./internal/server -run '^TestScrapeImageSaturatedGateCancellation$' -count=1` | PASS，0.251s | 已有通道饱和取消，不改变策略 |
| `node --check internal/server/web/app.js && node --test internal/server/web*_test.cjs` | PASS，280/280，0 skip，40.268s | Lead 独立重跑，`/tmp/emby-go-round3-web-tests.log`；非真实浏览器走查 |
| `go build ./... && go vet ./... && go test ./... -count=1`（最终整合） | PASS | 包含最终前端嵌入内容及本轮全部 Go 测试；server 91.200s / store 55.810s；`/tmp/emby-go-round3-final-go-tests.log` |

后续仍开放（本节收尾时的历史状态，版本隔离现状见 §26）：inspect 使用新剧照列表，代理按 `(provider,id)` 缓存列表 10 分钟、按 `(kind,provider,id,index)` 缓存图片字节，未绑定列表版本；上游重排后新预览可能取到旧下标内容。此处为源码复核发现，不冒充本轮已修复或真实远端复现；下一轮优先验证/修正只读候选对应关系，写入仍受 MT-03 布局决策阻断。本轮不部署、重启或提交。

## 26. 剧照列表版本隔离复核与空裁切参数修复（2026-11-04）

**状态：工作区既有版本隔离实现已复核，参数边界遗漏已修复，针对性、race 与最终全量验证通过；不是整批 MT-03 完成或发布验收。** 执行人 Lead；本轮按当前源码核实 §25 遗留项，不把工作区已有代码冒充本轮新建，不擅自选择多图持久化布局。

### 已有实现复核

- [inspect](<internal/server/scrape_inspect.go>) 对去空白、去重、最多 24 条的有序列表做 JSON 编码及 SHA-256 摘要，`stills[].preview` 携带 `version`；列表快照按 `(provider,id,version)` 保存，图片字节 key 同时含 `kind/index/version`，缓存身份仍用长度前缀区分。
- [图片代理](<internal/server/scrape.go>) 只在 `kind=still` 接受 64 位小写十六进制 `version`。快照丢失后重新查询，只有当前列表摘要相同才允许重建；不相同返回 404，查询失败返回 502，不能把新列表的同一下标偷换成旧图。列表丢失但同版本字节仍在时可安全命中字节缓存；失败响应不设置成功图片缓存头。
- 无版本旧链接保留原行为，与版本快照缓存隔离；不为其承诺列表重排后的稳定身份。保留 300 秒浏览器缓存、10 分钟列表缓存及既有下载通道，不新增锁、重试、限流或写图路径。
- 摘要描述的是有序 URL 列表，不是远端图片内容哈希；同 URL 的字节变化不在此保证内。此版本不是永久资源 ID，多图落盘仍受 MT-03 布局决策阻断。接口现状已在 [API 契约 §4.5](<API-CONTRACT.md#45-metatube-手动识别刮削预览>) 描述。

### 本轮实际修改与测试

- 独立只读复核未发现版本隔离仍会串图的阻断项，但指出 `optionalFloatQuery` 将显式空 `ratio`/`pos` 当作缺省。Lead 用新增回归先复现：普通图片返回 200、剧照触发上游后返回 502，而契约要求边界 400；这是不可信 HTTP 输入的实际错误，不是推测性加固。
- [参数解析](<internal/server/scrape.go>) 现用 query key 是否存在区分缺省与显式传入；存在则继续既有浮点/有限性/范围校验。空串、纯空白及裸参数不再静默兜底。不新增内部 nil 检查或辅助抽象。
- [裁切回归](<internal/server/scrape_image_crop_test.go>) 新增 7 项：primary 空 ratio、空白 pos、裸 ratio，thumb 空 pos，backdrop 空白 ratio，still 空 ratio/空白 pos；均断言 400，既有上游请求计数断言守住错误输入不出站。
- [版本隔离回归](<internal/server/scrape_still_version_test.go>) 补充两次明确 `update=true` 实际传为 `lazy=false` 的计数；字节独立淘汰仍按各自快照下载、列表独立淘汰命中同版本字节、双淘汰旧版本 404/当前版本重建，以及上游查询失败 502 且不下载。保留新旧列表替换/重排、无版本兼容、并发热命中及版本格式测试。本节当时未专门覆盖并发冷重建和图片下载失败组合，不把已有热命中测试说成覆盖这些组合；后续补齐证据见 §27。
- 修正列表缓存容量注释：每个版本快照分别计数，并非每个影片只占一个条目。

### 本机命令证据

基线 `52836dd` + 未提交工作树；Go 1.27.2 / linux-amd64；本节记录本机源码回归，不替代真实远端重排、人工浏览器或多平台发布矩阵。

| 命令 | 结果 | 边界 |
|---|---|---|
| `go build ./... && go vet ./... && go test ./... -count=1`（施工前基线） | PASS | server 93.891s / store 58.249s；开始于本节最终修改前 |
| `node --check internal/server/web/app.js && node --test internal/server/web*_test.cjs` | PASS | 280 pass / 0 fail / 0 skip，37.703s；本节不修改前端 |
| `go test ./internal/server -run '^TestScrapeImageCropParams$' -count=1`（修复前新增用例） | FAIL（预期复现） | 7 个新增非法输入未按约定返回 400；修复后由下面回归覆盖 |
| `go test ./internal/server -run 'Test(ScrapeStill\|EncodeStillList\|ScrapeInspect\|ScrapeImage)' -count=1` | PASS，1.583s | 本节最终源码及测试 |
| `go test -race ./internal/server -run 'Test(ScrapeStill\|EncodeStillList\|ScrapeImageCropParams)' -count=1` | PASS，10.054s | 本节最终源码及测试；不等于全部并发场景 |
| `go build ./... && go vet ./... && go test ./... -count=1 && git diff --check`（最终复跑） | PASS，退出码 0 | 所有包通过；server 90.069s / store 54.868s；本节最终代码修改后运行 |

仍开放：MT-01 Position 写回、MT-03 多图布局、MT-04 标记归属/撤销、MT-05/06 身份与演员资料、N-01 文件读取/ffprobe/升级回退等，按 §23 的决策请求与方案顺序推进。本节未替用户批准待决政策，不部署、重启或提交。

## 27. 剧照冷缓存并发与下载失败恢复回归（2026-11-04）

**状态：测试补齐，重复针对性/race 及最终全量检查均通过。** 执行人 Lead；延续 §26 遗留的两项测试缺口，仅修改[剧照版本回归](<internal/server/scrape_still_version_test.go>)及进度文档，没有修改生产缓存、锁、下载通道、自动重试或持久化政策。

### 本轮施工与复核

- 扩充 `TestScrapeStillVersionIsolation`：上游列表查询失败恢复后，允许同版本列表重建但令图片下载返回 502；HTTP 失败响应无成功缓存头，累积查询/下载次数为 7/9，说明一次请求未自动重试下载。调用方下一次显式请求成功，复用列表快照，随后再次请求命中字节缓存，总次数为 7/10；失败图片不会缓存，也不妨碍后续恢复。
- 新增 `TestScrapeStillVersionConcurrentColdCache`：全新应用中列表/字节缓存均为空，用起跑 channel 同时放行 16 个 HTTP 请求（旧版本 8 个、当前版本 8 个）。旧列表 `[a,b]` 已被上游 `[b,c]` 替换：旧版本全部 404 且无成功缓存头，当前版本全部返回 `b` 的字节及浏览器缓存头；完成后再次请求当前版本，不增加查询/下载次数。
- 冷启动过程中允许其他当前版本请求填入缓存；测试不承诺每个请求都 miss、不要求 singleflight、不固定当前版本查询或下载次数。`lookups >= 9` 来自 8 个不缓存的旧版本 mismatch 加至少 1 次当前列表重建，不是新增性能或合并请求契约。HTTP fixture 的字节仅标识上游 URL，不宣称真实图片解码、真实远端重排或人工浏览器验收。
- 独立只读复核未发现阻断问题：计数与生产代理中「先缓存列表、后下载图片、仅缓存成功字节」的顺序一致；共享计数及失败开关在 mutex 下访问，起跑 channel 在全部 goroutine 注册后关闭，WaitGroup 等待所有响应再检查计数；未新增生产同步原语。

### 本机命令证据

沿用基线 `52836dd` + 未提交工作树，Go 1.27.2 / linux-amd64；本批不修改前端，前端 280/280 的上一批证据保留在 §26，不记作本批重新运行。

| 命令 | 结果 | 边界 |
|---|---|---|
| `gofmt -w internal/server/scrape_still_version_test.go` | PASS | 本批唯一修改的 Go 文件 |
| `go test ./internal/server -run '^TestScrapeStillVersion' -count=10` | PASS，4.620s | 两个版本回归重复运行 |
| `go test -race ./internal/server -run '^TestScrapeStillVersion' -count=3` | PASS，19.256s | Lead 本机重复 race |
| `go test -race ./internal/server -run '^TestScrapeStillVersion(Isolation\|ConcurrentColdCache)$' -count=10 -timeout=120s` | PASS，60.986s | 独立只读复核报告；不等于全部并发组合 |
| `go build ./... && go vet ./... && go test ./... -count=1 && git diff --check` | PASS，退出码 0 | 本批最终测试源码；server 90.641s / store 55.089s |

MT-03 仍仅交付只读候选；未批准多图布局、未实现多图写入，不据本节宣布整批完成。其余待决项沿用 §23/§26。本轮不部署、重启或提交。

## 28. 显式零值选项联合回归及字幕链路复核（2026-11-04）

**状态：新增 HTTP 联合回归、重复 race、全量 Go/前端回归与差异检查均通过。** 执行人 Lead；本轮仅增加[选项往返回归](<internal/server/scrape_options_roundtrip_test.go>)并更新进度文档，未发现需修复的生产逻辑错误，不更改身份写回、标记归属或图片布局政策。

### 已完成与证据边界

- 新增 `TestScrapeZeroOptionsHTTPRoundTrip`：从手动精准标记 `FANZA:opaque%2Bid:0:false` 开始，经真实本机 HTTP preview→inspect→匿名图片代理→confirm；由 preview 返回的候选构造 inspect/confirm JSON，断言不透明 ID 中的 `+`、显式 `Position=0` 与 `Update=false` 未丢失。三次详情请求均为 `lazy=true`，两次 primary 图片请求都有 `pos=0`，确认时 thumb/backdrop 不带 `pos`。
- 确认前核对影片 DB 行全字段、NFO 字节及片目录项列表均不变；确认后断言三张图片保存、无图片警告/索引刷新错误，索引标题与规范身份正确。三张落盘 WebP 的解码配置均为 fixture 的 4×4。此为 JPEG fixture→代理→本地 WebP 编码/落盘的集成证据，不是实际上游裁切、浏览器交互或新增远端验收；前端选项传播仍由原有前端测试守护。
- 首次测试因错误预期标题为 `confirmed title` 而失败；现有 `buildFields` 经 `joinNumberTitle` 写成 `NEW-002 confirmed title` 是既定行为。核对源码后只纠正新测试预期，未借此改产品逻辑。
- MT-04 独立只读复核未发现批准范围内的阻断错误：显式 `-C/-U/-UC` 优先，同目录同主名且中文语言段的外置字幕才推断 C，裸字幕/英文字幕不推断；现有测试覆盖中文标签、仅主海报角标、删除字幕及只补缺失保留人工分类/标签。仍不承诺块级替换只撤销程序标签，不实现待决 `auto_marks`。

### 本机验证

基线 `52836dd` + 原有未提交工作树；Go 1.27.2 / linux-amd64。无提交、部署或重启。

| 命令 | 结果 | 范围 |
|---|---|---|
| `go test ./internal/scraper -run 'Test(DetectVersionMark\|VersionMarkForExternalSubtitle\|HasChineseSubtitleMissingDir\|ApplyChineseSubtitleMark\|ApplyFillOnlyKeepsCustomCategoryAndManualTags)' -count=1` | PASS，0.263s | 独立只读复核运行现有用例 |
| `go test ./internal/server -run '^TestScrapeZeroOptionsHTTPRoundTrip$' -count=10` | PASS，2.344s | 新联合回归重复 10 次 |
| `go test -race ./internal/server -run '^TestScrape(ZeroOptionsHTTPRoundTrip\|LookupOptionsCarryUpdate)$' -count=3` | PASS，21.424s | 新用例及已有 Update 请求回归 |
| `go build ./... && go vet ./... && go test ./... -count=1` | PASS | 所有包通过；server 89.778s / store 54.365s |
| `node --test internal/server/web*_test.cjs` 及 `git diff --check` | PASS，整条命令退出码 0 | 前端 280/280、0 skipped，38.642s；本批确实重新执行 |

MT-01/02/04 仍维持看板中的部分完成状态；继续开发待决写入能力前须先确认 §23 对应政策，不把重复回归记作新增功能或整批放行。

## 29. 用户确认多背景图布局与 Position 写回政策（2026-11-04）

本轮交互选择已返回，执行人 Lead、批准人为本会话用户；不外推为其它未决政策获批。

| 决策 | 用户选择 / 冻结结果 | 实际进度 |
|---|---|---|
| D-1 / MT-03 | A：片目录 `extrafanart/` 下 `fanart1.jpg…`，`movies.extra_backdrops TEXT` 保存有序 JSON 索引，客户端按序列举/访问背景图；迁移、重扫重建与备份一并适配 | 已授权，待补具体 API 契约并开始迁移/读写施工；不是已实现 |
| D-3 / MT-01 | A：Position 不写回 NFO，身份只保存 `Provider:escaped-ID`；位置仅供预览与主海报裁切，Update 仍为请求选项 | 现有实现与 §28 回归吻合；该政策阻断已解除，不需再请求确认 |

下一施工顺序：核对 store/scanner/图片端点与备份实际调用链 → 先补多图确认输入、顺序/缺图、失败及重扫契约 → 迁移与索引 → 文件写入/重建 → 客户端枚举、取图与选择界面 → 全量/真实图片/浏览器验证。保留原 `poster.webp` / `fanart.webp` / `landscape.webp` 的兼容行为；剧照来源仍由服务端按列表版本和受限下标复核，客户端不提交任意远端 URL。每个阶段分别记实际进度，不能把允许写图当成现有只读网格已支持写入。

D-2 字幕标记归属、D-4 饱和处理政策、D-5 文件/探测预算与发布矩阵仍未确认，保持原阻断范围，不因这两项选择擅自施工。历史 §23 的全套建议顺序不再要求先做未获批 D-4/D-2 才能推进已获批的 D-1。

## 30. 多背景图索引底座与单片保留修复（2026-11-04）

**范围：执行已获批 D-1/A 的第一阶段；生产代码、测试及阶段契约已落地，针对性、race 与最终全量回归通过，不代表 MT-03 完成。**

### 实际实现

- 追加迁移 17：`movies.extra_backdrops TEXT NOT NULL DEFAULT '[]'`；校验和、执行、账本和参考 schema 均接入，不改旧迁移语句或校验和。正常 upsert、统一影片读取/搜索基准扫描、JSON 诊断以及手工/坏 NFO 的派生结构刷新均接入该列；空集合写 `[]`，顺序保持不排序重写。
- 新增磁盘盘点职责：仅 `extrafanart/` 直接普通文件 `fanartN.jpg`，正整数无前导零，按数字序 1/2/10；排除目录和文件符号链接及不符合命名的文件，输出绝对路径。原三张图的查找优先级不变。
- 整库/增量与独立 RescanOne/ReindexOne 路径均重建索引；新 DB 可从仍存在的文件重建。fingerprint 版本 3→4，把 extra 图的路径、大小、mtime 纳入已存在的图片快照算法；不声称覆盖大小和 mtime 都不变的字节修改。
- 可变图片列表暴露原 `stillMatches` 单向包含判断的删除漏洞：现在先比较对应影片的快照数量，再逐项比较；删除其中一张或最后一张会阻止旧输入提交。
- 新测试真实揭露既有单片手工片缺失 NFO 的保留漏接：整库/增量已有保护，但单片走 IndexMovie 完整 upsert，曾把 manual/Alpha 清空为 pending/空标题。现已在同一现有写事务复用结构更新职责，保留元数据/演员/用户数据，只刷新磁盘派生结构并失效扫描基线；有效 NFO 仍正常刷新元数据，不扩大成 manual 永远拒绝更新。四类扫描分别覆盖手工片缺 NFO 与已索引片坏 NFO 下图片添加/清空、ID、标题、状态、身份、演员和播放状态保留。

### 检查与验证记录

- 存储子任务中途失败；Lead 检查实际工作区后补齐其未完成的迁移注册和全部新测试，未以子任务启动/中间消息当作成功交付。
- 第一轮新测试误用 `MarkManual` 签名导致编译失败，已纠正；随后把有效 NFO 更新手工片误设成禁止更新，按现有真实语义改用缺失 NFO fixture，没有为测试修改既有正确政策。
- 独立只读复核证实上述单片缺 NFO 缺陷，并发现旧“未来版本”fixture 使用 17 与新账本冲突；已把未知版本测试改为 18。复核时并行编辑曾造成测试暂时编译失败，该输出不作为最终源码结论；最终由 Lead 在修改稳定后复跑。
- `go test ./internal/store ./internal/scanner -run 'ExtraBackdrop' -count=3`：PASS，store 1.101s / scanner 2.697s。
- `go test -race ./internal/store ./internal/scanner -run 'ExtraBackdrop' -count=1`：PASS，store 6.009s / scanner 16.191s。
- 迁移回归覆盖合成 v16 升级、账本插入失败时新增列事务回滚、重启重入、旧校验和/影片兼容字段保留、新校验和篡改拒绝；有序数组往返、清空及坏 JSON 诊断通过。扫描回归覆盖数字序、非规范名/软链接排除、相对输入转绝对路径、增删改/稳定跳过、单片与新 DB 重建、提交前删除检测。
- 首轮全量 build/vet 通过，Go 测试仅稳定性演练两处末版预期仍写 v16 而失败；已同步预期 v17，冻结 v13 fixture 与 v16 故障注入未改。数据库备份/手工恢复演练另增加非空有序索引保留断言，**只验证数据库列，不代表图片文件已备份**。
- 最终命令：`go test ./internal/stabilityrehearsal -run 'TestSchema13' -count=1 && go build ./... && go vet ./... && go test ./... -count=1 && node --test internal/server/web*_test.cjs && git diff --check`，PASS，退出码 0。升级专项 1.244s；全量 store 55.380s / scanner 7.467s / server 90.387s / stabilityrehearsal 2.821s；前端 280/280、0 跳过，41.025s。台账追加曾引入一个文件尾空行，已移除并通过最终差异检查。

### 明确未完成

阶段契约见 [API 契约 §4.7](<API-CONTRACT.md#47-多背景图磁盘索引mt-03-分阶段施工>)。管理端原始 `movie` 增量含 `ExtraBackdrops` 路径，但路径不是客户端图片 URL；Emby DTO 与图片端点仍单背景图。多图确认输入、顺序/缺图/部分失败写入、选择界面、客户端枚举与访问、图片文件备份仍待后续施工。当前备份含数据库新列及可选 NFO，不复制 extrafanart 字节。未部署、提交、运行生产迁移或做真实客户端/浏览器验收；D-2/D-4/D-5 不变。此处是 §30 当时的范围记录，后续客户端取图推进见 §31。

## 31. 客户端多背景图枚举与按序取图（2026-11-04）

**状态：客户端只读访问已实现，首次/重复联合回归、race 与最终全量验证通过；不是整批 MT-03 或发布验收。** 执行人 Lead，延续已获批 D-1/A。

### 本轮实际落地

- 新增 `movieBackdropPaths` 承担统一排序职责，序列为非空原主 `BackdropPath` 在首，再追加 `ExtraBackdrops` 既定顺序；无原主图路径时 extras 从 0 开始。不依赖文件存在性动态过滤或重排，不修改存储数组。
- 主影片 `BackdropImageTags` 包含全序列，`ImageTags.Backdrop` 为首项；没有图片仍输出非 nil 空数组与对象。分段 `partItem` 的图片标签同样共享主影片序列，本轮未新增虚拟分段图片路由或声称该路由验收。
- 原有 `/Items/:id/Images` 元信息清单为每张背景图输出 `ImageType=Backdrop`、连续 `ImageIndex`、对应路径/文件名/tag；沿用原认证、对象范围与 no-store。没有另建元信息缓存。
- 现有匿名 `/Items/:id/Images/Backdrop/:index` 路由开始消费下标，`Fanart` 别名同序；省略下标为 0。非整数/负数/整数溢出 400，合法越界 404，文件缺失普通原图请求 404 而不是取另一张。其它类型/库/合集/实体封面下标行为未改。
- 选定真实路径后复用现有 serveImage 的 ETag、公开缓存、条件请求与缩略图管线，变体继续按真实路径隔离；`cachedMovie` 完整缓存 Movie 并用 g:version 命名空间，真实扫描的增删提交切换元信息，不增加独立背景图缓存、锁或自动重试。

### 复核与回归

- 独立只读复核既有链路（未改文件、未跑测试）确认版本刷新可复用，指出 extras-only、缺图占序号、分段 DTO 第二入口及真实 JPEG fixture 需求；实现和测试覆盖前三项，图片测试使用可解码 JPEG，而不是扫描测试中的名字标记字节。
- 新增 `TestExtraBackdropClientEnumerationAndImages` 通过真实服务、认证、SQLite 和实际扫描，覆盖主图+两张 extras 的 DTO/元信息/字节/ETag 对应、无下标与 Fanart/小写/emby 路由、非法/越界下标、元信息拒绝匿名、图片仍匿名、no-store/公开缓存、额外图 304、三个不同宽高比的真实 WebP 变体隔离；覆盖中间 extra 缺失不移位、主图缺失不回退、扫描后版本变更及旧最后下标 404、完全没有三张原图时的 extras-only，以及清空后空数组和 404。`TestMovieBackdropPathsDoesNotChangeStoredOrder` 验证不重排 DB 顺序。
- 首次 `go test ./internal/server -run 'Test(ExtraBackdropClient|MovieBackdropPaths)' -count=1`：PASS，0.284s。最终重复 `-count=10`：PASS，2.620s；联合 `-race`（新增用例及既有 CachedMovieVersion/ObjectScopeAuthorization/ImageHeaders/ImageConditional）：PASS，19.951s。随后 `go build ./... && go vet ./... && go test ./... -count=1` 全部通过，server 90.420s / scanner 7.356s / store 55.727s；前端 `node --test internal/server/web*_test.cjs` 为 280/280、0 跳过，37.197s；`git diff --check` 通过，完整命令退出码 0。

### 保留的边界与剩余工作

契约已同步 [API 契约 §4.7](<API-CONTRACT.md#47-多背景图磁盘索引mt-03-分阶段施工>)。已有路径 tag 采用 5 分钟正缓存/30 秒负缓存、实际 tag 仅取 mtime；扫描切换索引版本并不清 tag，覆盖同路径图像或缺失图带旧 If-None-Match 时仍可能短期命中旧 tag、304 或变体。这是既有缓存限制，不把本轮多图枚举说成已修复；本轮未改全局失效策略，不承诺跨扫描永久下标或字节快照。对象权限的细分用户范围继续由既有 ObjectScope 回归验证，新用例只额外断言匿名元信息 401。

仍待多图确认写入的输入/顺序/缺图/部分失败契约与实施、图片资产备份、选择界面及真实客户端/浏览器验收。没有部署、提交、重启或变更 D-2/D-4/D-5。此为 §31 当时范围，图片备份后续进度见 §32。

## 32. 额外背景图文件备份与手工恢复回归（2026-11-04）

**范围：已批准 D-1/A 的图片资产备份阶段；生产接入与专项/CLI/race、最终全量验证通过，不代表 MT-03 整批完成。**

### 实际实现

- 新增 `backup.Options.ExtraBackdrops` 与 `Result.ExtraBackdropCount`，前者零值 false，不改变已有库内调用的默认行为；CLI 新增 `--extra-backdrops` 默认 true，可独立关闭。`--nfo=false` 不再等于 CLI 的数据库单独备份；无 libraries 表的自定义数据库须同时关闭两个开关，使用说明已同步。
- 只读库清单在 NFO 或额外图任一开启时必需，仍只查 libraries，不读 movies 新列。物理遍历规范 `extrafanart/fanartN.jpg` 普通文件，与扫描器保持正整数/无前导零/大小写/溢出规则一致；根、子目录及文件链接不跟随，结果按库及完整库内相对路径存到 `extra-backdrops/lib-ID/`。旧应用数据库没有迁移 17 列也可复制图片，无需源库迁移或写入。
- 图片复制在现有 stage 内、替换完整备份前执行；复制错误返回、清理 stage/锁，同秒已有备份不会因该类错误被替换。不增加下载、文件锁、自动重试或共享匹配抽象。离线/删除库跳过，其他根检查错误传播；在线库遍历/复制失败不静默发布半份图片备份。NFO 原有行为不顺手重写。
- 恢复为离线手工复制：额外图文件树回到相应库根，绝对路径迁移需调整库路径并重扫。合并复制不会删除备份之外的新图，精确回退需人工对账；数据库和文件不是联合时点快照，不包含 STRM、视频、原三张封面或全部 JPG。

### 复核与实际验证

- 独立只读审查未改代码、未跑测试，指出 NFO 既有根错误吞并和符号链接文件跟随不能复制到新链路；新链路单独使用 Lstat/普通文件筛选，只有不存在根按离线跳过。原 NFO 逻辑的修改需单独确认，未扩大本轮范围。
- 新 `TestRunExtraBackdropsOptionsAndRestore` 在无 extra_backdrops 列的旧 schema fixture 上验证四种开关组合、根/嵌套目录、规范名筛选；真实 24×16 JPEG 逐字节不变，按产物相对路径手工复制到新根后可解码。此为字节/目录恢复测试，不冒称已用恢复数据库启动并完成客户端整链路验收。
- 链接/离线库跳过、extras-only 输出与媒体目录重叠拒绝、无库表时开启失败和 DB-only 兼容均回归。故障测试在快照后精确制造图片目标目录阻塞，验证复制错误保留先前同秒图片与完整 SQLite、清理 stage 与锁，不用 chmod 假定 root 会权限失败。
- 新 CLI 回归实际构建并运行 embybackup，验证默认、仅关 NFO、仅关额外图、两者关闭四种行为及计数/产物。
- `go test ./internal/backup -run 'ExtraBackdrop' -count=3`：PASS，0.555s；`go test ./internal/backup ./cmd/embybackup -count=1`：PASS，0.939s / 0.816s；新增专项 `-race`：PASS，1.338s。
- 最终 `go build ./... && go vet ./... && go test ./... -count=1`、`node --test internal/server/web*_test.cjs` 与 `git diff --check` 全部通过，完整命令退出码 0。全量 backup 1.283s / embybackup 0.678s / server 91.249s / store 55.335s；前端 280/280、0 跳过，37.278s。

### 保留限制

沿用现有备份发布/清理规则：替换同秒备份是 RemoveAll 后 Rename，不承诺跨崩溃原子替换；prune 也可能在完整产物已发布后失败。根文件系统在复制期间保持稳定仍为运行假设，未引入 TOCTOU 加固或跨 DB/文件事务。额外图多库同名隔离、源读取/磁盘满等故障组合未专门穷举；此处不把建议清单当成已执行测试。多图确认写入、选择界面、真实客户端/生产恢复验收仍未完成，D-2/D-4/D-5 未改。

## 33. 用户确认非删除多图写入及部分成功语义（2026-11-04）

**状态：确认 API 与 JPEG 写入完成本地实施，针对性重复、race、最终全仓 build/vet/Go 测试及前端回归 PASS；界面尚未接通，MT-03 未整批验收。**

### 本轮用户明确选择

- 按选择顺序落 `fanart1.jpg…`，overwrite=false 跳过已有非空图，true 可覆盖，保留未涉及的旧尾图；省略选择不写额外图。没有授权集合清空、删除旧图或仅追加策略。
- 新鲜详情列表版本不符在任何写入前 409；单张下载/编码失败保留旧目标，继续其它图与 NFO，200+warnings，不自动重试确认。NFO/索引原部分成功边界仍保留。
- 输入细节及恢复边界已登记 API 契约 §4.7；D-2/D-4/D-5 不在此次授权内。

### 施工与验证范围

- HTTP 确认增量 `stills{version,indices}`，入口拒绝非法版本、空/超量/负数/重复/非整数/空值下标；有选择才重新查详情及摘要，版本不符或范围错误在 Apply 前退出，不改 scrape_error。旧请求继续原 ScrapeOneMovie 路径。
- 内部只传经服务端归一化并解析的地址。选图走 MetaTube backdrop 路径并使用可信 RemoteURL，真正 JPEG 编码复用 imageutil 解码限制，temp+原目标提交锁，成功后 rename，统计写入数量及路径以复用 tag 失效和单片扫描。
- 真实 HTTP fixture 验证归一化版本、[1,0] 顺序、已有尾图保留、5 张写入及磁盘/DB/客户端可读 JPEG；坏图部分成功、保留旧目标、补缺失不下载及无 temp 残留。详情失败/当前列表越界不改影片与 NFO、无图片下载，关闭图片总开关零下载，Update=true 传 lazy=false，占用 NFO 门闩先于详情查询返回 409。空值下标已用 []*int 在边界拒绝，覆盖 [null] 与 [1,null]，不默认为 0。
- 首次 HTTP 联合 0.301s；修正及补齐用例后最终重复 10 次 3.147s，联合 race 13.769s。scraper/imageutil 全包 5.315s/0.375s、race 57.826s/4.223s 通过，覆盖真实 JPEG、选图顺序/质量路由、缺省/空选择/关闭开关、下载/编码/rename 失败保留旧目标、后续图及 NFO 继续、唯一 temp 清理和取消终止。
- 独立只读复核只报 null 转零这一阻断点（复核基于修改前快照，Lead 已同步修正并回归）；未发现其它新的服务端阻断。新增确认分支的“阻塞详情时取消并释放门闩”未单独注入测试，不能把 scraper 取消测试当成该 HTTP 场景覆盖。未引入新重试、锁或取消机制。
- 最终 `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全部通过（server 92.522s / store 56.644s / scanner 7.617s / scraper 4.917s），前端 280/280、0 跳过（37.344s），`git diff --check` 通过。所有本轮后台任务均已收集，原有工作区修改保留。
- 不把 fixture 当成实际 MetaTube 裁切证据；没有新生产迁移、部署、提交、重启或集合级原子提交保证。原已存在的同名主三图逻辑不做风格重构。

## 34. 剧照有序选择界面与浏览器回归（2026-11-04）

**状态：选择界面已完成本地实施，专项及全量前端 297/297、0 跳过通过；独立只读复核无阻断项；最终全仓 build/vet/Go 测试通过。MT-03 不标记整批发布验收。**

### 实施范围

- 沿用原管理台主题与组件，不引入依赖、字体或视觉重构。使用原生 checkbox/关联 label，默认不选；按点击顺序显示 fanartN.jpg，取消后压紧、重选排尾，支持计数与清空。空选省略 stills，不表示删除旧图；界面明确提示确认才保存及保留未涉及旧尾图。
- 版本取自 inspect 第一条 preview 的 version，仅合法 64 位小写十六进制摘要允许选择；非空确认只传版本与有序下标，不接受远端 URL。保留显式 update=false/position=0。
- 新搜索及成功候选切换/重读清空选择；详情查询失败保留上一份详情及绑定选择，不把旧选择绑定到新候选。沿用会话与搜索/详情序号隔离迟到响应。
- 确认期间同步冻结搜索、候选、选择和重复确认，保留关闭；失败恢复先前禁用状态。409 无稳定细分 code，故采用中性提示和显式重读，不按 message 推断原因、不自动重复写入。部分成功继续用“已保存”面板替换搜索/确认区并聚焦完成按钮；关闭后的迟到写入结果不污染新会话。

### 验证与复核

- 新增 VM/fake DOM 专项 14 项，覆盖载荷/顺序、空选、转义、切换失败恢复、搜索及详情竞态、busy、两类 409、部分成功及迟到确认；实现者专项 14/14 PASS。旧提取式测试扩展到新增 busy helper，旧 VM 注入 URLSearchParams，更新只读标题及空网格断言；未修改服务端业务以迁就测试。
- 新增真实 Chromium 专项 3 项，使用生产刮削 UI 段、实际样式及浮层 markup，在 1440×900 / 390×844 验证原生键盘 Space 与焦点保留、44px 卡片目标、无页面级横向溢出、顺序变化、确认冻结、409 重读后新版本、空选载荷、部分成功面板与关闭迟到响应。独立复核另跑 3/3 PASS、0 跳过；静态追踪实际 api、浮层关闭、refreshPage 与服务端版本核验调用链，范围内无有证据的阻断项。
- 首次联合运行的 3 个浏览器专项通过，但旧 VM 缺 URLSearchParams 导致 15 项失败、空网格旧正则误匹配工具栏导致 1 项失败；均为测试适配缺口，修正后联合 176/176、0 跳过通过。最终 node --check 及带 CHROME_BIN 的全量前端回归 297/297、0 跳过通过（59.631s）。
- 浏览器 fixture 的 API、浮层管理及刷新函数仍为 stub，不代表完整登录/路由/focus-trap 运行验收；未跑真实 MetaTube 下载、此次 UI 到真实磁盘/客户端的端到端链路、屏幕阅读器或跨浏览器验证。未单独断言本轮图片全部加载或网格列数，不挪用以前只读网格的证据。
- 最终 go build ./...、go vet ./...、go test ./... -count=1 全部通过，退出码 0（server 102.685s / store 66.092s / scanner 11.768s / scraper 8.949s / stabilityrehearsal 3.015s）。前端最终全量退出码 0，最终 git diff --check 通过；全部本轮后台验证任务均已收集。保留工作区既有修改，无部署、提交、重启或生产迁移。D-2/D-4/D-5 未决政策不变。

## 35. 完整应用剧照写入联合验收与取消边界（2026-11-04）

**状态：完整应用联合回归与取消边界已补齐并本地验证通过；不等于 MT-03 发布验收或整体方案已完成。**

- 新增 [scrape_stills_cancel_test.go](<internal/server/scrape_stills_cancel_test.go>)：走实际路由、鉴权、handler 与本地阻塞上游，等确认持有 NFO 门闩再取消；断言上游收到取消、handler 返回 502、门闩可重新获取，影片行、g:version、媒体目录文件字节及下载次数保持不变。专项重复 3 次通过（0.957s）；`go test -race ./internal/server -run '^TestScrapeSelectedStills(CanceledLookup|Confirm)$' -count=3` 通过（20.723s）。
- 新增 [scrape_stills_browser_test.go](<internal/server/scrape_stills_browser_test.go>) 与 [scrape_stills_browser.cjs](<internal/server/scrape_stills_browser.cjs>)：临时 SQLite/媒体目录、实际完整管理台登录/路由/浮层及生产事件处理、真实 Go HTTP API 和本地 MetaTube/JPEG fixture；不抽取 UI 片段、不替换 api/浮层/刷新函数。移动 390 与桌面 1440 各走默认不选、手工搜索/inspect、实际指针按 [1,0] 选图；Node 与 Go 握手，在确认前逐项比对影片行与整棵媒体目录路径/字节，关闭后再次比对；保存后解码两张 JPEG（80×24、40×24），核对实际 DB ExtraBackdrops 顺序与 Emby Backdrop/1、/2 响应字节。
- 使用已有 Chromium 与 Node，无安装或依赖变更。专项首次通过（8.954s）；重复 3 次通过（45.846s，12 次 Node 执行均 pass=1/skipped=0）；专项 race 通过（41.553s，4 次 Node 执行均 0 跳过，无 race 报告）。另验证未设置 CHROME_BIN 时显式跳过；该跳过结果不计浏览器覆盖。父会话检查实际代码及重复/race 日志终态，与报告一致。
- 最终 `go build ./...`、`go vet ./...`、`CHROME_BIN=... go test ./... -count=1` 全部通过，退出码 0（server 97.911s / store 54.780s / scanner 9.888s / scraper 5.710s / stabilityrehearsal 3.025s）；带 Chromium 的全量前端 297/297、0 跳过通过（50.469s），新增 Node 脚本语法检查及差异检查通过。所有本轮后台验证任务已收集。
- 首次编译碰到并行写入中的新浏览器测试文件残留 token，属于尚未完成文件的 setup 失败；完整文件落地后回归通过。浏览器测试初稿另有真实跳转销毁 evaluate 上下文、fixture 图片路由不匹配的问题，均仅修正测试，不是生产缺陷。本轮没有据此改写生产代码。
- 本轮仅新增测试与文档，不部署、提交、重启或迁移生产库；本地 fixture 不能替代真实 MetaTube、真实客户端应用、屏幕阅读器、跨浏览器或生产发布验收。D-2/D-4/D-5 未决政策不变，不据本地联合回归放行整批。

## 36. D-4/A 图片代理饱和立即拒绝（2026-11-04）

**状态：用户已批准 A，生产准入与契约已修改；专项重复/race 及最终全量验证通过，完成本地实施与回归，不等于生产放行。**

- 批准人：当前用户；执行人：Lead；日期：2026-11-04。交互确认明确选择“立即返回 503”：保留 4 路和单张 8MB，不排队、不自动重试；不增加频率限制或承诺总内存上限。D-2/D-5 仍待决。
- 图片代理先查字节缓存，命中不占名额；未命中用非阻塞通道准入，满时立即 503，不加成功缓存头。已准入的列表解析/下载失败或取消仍为 502，defer 释放名额；饱和时已取消的未命中请求同样 503，不新增取消优先级。保留取得名额后的二次缓存检查。
- 实际 HTTP 与本地上游回归已覆盖四路占满、第五路拒绝/无触达、饱和缓存命中、成功/错误/取消释放及后续恢复；固定容量 4 和单张 8MB，拒绝后同一 URL 的后续 GET 可正常成功。初次通过（0.920s），专项重复 20 次通过（22.573s，80 个叶子用例），专项 race 重复 5 次通过（80.629s，20 个 race 叶子用例，无 race 报告）。父会话检查最终源码，四路阻塞期间第五路必须完成，未以 sleeps 或释放上游伪装立即拒绝。取消用例通过实际路由 ServeHTTP 观察 handler 的 502，真正断开下游连接的客户端只会观察到自身取消，不将 502 冒充其收到的网络响应。原等待取消测试按新批准政策更新；不把旧测试结果作为新行为通过证据。
- 冷缓存版本隔离专项原为 16 路并发且全部断言 404/200，这一前提与新饱和 503 政策不兼容；改为 4 路（新旧版本各 2）并保留严格状态/字节/缓存断言，不吞掉 503 或自动重试；饱和由独立准入专项覆盖。版本专项重复 10 次通过（4.650s），race 重复 3 次通过（23.841s）。前端最终全量已通过 297/297、0 跳过（44.110s）。最终 build/vet 与启用 Chromium 的全仓 Go 测试全部通过，退出码 0（server 120.473s / store 73.688s / scanner 9.832s / scraper 5.923s / stabilityrehearsal 3.563s）；无生产改动发生在该最终验证之后。
- 最终 gofmt 检查无输出、git diff --check 通过；本轮全部后台验证任务已收集。保留既有未提交修改，不部署、提交、重启或迁移生产库；D-2/D-5 仍需另行确认，不据此认定整体目标或正式发布已完成。

## 37. D-2/A 标记归属与中断恢复（2026-11-04）

**状态：D-2/A 归属底座、刮削/API/UI 接入与并发收口已完成本地联合回归（最终证据于 2026-11-05 收集）；未完成整批 MT-04、独立整理入口或生产发布验收。**

- 用户已确认 `movies.auto_marks`：只撤销上次确实归属于程序的版本标签；无记录的历史同名标签保留，不从 C/UC 等文本追认。已记录同名标签以记录为准，无法辨认外部后来赋予的人工含义。覆盖模式保留其他旧标签，补缺失且已有标签、Tags 锁或全局锁时不改标签、不扩大归属；上游标签或无归属旧标签与当前标记同名时，不认领该文本。
- 用户随后授权休息期间按推荐方案自主决策、不因提问中断任务。执行人 Lead；继续保留未提交改动，不擅自部署、重启或迁移生产库。原三个子任务中断，无完成报告；已检查实际工作区，未把其启动或中间报告当作已实施。改用明确分离的 store/NFO/刮削接入任务，继续施工。
- 存储底座已落盘：迁移 18 默认空归属且沿用既有事务/校验账本；普通 upsert 新行走默认空集合、冲突保留原值，完整 Movie 读取与 JSON 诊断包含 AutoMarks。`SetAutoMarks` 仅改归属，以实际 library 发布版本，失败回滚、不清除 scrape_error。存储专项 5.745s、覆盖迁移/扫描/空元数据的 scoped race 160.432s、scoped vet/diff 均通过；首次仅新测试的 MovieIDs 索引类型误用导致编译失败，修正为 sourcePath 映射索引后复测通过。存储任务未冒称全包验收。
- NFO 合并在现有路径锁内完成。数据库记录与 NFO 不是联合事务：NFO 提交后归属记录失败应单独报告已保存的部分成功，不冒充图片或索引失败，也不声称重扫能恢复归属。基础接口已稳定并通过专项验证，接入与故障测试正在施工；API 契约 §4.8 已登记获批语义，不提前宣称联合通过。
- NFO 子任务已实际落盘：复用既有路径锁及公共实现，旧手工编辑接口语义保留；新增归属接口覆盖撤回、冲突、历史不猜、补缺失/字段锁/全局锁、读写失败与并发编辑。子任务报告全包 8.081s、归属专项重复 10 次 0.106s、全包 race 9.218s、scoped vet/diff 检查通过；父会话已核对核心合并与错误返回路径。接入和跨包联合测试尚未执行，不将单包通过冒充链路验收。
- 存储接口落盘后，父会话实际跑通四种普通扫描路径保留归属、缺记录不推断及删库重建不认领回归：重复 3 次 1.935s，专项 race 12.056s；旧 v13 升级失败回滚/重试与含非空归属、额外图索引的数据库备份恢复演练通过 1.184s。独立只读复核未发现 store 阻断，指出新测试未直达 manual 分支：父会话已补 manual 且 NFO 缺失的四类扫描，普通/manual 联合重复 3 次 3.315s、race 21.140s 全通过。复核不覆盖刮削接入，不把原始备份值断言冒充恢复后再刮削行为证明。父会话自有文件已 gofmt、差异检查通过；以上不替代存储全包、刮削接入与全仓测试，其余结果待登记。
- 刮削接入已落盘：当前生成标签与上游 Tags 分开合并，只有实际归属变化才写库；NFO 提交后优先记录归属，再执行取消检查及重扫。单条 HTTP 独立返回脱敏 auto_marks_error；批量仍计 NFO 成功，并保留人工核对诊断；前端沿用已保存面板，独立展示归属、索引及图片问题，不自动重试。接入专项重复 5 次 scraper 6.586s / server 5.244s，联合 race scraper 22.790s / server 30.803s，VM 18/18 通过；补强真实 RunMovies(nil-store) 的成功计数/OnItem 诊断及归属不变不发布版本，重复 5 次 6.357s / race 20.347s 通过。以上均为已收集子任务证据，不代替全仓验证。
- 联合复核发现真实触发路径：既有 claimNFO 允许同 kind 的两次 scrape 同时通过，两请求可读取同一旧 AutoMarks；第一次 NFO/DB 提交后第二次仍按旧归属合并，可能静默错配。按已批准 NFO 互斥边界和用户自主决策授权，Lead 仅将 scrape 加入既有独占条件，并在 confirm 取得通道后重读 Movie；未新增锁、排队、重试，也不改探测等同 kind 政策。原确认/部分成功/选图回归通过 2.891s；新增两项确定性并发专项重复 10 次通过 7.418s，与部分成功联合 race 重复 3 次通过 68.115s。测试覆盖真实 HTTP 双确认和真实批量任务与确认互斥，409 时 NFO/影片/版本/上游调用不变；完整认证路由的 JSON Read 屏障另覆盖 movieParam 读后、拿门前旧快照，证明只有独占而不重读仍会错误删除已放弃归属的标签。无生产测试 hook。
- 独立只读复核未发现生产 scrape 嵌套 claim 或门前旧归属回写路径；底层 Apply/RunMovies 本身不增加跨文件与 DB 互斥，保证限于 App 生产入口。既有批量释放 scrape 后再申请 scan 的窗口可能令整库刷新失败，但会明确上报 IndexRefreshFailed；不顺手改写既有任务生命周期。另确认取消与归属失败同时发生时，原取消错误会遮住成功响应/批量专属人工核对提示，归属错误仍写日志；契约已明确此诊断边界，不把 502 当作零写入，未扩大取消后的写入或重扫行为。
- 父会话最终 build/vet、前端语法检查和前端 300/300（0 跳过，42.342s）、差异检查通过；单独启用 Chromium 的完整应用剧照联合回归通过 18.649s（移动 390/桌面 1440，取消/确认四次 Node 执行均 0 跳过，实际 HTTP/JPEG 写盘/有序索引/Emby 图片链路）。后者是既有选图链路回归，不冒充新归属诊断的完整浏览器专项。最终全仓 `go test ./... -count=1` 通过（server 111.518s / store 71.256s / scanner 8.941s / scraper 6.637s / NFO 8.055s / stabilityrehearsal 3.377s，exit 0）；默认 Go 命令未启用浏览器，浏览器证据来自前述单独启用 Chromium 的实跑，不把跳过当通过。所有本阶段成员已交付，共享任务完成，后台验证作业均已收集。无部署、提交、重启或生产库迁移；整体后续计划仍未完成。

## 38. N-01 有界 NFO / STRM 读取（2026-11-05）

**状态：读取入口、扫描跳过与复核修复完成本地实施；增长专项、scoped race 与最终全仓验证已收集通过，不等于 N-01 / M1 或生产放行。**

- 依用户休息期间按推荐方案自主决策的授权，采用 D-5 建议阈值并明确二进制单位：NFO 全文 8MiB、STRM 全文 1MiB，精确等于可读，超过返回明确错误而不解析截断内容。使用原始读取入口的 LimitReader(max+1)，不依赖先 Stat；STRM 首行后的全文也计入。内部已读取字节的 XML 解析不重复校验，不新增通用 util、锁、重试或依赖。
- NFO 普通读取、预览、修改、演员头像、翻译提交和编辑恢复摘要使用统一有界读取；保留原有路径锁与原子写政策，纯写出 API 不新增输出限制。NFO 子任务全包 8.604s、race 17.709s、scoped vet/diff 通过；读写专项覆盖精确边界、超限、多倍上限、缺失/目录错误及拒绝时原字节/mtime/目录不变。摘要专项只证明读取与摘要边界，不冒充完整 HTTP 验收。
- 整库 full/incremental 对超限分组 WARN、按 STRM 成员数记 skipped，incomplete=true、failed 不增加；保留原影片/旧扫描基线，其余健康分组继续，禁止本次缺失清理。下一次显式扫描在文件恢复后更新及清理，无自动重试。RescanOne/ReindexOne 有 CD1 时先检查栈依赖；无 CD1 时保留逐项独立处理政策，不假称已提交的其它成员可回滚。真实 I/O、取消及库身份变化保留原错误语义；legacy 单条 NFO 不可读保留元数据与诊断政策不顺手改写。
- 首批 scanner 专项重复 3 次 4.988s、race 27.673s 通过。独立复核随后指出提交前 stillMatches 把超限折叠成普通变化，已改为保留原错误类型、两处调用按超限跳过；原普通变化仍失败。另补共享 NFO 与侧车同时存在的边界检查，并把非 HTTP 删除移到 NFO 大小检查之后，避免超限依赖仍删除旧行。共享/侧车、四种扫描与无 CD1 独立 CD 成员专项重复 3 次 9.186s 通过；不将修复之前的全量通过当作最终证据。
- 上次执行增长专项的成员中断且没有交付报告；恢复时已检查真实文件并唤醒同一成员收尾，未把启动或未收集结果当通过。增长/snapshot 专项重复 5 次通过（10.645s）；覆盖 Snapshot/增长/超限/额外图/Incremental 的 scoped race 通过（149.737s），随后 scanner vet、scoped diff 检查通过。12 个 snapshot 情形覆盖第一复核前/构建后、缺失 NFO 新建超限、正常变化及真实 I/O；16 个真实扫描情形在既有 context.Err/health 窗口触发第二复核，验证 full/incremental 跳过、旧行/演员/基线/版本保留、孤儿不清理及健康后组继续。第一复核窗口仅由直接 snapshot 测试覆盖，不声称原子文件快照。初次仅新测试的健康组 B.strm 排在 lowercased CD key 前导致窗口失配，改成 b.strm 后重复/race 通过；无生产测试 hook、锁或重试。重复命令中两项历史测试名未匹配，不据此声称额外覆盖；上述 race 用实际匹配范围补齐。成员全部作业已收集。
- 最终 build/vet、全仓 `go test ./... -count=1`、前端 300/300（0 跳过，41.828s）和差异检查通过，exit 0（server 117.883s / store 75.429s / scanner 21.557s / NFO 9.073s / scraper 8.587s / stabilityrehearsal 3.348s）。默认 Go 命令未启用 Chromium，不把完整应用浏览器专项跳过计作本阶段覆盖；本轮不改 UI，也不重报历史浏览器结果。早期全量曾因 edit_recovery.go 的未使用 os 导入编译失败，已删除导入；后续复核修复后的结果才是本次最终证据。全部本阶段成员与 Lead 的验证作业已收集，生产代码未在最终验证后变化。
- 本子项只处理文件解析读取，不限制配置 YAML、媒体 JSON、流式备份或总内存/耗时；ffprobe 输出/超时/进程治理及升级回退矩阵尚未实施。保留全部既有未提交改动，无部署、提交、重启或生产库迁移，整体目标继续。

## 39. N-01 ffprobe 执行资源边界（2026-11-05）

**状态：本地 ffprobe 子项完成；共享执行器、配置兼容、真实进程与 HTTP/批量重复/race、交叉编译及全仓验证通过。升级/重启/回退矩阵与 N-01 / M1 总体放行仍未完成。**

- Run 最大 60 秒预算含排队，更短正预算保留；Verify 保持最多 15 秒。两者在单实例进程共享 2 个名额，等候可取消，无新增 503/重试。stdout 4MiB、stderr 64KiB 精确边界允许，超过失败且不解析截断 Raw。
- Unix 子进程组取消，非 Unix 直接子进程降级；WaitDelay=1 秒用于继承管道收尾，可能附加于执行预算，不声称 OS 不可中断 I/O 或脱组后代能硬实时终止。实现不新增依赖，不扩张 NFO 门闩政策。
- 配置沿用历史默认/上限截取兼容方式：timeout 非正值默认 60、正值最多 60；workers 非正值默认 2、正值最多 2，单 worker 仍保留。旧高值 YAML 不新增启动拒绝。配置表格覆盖默认/负值/精确边界/短预算/高值及大整数，config 全包通过（0.011s），差异检查通过；不是进程实现已验收。
- 真实 HTTP/批量专项仅新增 server 测试文件，首轮 0.997s、重复 10 次 10.614s、race 重复 3 次 55.833s 和差异检查通过，全部成员作业已收集。使用有效 JSON + 空白超出 stdout 或独立 stderr 超限，避免把解析错误误当限额证明；单条 502/FFPROBE_FAILED 保留目标 NFO、已有或不存在的 mediainfo、Movie 和版本，随后健康调用恢复。批量纯失败不发布版本，真实 202 启动单 worker 坏项后仍处理健康项，Done2/Failed1/Success1，只有健康库刷新 +1、不误记取消/中止，NFO 门和 Verify 名额可复用。不据此证明真实远端、跨平台或精确阈值（后者由 probe 专项覆盖）。
- 独立只读复核 config/文档与稳定 probe 源码，未发现有证据的阻断缺陷；未改文件、未跑测试，不将审核当运行覆盖。证据包括 int 域先截取防 duration 溢出、两输出单 copy goroutine 所有权、Wait 后读 buffer、取消优先及所有退出释放名额。非阻断空白：非零 exit 的 stderr 摘要/空输出分支没有新增明确假进程回归，非 Unix 尚无运行证明，负值 YAML 仅 accessor 而非独立 Load 专例；不扩写无关旧逻辑。
- 真实进程资源专项重复 5 次通过（12.623s），probe 整包 race 重复 3 次（10.163s）、vet、差异检查及 Windows/Darwin amd64 交叉编译通过；`-exec /bin/true` 仅作编译，非运行。精确/+1 两流、Run/Verify 共用名额、排队取消/父期限/自身短预算且不启动、启动失败/超限释放与后续恢复均覆盖；Linux 同组取消、主进程先退继承管道和超输出清理使用真实子进程。/proc 消失或 Z/X 表示不再运行，不声称负责 init 回收 zombie。未实际等待 60/15 秒，常量/分支与短预算覆盖支持契约。
- 如实记录并修复测试失败：首轮资源专项 5 次 9.878s 后 race 出现 `process_linux_test.go:35 strconv.Atoi parsing "": invalid syntax`；文件创建可见早于 WriteFile 发布内容，仅将测试 awaitProbeMarker 改为等待非空内容，生产代码不变。修复后上述重复/race 全通过，不隐去失败也不把它当产品故障。
- Lead 最终全仓 build/vet/Go 与差异检查通过：server 105.163s、store 60.148s、scanner 25.698s、probe 3.181s、nfo 9.874s、stabilityrehearsal 3.625s；前端 300/300、0 跳过（70.946s）。不把默认 Go 里的浏览器跳过算运行覆盖，本阶段不改 UI。全部成员验证作业已收集，源码冻结；升级回退矩阵未完成。保留既有未提交改动，无部署、提交、重启或生产库迁移。

## 40. N-01 原生安装与进程重启冒烟（2026-11-05）

**状态：本轮原生安装清理、静态契约、本版真实二进制重启及校验和本地冒烟完成；最终 build/vet/全仓 Go 通过。不等于原生正式发布或升级回退矩阵完成。**

- 依据原生方案退出 Docker 支持，已删除经绝对路径核实的两个容器样例并替换 README 推荐入口，不删除历史台账。systemd 保留 emby 身份/严格沙盒，补 StateDirectory/RuntimeDirectory 0750、UMask0077、明确 PATH、KillMode=control-group 与 config 文件回写放行；媒体库须显式 ReadWritePaths + Unix/ACL/NAS 权限，不能靠沙盒放行授予权限。独立成员静态契约重复 10 次通过（0.009s）、最终复验 0.008s、vet/scoped diff 通过，全部作业已收集。不是安装/systemd/真实媒体挂载运行证明；未在当前主机安装/重启真实服务。
- 新增 Linux 真实入口点冒烟：在临时目录构建并记录 fixture 二进制 SHA256，核验 `-version` 与真实 `/api/version` 注入值；启动、SIGTERM join、重新启动，检查稳定 ServerId 与真实 SQLite 设置保留。首次通过 5.474s，两个关闭分别 7.456499ms / 4.029685ms。不是 Handler 测试，也不是生产或 systemd 运行证明；版本标记为 fixture，不伪造正式 SHA。空闲进程退出不能代替带后台写入/长流的全部关闭证据。重复 3 次通过（11.810s，6 次 SIGTERM join 3.671933–6.558132ms），已有 stabilityrehearsal 全包 2.421s、cmd vet 及差异检查通过；这仍不是前版升级/回退证明。
- 发布 workflow 在原同批版本输出核验后生成并反查 `SHA256SUMS`，由既有 dist 上传步骤发布；本地临时目录实际运行同样的 trimpath build、版本注入、`sha256sum` 生成/反查命令通过，fixture 产物位于 `/tmp/emby-go-native-checksum.Jsiz4l`。不声称本轮跑了 GitHub Release、其它平台二进制或同 SHA 正式门禁。最终 build/vet/全仓 Go 通过：cmd/metatube 2.916s、stabilityrehearsal 3.510s、server 161.692s、store 100.152s；差异检查通过，所有成员与本轮作业已收集。无 UI 代码变更，本轮未另跑前端/浏览器，不挪用上一轮前端结果作为本轮重跑证据。
- 独立只读迁移/回退复核：schema13→18 两次 Open、ledger16 注入失败回滚与重试、未知未来19/checksum 拒绝已有组件证据；但升级后 DB 复制给同一新版、NFO 备份比对、仅字符串额外图索引不是真正回退。只读核到本地 v2.2.6（234a220c9febfba4b1d610fd764448609a31e325）旧代码识别到16，面对17/18预期拒绝而非向后一版兼容；尚无旧 artifact 实际启动证据，WAL PRAGMA 在迁移前执行，不承诺拒绝前后 DB 字节零变化。发布同 SHA CI 绑定、受支持平台原生运行、迁移进程中断/磁盘满/备份失败仍待补，不把事务 trigger 等同断电。复核无编辑/测试；校验和原缺口已由本轮修改收口，但未跑 GitHub 发布。
- 重新逐项阅读详细方案：实际 ffprobe 出站/重定向/嵌套资源机制、文件特殊类型/链接/挂载耗时支持矩阵仍未冻结或实测，之前资源预算子项不能代替。升级回退还需具名前版二进制/schema 与匹配 DB/NFO/额外图及归属恢复、秘密保护/恢复后凭据处置、损失窗口和真实平台证据。未放松未知迁移拒绝，无部署/提交/生产迁移。

## 41. N-01 匹配快照恢复与本地跨版演练（2026-11-05）

**状态：冻结组合的本地匹配恢复/跨版演练子项完成；不放行 N-01/M1 或正式发布兼容矩阵。**

- 冻结本地组合：Linux 6.1.0-53-amd64 x86_64 / Go1.27.2，旧源码 v2.2.6 tag `234a220c9febfba4b1d610fd764448609a31e325`，从本地 git archive 到临时目录、GOPROXY/GOSUMDB=off 构建；未 checkout/清理当前 dirtytree，未联网下载。旧二进制 `/tmp/emby-go-v2.2.6-source.Y1gtmy/emby-go-previous`，SHA256 `25665f57b56e618ca51119bb6a71103c38a49e3a4cabaaca6ad6eb2a64e8d7b5`，版本输出明确 local-rehearsal，不冒充官方发布 artifact。新版为当前 HEAD52836dd 加既有未提交施工，测试另记实际 binary SHA，不伪称正式源码 SHA。
- 两条验证分开：schema18 DB + 人工 NFO + 真实额外图片 + auto_marks 匹配恢复验证当前归属；旧二进制实际生成16后再由新二进制升级18、旧版实际拒绝17/18、新版停服后恢复旧匹配集合再启动验证跨版路径。旧16无 auto_marks，不给它虚构归属能力。缺旧artifact环境时 skip 不计通过；不删除ledger绕过拒绝。
- README 已补恢复流程：备份前全体写入者停写、配置与工具未涵盖图片另存、先原始业务/文件清单比对再重扫、停服后处理 WAL/SHM、显式对账新增文件。损失窗口为快照后全部写入；恢复不是自动合并或零损失。工具不加密且默认目录不保证私密，材料含摘要/源 URL/配置凭据，要求私密父目录和受控传输；恢复旧凭据可能复活撤销前状态，维护隔离中按目标版本流程完成轮换/撤销后再开放。未声称上述安全处置已做真实旧版验收。
- 跨版实证：新增 `cmd/metatube/native_upgrade_test.go` 实际依次启动/停止两个二进制，旧16种业务数据→新18两次健康启动/优雅退出→旧明确拒绝 migration17→恢复匹配 DB/config/STRM/人工 NFO/真实 JPEG、移除后增图与旧 WAL/SHM→原始比对后旧健康启动。业务快照覆盖账户摘要/库授权/影片/个人播放状态/会话事件/settings/版本；启动不改 config/媒体；旧16既有标签在新18的 auto_marks 为空。首轮5.606s，最终有旧env重复3次10.744s（0skip）；既有Restart 2.959s、cmd vet/限定diff PASS。未设旧env单独执行明确skip1（0.022s），不计升级证据。新版 fixture SHA256 `e756168af9348bf03e5a70e23c86e782a0f6effdf5703efea32daa7638add827`，工作树源码本地构建非官方artifact。
- schema18 匹配恢复新增 `internal/stabilityrehearsal/matched_restore_test.go`：升级历史fixture至18，停写后由真实 `backup.Run` 备份 NFO/额外图；再实际改账户摘要/授权/播放/标签归属/警告及 NFO/JPEG、删除旧图和新增图；覆盖回原路径并删新增图，**先原始 SQL 与 NFO/JPEG bytes/目录清单比对**，再 `Store.Open` 验证认证/授权/播放/非空 auto_marks 与有序额外图。所有匹配snapshot/restore阶段的 SQL/Store 句柄显式关闭；历史隐藏句柄只在fixture准备子测试结束时cleanup，不把准备复制视为离线快照证明。初始新测试错引用 `Movie.ScrapeError` 且 SQL 列名错写 `scrape_error`，Lead静态契约命令被编译阻断，成员已改为真实 `LastScrapeError/last_scrape_error`；修正后专项重复3次1.710s、race专项1次5.175s、稳定性包 vet/限定diff 均 PASS，成员全部job已收集（task-6完成）。两条专项均进入最终全仓回归。
- 最终 Lead 作业 `bash-9a8d70f8ff264781` exit0，build/vet/全仓 Go/diff 均 PASS；全仓 Go 显式设置实际旧artifact env，不以默认skip充当升级证据（cmd 5.539s / stabilityrehearsal 3.439s / server 98.816s / store 59.550s）。Lead 阅读两条新测试并复核修正，两项共享任务完成、全部成员及后台作业结果已收集。无 UI 改动，本轮不重标此前前端数字；不安装/启动 systemd、不触碰生产库/服务，不据此补齐迁移kill/磁盘满、实际 ffprobe 出站或特殊文件/挂载支持门禁。

## 42. N-01 迁移中断与引擎容量失败演练（2026-11-05）

**状态：两项本地测试门禁完成，专项重复/race、独立复核与最终全仓回归通过；不放行 N-01/M1。**

- 延续用户休息期间自主采用推荐方案的授权，新增测试证据，不改生产迁移/驱动或既有 helper。既有 `TestSchema13UpgradeLateFailureAndRetry` 的 RAISE SQL 失败只证明事务错误返回，不是进程中断或容量耗尽；本次区分这些路径。
- task-8 限新 `internal/stabilityrehearsal/migration_interrupt_test.go`：测试子进程独有 scalar 注册及 ledger18 trigger 同步，在迁移 schema 工作完成但事务未提交时通知父进程；真实 Kill/Wait 后检查旧13 schema 与原始业务，移除测试 trigger 后重试18。父 fixture 准备子测试cleanup后不保留跨kill连接；不能以 sleep 猜测进度，不声明脏页已spill或真实断电恢复。新测试已稳定，专项重复3次1.617s PASS；trigger还在事务内检查17 ledger及17/18新列存在。Lead已完整阅读；专项race 3.903s、包vet/新文件空白检查通过，task-8已完成且全部job已收集。容量测试与最终全量证据亦已收集，见下。
- task-9 限新 `internal/stabilityrehearsal/migration_capacity_test.go`：子进程独有连接hook设置 `max_page_count=page_count=60`，实证迁移14由真实 SQLite 引擎返回 `*sqlite.Error Code()==13`，原始错误为 `schema migration 14: database or disk is full (13)`；父进程在任何Store.Open前比较完整schema/ledger与所列11历史表完整行，检查完整性/FK，再无hook重试18并核对认证/授权/播放历史/新列默认值。专项重复3次1.464s、race 4.185s及包vet已通过，Lead已完整阅读；最后独立核对新文件 no-index 空白检查无诊断（exit1仅表示新增差异）；task-9已完成、成员全部job已收集。首轮曾误要求错误文本包含符号字面量 `SQLITE_FULL`，实际驱动只输出通用错误与码13，已改以权威类型/错误码及迁移来源断言，不改生产。页容量限制不是 OS ENOSPC、配额/挂载或掉电证明，不真实填满宿主磁盘。
- 最终 Lead 作业 `bash-9041b42872fb42df` exit0：`go build ./...`、`go vet ./...`、显式旧artifact env 的全仓 Go 与差异检查通过（cmd 6.369s / stabilityrehearsal 5.157s / server 97.374s / store 57.900s）。两项共享任务均完成 revision3，Lead完整阅读并收集成员与全部后台结果。无 UI 改动，本轮未重新运行前端；不以默认skip充当旧16跨版覆盖。仅证明上述固定 fixture 的真实 SIGKILL 与引擎页限失败回滚/重试，不覆盖 OS ENOSPC、断电、WAL spill/耐久性或历史空凭据转换分支，不要求DB/WAL字节零变化。当前不涉及生产库、安装、服务重启或提交。

## 43. N-01 NFO / STRM 普通文件打开边界（2026-11-05）

**状态：普通文件打开边界本地子项完成；共享入口/NFO/扫描重复与 race、最终 build/vet/全仓 Go 均通过，全部任务及作业已收集。仍不放行 N-01/M1。**

- 复核实际调用链：FIFO 可从 Walk/单片 CD 分组进入 STRM/NFO 读取，也可从直接 NFO 编辑持有 App 门闩/路径锁后进入；无 writer 的 `os.Open` 在既有字节边界生效前阻塞，提交前复验时替换/新增 FIFO 同样真实可触达。不是为理论边界加锁；沿用休息期间自主采用推荐方案授权收口普通文档入口。
- Lead 新增 `internal/regularfile/open.go` 与 Unix/非 Unix flag 文件，两实际调用方为 `nfo.ReadFile` 和 `scanner.readSourceFile`。预 Stat 跟随目标拒静态非普通对象，Unix O_NONBLOCK OpenFile 后 f.Stat 拒替换后的非普通句柄，所有打开后失败分支关闭 fd；类型错误统一 `ErrNotRegular`，原始 ENOENT/I/O 错误保留。保留 max+1 字节上限、普通文件链接含库外目标和 NFO 原路径 rename；不加 SameFile、路径沙箱、新锁/重试/依赖或 IO goroutine。
- Lead 共享入口重复5次 PASS 0.022s（每次实际 FIFO 子进程无 writer，另外执行真实非阻塞 flag 分支；静态 `/dev/null` 与 FIFO 链接拒绝），既有 NFO 大小边界专项 PASS 0.559s。父套件的 helper-only skip 不是覆盖，真正子进程每次均已执行。尚未模拟真实 Stat→Open 路径替换竞态或设备 driver 行为。
- d2-nfo 仅新增 `internal/nfo/special_file_test.go` / `special_file_unix_test.go`，10 API × direct/link FIFO = 20 隔离子进程，5s watchdog + Wait；含有效 translation snapshot 后换 FIFO、零 ownership/changed 误报、原 inode/mtime/link target 与目录条目不变。普通文件链接四种改写验证原路径替换链接而目标字节不变。重复3次0.108s、race1.225s、NFO vet/限定diff PASS，全部job已收集；Windows/Darwin amd64 仅编译，不作特殊文件运行证明。Lead完整阅读两文件。
- task-10 由 d2-store 独占扫描生产分类与新 special-file 测试；范围只扩展既有超限跳过分支，不把非普通 NFO 误当坏 XML 覆盖原行，也不提前删除原非 HTTP 行。完整/增量/单片、CD 依赖及复验窗口已接通类型传播；新测试冻结后开始全仓回归。Lead 完整阅读 314 行新测试：36 预存 FIFO 场景（4入口及 full/inc 健康组继续 × 6依赖）、6 替换窗口、缺 NFO 直接复验及4普通链接路径；显式 seed ownership、比较 Movie/演员/基线/版本、保留 orphan 且健康组继续。未显式 seed 用户播放数据，不能将源码保留推论冒称该项 fixture 证据。首轮全仓发现旧测试 `TestIncrementalNFOSelectionSwitchesAndReadFailure` 将目录当 I/O 失败，现正确归类为非普通跳过而使旧断言失败；已由原成员最小对齐并重命名为 `TestIncrementalNFOSelectionSwitchesAndDirectorySkip`；旧 growth 目录 fixture 也改名并精确断言 ErrNotRegular，不改生产语义、不再冒称真实 I/O 失败证据。共享入口 race 重复3次4.040s、vet、Win/Darwin仅编译已 PASS；扫描冻结后同范围重复3次32.149s、race 178.813s、scanner vet/限定空白检查均 PASS，task-10 revision3完成，成员全部job已收集。
- 修正旧目录 fixture 后，最终 Lead 作业 `bash-e66a9c3c99e644b0` exit0：`go build ./...`、`go vet ./...`、显式旧 artifact env 的全仓 Go 与差异检查均 PASS（cmd 9.897s / scanner 24.297s / nfo 9.200s / server 122.287s / store 75.027s / stabilityrehearsal 5.673s）。冻结后的扫描重复/race/vet 最终报告亦已收集，未遗留运行任务。不把首轮 FAIL 或父 helper-only skip 当作成功覆盖。无 UI 改动，本轮未重跑前端。
- 剩余边界：非 Unix Open 替换竞态不具备 Unix FIFO 保证；Stat/Open/普通文件读取本身仍可能受挂载、NFS/FUSE 或 OS 不可中断 I/O 影响。此变更不是一致性快照、恶意目录/设备攻击沙箱、根标记/图片/备份全覆盖或硬关闭时限，也不证明 ffprobe 出站/重定向/嵌套资源政策与正式发布门禁。无生产操作/提交。

## 44. N-01 发布与完整 CI 的同 SHA 绑定（2026-11-05）

**状态：同 SHA 工作流本地子项完成；专项、独立复核、build/vet/全仓 Go 与分片脚本回归均通过，作业已全部收集；不等于远端发布或 N-01/M1 放行。**

- 复核缺口：此前 release.yml 独立接受 v* 标签，虽已有 HEAD 版本输出与校验和，但没有完整 CI 成功依赖。按原生设计 §3.4 同 SHA 要求，本轮使用本地 reusable workflow 而非查询历史绿色状态或增加轮询/重试。
- ci.yml 保留 main/PR 触发并增加 workflow_call；原四类 Go/覆盖率、race、漏洞、前端任务不删减。release.verify 显式只读调用同提交 ci.yml，build 的 needs:verify 保持默认成功传播；失败/取消/未成功不进入构建上传。四处 CI checkout 与一处发布 checkout 均显式 github.sha；PR 仍检查 merge SHA，标签使用事件 SHA。发布构建继续取 HEAD、标签版本、UTC 时间，五平台同批产物、本机版本实测与 SHA256SUMS 生成/复核后才唯一上传。
- 新增 internal/stabilityrehearsal/release_gate_test.go：真实 YAML 解析验证 workflow_call、本地 uses、权限、needs、完整四类 job、固定 SHA、不忽略错误与上传前检查顺序。专项与既有 NativeDeployment 静态契约重复5次0.015s PASS。不是 GitHub-hosted 执行、产物溯源/签名或所有平台原生运行证据，也未执行标签推送/发布。
- d2-integrate 独立只读复核三文件，未发现具体阻断；确认 caller/callee 权限不提升、上下文 SHA 继承和 concurrency 无相互取消。没有编辑/测试/外网，不能把覆盖数当验证质量。Lead 最终作业 bash-21b2a1d5db2c4c13 已收集 exit0：build/vet/全仓 Go（显式旧 artifact env）PASS，cmd 7.178s / server 98.382s / store 58.194s / stabilityrehearsal 4.814s；既有 Python 分片脚本14项 PASS，差异检查通过。所有本轮后台作业已收集。
- 并行 d2-nfo 对 ffprobe 出站只读复核中途退出，无最终报告。中间回报本机 5.1.9 的内存回环 fixture：现有参数发生 302 后访问媒体，HLS 即使解析失败也先访问 segment；加 http/https/tcp/tls 白名单仍会访问。pipe-only WAV 可解析、该 HLS HTTP 访问为0只是局部观察，不是所有 demuxer 或直链兼容证明。既有普通播放 AllowPrivate:true 是兼容事实，不能移作 probe 放行政策。此轮未落地网络机制或宣称零触达验收；后续须把最终连接、嵌套资源、输出与取消一并收口，不能只补 URL 预检。
- 无业务源码/UI 修改，不重跑前端来冒称 CI 实际全绿；当前改动仅工作流、静态契约、README 与施工文档。挂载耗时、实际出站隔离、真实系统安装/平台与正式产物门禁仍未全部验收。未提交/部署/启动生产服务。

## 45. N-01 ffprobe 实际输入机制与兼容性证据（2026-11-05）

**状态：本轮候选机制实证与回归子项完成；最终18项矩阵重复、probe全包race、build/vet/全仓Go均通过，结果全部收集。生产Run尚未改造，实际出站门禁继续施工，不放行N-01/M1。**

- 重新核对实际调用：仅 server/probe.go 的单片和批量执行调用 probe.Run，调度复用批量路径；当前 Run 仍把 URL 交给 ffprobe。共享 outbound 默认读取环境代理，未来专用传输必须显式禁代理并复用直连的逐跳地址检查、实际拨号解析校验与按已验证 IP 连接，不能只做 URL 预检。普通播放 AllowPrivate 兼容不代表探测放行私网。
- 新增 internal/probe/pipe_input_boundary_test.go：显式 EMBY_GO_TEST_FFPROBE 才运行真实二进制，缺 env 为明确 skip。复现命令：`EMBY_GO_TEST_FFPROBE=/usr/bin/ffprobe go test ./internal/probe -run '^TestFFprobePipeInputBoundary$' -count=3`。纯 Go 构造有限 PCM WAV，不新增 ffmpeg 依赖；真实 stdin+pipe 白名单下验证音频 codec/采样率与 filename=pipe:0。HLS HTTP 分片、302、AES key 和 concat 分别测试 pipe-only 与补充格式白名单：均非成功退出，受保护 HTTP 监听器请求计数逐项及最终均0；不是通过“API 出错”推断未访问。格式白名单测试还核对实际拒绝诊断。
- Lead 本机 /usr/bin/ffprobe 5.1.9 首次9子项 PASS 1.055s，重复5次4.249s、race1.804s、probe vet/差异检查 PASS，作业均已收集。测试只覆盖有限候选 stdin 执行，不经过生产 Run/Go下载/HTTP管理API，不证明公网直链→私网302、DNS切换、无限输入或取消时传输收尾；不把成功的候选测试宣传成生产隔离。
- 独立只读本机实验确认 plain pipe 不能直接替换直链：同一552346字节、3秒320×180 MP4，faststart完整及64KiB前缀可给出尺寸；尾moov完整输入可能 exit0/duration3 却 width=height=0 并报 partial file，前缀则 moov atom not found。WAV完整/80字节前缀均可能 exit0而无时长/大小，TS亦可缺format.duration。正常文件路径有正确尺寸，5.1.9不支持fd协议，把文件fd作为stdin仍不能修复pipe非seek特性。不能以exit0或有stream判信息完整，更不能伪造缺失字段。
- 同一实验验证 file 嵌套边界：pipe-only 下显式 file: ffconcat 被协议拒绝；放宽 pipe,file 后能读本地文件，因此不为“兼容本地输入”顺手增加 file 权限。HLS/concat格式白名单是补充拒绝策略，不是网络或文件沙箱。独立复核指出 concat 默认safe=1会在协议门前提前拒绝，Lead 已加safe=0并要求pipe-only实际出现http协议白名单诊断；不再只按任意失败计数。
- cache:pipe 实证已收口：协议限定cache,pipe，read_ahead_limit=67108864；完整尾/faststart MP4（各552346B）及TS（622844B）返回尺寸320×180、时长3s、实际size/总码率，约127–174ms；截断尾MP4仍失败，HLS拒绝且监听器0请求。成功时可带seekback -38诊断，不能把非空stderr等同失败。暂停131072B输入后观察到ffprobe持有已unlink的/tmp/ffcache磁盘文件，缓存随输入增长；read_ahead_limit只是向前seek预读限制，不是总输入/磁盘限额。
- 下一实施候选明确为专用Go直连HTTP→有界stdin→cache:pipe:0，不采用plainpipe、file放权或失败后直URL回退。64MiB总输入上限是待验证的推荐预算，不由read_ahead_limit自动提供；需在2个全链路名额内把排队、HTTP、复制、ffprobe纳入同60s预算，超限/读失败必须取消并独立失败，不能截断为EOF再保存不完整metadata。需关闭body/stdin并join复制与进程，覆盖磁盘满、DNS/302/私网/环境代理0触达、元数据完整性与凭据脱敏；完整抓取语义会拒绝所有大于64MiB的对象（包括faststart MP4，不限于远尾moov），直播与嵌套播放列表亦不得静默降级。此时仅选定后续验证方向，尚未宣称生产实现完成或64MiB以内所有媒体受支持。后续兼容性复核及有限组件见§46。
- 初版只覆盖plainpipe的全仓作业bash-1aef73d18c314565已收集exit0：build/vet/Go（显式旧artifact及真实ffprobe env）PASS，probe2.526s/server94.230s/store55.143s/stabilityrehearsal4.661s；此证据先于后来缓存/诊断强化，不当作最终矩阵验证。强化后首次重复FAIL发现HLS的无扩展名/redirect先被allowed_segment_extensions拒绝，已仅把fixture改为/redirect.wav，不放宽该独立门；concat同样通过safe=0避免假阳性。最终矩阵含pipe与cache-pipe各9项、缓存WAV真实时长/大小，并要求协议/格式拒绝诊断。最终作业bash-474075013a5949bf已收集exit0：18项矩阵重复3次3.755s、probe全包race4.866s，build/vet/全仓Go均PASS（显式旧artifact及真实ffprobe env；probe3.120s/server97.286s/store56.726s/stabilityrehearsal5.192s/cmd7.207s/scanner18.653s），差异检查PASS。
- 本轮只增加测试和进度文档，无业务/UI改动，不重跑前端来替代新的网络证据；独立成员全部实验作业已收集，无提交/部署/生产服务操作。

## 46. N-01 Go有限HTTP响应组件（2026-11-05）

**状态：独立组件及本轮复核完成，尚未接入生产Run；强化后专项重复、probe全包race、build/vet/全仓Go均通过，作业全部收集。不放行N-01/M1。**

- 新增[有限输入组件](<internal/probe/input.go>)，专用固定outbound client显式禁用环境代理，不继承播放的私网或主机放行；GET使用identity编码请求，实际连接与逐跳策略复用既有outbound。请求/传输/读取错误仅返回固定诊断或标准context错误，不泄露源URL、凭据、Location与任意远端错误。该组件不自建重试、不创建临时文件。
- 仅接受无Content-Range字段的HTTP200，已知长度大于64MiB在读取前拒绝，实际读取最多64MiB+1；恰好上限允许，超限/读取错误/已知长度不匹配/观察到取消都返回nil，不给下游可消费前缀。独立复核发现Header.Get会漏空值与首空的重复头，已按字段存在性修正并补真实HTTP回归。net/http负责wire头键规范化。64MiB是内容预算，不是RSS上限；响应framing完整不代表恶意源提供的媒体语义完整，不能用Content-Length验证整部影片。
- 新增[输入边界测试](<internal/probe/input_test.go>)：已知/未知长度恰好上限、已知过长0读取、实际超限最多max+1、伪造Response长度及读取错误无prefix；真实HTTP完整/chunked响应、206、200带普通/空/重复Content-Range、短Content-Length、502与取消。真实body测试使用普通本地client，仅证明framing，不外推固定公网client可访问公网或全媒体完整性；取消发生在readInput前，不冒称在途阻塞读取收尾。
- 私网/IPv6/元数据地址/file/非法URL与凭据canary请求均失败且不泄漏，受保护监听器和代理请求均0。按独立复核意见，代理差异测试放入全新测试进程，避免ProxyFromEnvironment进程缓存及-count干扰；相同环境下普通outbound访问非loopback的upstream.invalid会命中本地代理1次并返回fixture正文，固定probe client仍为0。此对照证明所测环境代理差异，不是公网DNS切换/302/HTTPS隔离验收。
- 独立只读task-11及后续复核完成，无源码编辑、命令或网络。明确兼容限制：全文下载预算拒绝所有大于64MiB对象，包括faststart影片；1GiB/4GiB分别为预算16/64倍。不能靠Range前缀伪装完整对象，也不自动回退直URL。保留生产Run现状是阶段隔离，不表示现有URL子进程已安全。
- 首版作业bash-03197e0f154f4d3a已收集exit0：专项重复5次1.374s、probe全包race21.498s、build/vet/全仓Go通过（真实ffprobe与旧artifact env；probe3.521s/server99.441s/store58.703s/stabilityrehearsal5.204s）。该证据早于头字段/代理测试强化，不能当最终版本验证。强化后专项重复3次1.100s通过；最终作业bash-94e6ddf0834b4a64已收集exit0：专项重复5次1.735s、probe全包race23.341s、build/vet/全仓Go均通过（显式真实ffprobe与旧artifact env；probe3.416s/server98.506s/store57.061s/stabilityrehearsal4.790s/scanner20.090s），差异检查通过。本轮所有相关作业已收集。
- 接入门槛仍包括：大媒体/Range/直播/播放列表支持政策与明确unsupported分类、合法元数据完整性、名额在HTTP前取得且全链路同60s、受校验连接/逐跳302/DNS钉IP实证、body/stdin关闭及join、磁盘缓存失败/取消后无残留和失败零metadata写入。当前无生产调用、无完整作业预算及共享2名额保证，不把组件测试冒称生产API安全或N-01整体完成。无UI改动，本轮不重跑前端；无提交/部署/生产迁移。

## 47. N-01 输入生命周期与最终DNS拨号复验（2026-11-05）

**状态：本轮资源生命周期与DNS拨号验证子项完成，仅补测试、不改生产代码；专项、双包race、build/vet/全仓Go均通过，所有作业已收集。不放行N-01/M1。**

- 新增[响应体生命周期测试](<internal/probe/input_lifecycle_test.go#L60>)，通过实际本地HTTP响应观察downloadInput：成功、502、206、空Content-Range、已知超长、短Content-Length、重定向回调拒绝、收到prefix后主动取消、收到prefix后截止时间共9条路径。逐项断言body.Close恰好一次、失败数据nil、identity请求头；状态/部分响应/已知超长早拒绝未经过body.Read。成功/截断/取消/超时收到6字节prefix，重定向失败时net/http先收尾读取该短响应再关闭，固定请求错误不透出Location或回调canary。
- 在取消/超时场景，服务端发出prefix后保持请求未结束，body观察器在下一Read调用点通知测试；等待取消或真实deadline结束，断言标准context错误、无失败prefix，且被挂起的本地服务handler因客户端结束而释放。不把这扩大为内核读状态观测、强制close任意自定义reader或NFS有界I/O保证。
- 为观察真实body，本测试在串行子项中临时替换固定client；全包无t.Parallel（完整搜索确认），worker通过done通道join后才恢复client，race验证通过。该fixture绕过公网profile只测资源生命周期，不外推生产client的公网/TLS/重定向安全。已有§46固定profile负例及代理差异对照保持不变。未为测试在生产代码增加hook、接口、锁或重试。
- 新增[最终拨号DNS测试](<internal/outbound/dial_boundary_test.go#L25>)：独立子进程内用Go resolver把DNS全部送本地UDP fixture，首次A=9.9.9.9使实际policy.validateURL通过，第二次A变127.0.0.1/10.0.0.1/169.254.169.254时实际policy.dialContext拒绝，socket Control调用为0。全公网第二次返回1.1.1.1与8.8.8.8，两枚字面IPv4各进入Control；A查询数严格为2。若把hostname再次交给底层拨号，第三次DNS会返回私网并使断言失败，避免仅测纯分类函数冒称钉IP。
- Control统一在connect前返回sentinel，所有DNS只发本地UDP，没有公网连接、OS网络/DNS修改或依赖变更；复用已存在间接依赖dnsmessage。该证据仅到当前IPv4 socket-Control边界，不是HTTP/HTTPS/真实公网连接/逐跳302/IPv6或whole-probe验收。
- durable task-12完成revision3，成员仅写DNS新文件。Lead读完205行并复核机制，未发现具体阻断；成员作业均已收集：专项四子例0.017s，重复20次0.419s、race重复5次21.279s、outbound全包0.025s及vet/限定差异检查通过。Lead生命周期首版8路径重复3次3.468s，最终9路径重复5次5.777s及相关输入race7.624s通过。首版不冒充最终九路径证据。
- 统一最终作业bash-a945059428304ea3已收集exit0：真实ffprobe env双包race通过（probe11.888s/outbound5.223s），build/vet及带真实ffprobe、旧artifact env的全仓Go通过（probe4.452s/outbound0.016s/server97.059s/store57.771s/stabilityrehearsal4.431s/scanner17.507s），差异检查通过；所有本轮相关作业已收集。无UI改动，不重跑前端来替代新网络证据。全链路2名额/60s、GB级兼容支持政策、媒体metadata完整性、stdin及磁盘缓存收尾、失败零写入与真实公网/302仍是后续接入门槛；生产Run本轮不改。无提交、部署、生产迁移。

## 48. D-2 真实应用归属诊断浏览器复验（2026-11-05）

**状态：本子项完成；真实浏览器四场景、重复/race、build/vet/全仓Go及前端300项均通过。补 §37 的专项缺口，并局部修复发现的移动端长标题布局，不放行 MT-04 整批或 N-01/M1。**

- Lead 新增真实 Go App/SQLite/HTTP 夹具 `scrape_auto_marks_browser_test.go`，durable task-13 成员仅写配套 `scrape_auto_marks_browser.cjs`。390/1440 两种宽度，各含归属失败和归属+索引+图片失败两种场景。真实登录、搜索/inspect、确认与结果页均走生产代码；只有本地 MetaTube 与 SQLite 触发器是假数据/故障来源，不替换 API、fetch、页面函数或 DOM。
- Go 通过透传 ResponseWriter 被动观察实际确认 status/body/count，浏览器停留在已保存结果面板时用 stdin 确认屏障核对实际 NFO/DB：NFO 已保存新标题/标签，归属仍空且持久记录人工核对诊断；索引失败时保留旧标题，正常刷新时索引新标题。错误不得透出 SQL canary/临时路径；关闭结果后媒体文件和 Movie 快照不得再变、确认不得重发。
- 浏览器检查独立归属/索引/图片提示、持久 panel、无重新确认控件、aria-live/status、完成按钮焦点与键盘完成；上游标题含 HTML canary，检查真实 DOM 不执行注入。复用已存在 Chromium/CDP helper，agent-browser 不可用，不安装软件或启动替代 GUI 服务。属于真实 Chromium 本地 fixture 证据，不是远端 MetaTube、其它浏览器或实际辅助阅读器验证。
- 首轮发现两类失败，均保留原断言后定位：390px 的不可信长标题使 modal.right=398.109375，超出视口；仅补 `.scrape-modal` 的 min-width:0 与结果 status 区 overflow-wrap:anywhere，保留完整文字不做裁切。桌面 Enter 不关闭是 CDP keyDown 缺少 text/unmodifiedText 回车，不是生产键盘问题；只修测试输入，桌面两项7.278s通过。随后强化内部横溢断言，四项真实 App 复验10.625s全部通过：390px modal左右12/378，body与status的scrollWidth均等于clientWidth，桌面200/1240；屏障处实际NFO/DB部分提交与关闭后不重试均通过。
- 失败的脚本成员未自动重试，由Lead接手现有草稿与task-13。复核并修正观测通道只发布第一次确认、持续计数所有请求，避免错误重试填满通道导致handler关闭挂住；快照提前Fatal也取消并Wait Node。此处不声称强制杀Node后的Chromium进程组回收已验证。最终作业bash-69d99705287b41af已收集exit0：四场景重复3次27.982s、归属/选图相关race52.177s、build/vet通过；带CHROME_BIN与真实ffprobe/旧artifact的全仓Go通过（server112.127s/store58.462s/stabilityrehearsal4.591s），前端300/300、0skip通过43.447s，差异检查通过。task-13已由Lead完成，所有本轮相关作业已收集。省略CHROME_BIN时明确Skip，不算浏览器覆盖。整体仍有探测生产入口/大媒体兼容与其余方案待办，不把本子项变成整体已完成。

## 49. N-01 HTTP/TLS 逐跳边界与大媒体接入决策（2026-11-05）

**状态：本轮逐跳边界测试强化与本地回归完成；生产 Run 接入仍未完成，大媒体支持政策待确认，整体目标保持 active，不放行 N-01/M1。**

- 基于当前工作区已有的 [HTTP/TLS 边界测试](<internal/outbound/http_boundary_test.go>)，补 HTTPS 受保护目标、来源内 `/hop` 相对307后再跳受限目标；HTTP/HTTPS 两种来源 × 六种目标 × 直接/第二跳共24条拒绝路径。目标包含受监听的回环HTTP/TLS、私网、元数据地址、IPv6与file；真实受保护服务的 ConnState 新连接计数与应用请求计数均为0，错误必须明确为地址阻断或协议拒绝，来源计数严格1/2，不把证书拒绝冒充策略成功。保留来源正常相对跳转、循环跳数上限与证书链/主机名检查。私网/元数据/IPv6目标没有各自实际监听器，不能把本地两个监听器的零连接外推为它们的逐个抓包证据。
- 测试仍只为来源 `127.0.0.1` 添加 fixture 例外，并在 HTTPS 来源信任测试证书；没有改生产 profile、系统证书或 DNS，不连接公网，不改变 Run。它证明真实本地 HTTP/TLS 下逐跳策略行为，不等于生产 probe 的公网→私网302、全部容器、完整任务资源或安全接入验收。
- `go test ./internal/outbound -run 'TestDirect(HTTPRedirect|TLSCertificate)Boundary' -count=10` PASS 0.266s；双包race PASS（outbound6.016s/probe9.968s）；build/vet及全仓Go PASS（server94.750s/store54.981s/probe4.469s/stabilityrehearsal5.161s），差异检查PASS。后台作业 `bash-243f8ac3ea6443d5` 已收集exit0。全仓显式设置 `EMBY_GO_TEST_FFPROBE=/usr/bin/ffprobe`，真实候选测试执行；旧版测试读取的是 `EMBY_GO_PREVIOUS_BINARY`，本次误设成 `EMBY_GO_TEST_PREVIOUS_BINARY`，未产生旧版覆盖，且历史临时路径的可执行检查未成功。本轮不重建旧版、不计其skip为升级证据；未设CHROME_BIN，本轮无UI改动，不重跑前端/浏览器，不把其条件skip算新证据。
- 独立只读接入复核已收到：`Run` 仍直接传target；现有 `execute` 只在进程启动前取2名额。未来下载若放其前会绕过并发预算，外层/内层重复取同通道可死锁，须统一全链路入场与60s预算。全文64MiB不是采样预算，1GiB/4GiB是其16/64倍；faststart也被拒，HTTP framing完整不证明媒体语义完整。既有解析成功条件不能代替尺寸/时长/大小兼容验证。复核不修改代码、不运行测试；后续尝试向已结束子代理发送复核消息未成功，本轮强化测试没有独立二次审查结论，不伪造该覆盖。

### D-6 大媒体探测政策（历史备选；2026-11-05用户取消，已由§50取代，不再待确认）

| 选项 | 范围与代价 |
|---|---|
| A · 接受有限全文输入 | 生产探测只支持完整对象≤64MiB，超限显式失败；所有GB级影片无法探测。浏览、PlaybackInfo与302播放必须保持，并补失败零写入验证。仍需落地共享2名额/60s、受限cache:pipe、磁盘失败/取消/清理，不因选A自动完成安全接入。 |
| B · 保留大媒体能力，先设计受控随机读取（建议） | 保留GB级合法直链探测目标；先验证Go受控seek/range或专用代理机制，冻结实际HTTP/重定向/DNS/嵌套访问及网络/内存/磁盘总预算，再接Run。施工量更大，不保证任意远端Range行为可兼容；不自动批准新服务或监听入口。 |

批准前不将有限下载直接接生产，不提高全文常量假装支持GB、不把前缀/206当完整输入、不开放file或网络协议、不在失败后回退直URL；当前旧Run尚未满足出站门禁，保持现状不是安全放行。候选机制和无法支持类型按原生设计§3.3/§9.1登记确认后继续实施。本轮无提交、部署、生产服务重启或生产迁移。以上是§49结束时的历史状态，后续用户决定见§50。

## 50. STRM 默认信任与探测隔离范围撤销（2026-11-05）

**状态：用户明确取消探测出站隔离，默认信任STRM链接；已同步当前技术方案、M1门禁与目标范围。此子项是需求撤销，不是隔离实现或安全验收完成，整体施工继续。**

- 用户在解释A/B方案后决定不做该功能、默认信任STRM；据此不再要求A/B选择，不做Go下载→stdin/cache、受控Range或探测代理，不接入64MiB全文组件。保留现有可选ffprobe技术信息探测，不解释为删除探测UI/API、禁止GB影片或取消所有出站校验。
- 当前生产[Run](<internal/probe/probe.go#L194-L209>)仍直接向ffprobe传target，符合新政策，无需为此改业务代码；[执行器](<internal/probe/execute.go#L12-L20>)既有60s、stdout4MiB/stderr64KiB、Verify/Run共享2名额、取消与进程组清理保持。媒体库STRM及其重定向/嵌套来源由部署者信任；不承诺私网/回环零触达，恶意或失陷链接可能使用服务器网络权限，不能声称SSRF风险已消除。入口HTTP(S)、文件字节上限、凭据脱敏、其他元数据/翻译客户端策略不变。
- 已同步[主计划当前范围与排期](<dev-plan.md>)、[原生方案§3.3和决策表](<docs/native-roadmap-design.md#33-ffprobe-可信-strm-输入与进程资源>)以及本台账当前门禁行。§45–49及旧评审的探测隔离施工要求是历史，不再作为该项发布阻断。历史研究组件/测试没有生产调用，本轮不继续扩建、不删除既有未提交成果，也不放宽共享outbound策略。
- 本轮仅修订三份文档与持久目标范围，并静态核对生产调用和资源常量；不重跑长测试来冒充新业务修改或隔离验收。文档差异检查结果以本轮实际命令为准。其他文件/挂载支持、原生发布及升级回退余项仍须依原计划验收，未宣布M1或整体完成，无提交/部署/生产操作。

## 51. N-01 备份发布前失败保留与显式重试（2026-11-05）

**状态：本地三阶段失败回归及完整验证完成；不代表实际OS磁盘满、断电、正式安装或整个M1发布矩阵完成。按§50默认信任STRM，不再施工探测出站隔离。**

- 独立只读检查指出既有 `TestRunReleasesLockOnFailure` 使用损坏源DB，实际在 `readLibraries` 阶段失败，尚未到输出锁/暂存目录，因此不能证明持锁后失败清理。既有额外图测试只覆盖另一复制分支，也没有清故障后的显式重试。本切片按原生方案§3.4“备份失败不损坏旧环境”补证据，不修改旧测试或生产实现；只读成员没有跑测试、编辑或提供新测试二次审查，不把建议当验收。
- 新增[失败保留回归](<internal/backup/failure_preservation_test.go>)：仅使用fixture临时目录和既有Before/AfterSnapshot同步点，分别让db.sqlite目标成为目录、nfo/extra-backdrops目录成为普通文件，触发真实VACUUM或目的目录创建失败。没有新增生产注入点、锁、重试或异常兜底；不依赖root可绕过的chmod权限拒绝，也不使用全局磁盘填满。三个场景均先发布两份完整备份，再修改源NFO为新内容，以同秒替换+retain=1触发故障。
- 逐项要求注入恰一次、错误命中具体阶段、Result为空；旧备份整树路径/内容及源DB/NFO/JPEG字节不变，无stage/锁遗留，较早一份没有提前prune。撤销hook后显式重试必须成功，DB通过integrity_check，整树含新NFO/两份NFO/一份真实JPEG，且只有重试成功后才prune更早备份，输出仅余新完整目录。比较重试前保存的旧备份树并更新预期NFO，不以同一路径自比较作证明。全局hook只在串行测试使用并Cleanup复原。
- 首轮测试错误地把 `Pruned` 与绝对路径比较，三场景都在重试结果断言处失败；实际既有契约返回目录名，已将预期改为 `filepath.Base(first.Dir)`，未改生产语义。修正后专项重复10次1.921s通过；最终backup全包race重复3次6.874s、build/vet/全仓Go均PASS（backup1.161s/cmd2.350s/server93.700s/store54.555s/stabilityrehearsal4.356s），作业 `bash-6f09b68f90244d20` 已收集exit0，差异检查通过。本轮未显式启用旧artifact/真实ffprobe/浏览器专项，不把条件skip算新增覆盖；无UI变更，不重跑前端。
- 这是发布前路径创建失败传播与恢复证据，不是实际OS ENOSPC、写到一半、fsync/rename故障、崩溃断电或跨DB/NFO一致性快照证明；发布后同秒替换窗口等其余既有行为未改。整体目标保持active，文件/挂载和原生发布支持矩阵余项继续按原计划，不因本测试放行M1。无提交、部署、生产服务或生产库操作。

## 52. N-01 非特权服务身份原生重启冒烟（2026-11-05）

**状态：Linux amd64本地真实非root子进程冒烟完成，N-01/M1仍未放行；未启动systemd或操作生产服务。**

- [原生重启测试](<cmd/metatube/native_lifecycle_test.go>)保留当前用户分支，并新增service-account分支。两者复用相同真实二进制启动/健康/version/稳定ServerId/SIGTERM/DB持久化断言，不模拟业务Handler。仅抽出已有重启断言供两个实际调用点使用，生产启动代码和部署unit不变。继续默认信任STRM，不重开探测出站隔离。
- 服务分支仅在测试runner为root时运行，以临时可遍历安装根+root拥有的0755二进制+UID/GID65534拥有的0750状态目录及0600配置运行；`syscall.Credential`指定实际用户、主组和补充组，不创建或查询系统账号、不改真实/etc或/var/lib。非root runner明确skip该分支，原current-user冒烟继续执行；不能据此宣称非root runner已覆盖独立服务UID。
- 子进程健康后读取/proc/PID/status，逐个核验真实/有效/保存/文件系统UID及GID均为65534，防止只chown夹具却仍以root运行的伪验收。正常SIGTERM退出后核验配置及DB归属、无other权限，配置确已写入稳定server_id；父进程随后核对并写入真实SQLite状态供第二次启动读取，两次身份均不变、退出码正常且日志包含已退出而非强制退出。
- 首轮真实服务启动与退出正常，但DB以继承runner的022 mask创建为0644，权限断言失败；这是夹具未模拟unit已有UMask0077，未将其冒充生产缺陷。修正为仅服务子进程通过/bin/sh设置umask077后exec绝对路径真实二进制，runner全局mask不变。没有修改生产创建权限或放宽断言。
- 修正后专项重复3次6.170s，两身份每次各启动/退出两次均通过；观察到服务SIGTERM约3.9–7.0ms（仅空载本地实测，不是负载承诺）。工作树二进制SHA256 `73fa5c381fa75e8c2a1b5a882b16552b28e654c9519905fd8467ce7e99c60bb4`，标识native-rehearsal/fixture，不是官方发布产物。cmd/metatube全包race7.083s、build/vet/全仓Go通过（cmd2.258s/server94.056s/store54.589s/backup1.279s/stabilityrehearsal4.480s），作业 `bash-60fdbaa8e6c04434` 已收集exit0，gofmt及差异检查通过；首轮失败作业 `bash-1825367cddf345d3` 亦已收集。不将未显式启用的旧artifact、真实ffprobe、浏览器专项算新增证据，无前端变更。
- 证据范围仅临时本地布局的Linux UID/GID、mask、真实入口与重启；配置位于状态目录，并非完整/etc独立布局。不证明systemd ProtectSystem/ProtectHome/ReadWritePaths、NoNewPrivileges、capability剥离、OS账号创建流程、NAS/NFS/CIFS权限或其他CPU/OS平台；也没有新增路径沙箱、磁盘满或断电保证。正式unit/安装路径/媒体挂载与发布产物矩阵继续待验收，无提交、部署或生产数据操作。

## 53. N-01 独立只读配置父目录的真实服务身份验证（2026-11-05）

**状态：本地Unix布局补验通过；这是§52已知布局缺口的补充，不是systemd沙箱或正式部署验收。**

- [原生重启冒烟](<cmd/metatube/native_lifecycle_test.go>)的公共断言显式区分状态根与配置目录：current-user保持原同目录行为；service-account将配置放入临时独立etc/emby-go目录，DB、日志等仍在单独状态根。没有新增生产配置项或启动包装接口。
- 逐项核对[安装说明](<README.md#L312-L336>)及[unit样例](<deploy/emby-go.service>)：配置父目录由root拥有、组为服务组65534、0750，只通过组授予读取/遍历，不授予服务目录写权限；配置文件服务账号0600，状态目录服务账号0750；继承§52的子进程UMask0077和真实UID/GID核验。仅设置临时夹具，不创建账号或触碰实际/etc、/run、/var/lib。
- 两次真实入口启动均能在无父目录写权限情况下回写已有配置的server_id，并保留身份和数据库持久状态；两次SIGTERM正常退出。最终断言配置父目录的root属主、服务组、0750未改变，目录内只有config.yaml，没有把DB/日志/临时产物错误写入配置目录。现有配置回写直接打开文件，不要求父目录可写；未改成rename式写入，也未放宽目录权限。
- 中间较宽0755配置父目录专项3次7.241s通过，随后按文档收紧root:服务组0750并重验。最终专项3次7.188s、cmd/metatube全包race7.299s、build/vet/全仓Go通过（cmd2.218s/server94.206s/store54.637s/backup1.041s/stabilityrehearsal4.263s），格式/差异检查通过。两个作业 `bash-861c8c8089bd4e65`、`bash-3dc5025bbc3841b5` 已收集exit0；工作树fixture二进制SHA256仍为 `73fa5c381fa75e8c2a1b5a882b16552b28e654c9519905fd8467ce7e99c60bb4`，业务入口源码未变，不是正式release产物。
- §52所述“配置与状态同目录”限制已由本轮本地Unix证据补齐；仍不证明systemd对单个配置文件的可写bind mount、ProtectSystem/ProtectHome/PrivateTmp、NoNewPrivileges/capability剥离，亦不证明真实安装绝对路径、系统账号流程或NAS/NFS/CIFS/失联挂载行为。生产服务、正式发布与N-01/M1放行均未执行，默认信任STRM的既定政策保持不变。

## 54. N-01 私有定容tmpfs真实ENOSPC与备份恢复（2026-11-05）

**状态：本地Linux真实OS空间耗尽子项通过；不是整机填盘、正式挂载介质或N-01/M1发布验收。**

- [新增Linux备份回归](<internal/backup/disk_full_linux_test.go>)必须显式设置 `EMBY_GO_TEST_TMPFS_ENOSPC=1`，并具备创建挂载命名空间/tmpfs的权限；未启用明确skip不计证据，启用后缺权限按失败处理而非静默跳过。父测试通过CLONE_NEWNS启动独立测试子进程，子进程先核验命名空间与父进程不同再设private传播，只在自己创建的临时输出目录挂载1MiB tmpfs，随用例卸载；即使未观察到ENOSPC，填充尝试也只允许2MiB。未改变宿主挂载传播、生产磁盘容量、系统账号或任何服务。
- 三个串行场景分别在既有快照前/快照后同步点实际write填满临时卷，并用 `errors.Is(err, syscall.ENOSPC)`确认内核错误，不替换驱动或伪造错误。快照失败实际为 `backup: snapshot: database or disk is full (13)`；NFO/额外图复制错误均为 `no space left on device`且保留可解包ENOSPC。未添加生产hook、重试、锁、配置项或修改备份实现。
- 每次先发布两个完整备份，在最新备份同秒、retain=1条件下触发失败，逐目录/字节核对旧备份、源SQLite和媒体不变、stage/锁无残留且失败Result为空。释放仅本fixture的填充文件后显式重试，检查快照integrity、新图片字节、完整树和计数，同秒替换成功后才清理较早备份。源图片只作规范路径的字节复制fixture，不宣称图片解码测试。
- [原路径冲突回归](<internal/backup/failure_preservation_test.go#L137-L162>)中的目录/字节快照断言提取为两组真实调用方共用，原三场景继续执行。首轮两组通过0.293s；专项重复10次2.831s、backup全包race重复3次11.068s通过。最终build/vet及显式启用真实ENOSPC的全仓Go通过（backup1.217s/cmd2.688s/server96.047s/store55.902s/stabilityrehearsal4.575s），作业 `bash-664cb4167bae42f5` 已收集exit0；真实ffprobe、旧artifact及浏览器专项未显式启用，不挪用历史证据。Node基础门禁300/300、0skip通过41.729s，作业 `bash-e9669d53ec7d4721` 已收集exit0；没有前端源码改动。
- §51仅路径冲突、§42仅SQLite页限的证据限制不作历史篡改：本轮补的是实际tmpfs文件写入ENOSPC与备份发布前失败恢复，尚未覆盖迁移事务中真实OS容量失败、publish/rename/fsync失败窗口、断电一致性、正式磁盘/NFS/CIFS行为或真实systemd。只读子任务另确认实际二进制SIGTERM与正在运行的探测进程链路仍缺端到端证据，现有空载重启/直接取消测试不能替代；留作后续施工，不把建议写成已验收。可信STRM政策不变，无提交/部署/生产数据操作。

## 55. N-01 活跃后台探测中的真实OS SIGTERM（2026-11-05）

**状态：本地Linux信号到后台探测进程树的端到端回归通过；不是systemd、正式媒体或M1发布验收。**

- [新增原生关停回归](<cmd/metatube/native_shutdown_test.go>)构建真实入口二进制，通过真实HTTP初始化/登录/建媒体库/扫描后启动批量探测，要求202且恰好一个候选；不是直接调用BeginShutdown或取消probe.Run。测试探测器只在版本自检返回成功，实际执行时启动持有继承管道的sleep子进程，并记录自己与子进程PID；等待标记且确认两者仍活着后才发SIGTERM，避免空载关停伪通过。全部配置、数据库、媒体和探测器均为临时fixture，不接公网媒体或操作生产服务。
- 实际服务必须17秒内正常退出（15秒共享预算加测试余量），日志包含“已退出”且不含“强制退出”。在测试defer失败清理之前检查两个PID均已消失或进入Z/X死亡态；每个PID允许退出后1秒观察余量，因此不证明子进程严格早于主进程退出。孤儿僵尸回收归宿主init，不把它计作仍运行。NFO字节、完整影片结构前后不变，媒体目录仅保留原STRM/NFO，无辅助输出；取消相关任务状态/日志允许记录，未要求整个DB字节不变。失败清理只用于不泄漏fixture，不能作为成功清理证据。
- 只读复核发现初版失败清理依赖已解析PID：若在HTTP返回/标记解析前失败，单杀服务进程不能清理已独立分组的probe。已改为测试服务独立Setsid会话，fallback按/proc会话ID发现所有非Z/X成员，SIGKILL后有界等待无活成员（2秒）；不要求PID标记已成功读取，也不把fallback用于通过正常关停断言。新增无标记、实际shell+sleep的清理专项；只改测试夹具，不加生产hook/防护。修正后两个专项重复10次15.705s、cmd/probe全包race18.300s/10.130s、build/vet及显式ENOSPC全仓Go均通过（cmd4.055s/backup1.441s/server93.995s/store54.901s/stabilityrehearsal4.737s）。作业 `bash-e5289bc59ed943cd` 已收集，最终因本次台账追加的EOF空行触发 `git diff --check` exit2，而非Go用例失败；已删除多余空行并独立重跑差异/格式门禁，保留原始日志 `/tmp/emby-go-round10-final-go.log`，不将该作业误记为exit0。
- 首轮专项2.429s，活动关停约5.975ms通过。续轮之前两个作业句柄失效且无完整Go持久日志，不计全量验证；未发现仍运行的旧Go测试或sleep fixture后重新收集：专项重复10次30.593s、cmd/metatube及probe全包race分别34.121s/15.928s、build/vet及显式启用ENOSPC的全仓Go通过（cmd4.562s/backup1.707s/server95.216s/store55.331s/stabilityrehearsal4.657s）。作业 `bash-6b32659764c64bb6` 已收集exit0，持久日志 `/tmp/emby-go-round10-go.log` 含 `VALIDATION_EXIT=0`；fixture SHA256 `d10cc146a3f8bb97712bc00e32e225ddb466728d2206d1c6d17ac0e1c8c783c8` 是工作树构建，不是正式release。
- Node初轮持久日志仅42通过、11取消，诊断为 `Promise resolution is still pending but the event loop has already resolved`，没有fail但不能记为通过；根因未定，不据此修改前端或运行器。未改前端复验300/300、0fail/0cancel/0skip通过58.469s，作业 `bash-f83a51f18b84448d` 已收集exit0，日志 `/tmp/emby-go-round10-node.log` 含 `VALIDATION_EXIT=0`。两份持久日志保留；不把复验通过解释为根因已修复。
- §54登记的真实SIGTERM到活动探测链路缺口现有本地证据补齐；并不证明DB关闭精确时序（仍由直接worker join回归承担）、外部信号后的readiness503可观察窗口或所有新任务入口拒绝（监听器可能已关闭）。本次子进程为受控探测器而非真实ffprobe媒体网络访问，也未显式启用旧版artifact、Go浏览器或真实ffprobe专项。systemd沙箱、正式产物/平台矩阵、迁移真实OS容量失败及媒体挂载等门禁仍保留，N-01/M1不放行；未改生产逻辑，不重开已取消的探测隔离。

## 56. N-01 迁移提交时真实OS ENOSPC与显式重试（2026-11-05）

**状态：本地Linux私有tmpfs上的迁移提交容量故障回归通过；不是正式存储/所有迁移阶段或M1发布验收。**

- [新增迁移磁盘满回归](<internal/stabilityrehearsal/migration_disk_full_linux_test.go>)沿用显式开关 `EMBY_GO_TEST_TMPFS_ENOSPC=1`；默认skip不计容量验收。父进程创建CLONE_NEWNS子进程，核对挂载命名空间不同，先设私有传播，再只在测试目录挂载8MiB tmpfs（nosuid/nodev/noexec）。不填宿主盘、不操作systemd/生产库。正常清理按连接关闭→卸载→临时目录删除；硬超时/被杀由命名空间销毁释放挂载，可能残留空临时目录，不承诺硬杀时Go cleanup已执行。
- 独立schema13 fixture准备连接全部关闭后运行真实store.Open；仅测试库增加ledger18 BEFORE INSERT触发器，先验证同事务内schema17/18列与ledger17已存在，再通过仅子进程注册的函数实际写填充文件直到内核ENOSPC。回调返回成功，不以RAISE/驱动错误注入替代磁盘满，也不设置max_page_count。最终要求SQLite错误code13且错误来源前缀为 `commit schema migration:`，明确证明tx.Commit分支，而不将最后账本INSERT失败混称提交失败；实际为 `commit schema migration: database or disk is full (13)`。
- 失败后释放仅该临时卷的填充文件，先用原始SQL连接比较sqlite_master全部DDL及既有capacitySnapshot列出的11张表全部行，ledger完整恢复13并通过integrity/FK检查，之后才删除测试触发器并显式重试store.Open。重试保留fixture业务数据、ledger完整18条、双用户sessions/events各2及auto_marks默认值。此快照不包含access_tokens/api_keys/login_events等表的历史行，原fixture也未填这些行，不能据此宣称token删除/stable_id回填的历史行回滚已全覆盖；不修改共用快照函数或生产迁移。
- 初轮0.569s通过；专项重复10次5.233s、stabilityrehearsal全包race34.138s、build/vet、显式启用ENOSPC的全仓Go通过（cmd5.826s/backup1.446s/server94.590s/store55.741s/stabilityrehearsal5.025s），作业 `bash-ac4c0772a37a47d7` 已收集exit0，原始日志 `/tmp/emby-go-round11-validation.log`。只读复核未改文件/运行测试，指出初版code13不足以定位Commit；已加严来源断言并最终专项重复10次5.269s通过。全包race/build/vet/全仓Go发生在该最终断言加严前，断言变更后不冒称重新全验。格式/差异检查单独复验。
- Node基础门禁300/300、0fail/0cancel/0skip通过50.341s，作业 `bash-6cb077dc39014369` 已收集exit0，日志 `/tmp/emby-go-round11-node.log`；没有前端源码改动。未显式启用真实ffprobe、旧artifact或Go浏览器专项，不挪用历史证据。
- 本轮补齐§54/§55登记的迁移事务提交真实OS容量失败本地证据，不等同物理磁盘/NFS/CIFS、所有中间迁移阶段、publish/rename/fsync故障或断电一致性。systemd沙箱、正式产物平台矩阵与媒体挂载等仍未放行，N-01/M1继续施工；无生产逻辑变更、提交、部署或生产数据操作，可信STRM政策保持不变。

## 57. N-01 迁移容量失败的认证历史回滚补验（2026-11-05）

**状态：真实OS ENOSPC回归增加非空凭据与登录历史，失败回滚及重试规则本地通过；不改生产认证语义。**

- [迁移磁盘满测试](<internal/stabilityrehearsal/migration_disk_full_linux_test.go>)在独立schema13 fixture中增加合成64位摘要的live user7 token/key、已删除账号700的token/701的key及user7/已删除702的登录行；保留原8MiB私有tmpfs、实际kernel ENOSPC和Commit来源/code13双断言。未改生产hook、事务、认证或密钥处理。已删除账号的key保留、token删除，是既有v14/v15语义，不作同化处理。
- [共用容量快照](<internal/stabilityrehearsal/migration_capacity_test.go#L73-L111>)在原11表之上加入access_tokens/api_keys/login_events全部行，失败后原始SQL完整对比14表与DDL，证明这组非空认证历史没有随失败部分提交。没有将全表快照扩大解释为所有业务表或任意历史库；另一组max_page_count测试仍使用空认证fixture，新增三表快照只验证其空表不被污染。
- 填盘触发器要求live token保留/孤立token已删除、认证新列已出现、key两条、login两条和历史最高用户序列702已写入，从而不会在认证迁移尚未执行时伪通过。初版直接引用新增auth_generation/stable_id字段导致v14 ALTER/DROP/RENAME校验触发器时报 `schema migration 14: SQL logic error: error in trigger stability_fill_v18: no such column: auth_generation (1)`；已改为pragma_table_info字符串列名检查，不变更生产DDL，修正后首轮0.502s通过。失败诊断现保留真实store.Open错误。
- 释放填充文件后先证明原始schema13回滚，才删除fixture触发器并显式重试。要求live token摘要/原字段/generation1不变、总数1（孤立token已删）；两key原摘要/name/prefix/user/time保留，stable_id长度32且两条各自唯一、version1、rotated_at为空；login全行不变、users序列702，并保留原双用户播放数据及完整schema18 ledger断言。
- 两组容量专项重复10次11.963s、stabilityrehearsal全包race38.326s、build/vet及显式启用ENOSPC的全仓Go通过（cmd4.324s/backup1.108s/server94.575s/store55.562s/stabilityrehearsal5.070s），作业 `bash-a654958688a84baf` 已收集exit0，日志 `/tmp/emby-go-round12-validation.log`。Node300/300、0fail/0cancel/0skip通过45.688s，作业 `bash-3781003cb488449d` 已收集exit0，日志 `/tmp/emby-go-round12-node.log`；无前端源码变更。格式/差异检查最终单独复验。
- 独立只读复核未改文件/运行测试，确认断言与既有迁移规则一致，未发现实质缺陷；提醒702来自login且高于token700/key701，不能分别证明用户序列UNION每个来源都覆盖，未实际创建下一账号。stable_id断言不覆盖hex格式、再次成功重开稳定性或实际鉴权/轮转；不将这些说成已完成。未显式启用旧artifact、真实ffprobe或Go浏览器验收。正式存储/断电、systemd/发布平台矩阵与挂载等门禁不变，N-01/M1未放行；无生产操作，可信STRM政策不变。

## 58. N-01 真实systemd沙箱回归（2026-11-05）

**状态：真实manager能力预演与实际应用transient unit双启动/关停、沙箱进程取证本地通过；不据此宣布正式安装样例已整体验收。**

- 本机Debian12 / Linux6.1.0-53-amd64 / x86_64，systemd252.39-1~deb12u2，go1.27.2；PID1为systemd且manager running。只操作唯一命名的 `emby-go-rehearsal-*` 临时单元，无账号创建、现有服务/安装配置修改或生产数据操作。
- 已用systemd-run的 `--wait --pipe --collect` 实际运行无文件写入的 `/bin/sh` 和 `/bin/true`，确认数字UID/GID65534、NoNewPrivs1、UMask0077、NOFILE65536；ProtectSystem=strict下/run实际挂载ro，而宿主/run为rw。ProtectHome/PrivateTmp同时启用，观察/root为ro、/tmp为私有单元中的rw挂载；未用“无/etc Unix写权限”冒充沙箱ro证明。ExecStopPost可以在单元GC前查询到Result=success/ExecMainCode=1/ExecMainStatus=0，供应用测试保存退出证据。初次能力脚本缺少内部set-e，后已改为每项断言均传播失败并重新通过，不以初版echo作为身份断言证据。
- 宿主/run同时带noexec，不能直接在其上执行binary。新增[真实systemd测试](<cmd/metatube/native_systemd_test.go>)将状态/配置放在独立/run目录，binary构建到独立/tmp安装目录，通过BindReadOnlyPaths显式映射给PrivateTmp；未全局remount /run或改正式安装路径。原实现子任务在完成前异常终止，没有留下文件或完成报告，未将其计作实现/复核结果；Lead随后自行实现、检查及验证，无自动重派实现。
- 显式开关EMBY_GO_TEST_SYSTEMD=1；默认skip，启用但root/manager不满足则fail。真实主进程/proc取证要求UID/GID各四项65534及NoNewPrivs1；mountinfo按最长挂载前缀证明fixture/配置父目录/安装目录ro、state及独立配置文件rw，避免把Unix权限错误当作沙箱证据。服务视图/tmp与宿主/tmp不是同一目录、/root被替换成权限000的不可访问目录，证明PrivateTmp/ProtectHome实际作用于应用。配置父目录保持root:65534/0750且仅config.yaml，config/DB均65534:65534/0600。
- 同一工作树binary双次启动，真实HTTP health与PublicInfo就绪且server ID跨单元重启稳定；server ID在父目录ro但文件rw情况下成功写回配置，停服后设置SQLite条目并在第二次停服后核验保留。systemctl stop要求17s预算内返回，ExecStopPost保存Result=success/ExecMainCode=1/ExecMainStatus=0；专项3轮的6次实际停止约34–52ms。fixture SHA256 `73fa5c381fa75e8c2a1b5a882b16552b28e654c9519905fd8467ce7e99c60bb4`，不是发行产物。失败或正常结束均仅停止自己的unit，确认inactive/failed/unknown后才核验绝对路径/父目录/前缀并删除自己的目录；无法确认不活跃则保留现场并报错，不通过清理制造成功。
- 新测试接入前的基线build/vet通过（作业 `bash-f7c6a92724db408d` exit0）；已有真实原生启动/非特权身份/独立配置/活跃探测SIGTERM及fixture清理race通过10.414s（`bash-9620219560be4a52` exit0）。全仓Go显式启用既有ENOSPC通过（`bash-7c176b9e316140c8` exit0，server100.314s/store58.143s/stabilityrehearsal5.173s，日志 `/tmp/emby-go-round13-baseline-go.log`）。这些基线早于新增systemd测试，不冒称其应用路径已执行。
- 增强后应用专项3次5.782s、显式启用systemd的cmd全包race13.612s、build/vet及同时启用systemd/真实ENOSPC的最终全仓Go通过（cmd5.975s/backup1.250s/server95.937s/store55.538s/stabilityrehearsal5.272s），作业 `bash-da6d2475bf1d45b2` 已收集exit0，日志 `/tmp/emby-go-round13-validation.log`。首次基础双启动2.996s通过，随后增加PrivateTmp/ProtectHome实证及配置父目录核验再跑上述完整验证。Node/旧artifact/真实ffprobe/Go浏览器本轮未显式重验，未改前端或生产业务逻辑。追加台账曾触发EOF空行diff检查exit2，已删除多余空行；最终gofmt/diff检查通过，systemctl无该前缀残留unit，/run与/tmp无自身fixture目录。默认未启用时明确skip（0.009s），不将此skip计作实际验收；启用验收证据为上述专项/race/全仓运行。
- [README](<README.md#原生-linux-部署样例>)登记可复现开关与前置条件。样例实际StateDirectory/RuntimeDirectory创建、正式绝对安装路径、Restart策略、活跃worker在systemd下关停、发行产物与平台/挂载矩阵仍需各自验收。临时路径与额外只读binary映射不冒充原样安装；这些门禁不变，N-01/M1保持未放行，可信STRM政策不变。

## 59. N-01 systemd自动目录生命周期（2026-11-05）

**状态：唯一命名StateDirectory/RuntimeDirectory实际创建、权限、应用重启及停服保留/清理本地通过，完整复验通过；不等同正式安装目录迁移验收。**

- [真实systemd测试](<cmd/metatube/native_systemd_test.go>)不再手工mkdir/chown状态目录。用测试根目录随机名称派生唯一目录名，StateDirectory指向/var/lib下自身目录、RuntimeDirectory指向/run下自身目录，Mode均0750。预先Lstat证明两路径不存在，再注册其清理；若遇同名既有路径，仅删除刚创建的空测试根目录，不触碰碰撞路径。没有使用固定emby-go名称或修改现有服务/账号。
- manager实际完成创建/赋权：两目录均65534:65534/0750；真实应用/proc环境含对应STATE_DIRECTORY/RUNTIME_DIRECTORY，mountinfo证明两路径有效rw。ExecStartPre以同一服务身份向运行目录写fixture并核对字节。独立配置父目录ro、文件rw、实际UID/NoNewPrivs、PrivateTmp/ProtectHome和配置持久化等§58断言继续保留。
- 两次启动复用同一StateDirectory，停服后SQLite/server ID/显式持久设置保留；每次systemctl stop后RuntimeDirectory必须已经不存在，下一单元重新由manager创建。ExecStopPost继续把正常退出证据保存到状态目录。测试完成确认自身unit不活跃后，才核验绝对路径、直接父目录和自身前缀并删除自己的持久目录；确认失败则报错保留现场。
- 首轮真实应用双启动2.331s通过，两次正常停止45.640ms/39.054ms，ExecMainCode=1/Status=0/Result=success；作业 `bash-4db6fa46aeaf4470` 已收集exit0。工作树fixture SHA256仍为 `73fa5c381fa75e8c2a1b5a882b16552b28e654c9519905fd8467ce7e99c60bb4`，不是发行产物。后续专项3轮5.408s（6次正常停止约34–42ms）、启用systemd的cmd全包race13.023s、build/vet和同时启用systemd/真实ENOSPC的全仓Go通过（cmd5.906s/backup1.263s/server95.036s/store55.289s/stabilityrehearsal5.445s）；作业 `bash-c2269d33605345a4` 已收集exit0，日志 `/tmp/emby-go-round14-validation.log`。gofmt/diff检查通过，systemctl无自身前缀unit、/run /tmp /var/lib无自身fixture残留；未将此清理检查代替上述运行证据。本轮未显式重验Node、旧artifact、真实ffprobe或浏览器。
- 本节仅补齐§58“自动目录未验”的唯一名称、本机新目录子集，不宣称固定安装目录的既有数据/属主变更、Restart、活跃worker在systemd下关停、正式产物平台矩阵及媒体挂载已验收。[README](<README.md#原生-linux-部署样例>)同步覆盖范围；无生产业务逻辑修改、提交或生产部署，可信STRM政策不变，N-01/M1仍未放行。

## 60. N-01 systemd故障自动重启（2026-11-05）

**状态：真实故障自动重启与直接SIGTERM成功退出回归、重复/race及全仓验证本地通过；仅本机唯一测试单元，不是正式安装放行。**

- [真实systemd测试](<cmd/metatube/native_systemd_test.go>)在自己的唯一单元启用样例Restart=on-failure/RestartSec=3s。第一次启动完成既有真实UID/GID、沙箱、目录与HTTP身份核验后，只向该fixture MainPID发送SIGKILL；要求不同MainPID恢复健康、PublicInfo身份保持、有效策略为on-failure/3s、NRestarts恰为1，随后对新进程再检查沙箱。ExecStopPost必须保留Result=signal/ExecMainCode=2/ExecMainStatus=9，避免仅凭健康请求或换PID冒称故障路径执行。
- 首单元自动重启后正常systemctl stop，第二单元改为直接向应用发送SIGTERM，不创建stop job来提前抑制重启。要求单元最终LoadState=not-found（--collect收集、不再持有自动重启任务）、正常退出success/1/0、运行目录消失，继续检查配置身份、SQLite跨单元设置和权限。无需靠固定sleep猜测子进程启动/单元退出；轮询有截止时间，底层systemctl命令沿用既有独立超时，不宣称整个异常路径硬实时界限。
- 首轮专项5.875s通过，真实SIGKILL后3.221s恢复；原工作树fixture SHA256仍为 `73fa5c381fa75e8c2a1b5a882b16552b28e654c9519905fd8467ce7e99c60bb4`，不是正式发行产物。随后专项3次25.486s通过，三次重启约3.211–3.478s；显式启用systemd的cmd全包race29.114s、build/vet和同时启用systemd/真实ENOSPC的全仓Go通过（cmd9.528s/backup1.227s/server96.291s/store58.852s/stabilityrehearsal5.252s），作业 `bash-22e1478c505f4b54` 已收集exit0，原始日志 `/tmp/emby-go-round16-validation.log`。最终gofmt/diff检查通过；自身前缀unit与/run /tmp /var/lib fixture无残留。Node300/300、0fail/0cancel/0skip通过50.509s，作业 `bash-9a53ae6bb8554887` 已收集exit0，日志 `/tmp/emby-go-round16-node.log`。未显式重验正式旧artifact、真实ffprobe或Go浏览器。
- 只读复核未发现本轮重启/正常退出断言可确认的伪通过缺陷，未修改文件或重跑测试；退出文件覆盖与轮次隔离、SIGKILL正MainPID范围、直接SIGTERM加成功退出文件和unit收集链已核对。局限：没有真实子进程负载；PID检查到发信号间未用pidfd绑定；15s收集期限可能在繁忙manager下假失败；只验证有效RestartUSec=3s并记录耗时，不把调度时延说成硬实时；重启后复核沙箱但未重验环境和runtime marker。既有失败清理接受failed状态，并不能严格证明停止超时/不可杀任务下cgroup为空；本轮无该异常证据，未擅自重写既有清理策略，也不宣称该失败路径已验收。
- 此次只补齐§58/59未验的本机Restart子集，未验证连续崩溃/启动限流、活跃探测任务在systemd下关停、SIGKILL期间并发业务写入或掉电持久性、固定正式目录迁移及发行平台/媒体挂载矩阵。失败清理仍先停止自己的unit并确认不活跃才删除自己的路径；未修改样例服务、生产业务逻辑、现有服务/账号或生产数据。[README](<README.md#原生-linux-部署样例>)同步范围，可信STRM政策不变，N-01/M1仍未放行。

## 61. N-01 systemd活跃探测关停（2026-11-05）

**状态：真实单元的已接纳探测进程树关停、拆分夹具后的重复/race/build/vet/全仓Go与Node验证本地通过；仍不是完整N-01放行。**

- [真实systemd测试](<cmd/metatube/native_systemd_test.go>)复用§58–60的唯一命名目录/单元，增加第三次启动；[活跃探测夹具](<cmd/metatube/native_systemd_probe_test.go>)仅以真实HTTP完成初始化/认证/新增库/扫描/批量探测（202、total=1）。STRM使用fixture.invalid源，脚本只模拟执行且不发网络请求；不是实际ffprobe格式或网络兼容验证，没有重新接入已取消的出站隔离。
- 同一临时只读安装目录放置服务用户可执行的ffprobe脚本，版本自检成功；实际探测分支忽略SIGTERM，再启动继承忽略设置的sleep子进程。停服前从PID marker与/proc检查两进程活着、父子关系与独立probe PGID、与MainPID同cgroup，且两者SigIgn包含Linux TERM位14，避免unit最初向整个cgroup发SIGTERM就使夹具退出、冒充接纳任务取消。此为已知shell/sleep负载，不是失联文件系统或不可杀任务。
- 只在第三轮systemctl stop，保留第一轮真实SIGKILL自动重启与第二轮直接SIGTERM不重启。停服须在17s（应用15s+宽限）预算内返回且ExecStopPost为success/1/0、RuntimeDirectory消失；在任何fallback清理前要求已知探测父子进程不存在或Z/X（死而未收割不等同仍运行），STRM/NFO原始字节、媒体目录仅2项和完整store.Movie行均保持原值。允许任务取消日志/状态等非Movie写入，不宣称全DB零写入或精确DB关闭时序；后者仍依赖已有直接worker/关闭测试。
- 首版专项5.514s通过，活跃轮真实systemctl stop32.584ms，两个已知探测进程当时活着且TERM忽略；工作树fixture SHA256仍为 `73fa5c381fa75e8c2a1b5a882b16552b28e654c9519905fd8467ce7e99c60bb4`，非发行产物。随后将新夹具按准备与停服断言、HTTP任务接纳、进程树证据拆成具名职责函数；最终专项3次22.306s通过，三次活跃轮stop约38.530/63.055/50.359ms。启用systemd的cmd全包race35.491s、build/vet及同时启用systemd/真实ENOSPC的全仓Go通过（cmd10.826s/backup1.348s/server95.869s/store56.104s/stabilityrehearsal5.702s），作业 `bash-dcf1acb858894717` 已收集exit0，日志 `/tmp/emby-go-round17-validation.log`。Node300/300、0fail/0cancel/0skip通过54.690s，作业 `bash-6c546ca3d0d143db` 已收集exit0，日志 `/tmp/emby-go-round17-node.log`。gofmt/diff检查及自身unit、/run /tmp /var/lib fixture无残留检查通过。未显式重验正式旧artifact、真实ffprobe或Go浏览器。
- 只读复核未发现本轮可确认的新伪通过或安全问题，未改文件/运行测试；已核对TERM忽略、HTTP接纳前后顺序、父子/PGID/cgroup、success/1/0及fallback前断言。额外范围限制：直接SIGTERM仍是第二轮空闲负载，活跃任务仅覆盖第三轮systemctl stop；阻塞夹具不覆盖真实ffprobe成功输出临近发布的全部竞争窗口，不保证测试属性与未来打包unit永远同步。
- 本节不证明任意后代逃逸、全部cgroup为空、systemd强杀兜底的单独归因、关闭超时/不可杀任务下的安全清理或真实ffprobe解析；§60既有failed状态清理限制不变，未擅自重写。正式路径既有数据迁移、发行/平台/媒体挂载矩阵仍未放行；无生产业务代码、正式服务/账号变更、提交或部署，可信STRM政策不变，N-01/M1保持进行中。

## 62. N-01 发布配置原生fixture（2026-11-05）

**状态：共享发布配置fixture、开发配置污染回归及三类实际原生场景的重复/race/build/vet/全仓Go验证本地通过；非正式发行artifact，N-01/M1未放行。**

- [共享构建验证](<cmd/metatube/native_release_fixture_test.go>)由[当前用户/非特权重启](<cmd/metatube/native_lifecycle_test.go>)、[OS信号活跃探测](<cmd/metatube/native_shutdown_test.go>)及[真实systemd](<cmd/metatube/native_systemd_test.go>)三个实际调用方复用，移除各自不同的普通构建代码。按[发布workflow](<.github/workflows/release.yml>)本机Linux目标设置子命令CGO_ENABLED=0、GOOS=linux与当前native GOARCH，显式-trimpath及-s -w；不修改父测试进程环境、不改生产工作流或新增构建框架。版本/commit/buildTime固定标记native-rehearsal/fixture/fixture，明确不是标签、clean SHA或真实发布日期。
- 构建后从实际Go buildinfo核验CGO、GOOS/GOARCH、trimpath与exe模式；从ELF检查无PT_INTERP、无导入共享库、无.symtab/.debug_info，并实际执行-version精确比较注入字段。保留Go构建信息与运行时必要元数据，不宣称删除所有符号线索。读取同一文件SHA用于日志及非特权安装副本；现有HTTP/身份/目录/重启/SIGTERM/systemd故障和活跃关停断言均继续执行，没有以构建成功替代运行证据。
- 首版专项失败39.793s（作业 `bash-7d394be3e7f34038` exit1，三项均为 `fixture build setting -ldflags="", want "-s -w -X main.version=native-rehearsal -X main.commit=fixture -X main.buildTime=fixture"`）；trimpath产物未记录该buildinfo键，已删除错误期望，仍通过ELF裁剪与真实-version核验实际效果，不删减构建命令。修正后三类专项7.748s通过，当前用户+服务身份两次重启、活跃探测SIGTERM及真实manager三轮均实际运行；活跃systemd轮stop36.474ms。三份构建SHA均为 `ca4a49545fa6955ec18dd1d774edc1c69bf85d90cbc77727e2b389749e5cee2d`（go1.27.2/linux amd64，非正式产物），修正日志 `/tmp/emby-go-round18-native-fixed.log`。随后重复三类场景39.248s、cmd race19.489s、build/vet及全仓Go通过（作业 `bash-e80dfb9897b84d3d` exit0已收集），未将此早一版结果冒充后续环境归一化后的验证。
- 只读复核确认构建/ELF/version均为实物断言，三处先验证同一个将运行的文件；发现条件性P2：继承GOFLAGS（含go env -w）可混入-gcflags/-tags，额外实验和微架构设置也可能偏离发布默认，原断言不能完整识别。复核时本机GOFLAGS/GOEXPERIMENT为空、GOAMD64=v1，没有证据说上面的运行实际被污染；但可构造的触发路径成立，故只在新helper的子构建环境设置GOENV=off、GOFLAGS/GOEXPERIMENT空值及GOAMD64=v1/GOARM64=v8.0，并从buildinfo检查额外编译选项为空和当前架构默认级别，不改父runner或生产发布工作流。
- 新增主动污染回归：用测试独有GOENV文件和t.Setenv临时注入禁优化、额外tag、非法实验及更高架构级别（测试结束自动还原），仍要求同一个helper成功构建、实际buildinfo与ELF/version符合约束；首次2.407s通过。最终四项专项重复3次24.942s全部执行、服务身份与真实systemd无skip，本机每次fixture SHA仍为上述值；活跃unit stop约39.843/57.104/40.474ms。cmd全包race17.666s、build/vet及同时启用systemd/真实ENOSPC的全仓Go通过（cmd9.509s/backup1.153s/server94.783s/store54.626s/stabilityrehearsal5.039s），作业 `bash-647bf96026934732` 已收集exit0，日志 `/tmp/emby-go-round18-final-validation.log`。race作用于测试包，子应用刻意仍按非race发布profile构建，不宣称子应用含race插桩。
- Node VM300/300、0fail/0cancel/0skip通过50.318s，作业 `bash-357feaa3be614404` 已收集exit0，日志 `/tmp/emby-go-round18-node.log`；此轮无前端修改，Node在最后一次仅Go测试配置修正前执行，不挪作真实浏览器证明。未显式重验正式旧artifact、真实ffprobe或Chromium专项。最终gofmt/diff检查和自身测试unit、/run /tmp /var/lib fixture无残留检查通过。
- 这是本机发布构建profile的运行子集，不是执行GitHub打包/上传、干净提交或官方Release身份认证；不将fixture的相同SHA推导为其它构建环境可复现。未验证其它OS/架构的原生运行、旧官方产物升级/回退、完整固定安装布局、连续崩溃或任意不可中断I/O，未放宽§60–61失败清理与cgroup局限。无生产业务逻辑/现有服务/账号/数据变更、提交或部署，可信STRM政策不变，N-01/M1仍未放行。

## 63. N-01 可信STRM探测错误脱敏与播放契约（2026-11-05–06）

**状态：真实ffprobe凭据回显已复现并最小修复，专项重复/race与最终全仓门禁本地通过；本子项完成，N-01/M1仍未放行。**

- 实际触发链为ffprobe将失败源URL写到stderr，[执行器](<internal/probe/probe.go>)返回首行错误，[单片接口](<internal/server/probe.go#L346>)此前未脱敏便加入failures，再由全失败502或部分成功200回显。批量已有相同日志脱敏机制。修复只在该收集点调用既有logging.SanitizeText，不改探测协议、错误状态或输出提交规则，不新增锁/重试/抽象层；直接URL和既有资源预算不变。
- [真实回归](<internal/server/probe_trusted_strm_test.go>)使用本地HTTP无效媒体及有效MP4，源URL携带独立用户名/密码/query canary；先实际调用ffprobe，要求原始错误含query canary，避免安静stderr造成伪通过。真实认证HTTP覆盖全失败502与主片失败/分段成功200，要求保留Invalid data found诊断、响应和捕获日志均不含三种秘密、部分成功仅一个part结果且有一个失败；失败主片NFO字节、完整Movie记录不变、主片mediainfo不生成。不据此声称成功分段无写入。
- 两种组合均另验缺失ffprobe的500和实际探测失败之后，详情与PlaybackInfo仍200有效JSON，播放器流仍302到完整原始凭据URL。客户端禁止跟随重定向，并检查源HTTP命中数不增加，证明这些浏览/302请求本身未代客户端取源；不是证明ffprobe无出站访问，也未执行真实Emby客户端播放。测试明确允许受信本地回环输入，绝不重新引入已取消的隔离政策。
- [普通门禁回归](<internal/server/probe_limits_test.go>)扩展既有POSIX夹具的error模式，输出带源URL的真实stderr，断言502、可用诊断/主机路径仍在且三种凭据不在；无需安装外部ffprobe。继承既有Windows跳过限制，不冒充Windows运行验收。真实回归需显式EMBY_GO_TEST_FFPROBE；未设置跳过不算验收，指定但不可执行则失败。复演命令与可信输入警告已同步[README](<README.md#L352>)及[API契约](<API-CONTRACT.md#L214>)。
- 修复前真实两个子用例均失败：`probe response or logs exposed probe-user-canary`，作业 `bash-f9e27d0729b84059` exit1已收集，日志 `/tmp/emby-go-round19-before.log`；修复后首版专项0.995s通过（后续才补缺失二进制/逐文件断言，未挪用首版结果覆盖最终版本）。只读子任务只返回中途发现、随后执行失败，无最终审查报告，不计完整独立评审。
- 最终专项重复10次19.503s、四条探测关键路径race26.315s、go build/vet及同时启用真实ffprobe/systemd/真实OS容量故障的全仓Go通过（cmd13.800s/backup1.308s/server97.112s/store55.562s/stabilityrehearsal5.231s），作业 `bash-aa2a12718c7c4022` exit0已收集，日志 `/tmp/emby-go-round20-validation.log`。前端Node300/300、0fail/0cancel/0skip通过51.545s，作业 `bash-e9193a615d824cab` exit0已收集，日志 `/tmp/emby-go-round20-node.log`；包含既有Chromium用例但未新增本场景完整GUI/远端媒体/真实客户端专项。两作业结束后仅同步文档，不把临时日志视为持久发布材料。
- 没有正式旧artifact、跨平台或生产媒体挂载的新验收；实际ffprobe本机fixture不替代其余协议与真实服务用户环境。未部署/提交、未改现有服务/账号/生产库；不承诺SSRF防护、全响应内容任意秘密识别或任意网络协议兼容。文件/挂载支持与正式原生发行/安装/升级矩阵门禁仍待冻结/验收，整体目标继续。

## 64. N-01 升级/匹配恢复的实际认证契约（2026-11-06）

**状态：旧凭据、普通用户库授权及播放的真实进程契约本地通过；本子项完成，N-01/M1仍未放行。**

- [跨版本演练](<cmd/metatube/native_upgrade_test.go>)原有数据库/文件比较不足以证明迁移后的HTTP认证仍可用。本轮增加普通用户2、仅授权库1、库2受限但有效的HTTPS影片，保留用户1原有播放历史，同时种入用户2收藏/进度、NULL有效期token及固定稳定ID的API key。摘要按旧版本真实SHA256布局写入；NULL有效期避免读取触发续期，快照新增access_tokens/api_keys/login_events全行比较。登录历史本夹具为空，只证明没有意外新增/改动，不冒充已有登录历史逐行迁移的验证。
- [HTTP断言](<cmd/metatube/native_upgrade_api_test.go>)对同一旧token与API key分别执行：Users/Me为用户2且非管理员；递归列表仅影片1；详情保留Original、进度654321、播放2次和收藏；PlaybackInfo含一个相对DirectStreamUrl及非空PlaySessionId；流302的Location精确为原HTTPS URL；跨库详情/流404、管理接口403。客户端不跟随302，不访问example.invalid。检查分别在旧版种子、当前版第一次/第二次启动、匹配恢复后旧版运行；测试回调在真实进程就绪之后、SIGTERM之前执行，失败仍由既有defer杀死并join主进程。
- 当前二进制改用[已验证发布配置fixture](<cmd/metatube/native_release_fixture_test.go>)，仍是明确native-rehearsal/fixture/fixture身份。本机Go1.27.2/linux-amd64，旧SHA256 `25665f57b56e618ca51119bb6a71103c38a49e3a4cabaaca6ad6eb2a64e8d7b5`，版本v2.2.6-local-rehearsal、commit `234a220c9febfba4b1d610fd764448609a31e325`；当前SHA256 `3c32cbf77182c2b6bceb377eaa628e6b46bcf5bb6d15253296735f0e0b9cb3f3`。均为本地源码构建，不是官方Release。旧fixture身份与版本检查保持固定，未重建或用其它tag静默替换。
- 保留原有schema16→18、两次当前版启动的业务快照不变、拒绝旧版直接打开新DB、同停写窗口的SQLite/配置/STRM/NFO/封面匹配恢复及清除新增额外图断言；新增受限库文件也纳入匹配集合。恢复时比较SQLite原始字节，运行后只比业务行，避免把正常SQLite页变化当损坏。不提交新登录或播放事件，故不覆盖密码登录、有限期token续期、会话撤销/密钥轮换、新播放写入或真实Emby客户端取流。恢复旧凭据仍有效不代表可以直接对外上线，仍须按[恢复说明](<README.md#L303>)隔离并处置凭据。
- 首次专项4.040s失败于旧版PlaybackInfo404；原因是种子缺source_protocol，并非生产回归。两部影片均补https，避免受限流因无效协议而伪通过。修正专项3.878s通过；最终重复3次20.321s、启用旧fixture/systemd的cmd全包race28.308s、go build/vet及同时启用旧fixture/真实ffprobe/systemd/真实OS容量故障的全仓Go通过（cmd10.791s/backup1.142s/server102.016s/store60.299s/stabilityrehearsal5.155s）。作业 `bash-35ec1981a42e4690` exit0已收集，日志 `/tmp/emby-go-round21-validation.log`；前端Node300/300、0fail/0cancel/0skip通过48.613s，作业 `bash-e75ed74cacb74c8a` exit0已收集，日志 `/tmp/emby-go-round21-node.log`。包含既有Chromium回归，未新增完整升级GUI专项。父测试启用race不等于子二进制启用race，子二进制保持发布配置。
- 旧schema/凭据读取副作用的只读审查已返回，无编辑或重测试；Lead已复查最终测试源码、恢复比较及门禁输出。最终独立夹具只读复核已返回（无编辑、未重跑测试），未确认阻断问题；确认同步断言失败会展开Kill+Wait清理直接服务进程、保留凭据有真实身份/权限/数据断言、恢复仍是匹配旧包而非向下迁移。覆盖限制对账：PlaybackInfo仅检查DirectStreamUrl前缀，302检查另请求硬编码端点，未消费返回URL，故不证明客户端按返回URL及其query凭据成功起播；Movie快照是列子集，不是全列保护；登录历史为空，不证明非空历史迁移；直接进程清理不替代全进程树/systemd/cgroup验收，本机冻结旧fixture不等于官方artifact认证。复演入口及明确启用/跳过规则见[README](<README.md#L314>)。临时日志不是持久发行材料；本轮只改测试和文档，未部署/提交、未改现有服务/账号/生产库。正式旧artifact、安装布局、版本/schema、平台及文件/挂载矩阵仍待验收；可信STRM直接URL政策保持不变。

## 65. N-01 返回播放URL的真实query认证（2026-11-06）

**状态：§64独立复核指出的返回URL消费缺口已补齐，专项/重复/race/全仓门禁通过；本地子项完成，N-01/M1未放行。**

- [认证演练](<cmd/metatube/native_upgrade_api_test.go#L91-L129>)不再用HasPrefix接受可能的stream-bad路径。JSON及单一来源/非空PlaySessionId断言之后解析URL，要求相对地址、无主机、精确路径/Videos/1/stream且无fragment；query中的api_key必须等于当前请求凭据。URL属于子服务响应边界，校验仅在此执行，无新增生产参数、锁、重试或抽象。
- 不改写正向返回URL、不附加X-Emby-Token，直接向本机服务请求原始path/query，要求302及精确Location=https://example.invalid/original.mp4。随后仅移除api_key并保留其它query参数，再以无鉴权头请求要求401，排除匿名可用或原请求鉴权头掩盖凭据丢失的伪通过。原有单独带头请求流端点的302和跨库/管理拒绝检查保留。
- 同一旧token与API key分别在旧版种子、当前第一次/第二次启动和匹配旧包恢复后执行；共8组返回URL的正反向检查，并继续比较原有业务行及恢复文件。客户端仍拒绝跟随302，不请求远端媒体；不会登录、提交播放事件或新增ffprobe进程。这补齐URL/query认证链路，不等于真实播放器起播、远端媒体内容/协议/挂载或全进程树验收。
- 首次专项4.384s通过（作业bash-7368b5ea61714f6b，exit0已收集，日志`/tmp/emby-go-round22-focused.log`）；当前/旧二进制SHA与§64一致，仅修改测试断言。最终重复3次16.194s、cmd全包race39.966s、go build/vet及同时启用旧fixture/真实ffprobe/systemd/真实OS容量故障的全仓Go通过（cmd12.020s/backup1.349s/server98.185s/store57.207s/stabilityrehearsal5.222s）；作业bash-273a507083c248b4 exit0已收集，日志`/tmp/emby-go-round22-validation.log`。前端Node300/300、0fail/0cancel/0skip通过51.001s，作业bash-cd26ce227cc345a3 exit0已收集，日志`/tmp/emby-go-round22-node.log`；含既有Chromium回归，不是新增真实播放器验收。最终源码未在门禁后改变，gofmt/diffcheck通过、已检自有前缀临时unit/目录无残留。本节替代§64历史前缀检查的单项限制；非全Movie列快照、空登录历史、本地非官方旧artifact等限制继续有效。父测试race不等于发布配置子二进制race。仅改测试与文档，未提交/部署或修改现有服务/账号/生产库；整体目标仍继续。

## 66. N-01 非空登录历史与非默认认证元数据（2026-11-06）

**状态：首次专项、重复3次、cmd全包race、纠正执行环境后的build/vet/全仓门禁通过，独立只读复核无阻断项；本地子项完成，N-01/M1未放行。**

- [旧库种子](<cmd/metatube/native_upgrade_test.go#L270-L277>)包含3条不同登录历史：管理员与普通用户各一次成功、未知用户一次失败；覆盖确定ID、NULL用户、IPv4/IPv6和不同时间。普通用户及其有效token的auth_generation均为3；API key使用32位十六进制内部stable_id（对外ak_前缀由生产代码生成），version=4、非空rotated_at。它们是旧库显式SQL种子，不是实际登录/轮换操作产生，不证明这些写入操作本身或任意旧schema迁移。
- [全行快照](<cmd/metatube/native_upgrade_test.go#L120-L140>)已包含users/access_tokens/api_keys/login_events；本轮[将基线前移](<cmd/metatube/native_upgrade_test.go#L291-L298>)到旧版认证HTTP验证之前，并在旧版退出后比较，避免旧版先改写再采集而伪通过。随后沿用两次当前版启动、旧版拒绝新schema、匹配旧包恢复前后及恢复旧版退出后的比较。原有非空播放历史、业务选定列、配置/资源集合、稳定server ID及真实凭据权限/query认证检查保留。
- 首次专项4.630s通过，作业bash-ca844b46f86f40ba exit0已收集，日志`/tmp/emby-go-round23-focused.log`。当前/旧fixture SHA及身份与§64一致；仍是本机Linux amd64、本地冻结schema16→18及匹配恢复，不是官方artifact、全版本/schema、平台/安装/文件/挂载矩阵认证。URL仍不跟随302；未新增登录/播放事件请求，也未新增探测或生产抽象。
- 独立定向只读复核已返回（无编辑、未重跑测试），未发现本轮阻断项；确认基线前移、停机比较与匹配恢复顺序有效。覆盖限制对账：token/user代次均为3且NULL过期，不证明代次不匹配/过期拒绝、续期、撤销或改密；API鉴权只按摘要取用户，版本/稳定ID/轮换时间由全行相等证明保留，非管理列表/轮换CAS语义验收；登录历史仅证明原始3行，不覆盖写入、显示或清理；快照不含sqlite_sequence/schema_migrations及全Movie列（新列另验片1默认值）；匹配恢复为停机内存bundle手动回写，不是生产备份工具、在线WAL/崩溃一致性或未知新增文件发现。旧fixture按固定version串校验、记录实际SHA而非比对可信发行hash；缺少环境变量的skip不算升级证据。
- 首轮门禁的重复3次8.046s及cmd全包race23.042s通过，build/vet也通过；随后全仓失败（作业bash-4e1980ed0ebc40f0 exit1已收集，日志`/tmp/emby-go-round23-validation.log`），原因是执行时误将EMBY_GO_TEST_FFPROBE设为1而非可执行路径，导致TestFFprobePipeInputBoundary报`exec: "1": executable file not found in $PATH`及真实STRM测试报`ffprobe 不可执行: 1`；非本轮代码问题，不计作全仓通过。已按文档改用`EMBY_GO_TEST_FFPROBE="$(command -v ffprobe)"`（本机/usr/bin/ffprobe），不改源码，完整重跑build/vet/全仓门禁通过，作业bash-a71d89dd77e14190 exit0已收集，日志`/tmp/emby-go-round23-corrected-validation.log`（cmd13.428s/backup1.111s/probe5.050s/server100.512s/store59.512s/stabilityrehearsal5.160s）。同时启用固定旧fixture、真实ffprobe路径、systemd和真实OS容量故障。本轮未修改前端，未重跑Node/真实浏览器，不复用上轮300/300冒充本轮执行；父测试race不代表发布配置子二进制race。最终门禁后没有再改测试源码。
- 本节仅替代§64–65的空登录历史限制；其它既有限制保持。当前只改测试及施工文档，可信STRM直接URL政策不变，未提交/部署、未改既有服务/账号/生产库。正式artifact、完整版本/schema、平台/安装/文件/挂载支持矩阵仍未验收，整体目标保持进行中。

## 67. N-01 生产备份产物参与匹配恢复（2026-11-06）

**状态：首次专项、重复3次、cmd/backup两包race、build/vet及全仓Go门禁通过，独立只读复核无阻断项；前端300/300通过，本地子项完成，不放行N-01/M1。**

- [原生升级演练](<cmd/metatube/native_upgrade_test.go#L301-L356>)在旧服务SIGTERM并Wait之后，直接调用生产backup.Run生成SQLite与两库NFO，输出与源DB/媒体目录分离。实际生产路径为只读源库VACUUM INTO、integrity_check和NFO复制/发布，不经store.Open迁移。断言NFO=2、extra-backdrops=0、pruned=0；源DB前后主文件字节不变，生产快照的选定业务/认证/历史行与基线相等，独立只读查询快照MAX(version)=16，防止备份时提前迁移。
- [恢复阶段](<cmd/metatube/native_upgrade_test.go#L423-L448>)重新读取磁盘上的Result.Database与nfo/lib-1/ABC-123.nfo、nfo/lib-2/hidden.nfo，对比最初产物字节后回写，不存在产物读取失败则改用原始内存DB/NFO的兜底。沿用当前两次启动/schema18、旧版拒绝新schema、停机匹配恢复、旧版真实HTTP权限/query认证及非空历史比较。数据库采用VACUUM后的物理布局，不要求备份与源DB物理字节相同。
- 首次专项4.795s通过，作业bash-704e227eec6c4c1e exit0已收集，日志`/tmp/emby-go-round24-focused.log`。当前/旧fixture身份及SHA与§64相同；没有生产源码变动。独立只读复核已返回（未编辑、未执行测试），确认输出路径、产物消费、停机顺序、源不变和temp-root清理无本轮阻断项。
- 最终Go作业bash-3e5e254745b64280 exit0已收集，日志`/tmp/emby-go-round24-validation.log`：重复3次7.885s、cmd全包race22.003s/backup全包race4.612s、go build/vet通过；同时启用固定旧fixture、真实ffprobe可执行路径、systemd和真实OS容量故障的全仓Go通过（cmd11.436s/backup1.183s/server97.603s/store57.092s/stabilityrehearsal5.127s）。父测试race不等于发布配置子二进制race；门禁后未修改测试源码。前端Node300/300、0fail/0cancel/0skip通过42.018s，作业bash-833817597c584a39 exit0已收集，日志`/tmp/emby-go-round24-node.log`；含既有浏览器回归，不是新增备份管理UI或真实播放器验收。
- 覆盖限制：这是停止服务后的Go生产备份服务调用，不是备份CLI入口执行、在线写入/未checkpoint WAL/崩溃恢复或DB与NFO跨文件原子一致性证明。配置、STRM与普通poster仍是人工补充材料，不属于backup.Run输出；ExtraBackdrops虽启用但种子为空，后备新增fanart的手动删除不是额外海报备份恢复证据。仅一份备份，pruned=0不证明轮换清理。快照比较仍不含完整sqlite_master/sqlite_sequence/每个Movie字段/完整迁移ledger，MAX(version)不替代全schema校验；本地固定version与日志SHA不等于可信官方artifact。未知新增文件发现、敌意symlink/并发目录替换及正式安装/平台/挂载矩阵仍未验收。只补测试/施工文档，可信STRM直接URL政策不变；未提交/部署或修改既有服务、账号、生产库。

## 68. N-01 实际备份CLI与非空背景图匹配恢复（2026-11-06）

**状态：首次专项、重复3次、cmd/metatube及cmd/embybackup/internal/backup的race、build/vet/全仓Go与前端300项门禁通过；独立只读复核无阻断项，本地子项完成，不放行N-01/M1。**

- [实际CLI入口](<cmd/metatube/native_upgrade_test.go#L315-L352>)在旧服务退出后构建并执行embybackup，传入db/out/retain=1，省略nfo/extra-backdrops开关以验证默认开启。检查exit成功、NFO=2/额外图=2、无prune输出，输出根仅一个目录；源DB主文件字节不变，快照选定业务/认证/历史行等于基线且MAX(version)=16，未提前迁移。CLI本轮为普通本机go build，不冒充正式发布配置产物。
- [真实文件产物](<cmd/metatube/native_upgrade_test.go#L353-L378>)包含db.sqlite、两库NFO及extra-backdrops/lib-1/extrafanart/fanart2.jpg与lib-2/extrafanart/fanart1.jpg。两个JPEG内容不同，逐字节比源，放入匹配集合；不凭计数日志判定通过。升级schema16→18及两次当前启动后，[恢复阶段](<cmd/metatube/native_upgrade_test.go#L421-L475>)故意破坏第一张图、删除第二张图，并再次读取CLI磁盘产物（无原图兜底）恢复、即时比对；恢复旧服务运行退出后，所有非DB材料再次字节一致，数据库选定行保持。旧凭据、query-only返回URL至精确302/缺key401、库权限及非空认证历史检查保留，URL不跟随远端。
- 首次专项6.668s通过，作业bash-de74851fb6c94ae5 exit0已收集，日志`/tmp/emby-go-round25-focused.log`。当前/旧fixture身份和SHA仍同§64；显式提供固定旧二进制，不将默认skip计为升级通过。独立只读审查已返回，无编辑或重跑测试，确认实际CLI/默认开关、磁盘产物消费、两种损坏恢复及临时根内文件清理无本轮阻断。
- 最终作业bash-84ace4fea6994cbb exit0已收集，日志`/tmp/emby-go-round25-validation.log`：重复3次23.543s，metatube race38.570s、embybackup race3.384s、backup race5.017s；go build/vet通过。全仓Go通过（cmd11.459s/backup1.270s/stabilityrehearsal5.204s），环境同时设置旧fixture、真实ffprobe可执行路径、systemd与真实OS容量故障；server/store/probe等显示cached，不宣称本轮这些外部用例重新实际执行。父测试race不是发布配置子二进制race；门禁后未修改测试源码。前端300/300、0fail/0cancel/0skip通过52.928s，作业bash-6add5f7acbec4bc8 exit0已收集，日志`/tmp/emby-go-round25-node.log`；不代表新增UI或真实播放器验收。
- 限制：仅停服Linux本地冻结旧库/源码构建fixture和手工匹配恢复，不是官方artifact、在线WAL/崩溃一致性、systemd或一键恢复证明。配置/STRM/普通poster仍是人工补充；新fanart99由测试手动清理，不证明产品恢复器自动发现/删除未知新增文件。磁盘额外图非空，不代表schema17图片索引非空；目前仅另验片1迁移后extra_backdrops/auto_marks为空，未覆盖片2/已有schema17非空索引或图片API。显式retain=1且无旧备份，不证明默认retain或真实prune。DB恢复即时字节一致，但旧服务后仅比较选定业务行；Movie列子集、sqlite_master/sqlite_sequence/完整迁移ledger及checksum、正式版本/schema/平台/安装/挂载矩阵仍未验收。固定自报version及日志SHA不等于可信官方制品校验。本节只替代§67的CLI未执行/磁盘额外图为空限制；可信STRM直接URL政策不变，未提交/部署或改既有服务、账号、生产库。

## 69. N-01 实际备份CLI的活动WAL快照（2026-11-06）

**状态：首次专项、重复10次、CLI/backup两包race、build/vet及无缓存全仓Go门禁通过；前端300/300通过，独立只读审查无阻断项，本地子项完成，不放行N-01/M1。**

- [独立CLI测试](<cmd/embybackup/wal_test.go>)使用合成SQLite源库，确认journal_mode=wal、关闭自动checkpoint，初始TRUNCATE checkpoint成功后保存主库字节。两个标记行从代次1提交到2，要求WAL大于头部且主库仍与初始字节一致，以证明不是关闭源连接后才备份的伪在线场景。
- 源连接不关闭，保持将两行改为3的写事务未提交，通过独立CLI进程运行数据库备份（关闭NFO/额外图）。要求CLI退出成功、唯一备份目录的真实db.sqlite通过integrity_check且两行均为2；同时源主库/WAL字节不变。随后提交源事务并验证源为3，关闭源库；仅将备份db.sqlite复制到空恢复目录，不提供源WAL/SHM，恢复库与原备份均仍为2且integrity正常。
- 首次专项0.542s通过，作业bash-40846461574347d6 exit0已收集，日志`/tmp/emby-go-round26-focused.log`。真实执行普通本机构建CLI，不调用测试hook或生产Store.Open；无需旧fixture环境变量，不会因缺旧二进制而跳过。
- 最终作业bash-41905674c9ba40f7 exit0已收集，日志`/tmp/emby-go-round26-validation.log`：专项重复10次5.205s，CLI race2.266s/backup race4.367s；go build/vet通过，`go test ./... -count=1`无缓存全仓通过（CLI1.615s/cmd14.456s/backup1.249s/probe4.678s/server118.121s/store67.600s/stabilityrehearsal7.381s），同时启用固定旧fixture、真实ffprobe路径、systemd和真实OS容量故障。父测试race不是CLI子二进制race；测试源码在门禁后未修改。前端300/300、0fail/0cancel/0skip通过55.276s，作业bash-4d0c866b829a4585 exit0已收集，日志`/tmp/emby-go-round26-node.log`；不是新增备份管理UI或实际播放器验收。
- 独立只读审查已返回（无编辑、未执行构建/测试），确认实际CLI、未checkpoint证据、未提交事务持有顺序、源后续提交及不依赖源sidecar的恢复无本轮阻断项。可移植限制：依赖原生Go工具链及可执行临时目录；构建本身无超时，SQLite URI沿用直接拼接方式，特殊TMPDIR中的URI保留字符未覆盖；30秒为本地测试上限，不是性能承诺。
- 覆盖限制：这是合成SQL表和受控未提交的小事务，不覆盖大事务cache spill/超大WAL，不是实际应用HTTP/调度写入、多进程压力、写事务持续提交时精确快照时点、在线NFO/图片联合一致性、崩溃/断电或NAS失联验证。主库/WAL字节不变不代表SHM不被读取者使用。独立恢复为测试手工复制，不是自动恢复产品能力；CLI为普通本机go build，无官方artifact或平台/挂载矩阵支持承诺。§67–68的停服匹配恢复历史不改写成活动服务备份；可信STRM直接URL政策不变，未改生产逻辑或提交/部署。

## 70. N-01 正式前版Release制品升级与匹配恢复（2026-11-06）

**状态：真实下载前版专项、错误摘要拒执行、双入口重复/race、build/vet及无缓存全仓Go门禁通过，前端300/300通过，独立只读审查无阻断项；本地子项完成，不放行N-01/M1。**

- 来源：[仓库v2.2.6 Release](https://github.com/holll/Emby-go/releases/tag/v2.2.6)，published_at=2026-11-02T08:22:13Z；[Linux amd64资产元数据](https://api.github.com/repos/holll/Emby-go/releases/assets/701031564)，asset ID701031564、大小20017312、uploader=github-actions[bot]。实际`gh release download`原始产物的SHA256与GitHub API digest同为`7ab1e33307186c660ecfd5f154594293d7079f9f864314cc32274d14b522a758`；本轮下载路径`/tmp/emby-go-official-v2.2.6.jnYWPk/emby-go-linux-amd64`，原始元数据同目录asset.json。这是仓库分发来源与字节一致性证据，不是签名/attestation或独立供应链证明。
- 实际完整版本为`Emby-go v2.2.6-234a220 (commit 234a220c9febfba4b1d610fd764448609a31e325, built 2026-11-02T08:19:36Z)`，commit与本地标签解析一致。不是把§64的`v2.2.6-local-rehearsal`改名冒充正式二进制；当前仍由本地release-profile构建，SHA为`3c32cbf77182c2b6bceb377eaa628e6b46bcf5bb6d15253296735f0e0b9cb3f3`，明确不是当前正式发行artifact。
- [原生升级测试](<cmd/metatube/native_upgrade_test.go>)增加`TestNativeReleasedBinaryUpgradeAndMatchingRestore`，只接受`EMBY_GO_PREVIOUS_RELEASE_BINARY`显式路径，先读文件固定摘要匹配再首次执行-version，并精确比较版本/commit/time及换行；未提供SKIP不算验收，提供不匹配则FAIL。原`EMBY_GO_PREVIOUS_BINARY`本地fixture入口和身份约束不变；两实际调用方才提取共同流程，没有新增生产抽象或支持自动下载。
- 两入口共用旧schema16建库及SQL seed→真实token/API key权限/用户状态/query流URL→实际backup CLI停服快照+2NFO/2不同JPEG→当前schema18两次启动→旧版在迁移17边界拒绝新库且业务行不变→破坏/删除资源并从磁盘备份产物和补充材料匹配恢复→旧版实际启动与同一HTTP契约。当前启动不会追认旧额外图/自动标签索引；Movie仍为选定列快照，非全库所有列/ledger/sequence证明。
- 首次真实官方前版专项5.487s通过，作业bash-ac7019c64a4c4f0e exit0已收集，日志`/tmp/emby-go-round27-focused.log`。错误摘要边界：独立可执行脚本若被调用会创建标记；作为release路径执行测试时要求失败含`(refusing execution)`且标记不存在，外层作业bash-79e16a5ef42c4bab exit0已收集，证据`/tmp/emby-go-release-reject.9J6Jus/rejection.log`；这是预期拒绝，不是忽略门禁错误。
- 最终作业bash-2bb4e44e21424c97 exit0已收集，日志`/tmp/emby-go-round27-validation.log`：正式/本地旧版双入口各重复3次17.366s，cmd race28.372s，build/vet通过；`go test ./... -count=1`无缓存全仓通过（cmd17.340s/CLI1.421s/backup1.407s/probe4.534s/server117.906s/store68.549s/stabilityrehearsal6.923s），同时启用两前版路径、真实ffprobe、systemd与OS容量故障。父测试race不是子二进制race；测试源码在门禁后未修改。前端作业bash-23a55a4e7c604a4d exit0已收集，日志`/tmp/emby-go-round27-node.log`，300pass/0fail/0cancel/0skip，57.822s；不新增UI或媒体播放验收。
- 独立只读复核未编辑/执行构建测试，确认摘要先于执行、完整身份匹配、原本地调用方兼容、旧正式/当前本地来源日志、共用流程与清理无阻断项。旧启动helper注释笼统“不构成official release-artifact test”不改变旧侧实际下载来源；本节与运行日志明确区分，不顺手改既有helper。
- 覆盖限制：仅Linux amd64上的v2.2.6正式旧制品→当前本地源码fixture；非当前正式发布、完整升级/platform/schema/挂载支持矩阵或正式安装路径。库为旧版创建schema后人工SQL seed，不是长期生产数据；停服备份不覆盖活动应用写入，§69仍为独立合成WAL证据。配置/STRM/普通海报仍是手工补充材料，恢复由测试显式编排而非自动恢复工具；不跟随302、不证明真实媒体起播，不修改生产服务/账号、不提交/部署。可信STRM直接URL及既有资源边界政策不变。

## 71. N-01 旧影片全列保留与范围外字段恢复（2026-11-06）

**状态：双前版全旧列比较、范围外字段负向检测及匹配恢复的重复/race、build/vet、无缓存全仓Go通过，前端300/300通过；独立只读复核对其读取版本无阻断项，最终负向断言由主Agent核对并实跑验证。本地子项完成，不放行N-01/M1。**

- [原生升级快照](<cmd/metatube/native_upgrade_test.go>)此前只比较十个Movie列。现从`pragma_table_info('movies')`按cid发现列名，经SQL双引号转义后查询所有旧列，明确只排除迁移17/18新增的extra_backdrops/auto_marks；后两列仍在当前schema18另行断言为[]。快照将Columns与Rows一起编码，列名/列序变化不再能仅因值相同而假通过。普通未来新增的存储列不会自动排除，会使前后快照不同而失败；pragma_table_info不返回generated/hidden列，当前影片表无此类列，不据此承诺所有未来列均能检出。JSON不编码SQLite typeof/DDL，整数与等值浮点、某些BLOB/base64文本可能同编码，不是任意存储类型一致性证明。
- SQL seed补入非默认file_size/file_mtime/source_container、number/output_dir、original_title/plot/year/premiered/rating、director/series/maker/label/collection/official_rating/sortname/taglines/provider_id/genres/studios、backdrop_path/landscape_path/runtime_seconds；保持Title及原凭据/权限/query认证场景不变，不添加扫描、登录或播放写入。
- 新增真实负向敏感性检查：旧版拒绝升级库后，在当前库仅修改原十列范围之外的original_title/runtime_seconds，要求新快照与升级前baseline不同；随后连同文件损坏/丢失，从实际CLI产物和补充材料恢复，最终旧版运行后全列快照必须重新等于baseline。它同时说明回退会丢弃备份后的业务写入，不擅自批准生产可接受丢写窗口。
- 初次双前版专项9.725s通过，作业bash-474def4bb49d4f87 exit0已收集，日志`/tmp/emby-go-round28-focused.log`；这一首次输出早于补入范围外字段负向断言，不作为该断言通过证据。最终测试在源码改完后才启动。
- 最终作业bash-03fe68d228c64598 exit0已收集，日志`/tmp/emby-go-round28-validation.log`：包含最终负向断言的双前版各重复3次31.085s，cmd race42.103s，build/vet通过；`go test ./... -count=1`无缓存全仓通过（cmd16.032s/CLI1.315s/backup1.184s/probe4.774s/server95.134s/store54.474s/stabilityrehearsal5.307s），同时启用正式/本地前版路径、真实ffprobe、systemd与OS容量故障。父测试race不是子二进制race，源码在最终门禁后未改。前端作业bash-48a8764ae8e44cd9 exit0已收集，日志`/tmp/emby-go-round28-node.log`，300pass/0fail/0cancel/0skip，53.632s。
- 独立只读复核未编辑或跑测试，读取的是517行版本（revision9728f32c9dc8a4d3330006d0ae69735cc6cbfe6294c62ec5fb2ec3b74404d84a），确认动态列/标识符引用、列和值编码、非默认种子及调用兼容无当前阻断项；其明确未读到后来补入的负向断言。主Agent已核对最终533行版本中UPDATE两字段→快照必须不同→磁盘匹配恢复→旧版运行后快照回归的实际顺序，并由上述最终门禁验证；不把早期只读审查冒充完整最终版本独立复核。
- 覆盖限制：本轮只提升Movie旧列值/名称/顺序的保留证据，不是所有SQL表、sqlite_master/索引/触发器、sqlite_sequence或完整migration ledger验证。其余表沿用既有快照范围；某些字段仍是默认/空值，不能声称全部字段非空种子。正式前版固定资产/版本与当前本地fixture来源同§70，旧SQL种子不等于长期生产库；恢复仍是测试编排，配置/STRM/普通海报为补充材料；不是当前正式发布或全平台/schema/安装/挂载矩阵，不修改生产逻辑，不提交/部署，可信STRM政策不变。

## 72. N-00 M1验收矩阵与发布决策整理（2026-11-06）

**状态：矩阵及历史状态对齐已落地，用户已于2026-11-06批准D-M1-A/B/C验收范围，无例外；正式复核人及同候选包门禁待完成，不是N-00全部冻结或N-01/M1放行。**

- 新增[M1验收矩阵](<docs/m1-release-matrix.md>)，明确实施记录人为当前会话lead，发布验收范围/损失政策由当前用户于2026-11-06批准、正式发布/迁移复核具名负责人仍待登记。将已批准的STRM政策、NFO8MiB/STRM1MiB、探测资源、有界关闭和配套恢复要求与未决支持承诺分开，不重新询问已批准决定。
- 本轮只读环境核对：Debian12、Linux x86_64、systemd252.39、Go1.27.2、工作区/临时目录ext4，HEAD52836dd640d6cf169db2173fd8c8c49ceee775b0；核对时64个tracked改动、87个untracked文件（新增本轮矩阵前统计），不能用HEAD或fixture SHA冒充正式源码身份。集中记录正式前版下载制品、当前本地fixture、临时服务布局、CLI/匹配恢复和OS故障证据的实际边界。
- 草案阶段提议D-M1-A：首批限定Linux amd64/Debian12/systemd/本地ext4，远端NAS/其它平台未验收；D-M1-B：固定v2.2.6/schema16→18及现有样例正式布局，旧版拒绝新库、只做配套恢复；D-M1-C：升级验收前不重新开放业务写入，开放后需先评估/保存新增数据并另获丢弃批准，不自动回退。草案形成时建议尚未采用；随后用户在M1范围确认中明确选择“按建议范围继续验收 (Recommended)”，D-M1-A/B/C均获批准、无例外，已写回矩阵并仅勾选批准记录项。不授权操作生产账号/目录/服务、提交或发布；M5网页备份秘密政策仍独立待定。
- [原生详细方案](<docs/native-roadmap-design.md>)将“缺上限”的段落标为修复前历史，补上已采用阈值与台账证据，更新§9.1已落地API/文件边界及真正未决发布项；新增§9.2链接矩阵，不把2026-11-04旧待定表继续当作全部当前状态。
- 本轮仅文档及只读环境核对，未运行新Go/前端门禁，不沿用§71测试数字冒充本轮发布验收；git diff --check通过，当前工作区这四份文档的新增内部链接148处路径/行界/标题锚点校验通过，未测外链可用性或渲染。独立只读复核未编辑/运行命令，确认建议/授权、证据层级、关闭与凭据边界及主要链接一致；发现总计划§4.3仍残留“实际连接”门禁，主Agent已按2026-11-05决定改为直接URL/进程资源并明确取消历史出站隔离，同时澄清已定文件阈值/多图布局不重复待选。独立复核读到的是修订前残留，最终澄清由主Agent核对，不冒称已获二次独立复核。范围获批后仍须确定正式候选源码/制品身份、同包实际平台/安装/关闭/升级恢复/文件组合门禁；不能因批准矩阵而勾销未执行验收。

## 73. N-01 冻结源码与同包原生候选复验（2026-11-06）

**状态：固定私有源码快照及单一摘要验收候选的本地子项完成；不是正式Release来源、正式安装或M1放行。**

- [原生构建入口](<cmd/metatube/native_release_fixture_test.go>)新增成对EMBY_GO_NATIVE_BINARY/EMBY_GO_NATIVE_SHA256：任一非空则校验64位hex摘要和源字节，失败不回退构建；复制同一次已验证读取的data，不在校验后重新读取外部路径。随后仍共用buildinfo、CGO0/linux/本机架构/trimpath/默认微架构、无额外实验/标签、静态裁剪ELF及精确fixture版本检查，再首次执行-version。默认编译路径保留，污染环境回归主动清空candidate变量后独立构建。缺路径、缺摘要、非法hex、错误摘要四个负向子进程均非零退出、匹配拒绝消息且无恶意脚本marker；首次专项3.267s，作业bash-ab7aee2628af4fa3 exit0已收集，日志`/tmp/emby-go-round30-boundary.log`。
- 未提交/未跟踪工作不能仅用HEAD标识。将583个现存tracked/未忽略untracked路径冻结为私有目录`/tmp/emby-go-m1-candidate.uW2pse`中的source.tar及解包source；归档规范化时间/owner/group、保留文件mode，路径清单source-paths.z和base-head.txt/worktree-status.z辅助溯源。归档SHA256=`6eaf586dc29056b1d92df13fe9e3ac078d992e50ff3f440d418ddb47ee5e09a2`，基础HEAD仍是`52836dd640d6cf169db2173fd8c8c49ceee775b0`，不是干净commit。清单遵从既有.gitignore（其中`/docs/`被忽略），因此新M1矩阵等忽略文件不在归档中；这是可构建源码快照，不是整个工作区/全部文档的完整交付备份。临时私有材料不得提交或公开；/tmp不是长期发布存储。
- 从该解包源码一次构建`emby-go`：Go1.27.2、GOENVoff/GOFLAGS空/GOEXPERIMENT空/GOWORKoff、CGO0/linux/amd64v1、trimpath、-s -w，明确注入native-rehearsal/fixture/fixture；制品SHA256=`38bef8d8c81f24a08db8affae8bb7bc4e4f0ece64f73592d05ea118c6fe39289`，精确身份`Emby-go native-rehearsal (commit fixture, built fixture)`。SHA256SUMS关联归档和二进制，构建作业bash-726601f6714b4bd9 exit0已收集。源码归档摘要是本地快照身份，不把fixture嵌入的commit字段冒充该身份或正式版本。
- 从同一解包源码运行最终作业bash-923d7be3d10a4f8a exit0已收集，日志`/tmp/emby-go-round30-validation.log`：`^TestNative`重复2次33.759s，无SKIP；日志十次明确复用同一个上述SHA，分别为重启/非特权身份、实际SIGTERM活跃探测、真实transient systemd、冻结本地前版升级恢复及正式v2.2.6前版升级恢复各两次。污染构建回归另行编译，不计为复用入口；恰好得到相同摘要不是其绕过编译。systemd的失败重启、正常停止及已知忽略TERM探测树覆盖沿用§61边界；正式旧侧固定资产701031564，当前侧为本次私有候选。
- 同作业cmd race25.400s、build/vet通过；`go test ./... -count=1`无缓存全仓通过（cmd11.551s/CLI1.082s/probe4.466s/server97.675s/store55.365s/stability5.401s），显式启用两前版路径、真实ffprobe路径、systemd与OS容量故障；归档/二进制最终sha256sum -c均OK。父测试race不等于候选或旧子二进制开启race，CLI仍由测试本地构建而非同包emby-go。冻结源码前端作业bash-3542a0b9fbef4ccf exit0已收集，日志`/tmp/emby-go-round30-node.log`：300pass/0fail/0cancel/0skip，48.041s。
- 独立只读复核cc273fef-66c7-4fc4-a733-822f1db3fa91未编辑/构建/执行，确认唯一边界、先摘要后复制/执行、同字节复制、共用profile/版本检查、污染回归未绕过、现有调用方隔离安装与返回字节兼容，无阻断项。其运行覆盖结论不代替主Agent实际日志；未专测合法hex长度错误/不可读源/正确摘要但profile或版本错误/源路径动态替换，未直接断言拒绝时目的地不存在，不据此新增防御或扩大范围。
- 本轮只改测试和文档；最终门禁后未再改测试/生产源码，当前工作区后续文档进度不在冻结快照内。本地SHA一致性不是签名/发布来源认证；仍无真实emby账号、正式路径安装/迁移及readiness/关停期间拒绝新任务的同包实测，不将SQL seed/手工配套恢复说成在线联合快照或生产回退。最终检查作业bash-3c3d491e2fb54408 exit0已收集：冻结清单的583路径无增删，工作区与冻结目录仅三份已入清单的进度文档内容不同，代码/文件模式未变；忽略的新矩阵另已同步。重打包解包源码与原source.tar逐字节相同，归档/制品摘要再次OK；gofmt/diffcheck通过，既有测试前缀transient units及临时目录无残留。M1矩阵的正式来源/安装/文件组合等五项仍未勾选，用户既有范围批准及可信STRM政策不变；无生产账号/服务操作、提交或发布。

## 74. N-01 实际SIGTERM的HTTP请求门与对外readiness（2026-11-06）

**状态：固定候选的实际SIGTERM请求门本地子项完成，重复/race/全仓门禁及独立复核已收集；不是M1放行。**

- [实际信号回归](<cmd/metatube/native_shutdown_test.go>)新增assertNativeShutdownAdmission职责：通过真实TCP连接提交已鉴权的POST /api/admin/probe/media头部（有效Content-Length和Expect:100-continue），收到100证明已进入handler读body边界，暂不发送body。保留既有已接纳且存活的探测父子进程，然后发送真实SIGTERM；不直接调用BeginShutdown，不增加生产hook或锁。
- 使用新连接轮询/healthz，接受明确的连接拒绝/reset/EOF，或HTTP503且status=shutting_down；不把任意错误/超时当成功，也不将连接关闭冒称HTTP503。外部不可就绪之后，才向原活动连接补齐合法JSON body，必须读到HTTP503且error=server shutting down，证明已在HTTP层接纳但迟到的探测任务被门拒绝。若进程提前退出、关闭该活动连接而未drain、返回接纳成功或其它业务错误，测试均不能通过。请求体解码在准入前、当前准入在prepare前的次序另由源码复核确认；本次黑盒503本身不证明prepare从未被调用。
- 时间戳在真实信号前记录，原17秒测试余量减去新增观察耗时，不重置关停预算；继续要求正常退出/无强制退出日志、已知父子进程不再运行、原NFO与完整Movie值不变及无辅助输出。失败回退仍先清理本fixture session并join主进程；新请求连接有明确测试deadline并defer关闭。并非增加生产超时或新的服务策略。
- 复用§73固定候选SHA256=`38bef8d8c81f24a08db8affae8bb7bc4e4f0ece64f73592d05ea118c6fe39289`，没有重建候选。首次专项作业bash-5097885d944a4486 exit0已收集，日志`/tmp/emby-go-round31-focused.log`，0.397s；实际观察为listener refused/closed，而不是HTTP503 readiness，迟到POST确实收到HTTP503，SIGTERM到主进程join为10.141ms。本轮新测试不在§73旧源码归档内，不把更新的测试源码说成该归档内容。
- 前端作业bash-1c95a6ce26d34eac exit0已收集，日志`/tmp/emby-go-round31-node.log`，300pass/0fail/0cancel/0skip，51.006s；无UI源码改动，不是新的浏览器/客户端验收。Go作业bash-0bcc6a0c8e31418e exit0已收集，日志`/tmp/emby-go-round31-validation.log`：固定候选20次专项9.607s（19次readiness为连接关闭/拒绝，1次实际HTTP503 shutting_down；20次迟到POST均HTTP503）、取消固定环境的默认构建兼容5.635s、cmd race34.362s、build/vet以及无缓存全仓Go通过（cmd13.238s/server99.773s/store58.172s/probe4.873s/stability5.791s）。全仓开启本地旧fixture、已钉住的官方v2.2.6旧制品、真实ffprobe路径、实际systemd和隔离tmpfs ENOSPC；父测试-race不冒称候选子进程-race。source.tar及候选SHA256SUMS再次通过。独立只读复核5f6e917e-4a02-46c7-90fd-792bc07dbdf3已返回及关闭，未编辑/构建/重测，静态检查101–183/365–378及真实路由顺序后未发现阻断缺陷；其结果不替代上述执行证据。归档583项逐文件复查当前生产代码/模式不变，仅README/PLAN-STATUS/dev-plan进度文档和本轮native_shutdown_test.go测试不同；忽略目录docs仍不在该归档内。
- 覆盖限制：只把probe/media这一真实body入口贯通到OS信号；其余入口沿用既有直接BeginShutdown测试，不推断全部入口或调度行为均经过本次信号实测。100-continue不是已经接纳后台任务；鉴权/handler结论仅限当前非debug调用链，debug原始body捕获可能提前读取，不在覆盖内。503说明该迟到请求被拒绝，不等于全库零写入。外部不可就绪与handler内部503分别记录，实际SIGTERM恰好观测HTTP503 readiness不是必需成功路径。仍是本机fixture/已知探测树、不是正式emby账号/安装路径或任意NAS/D-state关停；不修改生产逻辑，不提交/发布，既有可信STRM政策和M1五项未勾选保持。

## 75. N-01 部署样例驱动的真实systemd回归（2026-11-06）

**状态：部署样例接入的本地子项完成，固定候选专项、完整门禁及独立只读复核已收集；不是正式安装或M1放行。**

- [systemd回归](<cmd/metatube/native_systemd_test.go#L141-L239>)不再另写一套Service属性：读取[部署样例](<deploy/emby-go.service>)，仅处理当前简单Service段，将全部21条指令纳入转换；样例SHA256=`b5afe19f4026c3196768062b44aa7d5d5bc6e7f1263211fc2d7764c1554f9e39`。只替换User/Group/WorkingDirectory/StateDirectory/RuntimeDirectory/ReadWritePaths六项fixture身份/路径，精确核对ExecStart后换成测试二进制/config参数，追加原有BindReadOnlyPaths与启动/停止观测；其余安全、权限模式、PATH、重启与关闭策略直接来自样例。
- 两个实际调用方共用nativeSystemdServiceProperties命名职责，不引入通用INI解析器。重复指令可能把第二个StateDirectory带到正式路径，故在文件读取边界拒绝重复/续行/无等号；命令变化必须先适配测试。默认无需root/systemd的TestNativeSystemdServiceTemplate覆盖现有样例、重复目录/续行/无等号/改命令四类拒绝；缺少六项替换项也会在systemd-run前失败。不声称支持任意合法systemd配置语法。
- [实际执行](<cmd/metatube/native_systemd_test.go#L336-L394>)保留唯一单元/独立目录、数字UID/GID65534、实际进程身份与挂载隔离检查；新增管理器有效TimeoutStopUSec=30s、KillMode=control-group、LimitNOFILE=65536、UMask=0077检查。沿用真实SIGKILL失败重启、直接SIGTERM成功后不重启、活跃忽略TERM探测父子关停和NFO/Movie不变等既有覆盖；不把管理器property读取说成实际触发30秒超时或证明任意cgroup清空。
- 首次实现作业bash-7cf2c86d7d6e4ac4 exit0已收集，4.205s，日志`/tmp/emby-go-round32-focused.log`，早于后续样例摘要/有效策略检查及默认输入测试，不作为这些新增项的最终证据。最终源码专项作业bash-c5d14009648343a7 exit0已收集，日志`/tmp/emby-go-round32-focused-final.log`：输入边界与真实systemd各重复三次，共12.413s；三次日志均读取相同样例摘要并复用§73固定候选SHA256=`38bef8d8c81f24a08db8affae8bb7bc4e4f0ece64f73592d05ea118c6fe39289`，各完成一次失败重启及三次正常停止，无SKIP。
- 前端作业bash-28c910f71edb4e17 exit0已收集，日志`/tmp/emby-go-round32-node.log`，300pass/0fail/0cancel/0skip，55.677s；无UI改动，不冒充浏览器/播放器验收。全仓Go/race作业bash-70b7051a034a4155 exit0已收集，日志`/tmp/emby-go-round32-validation.log`：cmd race34.708s、build/vet、无缓存全仓Go通过（cmd20.064s/server99.592s/store57.866s/probe4.772s/stability5.185s/CLI1.770s），开启两类前版制品、真实ffprobe路径、systemd及隔离tmpfs ENOSPC；source.tar和候选SHA256SUMS通过。独立只读复核202883ca-33c4-4433-876e-7c769ff483a5已返回及关闭，未编辑/构建/重测，确认当前模板接入与隔离清理无阻断；不将静态复核冒称新增执行证据。主Agent已逐条核对§73冻结的583项，生产代码/模式保持一致，只有三份已入清单进度文档及§74/本轮两个测试文件不同；最终本轮测试SHA256=`eff0b28bd123e88161db436f4fdf9dff9b9e57f6bc7a59021421496b4be12e81`，样例字节未变，gofmt/diffcheck通过。
- 覆核补充：仅当前简单Service模板转换通过，不承诺任意模板演化；未来新增身份/路径指令，或出现与观测属性同名的ExecStartPre/ExecStopPost/BindReadOnlyPaths，须重新审查。当前effective断言仅覆盖列举属性，PATH/Type等虽承袭但未逐项另证完整等价。
- 覆盖边界：只验证Service段的样例派生transient执行，不应用Unit/Install的network-online依赖、开机启用或正式路径安装；六项替换意味着不证明实际emby账号/组、正式目录/配置迁移与媒体ACL。当前候选仍为明确fixture身份，新增测试不在§73原归档内；不改生产逻辑/部署样例、不创建正式账号或改变既有服务，不提交/发布，既有可信STRM政策、已批准范围及M1五项未勾选不变。

## 76. N-01 systemd沙箱内真实ffprobe成功探测（2026-11-06）

**状态：沙箱内真实ffprobe成功链路本地子项完成，固定候选三次专项、完整门禁及独立复核已收集；不是正式安装或M1放行。**

- 继续复用§73固定候选和§75样例派生Service策略。在既有三个场景停服后，仅在设置EMBY_GO_TEST_FFPROBE时追加第四次启动，把持久配置中的ffprobe_path改成裸命令ffprobe，保留server_id与既有用户/库；未配置时不把原有三个场景称作真实探测覆盖。
- 核对实际进程PATH、按该PATH选中的可执行文件与opt-in路径一致，并从进程根目录读取可执行文件摘要。通过loopback提供已知MP4媒体；已贯通真实HTTP鉴权/扫描/单片同步探测、NFO/mediainfo写入、显式重扫后的SQLite时长/源信息及播放路由检查：h264 320x240、9260bytes，NFO保留人工title/customtag，browse与PlaybackInfo匹配，stream返回精确源302且禁止跟随/无额外上游读取。同步探测本身不负责刷新索引，不将显式重扫掩饰为自动同步。
- 首次作业bash-a96d234d51454888 exit1已收集，日志`/tmp/emby-go-round33-focused.log`：真实ffprobe成功返回h264/320x240/9260bytes，但测试误要求正整数秒。现有半秒样例按既有映射截断为0秒，不是生产故障；只修正测试。修正后早期专项bash-2e922e9ff35643a4 exit0已收集，5.058s，日志`/tmp/emby-go-round33-focused-retry.log`；此时尚未加入后续陈旧时长及sidecar断言，不充作它们的最终证据。最终测试另将初始NFO技术时长置7秒，核对探测后索引仍7秒，显式重扫后为0；sidecar保留原始0.500000秒及视频流，避免零值断言空覆盖。
- 实际ffprobe为Debian 5.1.9-0+deb12u1，沙箱可见`/usr/bin/ffprobe` SHA256=`0e598fe6d247bfe63757f5c839dc145a166b3f876f2875ca88a306a5b8ec5a58`，PATH=/usr/local/bin:/usr/bin:/bin。前端作业bash-218ca1bf81364181 exit0已收集，300pass/0fail/0cancel/0skip，45.816s，日志`/tmp/emby-go-round33-node.log`；没有新增浏览器/播放器验收。最终Go作业bash-3e001acd82b64056 exit0已收集，日志`/tmp/emby-go-round33-validation.log`：最终源码三次专项14.364s（各一次实际HTTP源读取、一次成功探测/显式重扫、一次失败重启和四次正常停止）、cmd race25.906s、build/vet及无缓存全仓Go通过（cmd13.720s/server98.884s/store56.424s/probe4.550s/stability5.318s/CLI1.354s）。全仓启用固定候选、两类前版制品、真实ffprobe、systemd和隔离tmpfs ENOSPC。父测试race不代表候选子进程race；所有作业按实际退出码收集，早期失败另行保留。最终测试SHA256分别为native_systemd_test.go=`85deecd99d9e0e7a4bea9d1793b4580c324ab7f84614696bc141b0099c3d31be`、native_systemd_real_probe_test.go=`137582670cd8d0a46d908d47aaab6cc0c9e8182ed3aa7076d91e4bbb02ed3e38`，门禁之后未改测试源码。独立只读复核0ae339d0-1af8-40a6-a59e-7959c01f5ca0已返回及关闭，未编辑/构建/重测，核对第四次启动的配置切换、PATH/进程根目录观察、清理顺序、非空索引变更及nofollow后无额外上游请求，当前接入无阻断。可执行文件SHA只是当前命名空间内观察身份，不是来源认证或直接execve跟踪；静态复核不替代执行证据。
- 源码逐项比较§73归档583项，只有三份进度文档和§74/§75沿用测试不同；新real-probe测试不在旧归档中，生产代码与部署样例未变。仍为独立transient目录/UID65534，不安装或修改主机ffprobe/正式emby账户，不修改生产逻辑或样例。loopback请求是已批准可信STRM策略下的正常直接URL探测，不是出站隔离；不据此声称真实远端/播放器/正式布局或M1通过。

## 77. N-01 systemd实际身份的文件读取故障组合（2026-11-06）

**状态：实际服务输入八类组合本地子项完成，最终三次专项/race/完整Go与前端门禁及只读复核均已收集；不能据此完成M1挂载组合门。**

- 本轮在§76第四次启动中追加同一固定候选/实际UID65534的HTTP扫描回归，保留已有探测、停止/重启及清理断言。主机只读核对/var/lib位于/dev/vda1的本地ext4；新增helper将核对实际fixture所在文件系统，不把根用户单测代替服务权限失败。
- 目标覆盖STRM/NFO超限、FIFO及指向FIFO的符号链接、实际不可读文件、普通文件链接正向对照，核对部分成功、旧Movie/actor/分组保护、禁止不完整枚举的删除清理及修复后恢复。实际EACCES是既有扫描失败/HTTP500路径，不拟改成特殊文件的skipped或NFO解析损坏的unreadable_nfo，均以执行结果为准。
- 原实现子任务051148c0-2519-40f0-a8e2-30fd8ee61f78失败且未留下文件/结束报告；未重试派发，由主会话完成夹具。首次专项bash-dd326035273e4c12 exit1已收集，日志`/tmp/emby-go-round34-focused.log`：五类超限/特殊输入通过；测试误要求HTTP500含permission denied，并把进度接口的清理前快照当作最终清理结果，致不可读/普通链接用例失败。只修测试假设：核对root所有0600及既有source read failed摘要；HTTP200读取实际响应的最终清理计数，失败扫描才以进度核对部分结果。并按单场景职责展开函数减少嵌套，无生产修改。
- 修正后专项bash-2443dc0812274974 exit0已收集，6.367s，日志`/tmp/emby-go-round34-focused-retry.log`，八类全部通过；沿用同候选摘要与真实ffprobe成功链路、失败重启及四次正常停止。Node作业bash-beddea7fd39545c6 exit0已收集，300pass/0fail/0cancel/0skip，43.548s，日志`/tmp/emby-go-round34-node.log`。最终Go作业bash-6f5a9cba440e4788 exit0已收集，日志`/tmp/emby-go-round34-validation.log`：同候选三次专项17.798s（24个读取子例，各次同时保留真实ffprobe链路/一次失败重启/四次正常停止）、cmd race34.470s、build/vet、无缓存全仓Go通过（cmd15.712s/server99.470s/store58.186s/probe4.860s/stability5.758s/CLI1.338s）；全仓显式启用两类前版制品、实际ffprobe、systemd和隔离tmpfs ENOSPC。父测试race不表示候选子进程race。最终测试SHA256为native_systemd_test.go=`fd88a9829a18e4b5a1ef980b944595ffc4e9408f5a41c636f693a7ef5cc214b6`、native_systemd_read_boundary_test.go=`af7c5e85f28f4370e5528f5a6ac31c999611f971bdf401e7cc57dac355d43b39`；门禁后未改测试源码。独立只读复核fa165217-5791-4ac4-95a3-db65f1905d52已返回并关闭，未编辑/构建/运行测试/操作服务，当前快照无阻断：核对实际UID/ext4、定向库、非空Movie/actor/两个group基线、私有移除路径、HTTP成功最终结果与失败进度差异及恢复清理。非阻断限制：只测full稳定输入，不含等限/incremental/CD依赖/库外链接；未seed非空收藏/播放/AutoMarks；故障轮健康组即时提交以Success1及提交后计数契约证明，内容在恢复后才最终读取；恢复写回原内容不独立证明新元数据刷新。测试失败日志可能展示进度正文而断言取成功响应，不影响断言语义。
- 不承诺并发增长/替换时序、NAS故障、任意挂载或正式安装；不修改生产逻辑、已批准限额/普通链接支持或可信STRM策略，不创建正式账号、不提交/发布。最后gofmt/diff检查通过，自有emby-go-rehearsal单元和emby-go-systemd目录无残留；五个M1执行门保持未勾选。逐文件核对§73归档583项，只有三份进度文档及沿用shutdown/systemd测试不同，新读取helper不在旧归档中，生产代码/部署样例未变；冻结源码和候选校验文件再次验证均OK。

## 78. N-01 显式候选身份与具名本地制品（2026-11-06）

**状态：显式身份及具名本地候选子项完成，边界/同包原生/兼容/race/全仓Go/前端门禁与独立复核已收集；不是正式来源认证、发布或M1放行。**

- [共同候选入口](<cmd/metatube/native_release_fixture_test.go>)新增三项可选显式预期EMBY_GO_NATIVE_VERSION/COMMIT/BUILD_TIME：三项全空沿用原fixture身份，任一非空要求三项与BINARY/SHA全部齐备；只在入口校验一次，返回具体Image/Version/Commit/BuildTime。保留摘要先于执行、复制同一已读字节、默认固定编译参数、buildinfo/静态裁剪ELF与CLI精确整行核对，不从文件名/CLI输出推导预期，不校验Git来源或版本真实性。
- [重启测试](<cmd/metatube/native_lifecycle_test.go>)将同一已验证身份传给当前用户和UID65534两种重启，HTTP三字段精确核对；[升级测试](<cmd/metatube/native_upgrade_test.go>)只将当前侧version改取该值，旧本地/官方前版身份与固定SHA保持原约束。当前升级HTTP仍只比较version，shutdown/systemd不新增HTTP三字段或启动日志身份断言，仅通过共同入口核对CLI；不把不存在的旧日志断言描述为已替换。
- 默认编译污染回归清空全部五项环境输入；拒绝子进程也显式设置五项，包括空值。十类缺项/坏摘要/不符摘要回归均要求指定错误及spoof marker未执行；另用有效默认静态fixture分别只改version、commit、build_time，要求身份专属失败，避免把profile/摘要错误当作身份校验成功。
- 构建/边界作业bash-8acbef4719584b9b exit0已收集，日志`/tmp/emby-go-round35-boundary.log`，边界及默认隔离合计4.863s。具名本地候选来自§73原私有快照SHA256=`6eaf586dc29056b1d92df13fe9e3ac078d992e50ff3f440d418ddb47ee5e09a2`，不是当前干净Git提交；候选`/tmp/emby-go-m1-identity.Bu37Hr/emby-go` SHA256=`0c44b1e85227fdf380862ef756e7459e90517f9159e2291914e51d4155204696`，CLI精确为`Emby-go m1-local-20261106 (commit snapshot-6eaf586dc29056b1, built 2026-11-06T02:31:32Z)`。commit字段为快照标签而非Git commit，构建参数沿用原profile；同目录candidate-record.txt/SHA256SUMS保留对应关系和校验文件。它替换旧fixture元数据，不改变冻结的生产源码；新验收测试不在原583项归档中，私有/tmp不代表长期/官方产物保存。
- 前端作业bash-b7d7341f7c20479a exit0已收集，300pass/0fail/0cancel/0skip，56.502s，日志`/tmp/emby-go-round35-node.log`。独立只读复核de1341e3-3c3f-4bcd-9316-9f2f72234eee已返回及关闭，未编辑/构建/重测，当前三文件内容无阻断；它们原为untracked，无Git差异基线，不声称历史逐行未变。复核确认输入成套/环境隔离/先摘要/三字段拒绝有效；静态复核不代替具名候选正向运行。少数对称缺项组合未逐案枚举，负向身份回归使用默认fixture。
- 最终Go作业bash-3b5773f1a7264f63 exit0已收集，日志`/tmp/emby-go-round35-validation.log`：原生场景重复两次50.716s，十次场景入口均复用同一具名候选摘要，覆盖两类UID重启HTTP身份、实际SIGTERM、样例派生systemd/真实ffprobe/八类读取及两种前版升级恢复；默认污染/负向回归按契约独立构建。旧fixture不提供三项身份仍兼容，重启专项4.197s。cmd race35.839s、build/vet、无缓存全仓Go通过（cmd18.461s/server101.458s/store59.067s/probe4.816s/stability5.900s/CLI1.929s），启用两类前版制品、真实ffprobe、systemd和隔离tmpfs ENOSPC。父测试race不代表候选子进程race。三份改动测试门禁后未改；最终SHA256：release_fixture=`2b7910f48e9f8fd2ff4f86f0724573b0ab828b3e7e321be122e2236ea104b855`、lifecycle=`4018eb18628997fa3c1116deb82d7f372020905c4b1a9887a0499bdce079746b`、upgrade=`98e80c7319255cacfd353cb7013ba7b4a498995f3bdd04617066a3d041b42afb`。逐项比较§73归档583文件/模式，变化仅三份进度文档、既有shutdown/systemd与本轮三份测试；生产代码/部署样例未变，新候选校验文件OK。正式来源、正式emby账号/布局与完整挂载/安装组合仍未完，五个M1执行门不勾选，不创建正式账号、不提交/发布。

## 79. N-01 只读原生安装静态核验工具（2026-11-06）

**状态：只读工具及隔离布局子项完成，专项/race/build/vet/全仓Go/前端门禁均已收集；不是正式安装或M1通过。**

- 新增[管理员静态核验脚本](<deploy/verify-native-install.sh>)，只使用读取命令/文件属性：主机getent解析emby用户/主组，核对非零UID/GID与主组、样例固定二进制root:root0755及外部预期SHA256、配置目录root:emby0750/配置emby:emby0600/state0750、配置/state挂载ext4及存在时runtime0750。正常目标symlink沿用stat跟随，不冒称路径沙箱。摘要预期应来自独立核对的候选材料，不对被验收文件自算后当独立预期。
- 不执行应用、不打开SQLite、不读取配置正文/秘密，不写文件、创建账号或控制服务。可选绝对暂存根目录仅给文件路径加前缀，账号仍由执行主机NSS解析；不创建chroot，不代表该根目录内运行与正式账户/布局已验收。runtime缺失明确NOT CHECKED，即使静态检查通过也不证明服务停止或启动成功。
- [使用说明](<README.md#原生-linux-部署样例>)已列出工具不能检查的配置语义、DB/schema、CLI/HTTP身份、Unit/Install/有效策略、媒体ACL及挂载、启动/关闭和升级恢复，不把本工具替代现有运行门禁。参数数目/摘要/根路径在入口拒绝；本轮不新增生产防御、锁、队列或框架。
- sh -n与真实坏摘要拒绝已通过；脚本转义以实际字节核对，工具显示双斜线不是源码错误。编写夹具的子任务f98de7ef-0caf-4aa9-b341-2dcf915b9295终态失败，无关闭报告且文件不存在；未自动重试，Lead检查现场后创建[隔离回归](<cmd/metatube/native_install_check_test.go>)。6项参数与12项布局检查覆盖成功/缺失runtime/大写SHA、错SHA/配置模式与owner/runtime模式、非ext4、UID0/主组不符/NSS缺失；真实stat/sha/findmnt，只有getent使用65534身份夹具，non-ext4单负例额外替代findmnt为tmpfs。不修改宿主NSS、不创建正式emby账号或服务，不把替代findmnt当真实tmpfs挂载证据。
- 每次执行前后比较整个夹具树的文件字节/Mode/UID/GID，验证可执行伪应用的marker不存在、配置canary未泄漏；这是终态比较，不覆盖树外/暂态写后恢复、时间戳/ACL/xattr或未构造的链接目标，静态只读数据流另由审查核对。默认布局组skip；Linux显式开启EMBY_GO_TEST_INSTALL_CHECK=1却非root会失败，真实ext4不满足也失败。参数回归不需root。
- 首次专项bash-56f055daf6064d6e exit0（18子例，0.241s，`/tmp/emby-go-round36-focused.log`）；最终Go作业bash-5d30c7abf8fe4adb exit0已收集，`/tmp/emby-go-round36-validation.log`：18子例重复3次0.418s，cmd race38.493s、build/vet、无缓存全仓Go通过（cmd19.857s/server97.404s/store56.110s/probe4.752s/stability5.605s/CLI1.491s）。启用具名固定候选、两种前版、真实ffprobe、systemd、隔离OS ENOSPC及新增安装布局开关；候选SHA校验通过，新增静态工具不执行候选。前端bash-5ecaca6021444914 exit0已收集，300pass/0fail/0cancel/0skip，43.630s，`/tmp/emby-go-round36-node.log`。
- 独立只读脚本复核4b9b18d0-34ef-409b-bd9b-55d19ea062a2正常返回无阻断，未运行/编辑/构建；另夹具复核dee6bcd4-c180-4828-846d-1c62b660ae0d已发完整只读报告、未发现阻断，随后子任务终态失败且无关闭消息，不冒称正常闭合。报告明确上述合成NSS/非ext4替代及终态比较限制，Lead按实际代码和专项核验；不自动重试或扩大范围。
- 工具/夹具门禁后未改，SHA256分别`428ffee4c4aeabd85310679a33761c72f2ff7e77f54f101b42499c448febca90`/`5c6e95657080c1d52a013653d23e800a1adb88b082033c023389e7c1b4002d76`。逐项比较§73原583项归档，变化仍仅既有进度文档及已登记原生测试，冻结生产应用源码/部署service未变；本轮脚本和新测试不在该历史应用归档内。五个M1执行门保持未勾选，正式来源、正式账号/布局运行与剩余挂载组合仍待验收，不提交或发布。

## 80. N-01 增量与CD依赖的实际身份读取组合（2026-11-06）

**状态：同一具名候选在实际UID65534/本地ext4上的24例组合、重复/race及全仓门禁本地通过；子项完成，不是M1放行。**

- 沿用§76–78同一具名候选与实际systemd UID65534/本地ext4组合，在第四次启动中将八类读取边界同时覆盖full/incremental；统一先full建基线，然后指定模式，经真实HTTP执行。NFO/STRM超限、FIFO/链接、root0600及普通文件链接范围不变，不增加生产防御或探测隔离。
- 增加非默认播放进度/次数与收藏状态，故障后及修复后分别核对；健康组在故障扫描结束后、修复之前立即从SQLite查询，不再只靠Success计数和恢复后结果。修复NFO使用变化title验证重建；incremental恢复要求一个changed/一个unchanged，不误把正确跳过当漏扫描。普通链接对照同样携带变化元数据。
- [CD依赖夹具](<cmd/metatube/native_systemd_stack_boundary_test.go>)由独立子任务仅新增专用文件，Lead接入并复核，覆盖full/incremental×CD2 STRM/shared NFO/两者与sidecar并存时各自超限共八例；连同[单片16例](<cmd/metatube/native_systemd_read_boundary_test.go>)为24组合（22故障＋2普通链接正向），不是24故障或CD完整FIFO/权限矩阵。整组Movie/actors/旧扫描基线与非空个人状态保留、healthy修复前实际落库、orphan保护及恢复title变化/清理均有实际断言。full恢复Success/Unchanged为2/0，incremental为1/1，演员仅验证故障保留，不声称恢复时演员发生变更。
- 首次专项作业`bash-bf088a762c6f4229` exit1：逐例重复登录触发现有限流429；这是夹具负载错误，不改生产限流或增加重试。改为套件入口一次真实认证传token，重跑`bash-391d7009849844fb` exit0，24例全部通过6.982s，日志`/tmp/emby-go-round37-focused-retry.log`。旧失败不记为通过。
- 最终作业`bash-8008317670694f6d`已收集exit0，日志`/tmp/emby-go-round37-validation.log`：同一具名候选专项3轮20.009s（72子例，无SKIP），cmd全包race51.912s、build/vet、全部既有opt-in开启的无缓存全仓Go通过（cmd17.181s/server98.945s/store56.761s/probe4.521s/stabilityrehearsal5.374s）；固定候选摘要校验通过。前端作业`bash-6d91d1b3825c4bb7`已收集exit0，300/300、0fail/0cancel/0skip，44.935s，日志`/tmp/emby-go-round37-node.log`，未改前端源码。
- 独立只读复核正常完成，无编辑/运行/构建/服务操作，未发现有证据阻断；明确22故障＋2正向、CD仅依赖超限、title变化不等于演员变化。Statfs magic与ext2/3共用，本次另由实际findmnt确认/var/lib位于`/dev/vda1 ext4 /`，不以magic独自推导严格ext4。最终测试SHA为`f22a0bc7f4c784251f41e846b0071f253fe1f78468cc792c9552f97c24df7731`（单片）与`3fca350df2565d683867a43e1d09a0ca994dd24a71b70ce6eae051508a940151`（CD）；门禁后未改测试。格式/差异及自身unit、/run /var/lib /tmp fixture无残留检查通过。
- 保留限制：服务只作用于本轮唯一私有state路径，不创建正式账号或安装；稳定失败输入不代表并发增长/替换、任意网络挂载或真实播放器成功。生产应用及原生service模板不改，既有五个M1执行门仍不勾选。

## 81. N-01 安装核验的实际身份访问检查（2026-11-06）

**状态：实际身份专项/全仓回归及独立只读复核已收集，本地子项完成；不是正式安装或M1放行。**

- [只读安装核验](<deploy/verify-native-install.sh>)在原有stat/hash/findmnt检查后，通过setpriv降到NSS解析的服务UID/主组、清空补充组，仅用shell test核对程序可执行、配置可读写、state与已存在runtime可读写/穿越。不会执行应用、读取配置内容或创建文件；runtime缺省仍明确NOT CHECKED。依赖util-linux setpriv，管理员按README使用sudo。
- [隔离布局回归](<cmd/metatube/native_install_check_test.go>)新增usr/etc/var/run四种root0700父目录阻断；叶节点模式仍正确，确保不是stat错误触发的失败。实际setpriv UID/GID65534，正例NSS仍是夹具，不创建正式账号。原有配置canary不泄漏、应用执行marker不存在及整树bytes/mode/UID/GID不变断言保留。
- 初次专项发现夹具MkdirAll把中间父目录也设为root0750、正例真实UID无法穿越；第二次修复后发现缺runtime对照连run父目录也没创建。两次作业exit1，不记为通过；仅修测试父目录布局为可穿越，故障例再显式设置0700，不降低实际核验标准。
- 独立只读复核正常完成，无编辑/测试/服务操作，未发现新增权限检查的阻断问题；指出执行marker原位于root0755父目录、降UID后不可写，不能作为独立未执行证据。Lead随后只将marker移入夹具中服务可写state；复核未重读此修正，最终专项/race另行重跑。快照仅覆盖夹具内路径集合/文件bytes/mode/UID/GID，不证明时间戳或瞬时写后还原，也不证明外部账号/服务状态。
- 作业`bash-4680679e6b6149ac`已收集exit0：22项检查三轮0.614s、专项race1.285s、build/vet及INSTALL_CHECK=1的无缓存全Go通过（server126.325s/store81.908s），日志`/tmp/emby-go-round38-focused-final.log`、`/tmp/emby-go-round38-go.log`。此作业在哨兵修正前启动，不能作为最终测试版本证据。
- 哨兵修正后的作业`bash-efd01ab3f66f405d`已收集exit0：最终22项检查三轮0.629s、专项race1.241s、全Go通过（部分包缓存，server92.634s；日志`/tmp/emby-go-round38-reviewed-go.log`），sh -n/差异检查通过；未重跑其它systemd/真实ffprobe/旧版制品opt-in，不复用成当前全矩阵证据。脚本SHA `1599b6a4dbe663158a0a5865707da5bf036861996df2f43e5bc6a06de25cedb3`，测试SHA `8798490f339061f20c7476eeb7d261d15372138e254476564c3a0ae8ae23755a`；此后仅改文档。前端作业`bash-737fd9c203aa4a61`已收集exit0，300/300、0fail/skip/cancel，59.604s，日志`/tmp/emby-go-round38-node.log`。
- 边界：这是主机可见路径/主组的访问判断，不模拟systemd命名空间、补充组/媒体ACL或实际写入成功，也不覆盖并发权限替换；仍不执行应用/打开SQLite/创建账号/控制服务。默认信任STRM及取消探测隔离的范围不变。

## 82. N-01 真实进程重启后的播放会话绑定（2026-11-06）

**状态：最终专项、默认构建兼容、完整门禁与独立只读复核均已收集，本地子项完成；不是M1放行。**

- [重启夹具](<cmd/metatube/native_lifecycle_test.go>)复用原有两次启动/SIGTERM/退出join流程；[播放断言](<cmd/metatube/native_restart_playback_test.go>)在current-user及实际UID/GID65534两种进程身份下，用真实HTTP初始化、认证、创建active/stopped会话及非空播放位置/计数。仅元数据用Store写入已由服务创建的SQLite，不扫描媒体、不更改文件所有权或生产代码。
- 重启沿用原token，不重新认证；逐一发送两个会话的跨影片start/progress/stopped，要求409与PLAYBACK_SESSION_MOVIE_CONFLICT。每次对比playback_sessions/playback_events/userdata/sqlite_sequence/kv全列快照；先验证两会话、一停止、五事件、播放次数1/位置12000000，避免空表相等的虚假证据。最后同片重复Stopped应204，播放行/序列不变；成功事件缓存失效不属于此幂等性承诺。
- 首次作业`bash-830cab7a019e44ff`已收集exit1：两种身份的重启/六冲突核对通过，但成功重复Stopped会使既有kv缓存版本递增，测试错误地要求kv也不变。仅修正正向重复事件的断言，冲突仍要求全部选定表及kv不变；未改生产行为。
- 原helper子任务失败且未落盘，Lead核对文件不存在后实现，未自动重试。独立只读复核已返回无阻断报告（未编辑/构建/测试/服务操作），核对实际绑定优先顺序、选定表快照、服务文件所有权及Close、日志读取均在join后。最终作业`bash-563c098892da4d05`已收集exit0：默认新构建兼容4.365s、cmd全包race74.124s、build/vet及无缓存全仓Go通过（cmd18.148s/server102.225s/store59.379s/probe5.307s/stability4.922s）；同时启用固定具名候选、两个前版入口、真实ffprobe、systemd、OS ENOSPC和安装核验，摘要校验通过。日志`/tmp/emby-go-round39-validation.log`。这是本地组合证据，不补齐正式安装或发布门禁。
- 固定具名候选专项`bash-0a5802fb70a04ec4`已收集exit0，三次完整用例4.192s，六组身份场景、共36次409逐请求对比及六次正向重复停止通过；均复用§78同一SHA。前端作业`bash-383a7d2018bd48b9`已收集exit0，300/300、0fail/skip/cancel，54.118s，日志`/tmp/emby-go-round39-node.log`。
- 覆盖两次启动之间的一次正常SIGTERM重启及管理员单用户HTTP；最终播放快照在第二次SIGTERM之前，不代表第二次退出后的全表复查、强杀/断电、真实客户端、普通用户隔离、系统安装布局或整个数据库的零写入证明。非root运行明确跳过服务身份组。baseline只对元数据种子执行HTTP上报，不声称真实媒体起播；默认信任STRM范围不变。

## 83. 总施工方案收敛与状态校准（2026-11-06）

**范围：用户要求优化施工方案；仅调整文档，不改业务实现，不执行安装/迁移，不提交或发布。整体施工目标与M1尚未完成。**

- [总计划](<dev-plan.md>)从298行的历史流水重整为157行当前计划；保留产品/技术结构及§3正确性边界，稳定原有§4.1/§4.2/§4.4标题与外部锚点。历史证据仍保存在本台账，不复制每轮命令和耗时形成第二份流水。
- 看板区分核心本地完成、收口中、待冻结及未交付；补齐N-01至§82的成果导航，纠正MT-04归属诊断浏览器仍被列待办的旧描述，独立整理入口仍待交付；MT-05身份/真源未冻结、MT-06依赖关系不变。
- M1仅剩A来源/候选、B正式账号/布局运行、C停写升级/匹配恢复、D增长/替换差距、E同包汇总五个执行包，各自写明已有证据、缺口、退出标准和依赖。先A与D差距盘点，授权环境具备后B→C，最后E；不再用重复局部测试替代正式安装缺口。
- [验收矩阵](<docs/m1-release-matrix.md>)与总计划A–E对齐，仍为一项范围批准、五项执行未关闭；[原生方案](<docs/native-roadmap-design.md>)把政策已定/执行待验与后续批次未冻结分开；[MetaTube方案](<docs/metatube-alignment-design.md>)标明历史冻结记录与当前状态的区别。不重开可信STRM隔离，不新增NAS/平台承诺，不重复请求D-M1-A/B/C批准。
- 补齐§82已收集的默认构建、race/build/vet/完整Go结果，移除“后续补记”的滞后文字；没有为文档整理重跑Go/Node或挪用旧门禁宣称新增代码验证。
- 已执行本轮五份文档本地链接/标题锚点/行号与状态断言、差异空白检查；独立只读文档复核正常结束、无阻断项，确认五包依赖、已批准/未执行边界及MT状态一致。复核未编辑、执行代码或测试，未重新核验历史制品/日志，也未读取基线作逐字差异审计，不能代替实现审计或运行验收。M1放行、生产操作及提交/发布授权仍独立，未因本次优化改变。

## 84. M1-A源码复现与M1-D窗口差距收口（2026-11-06，本地补证完成，正式门禁未关闭）

- 按总计划A与D并行：只读盘点确认增长/替换已有确定性snapshot、full/incremental复核测试；原生24组合是先造故障再发扫描，不冒充扫描中变化。剩余正式emby身份/ext4/最终native二进制窗口组合仍未执行，不扩NAS或CD笛卡尔积。
- 捕获当前589个正常文件、显式记录两个已删除的Docker部署文件；原始源码清单来自tracked+非忽略untracked，忽略docs/reports/tools、私有配置/运行数据不混入源码包。归档不是整个工作区备份，也不是已审签可发布源码。
- 从独立解包源码、禁用开发环境GOFLAGS/GOEXPERIMENT/GOENV后按既有CGO0/linux-amd64-v1/trimpath/stripped参数重建，逐字节SHA仍为`0c44b1e85227fdf380862ef756e7459e90517f9159e2291914e51d4155204696`，CLI三字段保持`m1-local-20261106` / `snapshot-6eaf586dc29056b1` / `2026-11-06T02:31:32Z`。快照标签指原生产源码基线，不伪装Git commit。首次源码捕获把已删除tracked当文件而失败，第二次遇主机Python不支持extractall(filter=...)；均在构建/执行前失败，改为显式墓碑记录及主机tar后成功，未改业务代码。
- 原始复现产物在`/tmp/emby-go-m1-source-closure.cbpZ0M`，作业`bash-68c5b0db6df6433a`已收集exit0；包含源码tar、逐文件SHA/模式/大小manifest、HEAD/status、二进制及校验文件。后续新测试驱动与此捕获分别记录，最终交付来源及具名复核仍不能据此自动勾选。
- 不新增生产hook、重试或防护；只补已有八个代表窗口的非空播放/收藏和恢复闭环：full/incremental下STRM/NFO增长四例、STRM/NFO替换FIFO四例。它们执行同源码scanner测试，不声称最终native二进制本体、实际emby账号或安装布局验证。STRM/NFO已索引场景另增加非空个人状态，missing-NFO的pending控制仍保留原语义；source/nfo替换四例使用真实非空CD2、healthy即时落库及orphan保留/恢复清理，新标题证明确实发生恢复更新。
- 成功复现材料已从临时目录复制至工作区私有[来源记录](<reports/m1-source-closure.mk20XO/SOURCE-RECORD.md>)及其目录，源码包SHA`f522f06851c992d58fcd0cda1edb78f6e539b5e2f741cf0d558f99db2e8b26b1`、manifest SHA`a846ebd18b4674a1b1e02e243218baf2ce8dfec0fab737539a9e46a43c788ed5`；不混入可公开发布物。归档源码自己的restart驱动对复现候选运行通过，两UID、无SKIP、1.732s，见[日志](<reports/m1-source-closure.mk20XO/archived-restart.log>)；明确不是实际emby账号。
- 后加两个测试文件单独保存为`window-test-driver.tar`（SHA`ab2e1602000609a50fb9b285093907845087ef9937934a927de5760b151d94b7`）及逐文件校验，不能称它们已在589文件捕获中。第一次并发编译撞上协作文件半编辑态而未通过，待子任务正常结束后再统一编译；第一次完整专项又发现给missing-NFO pending片调用indexed-favorite返回no rows，仅修正测试播种范围，未改生产边界。子任务自己的full恢复计数首次误按Updated1，按实际full提交口径改为2后专项已过；最终作业`bash-67bc99ba6e4d4356`已收集exit0：[完整日志](<reports/m1-source-closure.mk20XO/window-validation-final.log>)中专项5.442s、scanner全包race267.497s、go vet及全仓go test通过（scanner18.089s/cmd10.421s，其他部分缓存），另行go build通过。无新增前端/生产代码，未重跑Node、浏览器及systemd/真实ffprobe/旧版升级/OS ENOSPC等opt-in整套组合；默认SKIP与缓存不当新增正式证据。差异空白检查通过。
- 独立只读复核已正常结束、无本地补证阻断项：实际核对589个tar成员与manifest的内容SHA/模式/大小、两项删除墓碑、候选及后加驱动摘要，并以原tar文件作为两项untracked测试的差异基线；未重新构建或执行测试。确认普通变化失败/超限跳过/incomplete及missing-NFO控制不混淆，新增healthy提交时按旧group逐项保持，而不错误要求全局版本不变。actor及growth parts主要是fixture baseline比较、没有独立非空数量门槛，恢复结果也非穷尽每个字段，不夸大成完整初扫健全性证明；replacement CD2及新增个人状态有明确非空门槛。
- 下一B的只读前置确认：本机没有emby NSS用户/组，样例二进制、配置、state/runtime及已安装unit路径均不存在；不因此自行创建。正式账号/布局、真实停写与匹配恢复及最终native窗口仍须授权环境，A–E五项仍未关闭。后续执行权限询问中，用户选择提供独立隔离验收环境，而非授权操作当前主机；目前尚未提供该环境的连接信息与具体操作授权，不创建宿主账号或路径。

## 85. M1-A离线来源材料核验入口（2026-11-06，本地工具完成）

- 未收到独立隔离环境交接，不连接其他主机、不创建当前宿主emby账号/正式路径、不操作服务；继续只做A包可独立推进的交接材料，不重跑D已完成窗口或添加安装替代证据。
- 在私有材料目录新增[离线核验脚本](<reports/m1-source-closure.mk20XO/verify-bundle.py>)，Python3.11+，只读核对源码tar、manifest、原候选及后加窗口驱动四项固定SHA，再逐一校验589个tar普通文件的内容/模式/大小与两项驱动内容；不提取、不执行候选、不联网、不构建。脚本及随包摘要不是数字签名或独立信任根，必须对照可信交接记录使用。
- [实际核验日志](<reports/m1-source-closure.mk20XO/offline-verification.log>)：原包通过；临时副本源码tar翻转一字节、替换候选为可写执行标记脚本两项均失败退出且正确分类拒绝，未出现候选执行标记；四项原始材料的内容摘要与模式前后不变，临时夹具清理完成。没有声称整个文件系统/时间戳/ACL无变化。
- 新核对明确589路径含生成缓存`.github/scripts/__pycache__/test_shard.cpython-311.pyc`。保留原始归档不改其SHA，来源记录显式标注：这不是已净化的Release源码；正式交付前决定是否排除缓存，并对新包重新关联来源，而不是悄悄覆盖现有复现基线。
- 同步[来源记录](<reports/m1-source-closure.mk20XO/SOURCE-RECORD.md>)与看板。本轮只新增私有材料核验脚本和文档，没有业务实现、Go测试或前端变更，因此不冒称重跑Go/Node/原生opt-in全矩阵。A正式交付处置/签收以及B–E仍待执行，总体目标保持进行中。最终Python语法解析、三份材料/计划中的本地目标存在性、M1五项执行门禁仍未勾选及git diff --check均通过；核验器SHA256为`5c8cc997f258d864dd12280fde6fa11c14dafc1fbf43ca3d987b96df09cdfbf2`。

## 86. M1-A净化私有来源与同候选复现（2026-11-06，本地验证完成）

- 原589项来源包和两项窗口驱动摘要重新核验通过；保留原包不覆盖，另建[净化来源记录](<reports/m1-clean-source.u2alm2hr/SOURCE-RECORD.md>)。新包仅删除已发现的Python生成缓存，并合入两个已验证窗口测试；588项，无其他文件内容/模式或生产源码变化，保留两项tracked删除墓碑。
- 新源码tar SHA256 `4a946143fe298c256838deb60167f6becd3201ef38b8352d10ee133108cef26b`，manifest SHA256 `994a3c4bb467025eb4776df9b400a2683ddce7e858881bb439d132d0cfae145a`；机器可读父包/驱动关系见[provenance](<reports/m1-clean-source.u2alm2hr/provenance.json>)，[差异核验](<reports/m1-clean-source.u2alm2hr/archive-delta-validation.log>)逐项确认内容/模式/大小及准确变更集合。
- 首次在仓库内解包目录构建遗漏显式关闭VCS注入，Go继承父Git的dirty模块版本与vcs字段，导致SHA为`7c734dd733d946c5a0beabc4be2025c65ed221860638e87d6cf1664fabf71a9c`，校验失败退出1；保存失败日志/产物并明确禁作具名候选。调查后仅增加`-buildvcs=false`，不改源码，新建产物与原候选SHA `0c44b1e85227fdf380862ef756e7459e90517f9159e2291914e51d4155204696`及CLI三字段完全一致。
- 从净化归档自身源码执行三个已有增长/替换专项，`-count=1`通过，scanner合计5.518s；[重建及专项日志](<reports/m1-clean-source.u2alm2hr/rebuild-window-validation-vcs-off.log>)退出0。归档自身[全仓vet/test](<reports/m1-clean-source.u2alm2hr/archive-go-validation.log>)已收集退出0，各包无`(cached)`，scanner20.619s、server101.305s、store58.334s；不是正式opt-in验收。八项来源/制品/日志/构建信息的[校验清单](<reports/m1-clean-source.u2alm2hr/SHA256SUMS>)全部通过，目录权限0700。
- 独立只读复核正常结束，在本次私有来源一致性范围内无阻断，不构成正式Release/M1签收：逐成员复核589/588项、准确三处差异、驱动与全部八项摘要、新旧候选逐字节一致；静态解析两产物buildinfo与元数据记录吻合。未改文件、未提取、未执行候选、未构建/测试，不独立重证历史构建因果，不扩展B–E；八项摘要不代表全目录完整性或外部可信签名。
- 新包仍为私有派生，不是当前工作区完整备份、Release或公开分发授权；文档仍保留父捕获时版本，忽略材料不擅自加入。目录0700，无宿主账号/安装/服务操作，无提交、发布或候选版本变更；未重跑race/Node/浏览器/正式native矩阵。A具名签收/交付范围与B–E继续待执行，独立隔离环境连接及具体授权仍未提供。

## 87. MT-05演员身份与资料冻结前草案（2026-11-06，仅设计）

- M1本地来源已收口，正式环境尚未交接；按总计划§4.3/§4.5允许的并行范围推进MT-05设计，不堆重复M1局部测试，不擅自推进安装。新增[冻结前草案](<docs/mt05-actor-identity-design.md>)并同步总计划/对齐方案；未改生产代码、契约、schema或既有候选。
- 独立只读盘点已正常结束，未改文件、构建/测试或联网。确认[姓名主键与关系](<internal/store/store.go#L235-L237>)会合并同名、改名换键；NFO已有MetaTubeID，但[扫描](<internal/scanner/scanner.go#L303-L307>)未投影、[刮削生成](<internal/scraper/apply.go#L431-L435>)只带姓名并可覆盖旧actor块。因此不能把字段存在、历史孤名或普通full扫描当持久身份/删索引恢复证明。
- [头像匹配](<internal/scraper/avatar.go#L273-L285>)不是任意搜索首条，而是第一个归一化精确姓名/别名命中；多个精确候选仍按顺序选择，再按姓名向全部参演NFO扩散。[旧PersonIds](<internal/server/library.go#L1660-L1685>)则编码/解码姓名，随后仍为姓名子串筛选，不是精准演员ID。仅记录本批设计必须覆盖的调用链，不顺手改旧行为。
- 建议持久演员sidecar＋影片NFO稳定引用，SQLite保留投影；只为建议，尚未决定路径/格式/跨库共享或新接口。三项待冻结：真源与恢复包；稳定身份与历史迁移；来源、人工确认、冲突与旧Person兼容。先有真实详情fixture，再补正式契约和迁移；未知旧NFO不猜身份，不自动合并或整库改名。
- 草案列出同名/更名/旧NFO/历史混合行、删索引、NFO覆盖与锁、头像副本/tag、增量资料变化、真源损坏、冲突、部分成功、备份回退的验收预期；这些是待实施测试，不是本轮PASS。本轮只做文档与证据检查，不重跑Go/race/Node/浏览器；M1与MT-05均未验收放行。最终校验通过：75项本地路径/章节/行号引用，三项草案决策及明确未批准/未实现/未迁移标记，五项M1执行门禁仍未勾选；新草案与本节无行尾空白，git diff --check通过。父会话复查了扫描投影、姓名头像匹配、影片刮削生成、PersonId编码/解码、头像跨库版本查询及备份筛选等关键源码片段。未将此设计文档混入既有冻结候选来源包。

## 88. MT-04独立本地字幕标记整理入口范围草案（2026-11-06，仅设计）

- 按总计划允许的并行范围推进真正缺少的独立入口，新增[范围与确认草案](<docs/mt04-organizer-entry-design.md>)并更新看板/对齐方案；未新增路由、生产代码、迁移、真实媒体操作或候选。D-2/A、schema18和已通过归属诊断浏览器不重做。
- 独立只读盘点正常结束，无改文件/构建测试/联网；父会话复查关键源码。现[刮削Apply](<internal/scraper/apply.go#L82-L162>)会翻译、下载图片、写全元数据及刮削结果，远端preview/inspect也不是本地预览。独立整理只能复用窄标签合并、归属持久化和非破坏性单片索引，不能伪造上游资料调用完整刮削或编辑后人工标记流程。
- 新入口草案收敛为三项待冻结：显式单逻辑MovieID且STRM/NFO可定位；仅标签/归属/索引、不重绘旧海报（默认只补缺失，覆盖须明确选择）；只读预览、依据变化重确认与真实部分成功反馈。不扩展全库任务、媒体探测、远端图片写入、额外权限或新队列。已有海报角标依赖远端图片下载，标签已整理不能冒称海报已同步。
- [目录枚举错误](<internal/scraper/mark.go#L53-L60>)在当前附加启发式中返回false，不能直接拿来证明字幕缺失并撤销标签；本稿记录新撤销入口的必要输入边界，不顺手改旧判断。当前仅SourcePath，不新增AdditionalParts字幕聚合；无CD1不擅自合组。NFO通道有同kind特例，新kind不能默认独占；门后重读归属、路径锁内校验与写入及关停准入均保留。
- 现[Movie JSON](<internal/store/store.go#L87>)为auto_marks而契约文字称AutoMarks，仅记录待校准偏差、不造第二字段。新增验收矩阵覆盖字幕新增/删除/不可读、人工文字/锁/补缺失、CD、预览变化、通道/关停、局部提交失败及无远端/图片/状态副作用；均为未来要求，未记为本轮PASS。最终67项本地路径/章节/行号引用、三项待冻结决策及明确未实现/无新路由状态核验通过，五项M1门禁未勾选；新文档/本节无行尾空白，git diff --check通过。首次核验退出1是脚本误要求“不重绘”字面串，而草案实际写“未重下载海报，旧图角标可能未同步”；纠正校验表达后通过，无产品错误或补救改写。无Go/race/Node/浏览器重跑，M1与MT-04均未验收放行。

## 89. MT-04推荐范围确认后的独立整理入口（2026-11-06，本地实现及综合验证完成；未正式放行）

- 用户确认按推荐方案实施且无需再次询问；冻结D-MT04-A/B/C：管理员单逻辑影片、本地只读预览后显式确认、默认补缺失/显式覆盖，仅改标签/实际归属/单片索引，不重绘图片。不授予真实媒体、生产服务、迁移、提交或发布权限；N-02原型暂缓。
- 先补[正式入口契约](<API-CONTRACT.md#481-独立本地字幕标记整理入口mt-04>)，校准既有只读JSON字段名auto_marks，不新增schema。新增后端预览/确认、既有NFO通道同类独占、路径锁内原文摘要比较及窄标签写入；确认读门后归属，文件提交后先记录归属，再处理取消和非破坏性单片刷新，不调用完整刮削或人工编辑后处理。
- 底层与前端分离写域并行实施；底层限定NFO更新/字幕检测及专项，界面限定详情面板/样式及Node专项，父会话负责HTTP/契约/路由与综合复核。底层普通与race、专项三次通过；父会话HTTP专项首次2.929s，整合归属展示/空白标签后4.366s，再补主片/CD2共用NFO后5.359s均exit0。组件Node15/15、真实Chromium375/667横屏/1280键盘/焦点/44px/长路径通过；组件API为fixture，不替代真实HTTP全栈。最终综合结果及限制见下，不将报告覆盖率当作验收。
- 独立只读复核正常结束，不改文件或运行测试。确认一处真实P2：索引保留原始标签空白，窄整理计划已trim，严格比较造成受保护标签无变化仍反复刷新并误报partial。已在两处索引核对统一逻辑标签比较，不改写受保护NFO原文；新增普通/空白标签×索引正常/陈旧四例，首次真实刷新后第二次无变化，manual/刮削时间与NFO字节保持。preview的previous_owned为当前实际归属、owned为拟提交归属，数组均非null；indexed只证明标签，不承诺其它元数据同步。
- 取消新增真实SQL trigger专项：写前root取消503不写；NFO已保存后归属成功或失败时取消均保留独立错误、未刷新索引，失败事务回滚且通道释放。子专项普通0.853s、race三次30.209s均exit0；不模拟浏览器断链/进程终止或授权后提交前的极窄窗口。已有HTTP实际DB归属/索引/双失败与NFO原子写失败分别核对部分提交，旧预览在NFO/字幕/归属/主片/模式/分段变化后409且无新写入。
- 测试开发一次编译失败仅为用错既有MarkIndexedManual参数与不存在的StatusManual常量，按真实(sourcePath,nfoPath)/manual修正后专项通过；不属于生产错误。另一次文件陈旧版本编辑被拒绝，重读后重基合入，无覆盖他人修改。
- 全量前端回归315/315、0跳过/0失败通过，node语法通过；底层race中nfo18.554s、scraper复用缓存。真实App/实际app.js/HTTP/NFO/SQLite浏览器专项10.710s exit0，375/1280×成功/真实归属SQL失败四组均PASS、各TAP pass1/fail0/skip0。验证真实登录、三次只读预览与一次显式覆盖确认、人工标签/图片字节保持、partial持续提示/禁止自动重试和普通用户两接口403；非管理员未单独做浏览器界面，图片不包含视觉角标验证。首次全栈fixture误期望默认模式修改人工标签，按已批准补缺失语义改为显式覆盖后通过，未改产品迎合测试。
- 首次综合build/vet通过而全仓Go失败于既有管理路由权限清单：新增preview/confirm两条未分类，精确报错unclassified management write；已按实际语义登记request-snapshot/admission，不删除清单断言或放宽权限。保留[初次日志](<reports/mt04-entry.zbpmjX/go-gates.log>)；失败发生在race之前，未把该轮未执行race记为PASS。修正后[权限清单及入口专项](<reports/mt04-entry.zbpmjX/route-policy-validation.log>)4.784s通过；[最终build/vet/test](<reports/mt04-entry.zbpmjX/go-gates-final.log>)全部通过，server98.369s，其余部分包缓存；[入口server race](<reports/mt04-entry.zbpmjX/server-race.log>)65.853s exit0。两轮综合job均已收集，失败记录保留，未自动复用一次失败当成功。
- [使用说明](<README.md#本地字幕标记整理>)已补管理员入口、补缺失/覆盖、仅主片证据、部分成功/重扫不能恢复归属及旧海报不变提示；更新总计划和对齐方案为本地实现及综合验证完成。初次文档核验误从总计划查五项checkbox而非M1矩阵，脚本纠正后256项路径/章节/行号及五项未关闭门禁检查通过；非产品或文档门禁被意外勾选。最终310项本地路径/章节/行号及五项未关闭M1门禁核验通过；gofmt和JS语法通过。末次diff-check发现新增README末尾多余空行（exit2），删除该空行后复核，无业务代码变化。
- 本批本地入口及验证完成，不等于M1正式验收或发布：无新候选、来源包、原生/systemd/真实播放器重验；新增源码仍未纳入旧SHA，M1 A–E不勾选。后续需冻结当前来源/候选并在已选独立隔离环境获得连接及具体授权，才能继续正式组合验收；MT-05仍是未批准草案，N-02原型未实施。

## 90. 纳入MT-04的新私有来源与候选（2026-11-06，私有复现完成；未正式签收）

- 按M1 A实际缺口捕获当前非忽略普通文件597项，对比§86净化588项为9新增/11修改/0删除；9项均为MT-04实现与驱动，11项为入口接线、权限清单、界面及公开契约/说明/进度更新。只排除原Python生成缓存，两个Docker tracked删除墓碑保留；未把忽略docs/reports/tools/src、运行时或私有配置自动纳入。正式交付范围及具名签收仍待定，不以私有捕获代替公开分发审查。
- 新材料位于私有0700目录[候选材料](<reports/m1-mt04-candidate.ceid7hr4/build-identity.json>)；[源码tar](<reports/m1-mt04-candidate.ceid7hr4/source.tar>) SHA256 d6f4051af03110cebc4a8144f1d367b88eda9a49b9a19f900f9771a71c0fdcbc，[逐文件清单](<reports/m1-mt04-candidate.ceid7hr4/source-manifest.json>) SHA256 bda9be72cd261cc87d209b1c08277f41a072fd063a27cd5a13c4b3bd14c3c1a4；捕获后逐成员类型、名称、内容、模式核对597项通过。旧包不改，文档为捕获时内容，后续§90实时记录不回填来源包。
- 实际私有身份version=m1-local-mt04-20261106、commit=snapshot-bda9be72cd261cc8、buildTime=2026-11-06T13:47:19Z；snapshot是manifest身份标签，不是Git commit。本轮实际Go1.27.2，固定linux/amd64/v1、CGO0、GOENV/GOFLAGS/GOEXPERIMENT/GOWORK、-buildvcs=false/-trimpath及三项ldflags，避免祖先Git注入改变身份。驱动使用PATH中的go、未强制验证版本，记录本轮工具链不等于以后运行工具链已锁定。
- [构建与验证日志](<reports/m1-mt04-candidate.ceid7hr4/build-and-check.log>)exit0：597项归档核验后写两个独立目录，双构建逐字节一致；新[候选身份](<reports/m1-mt04-candidate.ceid7hr4/candidate.json>)SHA256 b9cadda1cde0e2eecc3c8f5ab12298d7cad87f4e08c595484b747269f80c5090，CLI三字段及static/CGO0/trimpath/buildinfo一致。与旧0c44b1…候选区分，不给旧候选补记MT-04覆盖。
- [归档Go验证](<reports/m1-mt04-candidate.ceid7hr4/archive-go-validation.log>)vet/all test通过，日志各包无(cached)，server110.547s、scanner22.118s、store61.253s。[同候选真实重启](<reports/m1-mt04-candidate.ceid7hr4/candidate-restart.log>)固定新SHA/三字段、未重建替身，两组current-user/数字service-account均PASS、无SKIP，2.138s；保留播放绑定及六项冲突。不是正式emby NSS/路径/Service或实际播放器证明，本轮未重跑包内race/Node/HTTP浏览器及其他原生opt-in；§89历史结果不冒充本包全门禁。
- 独立只读复核正常结束，来源捕获一致性无阻断：旧588/新597路径/顺序/mode/size/hash一致，精确9新增/11修改/0删除，缓存/墓碑/忽略范围正确、生产源码与工作区一致。仅内存读取tar和静态驱动核对，未构建、执行、解包、测试；不替代实际运行结果或正式签收。[来源记录](<reports/m1-mt04-candidate.ceid7hr4/SOURCE-RECORD.md>)及[SHA256SUMS](<reports/m1-mt04-candidate.ceid7hr4/SHA256SUMS>)关联15项制品/身份/驱动/实际日志，全部复验通过；不覆盖展开源码树、不是签名或独立信任根。
- 最终247项本地路径/章节/行号引用核验、git diff --check及15项制品哈希复验通过；来源包与当前工作区仅两份实时进度文档内容变化，生产源码一致。首次文档校验误把矩阵§55已批准政策复选框当正式放行，触发AssertionError；改为只核对具名A–E五项均未勾选后通过，无产品或政策更改。
- 未安装或更换正在运行的程序、未动账号/服务/真实媒体/迁移、未提交发布；M1五项正式门禁不勾选。独立环境连接/具体授权仍缺；A后续为正式交付范围/具名来源签收与最终候选放行，B–E尚未完成。

## 91. D包最小窗口差距与隔离执行交接（2026-11-06，只读盘点完成）

- 新增[八代表窗口与执行交接](<docs/m1-d-window-handoff.md>)，并同步总计划D行和M1矩阵。固定§90新候选SHA/三字段、597项manifest与两份测试驱动摘要；不改封存来源或二进制，不把本页追加到旧来源后冒称原始捕获。本轮没有重跑局部窗口、稳定24、Go/race/Node/原生或浏览器门禁。
- 独立只读盘点正常完成，未写文件/构建/测试/联网；父会话复查关键调用链。八代表为full/incremental×source/NFO×增长/FIFO替换：增长source实际改CD2，替换source改CD1，NFO均为共享文件；增量预先改CD1避免fast path。源码窗口是[第二次context.Err触发](<internal/scanner/read_limits_growth_test.go#L83-L98>)，处于[首次stillMatches后、health内、第二次stillMatches前](<internal/scanner/incremental.go#L230-L272>)，已有个人状态、healthy即时提交及恢复清理断言，不重复计控制case或snapshot矩阵。
- 两个不能跳过的原生差距已落到证据：现[单片驱动](<cmd/metatube/native_systemd_read_boundary_test.go#L162-L212>)及[CD驱动](<cmd/metatube/native_systemd_stack_boundary_test.go#L173-L180>)先造输入再扫描，稳定24不能证明scan-in-flight；[systemd驱动](<cmd/metatube/native_systemd_test.go#L372-L379>)硬编码User/Group65534，而[实际HTTP入口](<internal/server/admin.go#L438-L442>)使用rootCtx/进度回调，没有源码自定义context握手。只固定候选或轮询progress并发改文件不能同时补齐窗口和实际emby身份。记录“未执行/未证明”，不伪记PASS。
- 最小交接包含候选及驱动身份、已批准ext4/实际账号/正式布局与媒体ACL、每例窗口命中证据、故障/恢复计数、旧逻辑ID/分段/演员/个人状态与healthy/orphan结果、原始命令/限制及具名复核。没有扩NAS/CD全笛卡尔积，没有生产钩子/ptrace/FUSE/新框架；仍需独立环境连接与具体账号/路径/Service/测试媒体操作授权，或管理员按交接执行并返回证据。D未签收，A–E五项仍未关闭。
- 最终只读核验通过：候选SHA/三字段、597项manifest摘要、两份当前/归档驱动逐字节一致、八case表达式、五个候选固定变量、15项封存材料哈希、259项本地路径/章节/行号引用、A–E五项未勾选及git diff --check。无业务代码、账号/服务、真实媒体、迁移、提交或发布操作。

## 92. 默认root部署与Web自升级需求（2026-11-06，政策更新；待实现）

- 用户明确决定程序默认部署在 `/opt/emby-go`，直接使用root，不使用emby账号。覆盖此前默认emby身份及分散安装目录的方案；停止为旧emby部署模型补正式身份验收。同步总计划及M1矩阵，并标记§91交接中的旧账号要求已被覆盖；历史测试和已封存候选不改写。
- 新增Web自升级需求：打开页面自动检查更新，左下角版本号处提示，点击弹窗由管理员确认，下载对应平台完整安装包、校验、切换程序并重启。设计方向为首装sh脚本管理初装和systemd，应用安装更新后正常关闭、由systemd重启，不创建emby账号、不配sudo、不增加常驻特权代理。后续用户明确收敛布局：正式程序直接为 `/opt/emby-go/emby-go`，不保留上一版，不创建releases/backups目录，不使用current链接。升级只在根目录暂存新文件，下载校验完成后原子替换，再正常关闭由systemd重启；不原地覆写运行中的二进制，不自动备份或降级。覆盖此前保留旧版和版本目录的建议，不删除既有手动备份功能或历史材料。实现与验收仍待施工，不能称已交付。
- 本轮仅更新文档；现Service模板仍为旧布局/emby账号，尚未适配。未修改系统服务、安装路径、运行身份、业务代码或真实数据，未构建测试、迁移、提交或发布。root与可信STRM组合意味着媒体探测使用root进程可访问的网络和文件权限，不再声明隔离边界。

## 93. 完成归档与DeepSeek新窗口交接（2026-11-06）

- 按用户要求，将MT-01—04已实施功能、N-01可靠性实现及来源/候选本地验证整理为[完成归档](<docs/archive/completed-2026-11-06.md>)。主计划移除已完成的逐项范围/流水，保留联合发布、真实上游覆盖及M1未签收事项；不是把本地完成冒称正式放行。产品边界及§3正确性约束不删改，历史台账与封存制品不改写。
- 新增[DeepSeek交接说明](<docs/deepseek-handoff.md>)：最新root＋单二进制平铺部署、Web自升级需求、无旧版/releases/backups/current、直接ffprobe、未清理输入组件、已有代码入口及未交付项。说明旧Service模板尚未适配，不再为旧emby模型补验；核查旧64MiB下载组件并提出删除影响，不擅自清理其它出站策略。归档不改变正式验收或数据恢复政策。
- 交接盘点工作区为63项tracked修改、2项tracked删除、98项untracked，共163项；不是新提交，也不能把已有改动都归本轮。docs/reports被git忽略，另一个窗口需使用同一工作目录，换clone/机器必须显式同步未提交源码及必要文档，私有材料不自动公开。新窗口先读交接/主计划/编码红线，再核查当前事实，不先通读所有历史日志或重跑全部旧门禁。
- 本轮只编辑文档，未实现U-01、删除C-01、操作真实服务/媒体/数据、迁移、提交或发布；Go/race/Node历史结果只存归档，未在本轮重跑。最终277项本地路径/章节/行号引用、归档与待办分离、五项正式门禁未勾选、git diff --check通过；主计划§3与封存来源逐字一致，571项Go/JS/CJS/CSS源码与上一候选一致。首次核验退出1是校验脚本误以为A–E标签带Markdown加粗，实际为普通文本，修正表达式后通过；没有产品故障或为通过校验改门禁。同步矩阵表内遗留旧emby条款为已批准root/平铺政策，不改历史结果。

## 94. U-01 仓库侧实现：root平铺部署与Web自升级（2026-11-06，未实机安装）

- 新增 `internal/upgrade`：`Check` 查 GitHub `releases/latest`（10 分钟进程内缓存，失败返回错误而非「已最新」），按 `emby-go-<goos>-<goarch>` 与 `SHA256SUMS` 选资产；`Begin` 只接受刚检查到的标签；`Run` 下载到可执行文件同目录临时文件（限制 200MiB、强制 https、边下边算 SHA256、`Sync`），校验 `-version` 后 `chmod 0755` + `os.Rename` 原子替换，再发一次重启信号。失败置 `failed` 并删除临时文件，旧程序不动；不建 backups/releases、不自动回退。
- 新管理端点 `GET/POST /api/admin/update`、`POST /update/apply`、`GET /update/status` 落在既有 requireAuth/requireAdmin/审计组；apply 需 `version` 等于刚检查到的标签，否则 409，客户端不能指定 URL/路径；202 后由关停门登记的后台 worker 执行，复用 `BeginShutdown`/`WaitBackground` 的有界关闭。`main` 收到重启信号后以 0 退出，`Restart=always` 拉起新程序；替换前失败则继续运行旧版本。
- 前端左下角版本号改为按钮：管理员页面打开静默检查并显示「有新版本」徽标，点击弹窗显示当前/最新/Release 说明；确认后轮询 `/update/status`，失败在面板内显示原因；`restarting` 或请求失败转 `/healthz` 恢复检测，再读公开 `/api/version`，只有实际版本等于目标或「目标-后缀」才显示成功，并更新版本号、给「重新加载页面」。非管理员版本号只读。
- 部署材料：`deploy/emby-go.service` 改为 User/Group=root、`WorkingDirectory=/opt/emby-go`、`ExecStart=/opt/emby-go/emby-go -c /etc/emby-go/config.yaml`、`Restart=always`、`ReadWritePaths=/opt/emby-go /etc/emby-go`；新增 `deploy/install.sh`（root、架构映射、下载+SHA256SUMS 校验、临时文件 mv、配置不覆盖、旧 `/var/lib/emby-go` 只提示不迁移）；release workflow 把 install.sh/Service/config 样例与二进制同批上传。README、API-CONTRACT §4.11、dev-plan 同步；`verify-native-install.sh` 明确标注仍按退役 emby 布局、未适配。
- 测试适配：`native_deploy_test.go`、`native_systemd_test.go` 改断言 root/`/opt`/`Restart=always`，沙箱夹具自建数据/运行目录（不再依赖 StateDirectory/RuntimeDirectory），迭代改为验证正常退出后 systemd 拉起新进程；管理路由权限清单登记 check=request-snapshot、apply=admission；新增 `admin_update_test.go` 与真实 Chromium 回归（假 Release，覆盖徽标→弹窗→下载失败原因→Escape 关闭），浏览器测试在无 `CHROME_BIN` 时跳过。
- 本地门禁：`go build ./...`、`go vet ./...`、`go test ./...` 全通过（server 约 102s），`go test -race ./internal/upgrade` 与 `./internal/server -run TestAdminUpdate` 通过；`node --check app.js`、update-*/version-* id 与 CSS 类交叉核对通过。未在真实 systemd 安装或执行升级，未替换当前服务；仍无同候选 A–E 证据或新发布包。
- C-01 复核（未删除）：`internal/probe/input.go` 的 `downloadInput`/`readInput`/`maxInputSize`/`inputHTTPClient`/`errInput*` 仅被 `input_test.go`、`input_lifecycle_test.go` 引用，`pipe_input_boundary_test.go` 只依赖 ffprobe 管道夹具，生产零调用；删除影响仅限该组件与两份专属测试，不改变 STRM 直接交 ffprobe。
- 未执行 `install.sh`、未改写 `/opt`/`/etc`/systemd、未操作真实媒体或迁移数据、未提交或发布。工作树仍含此前未提交修改，本轮新增均为未跟踪/未暂存。

## 95. U-01 收尾：真实发布证据、失败关闭与成功路径回归（2026-11-06，未实机安装）

- 核查真实 Release：`releases/latest` 为 `v2.2.6`，仅含 5 个二进制资产，没有 `SHA256SUMS`，也没有 install.sh/Service/config 样例；已提交的 release.yml 尚无校验和步骤（新 workflow 在工作树中）。首装与页面升级要等新 workflow 出一次包后才能实测；在此之前必须失败关闭，不能退化成无校验安装。
- `internal/upgrade` 检查缺资产时改为列出实际缺少项（`Release <tag> 缺少资产：...`），新增 `TestCheckReportsMissingChecksumsAsset` 覆盖无 `SHA256SUMS` 的 v2.2.6 形态；`deploy/install.sh` 在 SHA256SUMS 缺失时给出可读错误并以非零退出，不继续安装。
- 前端 `updateSucceeded` 修正：成功摘要改为「已升级到 X。」，并重新启用检查按钮；`internal/server/update_browser_test.cjs` 抽出 `openAsAdmin`，新增桩化成功路径（apply 202 → status downloading/restarting → healthz 200 → `/api/version` 为目标标签+`-deadbee`），断言面板显示成功、版本号更新、徽标隐藏、reload 可见、检查按钮恢复、无浏览器错误。与失败路径合计 2 项，真实 Chromium 下通过。
- 文档：README 增补 Release 必须同批提供 `SHA256SUMS` 与部署材料；API-CONTRACT §4.11 写明缺资产即 502 并列出缺项；dev-plan U-01 行补真实浏览器证据与 v2.2.6 缺校验和的现状；handoff §6 补 v2.2.6 核查与 verify-native-install.sh 的退役建议。
- `verify-native-install.sh` 仍按 emby 账号/`/usr/local/bin` 布局，检查项与新 root 平铺模型直接冲突，专属回归也编码旧身份；建议退役而非适配，但删除属清理动作，待用户确认，本轮未动。
- 本地门禁：`go build ./...`、`go vet ./...`、`gofmt -l` 干净，`go test ./...` 全通过；`go test -race ./internal/upgrade` 通过；真实 Chromium 的 `TestAdminUpdateBrowser` 通过；`bash -n deploy/install.sh`、`node --check` 通过。未安装、未重启当前服务、未操作真实媒体或迁移、未提交或发布。

## 96. U-01 收尾决策：安装核验工具适配平铺布局（2026-11-06，未实机安装）

- 决定把 `deploy/verify-native-install.sh` 适配新布局，而不是删除。理由：它被 README 与台账多处引用，删除会破坏已归档证据链；新布局仍值得一个只读安装后审计，成本只有约 40 行 shell 与一组夹具回归。
- 脚本改为检查 `/opt/emby-go/emby-go`（regular、可执行、root:root 0755、SHA256 与预期一致）、`/opt/emby-go` 与 `/etc/emby-go` 目录（root:root 0755）、`/etc/emby-go/config.yaml`（root:root 0600）。去掉 NSS `emby` 账号、`/var/lib/emby-go`、`/run/emby-go`、ext4 与 setpriv 降权检查；这些随旧服务账号模型一起退役，新模型没有可审的服务身份隔离。
- `cmd/metatube/native_install_check_test.go` 同步重写：参数负例保留；布局夹具改为 opt/etc 平铺目录，覆盖完整通过、大写摘要、错误摘要、缺二进制、非可执行、二进制模式/所有者、前缀目录模式、配置模式/所有者、配置目录模式、缺配置；继续断言审计前后字节/模式/所有者不变、配置内容不外泄、不执行二进制。布局组需 root，非 root 自动 skip；参数组不需 root。
- `deploy/verify-native-install.sh` 与新夹具随 release workflow 的部署材料同批上传，README 工具说明改为新检查项与运行方式；handoff §6 与 dev-plan U-01 行同步。
- 验证：`sh -n deploy/verify-native-install.sh`、`gofmt -l` 干净、`go test ./cmd/metatube -run 'TestNativeInstallCheck' -count=1` 通过（12 个布局子用例 + 6 个参数子用例）。未在真实主机运行该工具（无安装授权），未安装/重启/提交/发布。

- 收尾修正：前端 `waitForRestart` 不再在服务仍在线时立即判「版本不符」。重启窗口里 `/healthz` 可能短暂返回 200、`/api/version` 仍是旧进程，现在只有服务持续在线 90 秒仍读不到目标版本才失败，否则继续等待；`pollUpgrade` 的临时请求失败仍转恢复检测。浏览器成功/失败两条用例重跑通过。
- `deploy/install.sh` 的全部 curl 增加 `--proto-redir '=https'`，禁止重定向落到明文；`upgrade.New` 的 idle 状态带 `UpdatedAt`，状态接口不再返回零值时间。
- 最终门禁（本轮回填）：`go build ./...`、`go vet ./...`、`gofmt -l` 干净；`go test ./...` 全通过（cmd/metatube 19.9s、server 101.9s）；真实 Chromium 的 `TestAdminUpdateBrowser` 8.3s 通过；`go test -race ./internal/upgrade` 通过；`bash -n deploy/install.sh`、`sh -n deploy/verify-native-install.sh`、`node --check app.js` 通过。未安装、未重启当前服务、未操作真实媒体或迁移、未提交或发布。
- 文档一致性：修正主计划开头的两处过期表述——部署政策不再说“模板/说明/测试尚待适配”，Web 自升级不再标“待实现”，改为“仓库侧已实现，未实机安装”，并写明实机安装/升级演练与发布仍需单独授权。
- 本地发布预演（2026-11-07）：按新 workflow 的 `CGO_ENABLED=0`、`-trimpath`、ldflags 构建 linux-amd64/arm64、windows-amd64、darwin-amd64/arm64 五目标到临时 dist；`sha256sum emby-go-* > SHA256SUMS` 后自校验 5 行通过，`grep -E "  emby-go-linux-amd64$" | sha256sum -c -` 通过；`emby-go.service` 在 dist 布局下能被 install.sh 的查找顺序命中；`-version` 输出 `Emby-go v9.9.9-preflight-abc1234 (commit abc1234, built ...)` 与 workflow 的 expected 字符串一致。首次预演漏了 `CGO_ENABLED=0`，linux-amd64 为动态链接；补上后静态链接，与流水线一致（预演脚本必须照抄 workflow 的构建环境变量，否则文件类型会漂移）。
- 新增 `internal/stabilityrehearsal/release_pipeline_test.go`：静态守卫 release.yml 的同批打包（install.sh、verify-native-install.sh、emby-go.service、config.example.yaml）、`sha256sum` 生成/自校验与 `dist/emby-go-linux-amd64 -version` 断言；守卫 install.sh 的每个 curl 都有 `--proto-redir '=https'`、缺 SHA256SUMS 时报错退出、校验失败不安装，且禁止 `--ignore-missing`/`-k` 之类降级。该测试只证明文件契约，不证明真实构建或上传。
- U-01 边界补测：`TestAdminUpdateRejectsNonAdmin`（普通账户访问 4 个更新端点全部 403 且不启动任务）、`TestAdminUpdateApplySerializesAndAllowsRetryAfterFailure`（下载中第二次 apply 409；失败后旧程序未动，同一目标可再次由管理员确认）、`TestAdminUpdateCheckReturnsAvailability` 增加 idle 状态 `updated_at` 非零断言；真实浏览器新增非管理员用例（版本按钮禁用、title 提示“仅管理员”、1.5 秒内 `/api/admin/update*` 请求数为 0、徽标与弹层保持隐藏）。`go test ./internal/server -run TestAdminUpdate` 与 `TestAdminUpdateBrowser`（TAP pass 3/fail 0，8.3s）通过。
- 文档同步：dev-plan §4.4 U-01 证据补预演/静态守卫/边界测试；handoff §6 补本轮预演结论与 `CGO_ENABLED=0` 教训；未安装、未重启、未提交、未发布。

### 97. U-01 提交与真实 Release（2026-11-07）

- 提交与 tag：`6e8eb99`「feat: root 平铺部署与 Web 自升级（U-01）」；首次 CI 失败于 web VM 夹具（app.js 顶层 `document.querySelector` 被按源码切片执行时未定义，admin 启动与退出会话用例报 `updateVersionBtn is not defined` / `document is not defined`），改为按需查询后提交 `25453a9`；tag `v2.3.0` 曾指向 7eff2e6，删除后在 25453a9 重建并推送。
- 首轮 release run 42329805081 失败：`verify/web` 冷启动 `Chromium CDP startup timeout (15000ms)`，`verify/go` 的 `TestScrapeAutoMarksBrowser/width390/ownership` 报 `Chromium page load timeout ... /admin#items`（15s）。本地同套件通过：`TestScrapeAutoMarksBrowser` 4 子用例 7.89s，web 套件 315 pass / 0 fail。判断为慢 runner 上的 15 秒启动/加载超时；`gh run rerun --failed` 后 go/web 均通过，run 成功。主 CI run 42329794463 的 go 失败同因，亦已 rerun。
- Release 资产：`v2.3.0` 非 draft/prerelease，含 `emby-go-linux-amd64`、`emby-go-linux-arm64`、`emby-go-darwin-amd64`、`emby-go-darwin-arm64`、`emby-go-windows-amd64.exe`、`SHA256SUMS`、`install.sh`、`verify-native-install.sh`、`emby-go.service`、`config.example.yaml`。
- 本机下载核对（`/tmp/release-check`）：SHA256SUMS 五行覆盖五二进制；`grep -E "  emby-go-linux-amd64$" SHA256SUMS | sha256sum -c -` OK；linux-amd64 为静态链接 stripped ELF；`-version` 输出 `Emby-go v2.3.0-25453a9 (commit 25453a93690619be515f66cc65d3c2fd6d9d556e, built 2026-11-07T02:46:27Z)`；Service 为 `User=root`、`ExecStart=/opt/emby-go/emby-go -c /etc/emby-go/config.yaml`、`Restart=always`、`ReadWritePaths=/opt/emby-go /etc/emby-go`；发布版 `verify-native-install.sh` 对暂存根目录（发布二进制 + root:root 0755/0600 夹具）审计 PASS；发布版 `install.sh` 含 5 处 `--proto-redir '=https'` 与缺 SHA256SUMS 的失败文案。
- 未在真实主机安装、升级或重启；发布提交为 25453a9，工作树仍有未提交的 `dev-plan.md`/`PLAN-STATUS.md` 文档编辑与新增 opt-in `internal/upgrade/live_release_test.go`，另有被忽略的 `docs/`、`reports/`、`.github/scripts/__pycache__/`；这些按“提交需授权”留给用户决定。
- 真实 Release 端到端升级（2026-11-07）：新增 opt-in `internal/upgrade/live_release_test.go`（默认 skip，`EMBY_UPGRADE_LIVE=1` 才联网），在临时目录放"旧程序"占位文件后执行 `Check`（真实 GitHub API 读到 `v2.3.0`）→`Begin`→`Run`：下载 20MB 资产、SHA256 校验、执行 `-version` 验证、`os.Rename` 原子替换、关闭重启通道；结果 PASS（2.04s），替换后二进制 `-version` 为 `Emby-go v2.3.0-25453a9 (commit 25453a93..., built 2026-11-07T02:46:27Z)`，临时文件仅剩正式程序一个。该演练不触碰 `deploy/` 或当前服务。未在真实 systemd 主机安装/升级。

### 98. N-02～N-05 仓库侧实施（2026-11-07）

- 迁移：新增 schema 19 `task_records`（任务摘要，保留 500，启动把 queued/running 标 interrupted，id 从摘要续接）与 schema 20 `webhook_deliveries`（字段白名单 event/item_id/target_id/status/error_code/created_at，保留 1000 条且 7 天）。四处迁移接线（参考库列表、checksum switch、apply、record）同步；`internal/stabilityrehearsal` 的旧 schema18 演练与 `cmd/metatube/native_upgrade_test.go` 的期望升级为 20，`internal/store` 的未知版本负例改用 21，M1 矩阵/dev-plan/README 的目标 schema 同步为 20。
- N-02 任务中心：`startTask`/`finishTask` 尽力写库；异步扫描在 `registerScanTask` 内同步落 queued、`setScanTaskRunning` 落 started、`finishScanTask` 落终态与 `result_json`；`/api/admin/tasks` 合并内存实时任务与最近 100 条摘要（同 id 以内存为准、`history_error`），`/tasks/:id` 内存丢失后回落摘要；前端任务状态增加 `interrupted` 文案。契约 §4.12。
- N-03 诊断：`GET /api/admin/diagnostics` 聚合版本/库结构/数据量/任务与调度/Webhook/图片缓存/ffprobe 证据，只读不联网，DB 读失败 500；左侧「运维诊断 → 诊断」页（刷新、跳转既有入口，无自动修复）；契约 §4.13。
- N-04 投递历史：`notifyWebhook` 在 send 返回后按目标 best-effort 写库（失败只记日志，不回滚业务）；`GET /api/admin/webhook/deliveries?limit=`（1–500，默认 100）与 `DELETE`（返回清空行数）；设置页「投递历史」表（加载/刷新/清空需确认）；契约 §4.14。
- N-05 备份：固定范围 DB `VACUUM INTO` + NFO + extrafanart，Retain 7，目录固定 `<db 所在目录>/backups` 且由进程以 0700 建立（快照含凭据材料，不提供网页下载/恢复/删除单份）；`GET /api/admin/backup` 返回目录/留存/运行中/最近结果，`POST /api/admin/backup/run` 202 + `task_id`（已有备份 409，关停期 503），复用 `task_records`（`type=backup`）记录状态与结果元数据；诊断页「备份」面板显示最近结果并轮询完成；契约 §4.15。
- 测试：`internal/store` 的摘要/投递历史测试；`internal/server` 的任务中心回落、诊断聚合与 403、投递历史查询/清空与 403、备份 202/轮询/0700/重复触发 409/非管理员 403；冻结路由清单与权限矩阵登记新路由（`POST /api/admin/backup/run` 归 admission）；真实浏览器：设置页投递历史（`TestWebhookSettingsBrowser`）、诊断页备份面板（`TestAdminBackupBrowser`）；VM web 套件 161 pass（投递历史夹具补历史节点并隔离其调用序号）。
- 门禁：`go build ./...`、`go vet ./...`、`gofmt -l` 干净；`go test -count=1 ./...` 全绿（internal/server 107.6s、store 56.0s、scanner 18.1s、cmd/metatube 8.6s）；`go test -race ./internal/server -run 'TestAdminBackup|TestScanTaskRecordLifecycle|TestAdminWebhookDeliveriesListAndClear'` 通过；`node --check app.js` 与新增浏览器脚本通过。
- 未实机安装/重启、未操作真实媒体或迁移、未提交/未发布。剩余：M1 正式放行后按正式候选复验；任务中心暂无独立页面；备份不提供恢复。

### 99. 真实主机安装、受控升级与回滚演练（2026-11-07，未提交）

- 主机：Debian 12 bookworm（1.6GiB 内存，根盘 ext4），root 直跑 systemd 单元；升级前 `v2.2.6-234a220`。安装前布局：`/opt/emby-go/emby-go`、DB/WAL/log/avatars/image-cache/video-previews 同在 `/opt/emby-go`，配置 `/opt/emby-go/config.yaml`（0666），unit `ExecStart=/opt/emby-go/emby-go`（无 `-c`）、`WorkingDirectory=/opt/emby-go`、`Restart=on-failure`、`disabled`；DB schema 16、影片 11,342、用户 1；NFO 写 `/data/emby/media/AV/...`；nginx 80/443 反代 `127.0.0.1:8097`，同机另有 redis、easytier、smartstrm、nginx-ui 等无关服务。
- 变更前备份 `/root/prechange-20261107/`：`sqlite3 .backup` 一致性 DB（integrity_check ok、schema16、11342 行）、旧配置、旧 unit、旧二进制 v2.2.6，以及后续的 v2.3.0 二进制、schema20 DB 副本、df、sha256 清单。
- 安装：GitHub 直连下载反复 `curl (35) Connection reset`（IPv6 路径），改用用户提供的只读镜像 `https://<内部镜像>/github.com/<repo>/...`；install.sh 仅替换 `BASE=` 一行，其余未改（因此脚本仍自带 `--proto-redir`、SHA256SUMS 校验与原子安装路径）；下载物 sha256 与 Release SHA256SUMS 中 linux-amd64 行一致（`6fe8a07…`）。按 A 方案把配置复制为 `/etc/emby-go/config.yaml`（0600，内容 sha 与原文件一致），unit 使用发布模板、仅把 `ReadWritePaths` 追加 `/data`（NFO 写入目录）。安装后 `enabled`+`active`，`User=root`、`ExecStart=/opt/emby-go/emby-go -c /etc/emby-go/config.yaml`、`Restart=always`。
- 安装后核对：`/api/version`=`v2.3.0-25453a9`；schema 18（v2.3.0 不包含 19/20）；影片 11,342、用户 1；`/healthz` 200；nginx 443 的 `/` 与 `/api/version` 200；未认证 `/api/admin/update` 401；沙箱 mountinfo 中 `/opt/emby-go`、`/etc/emby-go`、`/data` 均 rw；启动日志无 error，ffprobe 就绪、计划任务 3/2。
- 受控升级演练（使用工作树构建的 v2.3.1-test，含 19/20 迁移）：`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v2.3.1-test -X main.commit=25453a9 -X main.buildTime=2026-11-07T07:20:00Z"` → scp → 以 `.emby-go.installing` + `mv` 原子替换 → `kill -TERM` 旧 PID。日志显示正常关闭（17:55:14 `Deactivated successfully`）→ systemd `Restart=always` 拉起新 PID（17:55:17 `Started`，17:55:18 `listening version=v2.3.1-test`）；`/api/version`=v2.3.1-test，DB 自动 18→20，影片数不变。这验证了“原子替换 + 正常关闭 + systemd 重启 + 恢复后版本核对”链路，但测试二进制并非 tag v2.3.0（见下一条）。
- 回滚：v2.3.0 的迁移代码只认识 ≤18，对 19/20 ledger 会报 `unsupported or modified schema migration 19` 并拒绝启动。停服后应用 schema20 回滚脚本：`DROP TABLE task_records, webhook_deliveries`、`DELETE FROM schema_migrations WHERE version>=19`、`PRAGMA wal_checkpoint(TRUNCATE)`；恢复 v2.3.0 二进制并启动。结果 schema 18、影片 11,342、用户 1、`integrity_check` ok、healthz 200、`/api/version`=v2.3.0-25453a9。schema20 DB 副本保留在备份目录。
- 真实主机运行 `deploy/verify-native-install.sh <installed sha> /`：5 项 OK（二进制 owner/mode、`/opt`、`/etc`、config、SHA256），rc=0；输出保留“static installation checks only / NOT CHECKED…”的口径，不构成 M1 放行。
- 状态：主机当前运行 `v2.3.0-25453a9`（schema 18），`/opt/emby-go/config.yaml` 留作回滚材料（0600）；N-02～N-05 尚未进入任何 Release；工作树未提交、未发布新版本。临时 SSH 公钥由用户加入 `/root/.ssh/authorized_keys`（尾注 `emby115-rehearsal-2026-11-07`），演练结束后应删除。
- 剩余：页面自升级（左下角徽标 → 管理员确认 → 下载/SHA256/`-version`/原子替换 → 正常关闭 → systemd 重启 → 恢复后核对版本）需要比 v2.3.0 更新的 Release 才能真机实测；建议以包含 N-02～N-05 的工作树发布 v2.3.1 后在主机上走一遍（需提交/发布授权）。

## 100. 真实主机页面自升级全链路（2026-11-07）

- Release：`v2.3.1`（提交 `f61840a`，含 N-02～N-05 与 schema20）、`v2.3.2`（提交 `2b679c9`，自升级/首装镜像支持）、`v2.3.3`（同一提交 `2b679c9`，仅用于页面升级目标）。三者均为正式 Release，资产含 `SHA256SUMS` 与部署材料。
- 缺陷与改进：v2.3.0 页面升级在真机失败于 `下载升级包失败：context deadline exceeded`——主机到 GitHub Release 资产约 15 KB/s，20 MB 需约 22 分钟，超出升级 10 分钟预算。因此新增可选镜像 `EMBY_GO_RELEASE_MIRROR`（服务端环境变量）与 `EMBY_GO_MIRROR`（首装脚本），只改写下载地址，`SHA256SUMS`、`-version`、原子替换不变；镜像必须 https。代码见 `internal/upgrade/upgrade.go`、`internal/server/server.go`、`deploy/install.sh`，测试 `TestCheckRejectsNonHTTPSMirror`、`TestRunDownloadsFromMirrorBase`。
- 真机准备：用镜像下载 `v2.3.2` 并校验 SHA256 后 `install -m 0755` 原子替换；新增 drop-in `/etc/systemd/system/emby-go.service.d/mirror.conf`：`Environment=EMBY_GO_RELEASE_MIRROR=https://<内部镜像>/github.com`，`daemon-reload` + `restart`。安装后 `/api/version`=`v2.3.2-2b679c9`、schema 自动 18→20、影片 11,342 不变。
- 页面实测（`/tmp/emby115-ui-upgrade.test.cjs`，真实 Chromium，19.3 秒）：打开管理页 → 左下角徽标出现 → 管理员打开弹层 → 目标 `v2.3.3` → 点击更新 → 服务端镜像下载、SHA256 校验、`-version` 通过后原子替换 → 日志 `升级包已原子替换，等待 systemd 重启`、`升级已安装：正常关闭，等待 systemd 重启`、`开始有界关闭 budget=15s`、systemd `Deactivated successfully` → `Started`（19:28:09）→ 新进程 `listening version=v2.3.3-2b679c9`；页面恢复后读取 `/api/version` 显示“已升级到 v2.3.3-2b679c9。重新加载页面即可使用新版本前端资源。”，徽标隐藏、按钮状态正确、无浏览器错误。点击「重新加载页面」后诊断入口出现，证明前端资源随程序更新。
- 主机核对：`/api/version`=`v2.3.3-2b679c9`、`NRestarts=1`、schema 20、影片 11,342、`integrity_check` ok、`/opt/emby-go` 无残留临时文件。
- 清理：删除演练用 `access_tokens` 行与 `/root/.emby115-rehearsal-token`，从 `/root/.ssh/authorized_keys` 移除尾注 `emby115-rehearsal-2026-11-07` 的临时公钥并删除本地私钥；清理 `/tmp/emby-go-install`。drop-in 镜像环境变量保留（该网络环境下后续升级需要）。
- 未做：M1 A–E 正式验收包、具名候选签收、真实媒体/NAS 操作；`/opt/emby-go/config.yaml`（0600）与 `/root/prechange-20261107/` 备份保留供回滚。本记录不改变“不实机迁移数据”的边界。

## 101. 死代码清理（2026-11-08，用户确认三步）

- 范围：本批 `52836dd..HEAD`（5 个提交、197 文件）静态审查；因本机为 2 核/1.8GB 且用户要求限流，未再跑全仓编译式 `deadcode -test`，改用符号引用统计与定向核查。
- 结论：本批新增代码（U-01、N-02～N-05、镜像支持）未发现未引用函数、未访问结构体字段或孤立常量；`app.js` 函数无孤儿。
- 清理（用户确认“3 步都做”）：
  1. `internal/nfo/nfo.go`：删除 `FromFields`（全仓仅定义本身，零引用）。
  2. `internal/server/requestlog.go`：删除 `requestContentLoggerTo`（生产 wiring 只用 `requestLoggerTo` + `requestRecoveryTo`）；`requestlog_security_test.go`、`requestlog_ids_test.go` 移除该中间件，依赖双行输出的计数 2→1，`[API REQUEST]` 断言改为 `[GIN]`。
  3. `internal/scanner/incremental.go`：删除 `enumerateGroups` 包装；4 处测试改用 `enumerateGroupsContext(context.Background(), lib)`。
  4. C-01：删除 `internal/probe/input.go`（72 行）及 `input_test.go`、`input_lifecycle_test.go`；`downloadInput`/`readInput`/`maxInputSize`/`inputHTTPClient` 生产零调用，STRM 直连 ffprobe 路径未动。
- 保留：`probe.flexNum.UnmarshalJSON`（`encoding/json` 反射调用）、`mountWebhookSettings`（`app.js` 页面入口），均非死代码。
- 验证：`gofmt -l` 干净；`go build ./...`、`go vet ./...` 通过；`go test ./internal/nfo ./internal/probe`、`go test ./internal/server -run 'TestDiagnostic|TestRequestLog'`、`go test ./internal/scanner -run '<受影响用例>'` 全部 ok；提交前以单核限流复跑全量 `go test ./...`（GOMAXPROCS=1 nice，全部包 ok）。改动随 `v2.3.4` 提交，不单独发布功能。
- 发布：`e902bb5` 打 `v2.3.4` 并推送；release run `42438003369` 首轮 `verify / Race (server-2/4)`、`server-3/4` 因既有 `/admin#items` hash 导航 page-load flake 失败，`--failed` 重跑后成功。Release 共 10 个资产；本机下载 `emby-go-linux-amd64` 用 SHA256SUMS 校验 OK，`-version` = `Emby-go v2.3.4-e902bb5 (commit e902bb5..., built 2026-11-08T02:31:38Z)`；`releases/latest` = v2.3.4。
- 测试基础设施修复：`internal/server/web_browser_helper.cjs` 的 `navigate()` 现在同时接受 `Page.loadEventFired` 与 `location.href` 到达目标，覆盖同文档 hash 导航（`/admin#items`）不发 load 事件的场景；该 flake 在 v2.3.2/v2.3.3/v2.3.4 各出现一次。回归：`TestScrapeAutoMarksBrowser`、`TestScrapeStillsBrowser`、`TestSubtitleMarksBrowser` 15.6s 通过；`TestAdminUpdateBrowser`、`TestAdminBackupBrowser` 9.8s 通过。

## 102. 独立任务中心页与升级下载进度（2026-11-08，仓库侧完成，未提交）

- 任务中心页：左侧「资料处理 → 任务中心」（`data-page="tasks"`），表格列 ID/类型/状态/开始/结束/结果/错误；类型与状态为客户端筛选，刷新按钮重新读取；存在运行中任务时每 5 秒轮询，`history_error` 显示摘要不可读提示。计划任务页删除原「执行记录」合并块（`#task-log`/`#task-refresh`/`#task-scan` 及观察中的重绘调用），观察逻辑只保留 `/scheduled` 只读轮询与状态提示。
- 结果摘要：`GET /api/admin/tasks` 的 items 增加 `result`（执行结果 JSON 对象）；`adminTasks` 从 `ResultJSON` 回填内存任务结果，摘要任务直接带结果；`result_json` 不对外暴露。前端 `taskResultText` 只格式化 added/updated/deleted/success/pending/failed/skipped/nfo_count/extra_backdrop_count，未知形状截断展示。
- 升级进度：`Manager` 以原子计数记录下载字节与资产总大小，`/api/admin/update/status` 在 `downloading` 阶段返回 `downloaded_bytes`/`total_bytes`；弹窗新增进度条（有总大小时显示百分比与 X/Y，无总大小时为 indeterminate；verifying/installing/restarting 显示阶段文案，失败/成功隐藏）。资产大小取自 Release API 的 `size`。
- 测试：`TestTaskListIncludesResultSummary`（result 摘要且不暴露 result_json）；`TestAdminUpdateBrowser` 桩化 status 返回 25%/75% 下载进度，断言进度条可见、宽度 75%、文案「已下载 75% · 15 MB / 20 MB」、成功后隐藏；新增 `TestAdminTasksBrowser`（`tasks_browser_test.cjs`：导航进入 `#tasks`、表格记录与七列表头、筛选/刷新控件、刷新后仍有行、无浏览器错误）；VM 套件 170 通过。
- 文档：契约 §4.11 补进度字段、§4.12 补 result 字段；dev-plan N-02 行改为“含独立任务中心页”，U-01 证据补下载进度；本条为施工记录，尚未提交/发布。
- 门禁：`gofmt -l` 干净；`go build ./...`、`go vet ./...` 通过；`GOMAXPROCS=1 nice -n 19 go test -count=1 ./...` 全部包 ok（server 101.4s、store 49.2s、scanner 15.5s 等）；`go test -race ./internal/upgrade` 与目标 server 用例通过；VM 套件 170、两个真实浏览器回归（update 9.2s、tasks 2.5s）通过。

## 103. 普通用户禁止登录 Web 管理台侧边栏分组（2026-11-08，未提交）

- 行为变更：Web 登录页与 `/admin` 只接受 `Policy.IsAdministrator=true` 的账户。登录成功后若响应 `User.Policy.IsAdministrator=false`，不写 `emby_token`、不跳转，表单内提示“该账户不是管理员，Web 管理界面仅对管理员开放”；已有普通用户 token 访问 `/admin` 时 `boot()` 清除本机凭据并 `location.replace('/')`。`POST /Users/AuthenticateByName` 响应结构不变，普通用户 token 仍可用于 Emby 客户端播放，后端管理端点仍由 `requireAdmin` 返回 403。
- 侧边栏分组：内容管理（媒体库、媒体墙）、资料处理（刮削、独立翻译）、自动化（计划任务、任务中心）、运维诊断（运行日志、活动记录、接口探针、诊断）、账户与安全（访问安全、设置）；「总览」保持未分组。共 5 组 13 项。
- 测试：`web_test.cjs` 新增“非管理员登录被拒绝：不存凭据、不跳转、提示仅管理员开放”，成功登录夹具补 `User.Policy.IsAdministrator`；`update_browser_test.cjs` 的非管理员用例改为真实浏览器验证：带 viewer token 打开 `/admin` → 回到 `/`、`.auth-error` 含“仅对管理员开放”、`emby_token` 已清除、无版本按钮、零次 `/api/admin/update` 请求。VM 套件 175 通过；`TestAdminUpdateBrowser` 三个子用例通过（7.95s）。
- 文档：契约新增 §4.16（登录响应不变、前端单点拒绝、API 仍 403）；README 后台章节注明仅管理员登录。
- 未提交、未发布；`go build ./...` 仅用于确认嵌入资源可构建。
- 浏览器回归稳定性：非管理员用例改用 `Page.navigate` 发起导航 + `waitForRedirect` 重试（导航销毁旧执行上下文时 `Runtime.evaluate` 会报 “Inspected target navigated or closed”，不能按页面稳定后再判断），连续 3 次运行通过；与任务中心回归合并运行也通过。

## 104. 入站请求 URL 取消脱敏（2026-11-09，未提交）

- 用户明确选择保留完整路径及全部查询参数（含 api_key/token），范围为普通/debug 请求日志与未知接口探针；不增加头或请求体采集。普通/debug 统一优先使用原始 RequestURI，缺省回退 URL.RequestURI()，保留转义拼写、参数顺序、重复值及尾部空问号；日志引号转义可逆，不再生成 URL 脱敏模板。
- 未匹配路由探针保存 URL.EscapedPath() 与 RawQuery，按 method + 转义路径去重，query 不参与 key；body 仍不读取、保存 [OMITTED]。扫描器与 /api/ 排除、128 槽/6 小时、突发60/每秒1次和1000条历史预算、清理代次及写入失败重试契约保持。删除无调用的路径/query 脱敏实现，不修改鉴权、HTTP 错误、app 日志脱敏、审计路径或上游错误保护。
- 普通模式仍仅记 error 布尔值并省略 panic 详情；debug 敏感头和认证/初始化/改密/密钥/解锁请求体保护保留，但敏感路由 URL 也原样记录。README 与 API 契约明确：debug=false 不能避免 URL 凭据落盘，不能删除或还原历史记录。
- 回归覆盖原始/回退 URI、未知及敏感路径、转义大小写、重复凭据、非法 query、长 URL、ForceQuery、探针 method/path 去重及不读 body、限额/清理/失败重试、头/body/error/panic 保护。首次定向测试发现两处旧策略断言（探针路径转小写、旧敏感路由标记），已按新政策修正；不是生产运行故障。
- 验证：定向回归 1.443s，go build ./...、go vet ./... 及 go test ./... -count=1 全部通过（server 114.779s、store 60.745s），作业 bash-1dc88c4b90cc42b6 exit0 已收集；日志/探针专项 -race 27.861s 通过，作业 bash-090fd00a269c4e53 exit0 已收集。gofmt 与 git diff --check 通过。浏览器专项 REPORT_BROWSER 及其它需显式环境启用的外部验收未额外启用，不以默认跳过算通过；未提交、未部署、未重启服务或启用 debug。

## 105. 默认工作流收缩为核心 Go 门禁（2026-11-09，未提交）

- 按用户确认方案，默认 CI 从 14 个验证 job 收缩为一个 Go job：gofmt、build、vet、无缓存全仓普通测试；保留失败摘要，不筛选业务测试、不放宽断言、不自动重试。移除默认覆盖率采集/70%门槛、11路 race、web 与漏洞检查。
- 新增手动 manual-checks 工作流，仅 workflow_dispatch，可选 race/web/vuln/coverage/all。原 race 分片、规划器验证、Node/Chromium、govulncheck 和70%覆盖率检查移到该入口；保留测试代码及脚本，无定时任务，不作为 Release 依赖。
- Release 文件与产物逻辑不改：仍在同一事件 SHA 复用核心 CI 后构建，核心失败不发布。默认验证14→1，含发布构建15→2；不承诺固定时长。main 推送和 tag 发布各自触发核心验证，不引入跨提交绿色结果复用。
- v2.3.6 的 store race 失败为 TestScanCancelledWhileWaitingWriterDoesNotDelete/sqlite=true/group=false 报 scan_state_test.go:413: cancel=interrupted (9)，非超时或明确 data race 报告；此轮不修改该业务路径或其测试断言，精简不等于修复该失败。不重写旧标签，旧tag重跑仍用旧工作流。
- README 已同步默认/手动门禁和安全取舍。首次全仓回归暴露旧发布契约仍要求 race/web/vuln 为默认门禁（CI must keep unconditional race gate），已同步 release_gate_test.go：要求默认仅一个 Go job且四条核心命令无条件执行，专项仅手动触发并按选项选择，同时保留同SHA checkout、失败传播、版本/校验和及发布依赖断言。
- 最终 actionlint v1.7.7 验证三个工作流通过；发布图专项0.007s、gofmt/build/vet及 go test -count=1 ./... 全部通过（server108.464s、store55.312s、stabilityrehearsal4.690s），作业 bash-6f276855b36244e3 exit0 已收集。git diff --check 通过。手动专项未实际重跑，远端工作流未触发；未提交/推送或修改已有tag。
