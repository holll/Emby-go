# Emby-go

一个用 **Go** 重写的、兼容 **Emby API** 的轻量媒体库服务端，面向 **AV / FC2 类 NFO + `.strm` 媒体库**（海报墙浏览走 Emby 客户端）。

- 元数据真源 = **现成 NFO**（`<title>/<genre>/<studio>/<actor>/<set>` 等）；可选内置 MetaTube 刮削与翻译补全
- 媒体只支持 `http(s)://` 的 `.strm`；播放走 **302 直拉**（服务端零带宽，不转码）
- 单二进制、单端口；Redis 缓存可选（未配置或连接失败回退进程内缓存，仅单实例安全）；内置深色 Web 管理后台
- 兼容端点覆盖：认证、媒体库/影片墙、详情、图片、合集(BoxSet)、进度/续播、搜索/过滤等 Emby 客户端浏览主链路

## 快速开始

本节按当前源码核实，覆盖前置条件、最短操作路径、API 调用与预期结果；未执行真实实例验证，不承诺给定媒体源可播放。

**前置条件**：源码构建需满足 [go.mod](<go.mod#L3>) 的 Go 1.27.0 要求；准备可读的测试媒体库（同名 NFO + HTTP(S) `.strm`），并为配置、SQLite 和日志提供写权限。Redis 可选，`ffprobe` 仅在媒体信息探测时需要。

### 构建与配置

```bash
go build -o metatube ./cmd/metatube
# 仅在 config.yaml 不存在时复制，避免覆盖私人配置
if [ ! -e config.yaml ]; then cp config.example.yaml config.yaml; fi
```

编辑私有配置后再启动：使用未占用的测试数据库，最短路径可设 `redis_addr: ""`；为首次初始化设置自己的随机 `setup_code`，并按实际来源限制 `initialize_allow_from`。详细字段见 [配置示例](<config.example.yaml>)；Redis 未配置或连接失败时回退进程内缓存，仅适合单实例。

> **监听安全**：`port` 只接受数字，实际监听全部网卡（`:<port>`），当前没有 `listen` 配置项，也不能通过 `port` 绑定回环。请在隔离环境或已有防火墙限制的环境中首次启动；仅访问 `127.0.0.1` 不代表端口仅本机可达。

### 运行与首次使用

```bash
./metatube -c ./config.yaml
# 可另行查看版本，不启动服务
# ./metatube -version
```

不带 `-c` 时，配置路径按 `EMBY_CONFIG` → 当前目录默认配置选择。相对数据库路径基于进程启动目录；私人配置由 [.gitignore](<.gitignore>) 忽略，不要直接用示例文件运行（启动可能回写 `server_id`）。

1. 同机浏览器打开 `http://127.0.0.1:18080/`（使用你配置的端口），创建管理员并登录。账号至少 3 个字符、密码至少 9 个字符；已初始化的实例直接登录。
2. 后台「媒体库」登记服务器上的绝对目录路径，点击该库扫描，等待进度完成。异步提交返回 202 只表示接受任务，不是入库成功。
3. 在「媒体墙」检查成功影片，再用 Emby 客户端连接同一服务地址。直接播放返回 302 到源站，不转码；客户端仍需能访问源站并支持其编码。

缺少 NFO 或解析失败的 HTTP(S) 条目进入待补录，不会出现在 Emby 可见影片列表。检查 `/healthz` 得到 200 只表示服务接纳请求，不代表媒体可播放。

### MetaTube 手动识别

在影片详情打开单条刮削预览，可直接修改搜索条件，无需先修改影片元数据或 NFO：

| 方式 | 标题 / 番号 / URL | MetaTubeId |
| --- | --- | --- |
| 常规搜索 | 影片番号或搜索词 | 留空 |
| 指定源搜索 | 影片番号或搜索词 | 源名称 |
| 精准 ID | 可留空 | `Provider1:movie-id` |
| 精准 URL | 完整影片源链接（HTTP/HTTPS） | 留空 |

精准 ID 中的 ID 是源网站影片页面使用的固定 ID，**不是番号**。URL 原样交给 MetaTube 识别，支持范围取决于所连接后端的源实现。点击「手动搜索」后查看候选、详情与字段差异，再点「确认写入」；搜索和预览不会写入本地文件、翻译或下载图片。未命中或请求失败时可直接调整条件重试，也可点击「按影片信息搜索」恢复原有自动搜索。

#### 已刮削影片的 MetaTube 标记

成功刮削后，影片 NFO 保存 `<uniqueid type="metatube">Provider:movie-id</uniqueid>`，ID 使用与官方插件一致的 URI 转义（不是番号）。扫描也兼容 Emby 插件保存的顶层 `<metatubeid>`，优先读取非空的 `uniqueid`；本项目旧版 `metatube:Provider:movie-id` 标记继续可读，无需批量重写已有文件。

自动预览与批量/定时刮削优先复用已有的有效标记，直接按源和 ID 获取详情，不再依赖当前标题或番号是否匹配。有效标记对应的影片不存在或源请求失败时，不会静默改搜其他影片；可在手动搜索里输入新条件重新选择。显式手动搜索始终优先于已有标记。无有效标记时仍沿用番号/文件名/标题搜索。

写入仍遵守「只补缺失 / 覆盖」和元数据字段锁；只补缺失不会替换已有标记。Emby 影片接口增加官方 `ProviderIds.MetaTube`，同时保留原小写 `metatube` 字段供旧客户端使用。

前端资源嵌入二进制，更新源码后需重新构建、重启服务并刷新后台才能使用。

### 查询真实程序版本

```bash
curl -fsS http://127.0.0.1:18080/api/version
# {"product":"Emby-go","version":"v1.4.4-ad21a94","commit":"ad21a94...","build_time":"2026-10-22T05:00:00Z"}
```

上述为格式示例。`GET /api/version` 公开且返回 `Cache-Control: no-store`，不需要登录；只返回构建信息，不包含配置、路径或凭据。正式发布的 `version` 为 **完整标签（含 v）+ `-` + 7 位 Git 提交号**，`commit` 为完整提交号，`build_time` 为 UTC RFC3339 时间。命令行 `-version` 和后台左下角使用同一构建版本。

[发布工作流](<.github/workflows/release.yml>) 在构建所有平台时通过 `-ldflags` 注入 `main.version`、`main.commit`、`main.buildTime`，并在发布前实际执行 Linux 二进制校验 `-version` 输出。直接执行普通 `go build` 未注入时返回 `dev` / `none` / `unknown`，不会伪造发布版本或在运行时读取服务器的 Git 目录。旧二进制不会自动获得此接口，需要升级到包含该改动的新构建。

Emby 客户端使用的 `/System/Info` 和 `/System/Info/Public` 的 `Version` 仍是 **协议兼容版本** `4.9.5.0`，不改成程序发布版本，以免影响客户端能力协商。

### 目录约定

每个条目形如：

```
ABF-018/
├── ABF-018.strm        # 内容为 http(s)://... 一行
├── ABF-018.nfo         # Kodi/Emby 风格元数据
├── poster.jpg          # 可选：主海报（也认 folder.jpg/cover.jpg）
├── landscape.jpg       # 可选：宽图（Thumb）
└── fanart.jpg          # 可选：背景图（Backdrop）
```

扫描时 `.strm` 按 scheme 分类：`http/https` + NFO → 可播放；`http(s)` 无 NFO → 待补录；`ed2k` 等其它 scheme → 不兼容（不进 Emby）。

扫描**不做图片格式转换**，直接引用目录里已有的图片（`webp`/`jpg`/`jpeg`/`png`）。媒体库封面优先取**库根目录**下 `poster`→`folder`→`cover`→`default` 任一命名的图片（`cover.webp` 同样适用）；没有则借用库内最近入库影片的宽图/海报。

### 增量扫描与全量重建

后台可选择增量扫描（推荐）或全量重建；**两者都会完整枚举目录**，没有文件监视器。增量模式读取 `.strm` / NFO 摘要并核对持久指纹与索引证据，只重建变化或失效的影片组；无变化组不解析 NFO、不重写影片/演员/特征、不推进内容版本。图片仅比较路径、大小与修改时间：图片被同大小、同时间原地替换时可能无法识别，可改动文件时间或执行全量重建。

原有 API 和旧 `scan` 计划未指定模式时仍为 full；新计划可选择模式，`reindex` 始终全量。界面展示组进度、未变化、新增/更新/删除、失败及清理状态；失败仍展示此前成功提交的部分结果。事务原子性只覆盖单个影片组，不是整库回滚；重试会检查落盘指纹与索引状态，内存运行任务不跨重启恢复。

目录读取失败、取消、输入不稳定或库根/配置变化时不会进行末尾缺失清理。已有索引却突然枚举为空时默认阻止清理；确认确实删除全部源文件后，才可通过手动扫描 API 显式 `allow_empty_removal=true` 放行。仍在盘的 manual 记录优先保留（包括成为 CD 附属段的人工记录）；普通独立 CD 记录可在合组时淘汰，真正缺失的人工源仍受完整清单清理。

远程挂载建议使用高级 API / 计划参数 `root_marker_path` 与 `root_marker_sha256` 配置只读根标识；相对路径、SHA-256 必须成对，标识文件不自动创建，最多 64 KiB。没有可靠标识时，即使目录仍可读且不为空，也不能保证识别所有挂载部分掉线。接口与确认边界见 [API 契约](<API-CONTRACT.md#44-全量与增量扫描>)。

持久扫描状态使用 schema migration 16，升级前备份数据库与 NFO；不能用旧二进制直接降级，回退须停服恢复升级前完整备份。`scan_groups` / `scan_group_sources` 是可失效的扫描证据，不是新的元数据真源；NFO 与 SQLite 仍非联合事务。

### 多分段（CD1/CD2）

同目录、同文件基名、末尾带 `-CD<数字>` 的 `.strm` 会被归为**一部逻辑影片**（对齐 Emby stacking 语义）：

```
Split/
├── Split-CD1.strm      # 主段（必须存在 CD1）
├── Split-CD1.nfo       # 主段 NFO 为元数据真源
├── Split-CD2.strm      # CD2+ 作为 AdditionalParts
└── Split-CD3.strm
```

- CD1 作为主影片进列表，`PartCount=CD 数`；CD2/CD3…（任意段数）不重复占列表位。
- 重扫时会清理历史遗留的片段独立记录（旧版本曾把 CD2/CD3 单独入库的情况）。
- `GET /Videos/{id}/AdditionalParts` 返回各分段；分段 Id 为 `part-<影片id>-<段号>`，可直接 `PlaybackInfo`/`stream` 302 播放。
- 只存在 CD2、无 CD1 时按普通单片处理，不猜测主片。

### 合集（BoxSet）

读 NFO `<set><name>` 自动聚合。Views 会出现「合集」媒体库（`CollectionType=boxsets`），合集内类型筛选只显示该合集数量≥1 的类型。

## Web 管理后台

`http://127.0.0.1:18080/admin` 提供（仅管理员账户可登录；普通用户即使密码正确也会被拒绝并提示，观看请使用 Emby 客户端）：
- 总览统计、媒体库管理（可删除库索引）、扫描/重建索引（带实时进度）
- 媒体墙（海报墙 + 无限滚动 + 在线播放；状态/协议筛选、搜索、排序、重读源、删索引）
- 刮削（批量 / 单条预览-确认 / 演员头像）与编辑（pending 影片补录并入编辑链）
- 仅翻译（已有影片单片 / 批量，支持手动启动与计划任务，不重新刮削或下载图片）
- API 密钥（创建后可直接调用 Emby 接口）
- 任务日志、运行日志（磁盘 app 日志增量查看）、未知接口探针、设置

后台保留暗色管理台风格，导航按内容、处理、自动化、运维与账户分组；管理台只接受管理员账户登录，普通用户请使用 Emby 客户端观看。设置分为账户与安全、用户与授权、通知、日志与保留、索引恢复和服务信息；既有 Webhook 目标按需展开编辑。计划任务先展示已有列表，新建/编辑配置按需展开；刮削先展示执行区，连接与策略配置收起；媒体墙和详情把日常浏览与维护操作分层，独立翻译入口不变。

后台为响应式布局，桌面 / 平板 / 手机自适应：窄屏（≤860px）使用可关闭的抽屉菜单，而非横向挤放全部导航；支持 Escape 关闭、键盘焦点约束与返回，触控按钮按至少 44 CSS px 目标设计。表格和表单按窄屏重排，触屏设备常显卡片操作按钮（不依赖 hover）；播放器按视口限高并适配安全区。前端资源随二进制嵌入，修改后须重建并运行新二进制，再刷新后台。

### 运行日志

侧栏「运行日志」读取服务器 `log/app-YYYY-MM-DD.log` 的脱敏视图：默认最新在上、约每秒增量刷新、可暂停/继续、按级别筛选、加载更早（按游标分批，跨午夜与半行不会静默跳过）。文件轮转、被清理或游标失效时会提示缺口并从当前尾部重载；单次最多展示 1000 行，任意内容都按文本转义。它不是 `debug` 原始请求日志，也不提供下载或删除；历史非结构化行无法证明不含凭据，只显示隐藏占位符。

### 仅翻译已有影片

翻译可独立于刮削执行：在已有影片中手动选择单片或批量翻译，也可创建类型为「独立翻译」的应用内计划任务，按时执行或点击立即运行。新建翻译任务默认不启用自动调度；保持禁用也能保存配置并点击「立即运行」，仅在你明确启用后才按计划自动执行。复用刮削设置中的翻译服务地址、密钥、目标语言与超时，不要求 MetaTube 可用；翻译服务自身的 SQLite 缓存继续由该服务管理，Emby-go 不新增重复缓存。

- 默认只选标题：读取 NFO 的 `<originaltitle>` 作为输入，成功后更新展示标题，沿用番号前缀与排序标题规则；原始标题保持不变。缺少原始标题时跳过，不拿现有译文猜测原文。
- 简介需显式勾选：输入是当前 `<plot>`，可能已经是译文，请确认后再重翻；缓存只能复用相同输入的结果，不能识别简介是否为原文。
- 仅修改选中的、未锁定的非空文本字段；不重新获取影片详情、图片、演员、标签等元数据，不新建缺失的 NFO。网络请求期间不持有 NFO 文件锁，提交前检查相关文本及字段锁，避免覆盖并发人工修改。
- 翻译失败不覆盖现有文本。每片写入后局部刷新索引；已保存但刷新失败会单独报告，不回滚已保存文件。任务取消只停止后续处理，不撤销已完成更新。
- 手动与定时翻译共享互斥和进度，并与扫描、刮削、探测及编辑的 NFO 写入通道互斥。运行状态是进程内数据，重启不恢复、不补跑；没有历史翻译失败标记，因此批量范围由你选择，而不是自动识别“曾翻译失败”的影片。

修改后需重建并运行新二进制，刷新后台；这里的计划任务由 Emby-go 自身管理，不依赖外部定时器。

### 在线播放

媒体墙点击海报即在内置播放器（ArtPlayer，MIT，已随二进制内嵌）中播放：

- 默认走 `/Videos/{id}/stream` 的 **302 直拉**（服务端零带宽）；
- 若后台是 HTTPS 而源站是 HTTP（浏览器混合内容拦截）或跨域/防盗链导致失败，自动回退 `/Videos/{id}/proxy`（服务端透传 Range 代理，**仅网页播放器使用**，Emby 客户端仍走 302）；
- 纯浏览器直连播放、不转码：H.265/MKV 等浏览器不支持的编码可能无法播放，请改用 Emby 客户端。

> 浏览海报墙也可用 **Yamby / iPlay** 等 Emby 客户端连接同一地址。

## Emby 客户端接入

Emby 兼容 API 同时注册在根路径与 `/emby` 前缀下：

> 对外接口的稳定字段、错误码、变更与弃用规则见 [API-CONTRACT.md](API-CONTRACT.md)（v2 起只增不改；弃用项见 §5.1）。

```text
服务器地址：http://<host>:18080          # 或 http://<host>:18080/emby
账号/密码：  初始化向导创建的管理员
```

已对齐的浏览主链路：认证 → Views（媒体库 + 合集）→ Latest/Resume/Suggestions → Items（排序/过滤/分页/搜索）→ 详情/图片 → 相似推荐 → PlaybackInfo → `/Videos/{id}/stream`（302 直拉）→ 进度/收藏/评分上报。

### 管理员 API 密钥

后台「管理员 API 密钥」创建后，可用密钥直接调用全部管理与 Emby 接口（全权、长期有效）：

```bash
# <user-id> 取对应账号登录响应的 User.Id，不要假定为 1；优先用头传凭据
curl -H "X-Emby-Token: <key>" 'http://<host>:18080/Users/<user-id>/Views'
# /emby 前缀也可用；完整登录与动态取 ID 示例见快速上手
```

密钥支持**在线轮转**：在后台密钥行点击刷新图标，填写当前密码与原因并确认；旧密钥立即失效，所有旧客户端需要更新，其它密钥与会话不受影响。轮转为条件写（`If-Match`），并发轮转只有一个成功；新明文只在成功响应中显示一次，列表不再回显。用 API key 调用轮转接口会被拒绝，必须使用交互登录会话。

> **权限边界**：当前实现已有多用户与媒体库授权，普通用户默认无库授权，不能访问管理接口。API 密钥仍是长期管理员全权凭据，泄露等于交出管理面；只发给可信管理员脚本。图片端点保持匿名公开以兼容浏览器 `<img>`/客户端，不因多用户授权或持有密钥而收窄；图片不具备与影片详情相同的私密性。

### 登录防护与应急解锁

登录、首次初始化、修改密码、Emby `AuthenticateByName` 与密钥轮转再认证共用同一套内存防护：单 IP 请求突发 10、每秒补 1；全局突发 40、每秒补 20；同时最多 4 个 bcrypt 校验（占满最多等 100ms）；账号与「IP+账号」组合失败分别计数，账号跨 IP 冷却最长 5 分钟。限流命中返回 `429`、`code=AUTH_RATE_LIMITED` 与正整数 `Retry-After`；重复失败不因一次成功登录而清空 IP 攻击额度。凭据请求体上限 8 KiB，超限返回 `413 AUTH_BODY_TOO_LARGE`。防护为单进程内存态，重启后清零。

管理员可在 `GET /api/admin/security/auth-limits` 查看聚合策略与拒绝计数，并用 `POST /api/admin/security/unlock`（交互会话 + 当前密码 + 原因）解除指定账号或 IP 摘要的失败状态；解锁不会清空全局桶。认证、改密、密钥与解锁路由即使在 `debug: true` 下也不会把请求体、Authorization/Cookie/X-Emby-Token 原文写入请求调试日志；审计路径保护保持不变。但普通和 debug 请求日志的 URL 均原样记录，**路径或查询串中的密钥不会被隐藏**。

## 配置（config.yaml）

```yaml
port: 18080               # 监听端口
debug: false              # true 开启未脱敏的原始请求日志，仅临时排障；修改后重启
db_path: "emby-go.db"     # SQLite 文件
server_name: "Emby-go"    # 对外站名
server_id: ""             # 留空自动生成稳定 UUID
redis_addr: "127.0.0.1:6379"   # 可选：留空或不可达时回退进程内缓存
redis_password: ""
redis_db: 0
session_ttl_days: 30      # 登录会话有效期（天），0=不过期；使用中自动续期
setup_code: ""            # 首次初始化验证码（公网/共享环境必填）
# initialize_allow_from: ["127.0.0.1/32"]  # 仅允许这些来源初始化
# trusted_proxies: ["127.0.0.1/32"]        # 仅信任这些反向代理的 X-Forwarded-For
server_domains: []        # 前端“服务器域名切换”候选
security:
  auth:                   # 登录防护；全部可省略，省略即用下列默认值
    ip_request_rate: 1.0
    ip_request_burst: 10
    global_request_rate: 20.0
    global_request_burst: 40
    bcrypt_concurrency: 4
    credential_body_bytes: 8192
    max_keys: 4096
    account_cooldown_max_seconds: 300
```

`server_id` 会写入配置文件以保持稳定；`redis_addr` 可选，未配置或连接失败时回退进程内缓存（重启失效、多实例不共享）。

### 原始请求调试日志

普通请求日志（`debug: false`）也原样记录请求 URI：未知路径不再替换为 `[UNMATCHED]`，路径大小写、百分号转义、查询参数顺序与重复值均保留，`api_key`、`token` 等也不再隐藏。仍跳过成功的图片、静态资源和流等高频请求；不新增请求头或请求体采集，错误只记是否存在，panic 详情仍省略。

在 `config.yaml` 设置 `debug: true`，重启新版本服务后生效。开启后，`log/request-YYYY-MM-DD.log` 除原始 URI 外，还记录非敏感请求头、完整的预算内非敏感路由请求体、请求错误和 panic 堆栈；Authorization/Cookie/X-Emby-Token 等敏感头继续遮盖，认证、初始化、改密、密钥轮转/删除与限流解锁等敏感路由的请求体仍不读取、不记录原文。**这些路由的 URL 同样原样记录，不再使用敏感路径模板或查询串掩码。** 图片、静态资源和流请求也记录，不再跳过。字符串使用可逆的引号转义，**不是脱敏**。

未知接口探针同样保存原始转义路径和原始查询串，`GET /api/admin/probe` 可由管理员读取；按 method + 转义路径去重，查询串不参与去重，因此同一路径不同 query 不保证各有记录。继续排除 `/api/` 和扫描器路径，保持既有速率、去重及历史容量限制；探针不读取请求体，`body` 始终为 `[OMITTED]`。

**危险：即使 `debug: false`，URL 中的密码、API key 和 token 也可能进入 request 日志或探针数据库。** debug 模式还可能记录含密钥的 Webhook 配置和上传内容。不要把日志或探针导出上传到公开 issue、Git 或聊天；确保日志目录、数据库的访问权限与保存期限。debug 仅在受控环境短时启用，排障后改回 `debug: false` 并重启；这只关闭额外采集，不恢复 URL 脱敏，也不会删除旧日志或探针。安全清理已有记录，若已泄漏应轮换凭据。启用 debug 时会打印风险警告。

这是 HTTP 请求诊断模式，不是网络抓包，也不转储响应正文/视频流；响应仅记录状态、耗时等。原有鉴权、请求大小限制（普通请求 1 MiB、图片上传 32 MiB）和限流仍生效；超限拒绝的请求不为凑日志而强行读完整个请求体。请求体会暂存内存并原样交回业务处理，可能增加内存、磁盘占用及延迟；不要长期启用。debug 额外采集的头、请求体、错误与堆栈只进入 request 日志，不新增到后台运行日志面板或持久化未知接口探针；探针原始 URL 记录不依赖 debug 开关。该开关也不会改变可信代理配置或自动修复客户端协议行为。

### 日志最大保存天数

管理员可在 **设置 → 日志保存期限** 中设置 0–3650 天：`0` 不自动清理（默认），建议 7–30 天。非零保存需要确认不可撤销删除，会立即清理现有过期的 app/request 日志，之后启动与跨日写入时检查；配置存入数据库，保存即生效、重启保留。按文件名日期计算且包含今天，不递归、不删其它文件或符号链接；不会改变内存运行日志面板。清理失败会明确提示，不影响正常日志输出。部署需重建并运行新二进制后刷新后台。

### Webhook：联动 SmartStrm 删除远端目录

默认不发送通知。管理员可在 **管理后台 → 设置 → Webhook 多目标** 中配置最多 10 个目标：每个目标独立名称、启用开关、接收地址、13 类事件订阅、JSON/form 编码、超时及内网许可。保存为整集合原子替换并立即生效、重启保留；无需手改 YAML。已有目标地址只写不回显，留空保留、填写新地址替换、显式清除需同时关闭该目标；保存不会试发通知；新增删除订阅需逐目标确认整目录删除风险，清空全部目标需显式确认。多目标配置一旦保存，旧单目标保存接口返回 409，避免覆盖其它地址。

以下 YAML 仅作首次启动/尚未保存后台配置时的后备值；后台保存后以数据库为准，修改 YAML 不会覆盖已有后台设置：

```yaml
webhook:
  enabled: false
  url: ""                   # SmartStrm 的 /webhook/<token> 或 /webhook/emby/<token>
  timeout_seconds: 5        # 1–30 秒，0 使用默认 5 秒
  allow_private: false      # 本机/内网 SmartStrm 需显式改为 true
```

缺省订阅仍只有删除事件：仅管理员通过后台/API **明确删除单部影片且数据库提交成功**，才发送 `item.deleted`，`Item.Path` 为该条目原始本地 `.strm` 绝对路径。扫描自动清理、重建及删除媒体库不会发送删除事件。

也可显式订阅播放、用户、个人状态及人工影片更新事件。下例**替换**默认删除订阅，不会发送删除命令：

```yaml
webhook:
  enabled: true
  url: "https://receiver.example/webhook"
  events: [playback.start, playback.stop, user.authenticated, user.logout, item.rate, item.unrate, item.markplayed, item.markunplayed, item.updated]
  format: json              # 部分 Emby 接收器使用 form：data=<JSON>
  timeout_seconds: 5
  allow_private: false
```

事件包含 Emby 风格的 `Event`、`Date`、`Server`、`Item` 及按需 `User` / `Session` / `PlaybackInfo`，不发送登录 token、密码或 STRM 内容直链；并非 Emby 插件所有事件/字段的完整实现。`playback.progress` 可选但高频，通知同步限时，订阅会增加播放上报延迟。

> **危险：SmartStrm 收到 `.strm` 路径后会去掉文件名，映射并删除远端整个文件夹，可能包含其它影片。** 请先用独立测试目录，核对其 `strm_in_emby`、`storage_path_map`、`storage`、`allowed_prefix` 和删除同步开关；不要用真实收藏试发通知。Emby-go 不直接删除本地 STRM/NFO，但配置的接收端会产生真实远端删除。

发送为提交后单次、同步、限时的 best-effort 通知，无重试或持久队列；单事件默认 5 秒总预算、最多 4 个目标并发，未获得预算的目标标记 `skipped_budget` 且不算成功。失败不撤销本地删除，仍返回 204，通过 `X-Webhook-Status: failed`/`partial`、后台提示及脱敏日志告知（`partial` 表示部分目标未送达，业务操作已完成）。`sent` 仅表示接收端确认，不保证远端最终状态；超时也可能已经删除，**不要盲目重发**。配置 URL 中的 token 是敏感凭据，不要提交或分享。

路径映射、事件 JSON 与状态语义以上文为准；接收端（如 SmartStrm）自身的路径映射与删除范围须在独立测试目录核对。

### 公网部署（D04 门禁）

- **TLS 由反向代理终止**：本服务不内置 TLS。当前 `port` 只能设置数字且绑定全部网卡，不能用它绑定回环；由防火墙/网络隔离限制后端端口，再由 Nginx/Caddy 对外提供 https，不要误以为访问回环地址就限制了监听范围。
- **初始化保护**：公网暴露前设置 `setup_code`（初始化必须携带），或配置 `initialize_allow_from` 限定来源 CIDR。
- **代理头**：默认不信任 `X-Forwarded-For`；只有配置 `trusted_proxies` 后限流/日志才使用代理提供的真实 IP。
- **单实例**：SQLite 为唯一事实源，不要多实例共享同一 SQLite 文件；Redis 可选，内存兜底仅单实例安全。
- **放行条件**：A03（未认证 401 不泄露）、A14（超大请求体 413）与 D01（会话期限）均已本地通过；公网启用仍需完成 PLAN-STATUS §3 的剩余人工项与备份/恢复演练。

## 备份与恢复（D11）

```bash
# 每日一次：SQLite 一致性快照 + 各媒体库 .nfo + 规范额外背景图，保留最近 7 份
go run ./cmd/embybackup -db ./data/emby.db -out /backups/emby -retain 7
```

- 备份目录按 UTC 时间戳命名，内含 `db.sqlite`、`nfo/lib-<id>/` 与 `extra-backdrops/lib-<id>/`，两类文件均保留库内相对路径；只删时间戳命名的旧目录，手工目录不会被清理。
- CLI 默认同时复制 NFO 和规范 `extrafanart/fanartN.jpg` 普通文件（N 为无前导零的正整数，不跟随额外图目录/文件符号链接）；可独立用 `-nfo=false`、`-extra-backdrops=false` 关闭。只备份自定义数据库、没有 `libraries` 表时需同时关闭这两个开关。额外图由物理目录盘点，不依赖新索引列，因此旧应用数据库也可备份图片；不包含 STRM、视频、原三张封面或其它 JPG。库离线跳过，在线库遍历/复制错误使本次额外图备份失败；数据库与文件不是跨介质联合快照，备份期间应避免修改媒体目录。
- 输出根目录必须与源数据库分离，并与各媒体库互不包含：不能相同、位于库内或作为库的祖先，即使关闭 NFO 复制也必须满足。已有路径的符号链接也会解析后校验，冲突会在任何备份写入或清理前拒绝。请使用独立备份目录，避免自包含复制或误清理源数据。
- 身份安全更新会自动执行 schema migration 14：保留现有账户和库授权，为账户建立不复用 ID 与会话凭据代际。升级前请完成备份；旧二进制不能直接打开升级后的数据库。需要回退时必须停服恢复升级前完整数据库备份，不能删除迁移记录或直接降级二进制。有留存引用的历史用户 ID 会纳入序列高水位；旧库中已删除且无任何留存引用的 ID 无法追溯，迁移后的新 ID 则永久不复用。
- 恢复：停止全部写入 → 用匹配备份的 `db.sqlite` 覆盖数据库、把 `nfo/lib-<id>/` 和 `extra-backdrops/lib-<id>/` 中的内容按原相对路径放回对应媒体库 → 按下述流程核对原始状态 → 隔离启动并完成凭据处置 → 必要时重新扫描 → 才恢复对外服务。新索引保存绝对路径，迁移媒体根目录时还需调整库路径并重新扫描。恢复为手工操作、没有自动覆盖冲突策略；合并复制不会删除备份中没有的新图片，要求精确回退时须先对账，否则重扫会重新收录。数据库单独恢复不能找回丢失图像，离线库或关闭开关的文件也不在备份中。
- 原生跨版本回退必须使用**升级前同一停写窗口的匹配集合**：旧二进制及其版本/校验和、配置、SQLite、人工 NFO、需要恢复的额外图。停止应用还不够：外部整理器/编辑器也应停写；在恢复集合中记录库 ID 与根目录映射、复制完成时点以及未纳入的离线库/文件。CLI 不复制配置或原三张封面，若升级会改动这些内容，必须另行保留；不要把在线 DB 快照与随后变化的文件宣称为联合快照。
- 精确回退时，先停服务并确认没有数据库句柄/媒体写入者，保留当前失败环境以便排查；恢复匹配 SQLite（同时处理停服后的 WAL/SHM，不把新库 sidecar 留给旧库）、配置和对应 NFO/图片，并核对/移走快照以后新增的文件。**先比较原始业务行、授权/播放数据、文件字节及文件清单，再启动并重扫**；不能靠一次重扫掩盖不匹配。换回旧二进制但沿用新版数据库不是回退路径，严禁删 migration ledger 伪造降级。可接受的数据损失窗口为快照完成后至停写恢复前的全部写入，不是零损失；是否接受应由运维在升级前确认。
- 恢复材料包含密码摘要、API 密钥摘要、可能含源 URL/凭据的 NFO 与配置，**摘要化不等于备份不敏感**。备份工具不加密且目录模式不自动保证私密；预建仅备份账号可访问的父目录（例如 `0700`）、受控传输/离线存放，不置于 Web 根或公开产物，不在日志打印内容。恢复旧 DB 可能复活快照时仍有效的会话/密钥，也可能回退之后的撤销；在隔离维护环境恢复后、重新对外提供服务前，按 D05 轮换管理员密码并撤销全部会话/API 密钥，另行检查配置/源 URL 凭据。恢复到不支持这些管理操作的旧版本时，不能沿用当前版本的安全处置承诺，应继续隔离并先落实该版本的处置流程。
- 契约：RPO 24h / RTO 1h；Redis 不备份；备份不含管理员密码明文（D03 摘要化后数据库内只有密钥摘要）。

维护者可用 `go test ./cmd/embybackup -run '^TestBackupCLIOpenWALSnapshot$' -count=1 -v` 复演[活动WAL快照测试](<cmd/embybackup/wal_test.go>)：合成SQLite源库保持连接及未提交写事务，真实CLI读取已提交但未checkpoint的数据；源库后续提交不会改变快照，单独复制备份DB即可恢复并通过integrity检查。此测试关闭NFO/额外图备份，不代表实际应用负载、跨文件一致性、崩溃/断电或正式制品验收；证据见[台账 §69](<PLAN-STATUS.md#69-n-01-实际备份cli的活动wal快照2026-11-06>)。

## 原生 Linux 部署样例

采用原生二进制、root 平铺部署（SQLite，Redis 可选）：程序、SQLite 与图片缓存都在 `/opt/emby-go`，配置为 `/etc/emby-go/config.yaml`，由 systemd 的 [服务模板](<deploy/emby-go.service>) 启动，[首装脚本](<deploy/install.sh>) 负责下载、校验与注册。**不再支持 Docker**，容器构建与 Compose 样例已移除。模板与脚本是需按主机调整的安装样例，不代表已通过真实 systemd、升级回退或所有 Linux 发行版验收；其它平台也不能据此列入支持矩阵。

### 安装与 Web 自升级

```bash
sudo ./deploy/install.sh                        # 安装最新 Release
sudo EMBY_GO_VERSION=v2.2.7 ./deploy/install.sh # 指定 Release 标签
sudo EMBY_GO_MIRROR=https://mirror.example.com/github.com ./deploy/install.sh
                                                # 无法直连 GitHub 时用镜像下载资产
```

脚本以 root 运行：按架构下载 `emby-go-linux-<arch>` 与 `SHA256SUMS`，校验后把程序装到 `/opt/emby-go/emby-go`；首次写入 `/etc/emby-go/config.yaml`（已有配置不覆盖），注册 `emby-go.service` 并启动。它不保留旧版本、不创建 `releases/` 或 `backups/`、不使用 `current` 软链接，也不迁移 `/var/lib/emby-go` 旧数据。

页面自升级同样支持镜像：在 `emby-go.service` 里设置 `Environment=EMBY_GO_RELEASE_MIRROR=https://mirror.example.com/github.com` 并重启一次服务，之后下载会改写到 `<镜像>/<owner>/<repo>/releases/download/<tag>/<资产>`。镜像只影响下载地址，`SHA256SUMS` 校验、`-version` 核对与原子替换不变；镜像必须是 `https://`，否则检查阶段直接报错。当前自升级单次下载预算为 10 分钟（含校验与替换），直连 GitHub 过慢时它会失败并保持旧程序不动。

安装与 Web 自升级都要求目标 Release 附带 `SHA256SUMS` 和同批部署材料（当前 release workflow 会生成）。缺少校验和时脚本或管理端直接报错停止，不会退化成无校验安装；旧发布流程产出的 Release（如仅含二进制的 `v2.2.6`）需先用新 workflow 出包，才能用于首装或页面升级。`EMBY_GO_VERSION` 仍可指向任一包含这些资产的标签。

管理员可在管理页左下角点击版本号检查更新：页面打开时会静默检查一次（服务端缓存 10 分钟），发现新版本在版本号旁显示徽标。确认后服务端重新核对目标版本，下载同平台资产与 `SHA256SUMS`，落到 `/opt/emby-go` 同目录临时文件，校验 SHA256 并实际执行 `-version`；通过后 `chmod 0755` 并 `os.Rename` 原子替换 `/opt/emby-go/emby-go`。随后进程发出重启信号，按正常有界关闭路径以 0 退出，systemd 由 `Restart=always` 拉起新程序；页面轮询升级进度，服务恢复后读取 `/api/version`，只有实际运行的版本与目标一致才显示成功，成功后提供「重新加载页面」。

**没有自动回退**：不保留上一版程序，也没有备份目录；替换前失败时旧程序仍在，替换后若新程序起不来，需管理员按[原生发布与回退门禁](<docs/native-roadmap-design.md#34-原生发布升级与回退>)手工处置。升级接口仅管理员可用，且只接受刚检查到的那个版本，客户端不能指定下载源或安装路径。

维护者可在已运行 systemd 的 Linux 测试主机上，以 root 显式执行 `EMBY_GO_TEST_SYSTEMD=1 go test ./cmd/metatube -run '^TestNativeSystemdSandbox$' -count=1 -v`，复演[临时单元沙箱测试](<cmd/metatube/native_systemd_test.go>)；默认跳过，显式启用而条件不足时失败。测试只创建唯一 transient unit、临时安装/状态/配置目录及数字 UID/GID 进程，不创建账号或改现有服务；停止并确认不活跃后清理自身目录。它验证样例安全选项的本地子集，现已覆盖夹具自建数据/运行目录的权限与状态保留，以及 `Restart=always`/`RestartSec=3s` 下真实主进程 SIGKILL 后的自动重启、新进程健康/身份/沙箱，和正常退出（Web 自升级依赖的路径）后 systemd 自动拉起新进程；另通过HTTP接纳带独立进程组、忽略TERM的已知探测父子夹具，验证 `systemctl stop` 正常退出、后代不再运行及STRM/NFO/Movie未被成功写入。另有[实际SIGTERM请求门回归](<cmd/metatube/native_shutdown_test.go>)使用已鉴权HTTP请求的100-continue把请求停在body读取：发送真实SIGTERM，观察新readiness连接被关闭/拒绝或收到503，再提交剩余body并要求原连接收到503 `server shutting down`；这验证已有连接中的迟到任务不被登记，外部连接失败不冒称HTTP 503。后台探测清理、NFO/Movie不变及17秒测试余量继续检查，详见[台账§74](<PLAN-STATUS.md#74-n-01-实际sigterm的http请求门与对外readiness2026-11-06>)。此为本机模拟负载，不是实际ffprobe解析或任意NAS硬实时关停；不测试既有正式目录迁移、完整安装路径或发行平台矩阵，详见[台账 §61](<PLAN-STATUS.md#61-n-01-systemd活跃探测关停2026-11-05>)。

systemd回归现直接读取上述[部署样例](<deploy/emby-go.service>)的Service段，而非手抄另一份安全/重启策略：在临时夹具中替换四项身份/路径并指定测试启动命令，附加只读绑定和观测命令，保留其余样例策略（含 `Restart=always`）并核对管理器实际属性。无需root的 `go test ./cmd/metatube -run '^TestNativeSystemdServiceTemplate$' -count=1` 检查样例与不支持的重复/续行/命令变更；遇到复杂语法须先适配测试，不静默跳过。此为样例派生的临时执行，不验证Unit/Install依赖、开机启用或正式 root 安装路径，证据见[台账§75](<PLAN-STATUS.md#75-n-01-部署样例驱动的真实systemd回归2026-11-06>)。

同时设置 `EMBY_GO_TEST_SYSTEMD=1 EMBY_GO_TEST_FFPROBE=/usr/bin/ffprobe` 运行上述systemd测试，会在前三个场景之后追加真实探测启动：配置改用裸命令 `ffprobe`，核对服务实际PATH及所选可执行文件，再通过本地HTTP提供半秒MP4，验证实际探测、NFO/mediainfo写入、显式重扫后的索引与browse/PlaybackInfo/精确302。所提供的ffprobe路径必须与部署PATH实际选中者一致，否则失败；没有真实ffprobe开关时，不把原三场景当作真实探测成功证据。样例小片整数秒结果为0，测试以原NFO的7秒陈旧时长验证显式重扫确实更新索引。仍不证明正式账号/安装路径、真实远端媒体或播放器起播；本轮执行状态和限制见[台账§76](<PLAN-STATUS.md#76-n-01-systemd沙箱内真实ffprobe成功探测2026-11-06>)。

第四次启动还会运行[文件读取边界组合](<cmd/metatube/native_systemd_read_boundary_test.go>)：要求fixture实际位于本地ext4，通过UID65534服务的HTTP扫描验证STRM/NFO超限、FIFO/指向FIFO的链接、root所有0600文件的读取失败，以及普通文件链接成功。故障组保留旧Movie/演员/分组基线，不完整扫描禁止删除缺失项，健康组仍可提交；恢复文件后完整扫描才清理缺失项。成功响应携带最终清理结果，失败HTTP500的部分结果从进度接口核对，不要求错误响应暴露底层errno。§77初版执行状态见[台账](<PLAN-STATUS.md#77-n-01-systemd实际身份的文件读取故障组合2026-11-06>)；后续[增量与CD依赖组合](<cmd/metatube/native_systemd_stack_boundary_test.go>)扩展为full/incremental共24例（22故障＋2普通链接正向），加入非空播放/收藏保留、修复前健康组落库和变化title恢复，incremental恢复计数为1 changed/1 unchanged。套件一次真实登录复用token，不修改生产限流；当前执行证据与限制见[台账§80](<PLAN-STATUS.md#80-n-01-增量与cd依赖的实际身份读取组合2026-11-06>)。CD八例仅为四类依赖超限，不覆盖CD完整FIFO/权限矩阵、并发增长/替换、NAS或正式安装。

上述原生fixture现复用[发布配置构建验证](<cmd/metatube/native_release_fixture_test.go>)：本机目标使用 `CGO_ENABLED=0`、`-trimpath`、`-s -w`，子构建忽略开发机Go持久配置、额外编译选项/实验并固定默认微架构级别；实际检查buildinfo、静态ELF/裁剪与 `-version` 后才执行运行场景。它仍注入明确fixture版本而非正式标签/SHA，不是官方发行artifact或其它平台的运行证明，详见[台账 §62](<PLAN-STATUS.md#62-n-01-发布配置原生fixture2026-11-05>)。需要同包复演时可同时提供`EMBY_GO_NATIVE_BINARY=/private/path/emby-go`与`EMBY_GO_NATIVE_SHA256=该文件的64位十六进制摘要`：入口校验摘要后复制同一已读字节，继续执行profile/ELF/精确fixture身份检查；任一项缺失、格式错误或摘要不符即失败，不回退重新构建。三项身份环境全空时仍要求`native-rehearsal (commit fixture, built fixture)`；验证其他候选须另成套提供`EMBY_GO_NATIVE_VERSION`、`EMBY_GO_NATIVE_COMMIT`、`EMBY_GO_NATIVE_BUILD_TIME`，作为CLI整行、重启HTTP三字段和当前升级version的显式预期，任一缺项或不匹配即失败。它们不用于重编默认fixture、不推断或认证来源；当前升级HTTP只比较version，shutdown/systemd仍只在共同入口核对CLI身份。污染构建环境与身份拒绝回归主动隔离全部五项输入并独立编译；显式身份及具名本地候选证据见[台账§78](<PLAN-STATUS.md#78-n-01-显式候选身份与具名本地制品2026-11-06>)；选中该测试时才执行。systemd、正式/本地前版路径等原有开关仍须分别设置；固定候选不把SKIP变成通过，也不把父测试race变成子候选race。私有源码快照和同包实跑证据另见[台账§73](<PLAN-STATUS.md#73-n-01-冻结源码与同包原生候选复验2026-11-06>)。

维护者可用 `go test ./cmd/metatube -run '^TestNativeBinaryRestart$' -count=1 -v` 复演[真实进程重启回归](<cmd/metatube/native_lifecycle_test.go>)：两次启动之间实际SIGTERM退出，核对服务ID/SQLite设置，并经真实HTTP创建active/stopped播放会话；重启沿用原token，六种跨影片上报逐一要求409且选定播放表、序列和缓存版本不变，同片重复停止仍204且不重复计数。root运行时同时覆盖数字UID/GID65534，非root会明确跳过该身份组。仅[元数据种子与播放上报](<cmd/metatube/native_restart_playback_test.go>)，不是播放器成功播放、断电恢复或正式安装证明；同包候选输入沿用上文，执行结果见[台账§82](<PLAN-STATUS.md#82-n-01-真实进程重启后的播放会话绑定2026-11-06>)。

维护者可用 `EMBY_GO_PREVIOUS_BINARY=/path/to/frozen/emby-go-previous go test ./cmd/metatube -run '^TestNativeBinaryUpgradeAndMatchingRestore$' -count=1 -v` 复演[升级与匹配恢复测试](<cmd/metatube/native_upgrade_test.go>)。当前只接受测试中固定版本身份的 `v2.2.6-local-rehearsal` 本地冻结二进制；不是任意旧 Release 或官方产物的兼容证明。未提供路径时跳过不算验收，提供但版本不符或无法执行时失败。演练在临时目录运行旧 schema16→当前20、两次当前版启动及匹配集合恢复；既有 token/API key 均需通过[真实 HTTP 契约](<cmd/metatube/native_upgrade_api_test.go>)验证普通用户身份、库权限、收藏/进度、PlaybackInfo、302及跨库/管理访问拒绝。另原样请求PlaybackInfo返回的相对流URL（不附加鉴权头），验证query凭据能取得精确302、仅去掉api_key后为401；这不是实际媒体下载或播放器成功起播的证明。不会跟随302访问远端，也不提交新登录或播放事件；恢复旧凭据仍有效是演练事实，不是对外重新启用旧凭据的安全建议，恢复后的凭据处置仍须按上文执行。覆盖限制和本机证据见[台账 §64](<PLAN-STATUS.md#64-n-01-升级匹配恢复的实际认证契约2026-11-06>)；返回流URL的认证补齐见[台账 §65](<PLAN-STATUS.md#65-n-01-返回播放url的真实query认证2026-11-06>)。非空成功/失败登录历史、非默认认证代次与API key版本/轮换时间的SQL种子及升级恢复证据另见[台账 §66](<PLAN-STATUS.md#66-n-01-非空登录历史与非默认认证元数据2026-11-06>)；此为历史数据保留检查，不等于真实登录/轮换操作的端到端验收。演练现实际构建并运行embybackup CLI，在省略NFO/额外背景图开关的情况下生成停机旧库快照、两库NFO和两张不同的额外背景图；恢复前破坏一图、删除另一图，再重新读取磁盘CLI产物恢复并逐字节核验。配置、STRM、普通海报仍额外人工保存，不属于该工具输出；磁盘图片非空不代表数据库图片索引非空。这不是在线WAL、崩溃一致性或自动恢复器验收，生产服务接入历史及当前CLI证据分别见[台账 §67](<PLAN-STATUS.md#67-n-01-生产备份产物参与匹配恢复2026-11-06>)、[台账 §68](<PLAN-STATUS.md#68-n-01-实际备份cli与非空背景图匹配恢复2026-11-06>)。

另可在Linux amd64使用 `EMBY_GO_PREVIOUS_RELEASE_BINARY=/path/to/downloaded/emby-go-linux-amd64 go test ./cmd/metatube -run '^TestNativeReleasedBinaryUpgradeAndMatchingRestore$' -count=1 -v` 复演同一升级/匹配恢复流程的[正式前版制品入口](<cmd/metatube/native_upgrade_test.go>)。这里只接受[仓库v2.2.6 Release](https://github.com/holll/Emby-go/releases/tag/v2.2.6)的Linux amd64资产701031564，测试先固定校验SHA256，再核对精确版本；未配置时SKIP不算验收，错误摘要在执行前失败。可用 `gh release download v2.2.6 --repo holll/Emby-go --pattern emby-go-linux-amd64 --dir /path/to/new-empty-directory` 获取原始资产；[GitHub资产元数据](https://api.github.com/repos/holll/Emby-go/releases/assets/701031564)的digest与测试固定摘要相同。此处旧版是实际Release下载制品，但当前版和备份CLI仍为本地源码构建；不是当前正式发布、制品签名或完整平台/schema支持矩阵证明，证据见[台账§70](<PLAN-STATUS.md#70-n-01-正式前版release制品升级与匹配恢复2026-11-06>)。

上述两个前版入口现比较全部旧影片列（连同列名/列序，只排除另行断言的新迁移列），并加入非默认元数据/技术/图片路径种子；恢复前特意更改旧快照未覆盖字段，要求能检测变化且匹配恢复后还原。这仍不是全库schema对象或sequence/ledger保留证明，详见[台账§71](<PLAN-STATUS.md#71-n-01-旧影片全列保留与范围外字段恢复2026-11-06>)。

管理员可先运行[只读安装静态核验工具](<deploy/verify-native-install.sh>)：`sudo sh deploy/verify-native-install.sh EXPECTED_BINARY_SHA256`。摘要须来自已核对的候选材料，不能现场给被验收文件算摘要再当作独立预期。工具按平铺布局检查 `/opt/emby-go/emby-go` 是否为 root:root 0755 且摘要匹配、`/opt/emby-go` 与 `/etc/emby-go` 目录模式、`/etc/emby-go/config.yaml` 是否为 root:root 0600；不执行程序、不读取配置内容、不打开 SQLite、不创建账号或控制服务。可选第二参数为绝对暂存根目录，仅给文件路径加前缀，不是 chroot 或正式布局运行证明。静态 PASS 不代表服务状态、媒体 ACL、schema/恢复、systemd 有效策略或 M1 放行。

核验工具的参数回归不需 root：`go test ./cmd/metatube -run '^TestNativeInstallCheckArguments$'`。Linux/root 下可直接跑隔离布局回归：`go test ./cmd/metatube -run '^TestNativeInstallCheckLayout$' -count=1 -v`；它只操作测试临时目录，覆盖缺二进制、非可执行、错误所有者/模式、错误摘要与正确路径，并断言审计前后夹具字节、模式与所有者不变。非 root 环境自动 skip。

以下为使用 systemd 的 Debian/Ubuntu 类主机安装步骤，供管理员审核后执行。推荐直接运行[首装脚本](<deploy/install.sh>)（需要 root，会下载对应架构资产、用 `SHA256SUMS` 校验、写入配置并注册服务；不保留旧程序、不建 `releases/`/`backups/`、不迁移旧数据、不覆盖已有配置）：

```bash
sudo ./deploy/install.sh
sudo EMBY_GO_VERSION=v2.2.7 ./deploy/install.sh   # 指定 Release 标签
```

从源码构建时手工执行等价步骤：

```bash
go build -o emby-go ./cmd/metatube
./emby-go -version
sudo install -d -m0755 /opt/emby-go
sudo install -m0755 emby-go /opt/emby-go/emby-go
sudo install -d -m0755 /etc/emby-go
if ! sudo test -e /etc/emby-go/config.yaml; then
  sudo install -m0600 config.example.yaml /etc/emby-go/config.yaml
fi
sudo install -m0644 deploy/emby-go.service /etc/systemd/system/emby-go.service
```

本批发布流程新增 `SHA256SUMS` 生成与校验；实际下载的发行版若附有该文件，须在核对可信来源后用 `sha256sum -c SHA256SUMS` 验证对应产物，旧 Release 不保证提供它。本地流程修改不代表产物已经发布。

启动前编辑 [配置样例](<config.example.yaml>) 对应的私人配置，设置随机 `setup_code`、初始化来源限制及实际端口；配置含秘密，不应公开。数据布局：程序为 `/opt/emby-go/emby-go`，配置为 `/etc/emby-go/config.yaml`；`WorkingDirectory=/opt/emby-go` 让默认相对 SQLite、日志及图片缓存都留在同一目录。服务以 root 运行，这是平铺自升级的明确取舍——升级必须原子替换 `/opt/emby-go/emby-go` 并写 `/opt/emby-go`；`ProtectSystem=strict` 仍把系统其余部分设为只读，`ReadWritePaths=/opt/emby-go /etc/emby-go` 只放行这两个目录。首次启动会回写 `server_id`，所以配置目录整体可写；既有配置也须人工核对这些权限。

**媒体库权限必须显式配置：**单元保留 `ProtectSystem=strict`、`ProtectHome=true`，默认不允许改写媒体库。以 root 运行时读取一般不受 Unix 权限限制，但 `ProtectHome`、挂载命名空间与 NAS 侧授权仍可能阻止访问。要使用 NFO 编辑、探测、刮削或图片保存，还需在存储侧允许 root 写入，并在 `sudo systemctl edit emby-go` 中按真实媒体库根目录追加：

```ini
[Service]
ReadWritePaths=/srv/media
```

上例 `/srv/media` 必须替换为存在的实际路径；`ReadWritePaths` 只解除 systemd 只读限制，**不会授予 Unix/NAS 写权限**。不要递归改属主或授予所有人写权限来代替核对。`/home` 等目录仍被 `ProtectHome` 隐藏，优先把媒体挂载到专用路径，不要直接关闭全部加固。未配置媒体库写权限时只能使用其可读范围，不要期待写入动作成功。

确认初始化网络隔离、配置、目录权限及可选探测依赖后，再由管理员启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emby-go
sudo systemctl status emby-go
```

`TimeoutStopSec=30s` 给进程内 15 秒有界关闭留出宽限，`KillMode=control-group` 为同组子进程提供停止兜底；这些字段不证明失联 NAS 的不可中断 I/O 可按时退出。Web 自升级只替换程序，不改数据库 schema 或配置文件；跨版本仍需按[原生发布与回退门禁](<docs/native-roadmap-design.md#34-原生发布升级与回退>)在升级前保存匹配的数据库、配置与 NFO/图片恢复材料，且不能只换回旧二进制或冒称已完成版本/schema 矩阵验收。公网监听和代理配置仍见上文「公网部署」。

### 探测媒体信息失败

媒体信息探测是可选功能，需在宿主机单独安装提供 `ffprobe` 的 FFmpeg 软件包；不安装不会阻断浏览和 302 播放。Debian/Ubuntu 可由管理员执行 `sudo apt-get install ffmpeg`，其它发行版按其软件源安装，再运行 `ffprobe -version` 核对可执行性。systemd 样例的 PATH 为 `/usr/local/bin:/usr/bin:/bin`，不会继承交互式 shell 的自定义 PATH；安装在其它位置时，在私人配置中将 `ffprobe_path` 设置为绝对路径，并确认 `emby` 可以执行且未被沙盒隐藏。

设置页「媒体信息探测」会显示解析结果与失败原因；「运行日志」面板可查看最近 500 条脱敏日志（完整日志在 `log/app-*.log`）。常见原因：未安装 ffmpeg、服务用户找不到或无法执行 `ffprobe`、`ffprobe_path` 配置错误、媒体库未授予写权限，或 `.strm` 指向的远端链接无法被服务器访问。交互终端验证不替代实际服务身份和 systemd 沙盒下的验证。

探测采用每次最多 60 秒预算（包含等待进程名额，更短配置保留），批量 worker 默认/上限 2，启动校验与实际探测在同一进程共享 2 个子进程名额；旧高值配置仍可加载，按新有效上限执行。stdout 上限 4MiB、stderr 上限 64KiB，超限报告失败，不把截断结果写进 NFO 或 mediainfo 缓存。Unix 取消/超时/超限会终止同组子进程；其它平台只保证直接子进程。继承管道最多另有 1 秒收尾，不是 OS 不可中断 I/O 或远端服务的硬实时停止保证。详见 [配置样例](<config.example.yaml#L108-L112>) 与 [API 执行边界](<API-CONTRACT.md#410-ffprobe-执行边界n-01-本地已验收子项>)；各目标独立提交，失败不回滚此前其它目标的成功写入。

默认信任媒体库中的 STRM，ffprobe 直接访问源 URL；不会通过下载代理隔离私网、重定向或嵌套资源。只导入可信来源，恶意链接可能使用服务进程的网络权限。探测错误响应与日志会脱敏源 URL 凭据，但合法 302 播放仍保留客户端取源必需的原始地址，不要把错误脱敏当作网络隔离。

维护者可用 `EMBY_GO_TEST_FFPROBE="$(command -v ffprobe)" go test ./internal/server -run '^TestTrustedSTRMProbeFailureKeepsPlaybackAndRedactsResponse$' -count=1 -v` 复演真实探测错误、部分成功脱敏及缺失/失败后仍可浏览与302播放的[本地回归](<internal/server/probe_trusted_strm_test.go>)。使用临时库与本机HTTP媒体fixture，不访问真实媒体库；未指定变量时跳过，显式路径不可用则失败。普通测试门禁另含不依赖安装ffprobe的错误脱敏回归；本地fixture不替代真实客户端、远端媒体协议或服务身份验收，证据见[台账§63](<PLAN-STATUS.md#63-n-01-可信strm探测错误脱敏与播放契约2026-11-0506>)。

## 开发

```bash
go build ./...
go vet ./...
go test ./...
node --test internal/server/web*_test.cjs   # 前端 VM 测试
```

`main` 分支推送与 PR 会跑 [ci.yml](.github/workflows/ci.yml)：build/vet/test、认证与出站关键包的 race，以及前端 VM 测试。发布仍由 `v*` tag 触发。

### 发布（GitHub Actions）

推送形如 `v1.0.0` 的 tag 后，发布工作流先在**同一事件 SHA** 复用完整 CI（Go/覆盖率、race、漏洞与前端），全部成功才构建多平台二进制并上传 Release。检查和构建均显式 checkout `github.sha`，不借用不同提交的历史绿色结果；失败、取消或未通过不会进入发布构建，也不自动等待/重试。构建继续从实际 HEAD 写入版本与提交、执行本机版本输出检查，并生成和验证同批产物的 `SHA256SUMS`。这是自动化门禁，不替代 N-01 支持矩阵与人工发布验收；本地契约测试不代表远端工作流或正式产物已经通过：

```bash
git tag v1.0.0
git push origin v1.0.0
```

## 设计参考

- [API 契约](<API-CONTRACT.md>)：对外端点、字段、错误码与弃用期；Emby 协议兼容版本为 4.9.5.0，不等于已验证所有真实客户端。
- [设计边界与后续计划](<dev-plan.md>)：当前模块、正确性边界与尚待评估的方向。
- [施工台账](<PLAN-STATUS.md>)：历史决策、本地验证证据与未完成的部署验收。
- 源码研究使用的旧版 Emby 是机制参考，不是现代协议的完整实现证明；本仓库不附带第三方源码或完整 OpenAPI 快照。

## 范围与不做

当前支持管理员与普通用户、按媒体库授权；仅 `http(s)-strm` 直拉播放，不做转码/HLS/直播音乐/通用刮削器插件（内置 MetaTube 适配器不在此列）。权限与图片公开边界见上文「权限边界」说明和 [API 契约](<API-CONTRACT.md#L89-L96>)。

## 本地字幕标记整理

管理员在影片详情展开维护区，打开「字幕标记整理」，先查看本地证据、当前与拟提交标签/归属，再明确确认。不需要 MetaTube 配置，也不会翻译、探测媒体或下载图片；只处理单部已索引影片的现有 STRM/NFO。

- 默认只补缺失，已有标签或字段锁会受保护；需要覆盖时显式选择并重新查看预览。覆盖只撤销程序拥有的标签，不追认或删除人工同名标签。
- 只按文件名显式 C/U/UC 或同目录同主名的中文外挂字幕推断，不读字幕内容；多 CD 只看当前主片，不聚合其它分段字幕。
- 依据变化会拒绝旧确认，须重新预览；关闭预览不保存，也不会自动重试写入。网络失败不能证明未保存，应先核对实际状态。
- 部分成功分别显示 NFO、归属和索引结果。归属失败需要人工核对，**重扫不能恢复归属**；索引失败可重扫。不要直接重新整理来尝试修复部分成功。
- 旧海报不会重绘，角标可能未同步。前端嵌入二进制，源码新增入口需重新构建、重启并刷新后台；本地实现通过不等于正式发布。接口及边界见[独立入口契约](<API-CONTRACT.md#481-独立本地字幕标记整理入口mt-04>)。
