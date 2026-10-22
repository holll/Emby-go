# Emby-go

一个用 **Go** 重写的、兼容 **Emby API** 的轻量媒体库服务端，面向 **AV / FC2 类 NFO + `.strm` 媒体库**（海报墙浏览走 Emby 客户端）。

- 元数据真源 = **现成 NFO**（`<title>/<genre>/<studio>/<actor>/<set>` 等）；可选内置 MetaTube 刮削与翻译补全
- 媒体只支持 `http(s)://` 的 `.strm`；播放走 **302 直拉**（服务端零带宽，不转码）
- 单二进制、单端口；Redis 缓存可选（未配置或连接失败回退进程内缓存，仅单实例安全）；内置深色 Web 管理后台
- 兼容端点覆盖：认证、媒体库/影片墙、详情、图片、合集(BoxSet)、进度/续播、搜索/过滤等 Emby 客户端浏览主链路

## 快速开始

先读 [快速上手：从本地启动到 Emby 浏览与播放](<docs/quickstart.md>)，其中包含前置条件、最短操作路径、API 调用、预期结果与常见错误。内容按当前源码核实；未执行真实实例验证，不承诺给定媒体源可播放。

**前置条件**：源码构建需满足 [go.mod](<go.mod#L3>) 的 Go 1.27.0 要求；准备可读的测试媒体库（同名 NFO + HTTP(S) `.strm`），并为配置、SQLite 和日志提供写权限。Redis 可选，`ffprobe` 仅在媒体信息探测时需要。

### 构建与配置

```bash
go build -o metatube ./cmd/metatube
# 仅在 config.yaml 不存在时复制，避免覆盖私人配置
if [ ! -e config.yaml ]; then cp config.example.yaml config.yaml; fi
```

编辑私有配置后再启动：使用未占用的测试数据库，最短路径可设 `redis_addr: ""`；为首次初始化设置自己的随机 `setup_code`，并按实际来源限制 `initialize_allow_from`。详细字段见 [配置示例](<config.example.yaml>)；其中“Redis 必选”的旧注释与当前内存回退实现不一致。

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
- API 密钥（创建后可直接调用 Emby 接口）
- 任务日志、未知接口探针、设置

后台为响应式布局，桌面 / 平板 / 手机自适应：窄屏（≤860px）侧栏折叠为顶部横向滚动导航、表格与表单自动换行、触屏设备常显卡片操作按钮（无 hover）；播放器按视口限高并适配安全区。

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

> **权限边界**：当前实现已有多用户与媒体库授权，普通用户默认无库授权，不能访问管理接口。API 密钥仍是长期管理员全权凭据，泄露等于交出管理面；只发给可信管理员脚本。图片端点保持匿名公开以兼容浏览器 `<img>`/客户端，不因多用户授权或持有密钥而收窄；图片不具备与影片详情相同的私密性。

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
```

`server_id` 会写入配置文件以保持稳定；`redis_addr` 可选，未配置或连接失败时回退进程内缓存（重启失效、多实例不共享）。

### 原始请求调试日志

在 `config.yaml` 设置 `debug: true`，重启新版本服务后生效；默认 `false` 保持原来的安全脱敏与高频请求降噪。开启后，`log/request-YYYY-MM-DD.log` 记录原始请求 URI（包括未知路径、全部查询参数）、全部请求头、完整的预算内请求体、请求错误和 panic 堆栈；图片、静态资源和流请求也记录，不再跳过。`ListItemIds` 等尚未识别的参数也可见。字符串使用可逆的引号转义，**不是脱敏**。

**危险：登录密码、API key、token、Cookie、含密钥的 Webhook 配置和上传内容可能全部落盘。** 不要把原始日志上传到公开 issue、Git 或聊天；仅在受控环境短时启用，确保日志目录权限和保存期限，排障后改回 `debug: false` 并重启，安全清理已有日志（关闭开关不会删除旧日志）；若已泄漏，应轮换凭据。启动时会打印风险警告。

这是 HTTP 请求诊断模式，不是网络抓包，也不转储响应正文/视频流；响应仅记录状态、耗时等。原有鉴权、请求大小限制（普通请求 1 MiB、图片上传 32 MiB）和限流仍生效；超限拒绝的请求不为凑日志而强行读完整个请求体。请求体会暂存内存并原样交回业务处理，可能增加内存、磁盘占用及延迟；不要长期启用。原始内容只进入 request 日志，不新增到后台运行日志面板或持久化未知接口探针。该开关也不会改变可信代理配置或自动修复客户端协议行为。

### 日志最大保存天数

管理员可在 **设置 → 日志保存期限** 中设置 0–3650 天：`0` 不自动清理（默认），建议 7–30 天。非零保存需要确认不可撤销删除，会立即清理现有过期的 app/request 日志，之后启动与跨日写入时检查；配置存入数据库，保存即生效、重启保留。按文件名日期计算且包含今天，不递归、不删其它文件或符号链接；不会改变内存运行日志面板。清理失败会明确提示，不影响正常日志输出。部署需重建并运行新二进制后刷新后台。详见 [日志保留指南](<docs/log-retention.md>)。

### Webhook：联动 SmartStrm 删除远端目录

默认不发送通知。推荐管理员在 **管理后台 → 设置 → Webhook** 中编辑：启用、接收地址、13 类事件订阅、JSON/form 编码、超时及允许内网。保存后立即生效并存入数据库，重启保留；无需手改 YAML。含 token 的地址只写不回显，留空保留，显式清除需同时关闭通知。保存不会试发通知；开启删除订阅需确认整目录删除风险。详见 [WebUI 配置指南](<docs/webhook-webui.md>)。

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

事件包含 Emby 风格的 `Event`、`Date`、`Server`、`Item` 及按需 `User` / `Session` / `PlaybackInfo`，不发送登录 token、密码或 STRM 内容直链；并非 Emby 插件所有事件/字段的完整实现。`playback.progress` 可选但高频，通知同步限时，订阅会增加播放上报延迟。详见[通用事件及格式指南](<docs/webhook-events.md>)。

> **危险：SmartStrm 收到 `.strm` 路径后会去掉文件名，映射并删除远端整个文件夹，可能包含其它影片。** 请先用独立测试目录，核对其 `strm_in_emby`、`storage_path_map`、`storage`、`allowed_prefix` 和删除同步开关；不要用真实收藏试发通知。Emby-go 不直接删除本地 STRM/NFO，但配置的接收端会产生真实远端删除。

发送为提交后单次、同步、限时的 best-effort 通知，无重试或持久队列；失败不撤销本地删除，仍返回 204，通过 `X-Webhook-Status: failed`、后台提示及脱敏日志告知。`sent` 仅表示接收端确认，不保证远端最终状态；超时也可能已经删除，**不要盲目重发**。配置 URL 中的 token 是敏感凭据，不要提交或分享。

完整路径映射、事件 JSON、状态与安全测试步骤见 [Webhook / SmartStrm 联动指南](<docs/webhook-smartstrm.md>)。

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
node --test internal/server/web_test.cjs   # 前端 VM 测试
```

`main` 分支推送与 PR 会跑 [ci.yml](.github/workflows/ci.yml)：build/vet/test、认证与出站关键包的 race，以及前端 VM 测试。发布仍由 `v*` tag 触发。

### 发布（GitHub Actions）

推送形如 `v1.0.0` 的 tag 会自动在多个平台构建二进制并发布到 Release：

```bash
git tag v1.0.0
git push origin v1.0.0
```

## 设计参考

- 协议契约以真实 Emby 4.9.5.0 + `docs/emby_openapi.json` 为基准，`src/Emby-3.5.2.0` 为源码参照
- 模块研究报告：`docs/emby-source-modules-report.md`
- API 差异与修复记录：`docs/api-review-2026-09-08.md`
- 完整开发计划：`dev-plan.md`

## 范围与不做

当前支持管理员与普通用户、按媒体库授权；仅 `http(s)-strm` 直拉播放，不做转码/HLS/直播音乐/通用刮削器插件（内置 MetaTube 适配器不在此列）。权限与图片公开边界见 [快速上手](<docs/quickstart.md>) 和 [API 契约](<API-CONTRACT.md#L89-L96>)。
