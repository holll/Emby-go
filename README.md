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

`http://127.0.0.1:18080/admin` 提供：
- 总览统计、媒体库管理（可删除库索引）、扫描/重建索引（带实时进度）
- 媒体墙（海报墙 + 无限滚动 + 在线播放；状态/协议筛选、搜索、排序、重读源、删索引）
- 刮削（批量 / 单条预览-确认 / 演员头像）与编辑（pending 影片补录并入编辑链）
- 仅翻译（已有影片单片 / 批量，支持手动启动与计划任务，不重新刮削或下载图片）
- API 密钥（创建后可直接调用 Emby 接口）
- 任务日志、运行日志（磁盘 app 日志增量查看）、未知接口探针、设置

后台保留暗色管理台风格，导航按资产、处理与运维分组。设置分为账户与安全、用户与授权、通知、日志与保留、索引恢复和服务信息；既有 Webhook 目标按需展开编辑。计划任务先展示已有列表，新建/编辑配置按需展开；刮削先展示执行区，连接与策略配置收起；媒体墙和详情把日常浏览与维护操作分层，独立翻译入口不变。

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

管理员可在 `GET /api/admin/security/auth-limits` 查看聚合策略与拒绝计数，并用 `POST /api/admin/security/unlock`（交互会话 + 当前密码 + 原因）解除指定账号或 IP 摘要的失败状态；解锁不会清空全局桶。认证、改密、密钥与解锁路由即使在 `debug: true` 下也不会把请求体、查询串、Authorization/Cookie/X-Emby-Token 原文写入调试或审计日志。

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

在 `config.yaml` 设置 `debug: true`，重启新版本服务后生效；默认 `false` 保持原来的安全脱敏与高频请求降噪。开启后，`log/request-YYYY-MM-DD.log` 记录原始请求 URI（包括未知路径、全部查询参数）、全部请求头、完整的预算内请求体、请求错误和 panic 堆栈；**认证、初始化、改密、密钥轮转/删除与限流解锁等敏感路由始终只记录脱敏模板**，查询串、凭据头与请求体不会因 `debug` 而落原文；图片、静态资源和流请求也记录，不再跳过。`ListItemIds` 等尚未识别的参数也可见。字符串使用可逆的引号转义，**不是脱敏**。

**危险：登录密码、API key、token、Cookie、含密钥的 Webhook 配置和上传内容可能全部落盘。** 不要把原始日志上传到公开 issue、Git 或聊天；仅在受控环境短时启用，确保日志目录权限和保存期限，排障后改回 `debug: false` 并重启，安全清理已有日志（关闭开关不会删除旧日志）；若已泄漏，应轮换凭据。启动时会打印风险警告。

这是 HTTP 请求诊断模式，不是网络抓包，也不转储响应正文/视频流；响应仅记录状态、耗时等。原有鉴权、请求大小限制（普通请求 1 MiB、图片上传 32 MiB）和限流仍生效；超限拒绝的请求不为凑日志而强行读完整个请求体。请求体会暂存内存并原样交回业务处理，可能增加内存、磁盘占用及延迟；不要长期启用。原始内容只进入 request 日志，不新增到后台运行日志面板或持久化未知接口探针。该开关也不会改变可信代理配置或自动修复客户端协议行为。

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
# 每日一次：SQLite 一致性快照 + 各媒体库 .nfo，保留最近 7 份
go run ./cmd/embybackup -db ./data/emby.db -out /backups/emby -retain 7
```

- 备份目录按 UTC 时间戳命名，内含 `db.sqlite` 与 `nfo/lib-<id>/`；只删时间戳命名的旧目录，手工目录不会被清理。
- 输出根目录必须与源数据库分离，并与各媒体库互不包含：不能相同、位于库内或作为库的祖先，即使关闭 NFO 复制也必须满足。已有路径的符号链接也会解析后校验，冲突会在任何备份写入或清理前拒绝。请使用独立备份目录，避免自包含复制或误清理源数据。
- 身份安全更新会自动执行 schema migration 14：保留现有账户和库授权，为账户建立不复用 ID 与会话凭据代际。升级前请完成备份；旧二进制不能直接打开升级后的数据库。需要回退时必须停服恢复升级前完整数据库备份，不能删除迁移记录或直接降级二进制。有留存引用的历史用户 ID 会纳入序列高水位；旧库中已删除且无任何留存引用的 ID 无法追溯，迁移后的新 ID 则永久不复用。
- 恢复：停止服务 → 用备份的 `db.sqlite` 覆盖数据库、把 `nfo/lib-<id>/` 覆盖回对应媒体库 → 启动后重新扫描；恢复后按 D05 轮换管理员密码、撤销全部会话与 API 密钥。
- 契约：RPO 24h / RTO 1h；Redis 不备份；备份不含管理员密码明文（D03 摘要化后数据库内只有密钥摘要）。

## 部署样例

`deploy/` 提供可直接改用的样例（单实例、SQLite、Redis 可选）：

- `deploy/Dockerfile`：多阶段构建静态二进制，运行层为 Alpine + `ffmpeg`（提供 `ffprobe`），非 root 运行，`/data` 挂配置与数据库。
- `deploy/docker-compose.yml`：`mkdir -p deploy/data && cp config.example.yaml deploy/data/config.yaml` 后 `docker compose -f deploy/docker-compose.yml up -d --build`；Redis 用 `--profile redis` 可选启用。
- `deploy/emby-go.service`：systemd 单元，含 `TimeoutStopSec=30s`（大于进程内 15s 有界关闭）与 `ProtectSystem=strict` 等加固。

配置项（含 `setup_code`、`initialize_allow_from`、`trusted_proxies`）见 `config.example.yaml` 与「公网部署」一节。

### 探测媒体信息失败

探测依赖宿主机上的 `ffprobe`（Docker 镜像已内置）。设置页「媒体信息探测」会显示解析结果与失败原因；「运行日志」面板可查看最近 500 条脱敏日志（完整日志在 `log/app-*.log`）。常见原因：未安装 ffmpeg、`ffprobe_path` 配置错误、`.strm` 指向的远端链接无法被服务器访问。

## 开发

```bash
go build ./...
go vet ./...
go test ./...
node --test internal/server/web*_test.cjs   # 前端 VM 测试
```

`main` 分支推送与 PR 会跑 [ci.yml](.github/workflows/ci.yml)：build/vet/test、认证与出站关键包的 race，以及前端 VM 测试。发布仍由 `v*` tag 触发。

### 发布（GitHub Actions）

推送形如 `v1.0.0` 的 tag 会自动在多个平台构建二进制并发布到 Release：

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
