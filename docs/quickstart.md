# 快速上手：从本地启动到 Emby 浏览与播放

完成本指南后，你可以初始化管理员、登记一个已有媒体库、完成扫描，并通过 Emby 兼容 API 查询影片与播放信息。

**适用范围**：仓库提交 `65e5c8d` 的当前实现（核对日期：2026-10-20），不是某个已验证的发行版。新手优先走 Web 最短路径；已有实例的脚本开发者可直接看 [API 调用示例](<#api-调用示例>)。本文依据仓库内契约、实现和测试示例整理；没有收到额外接口文件或业务示例，也没有执行构建、启动服务或真实播放。以下 HTTP 状态和字段均为**源码预期，不是本次运行结果**。

## 前置条件

- 你已经取得本仓库源码，并在仓库根目录操作。下面命令使用 Linux/macOS 的 Bash。
- 从源码构建需要满足 [go.mod](<../go.mod#L3>) 声明的 **Go 1.27.0 或兼容更新工具链**。本轮未确认你是否已安装可用版本；不要把要求擅自降为 Go 1.26。依赖尚未缓存时，构建可能联网下载模块。
- 服务进程可以读媒体库目录，并可以写配置、SQLite 数据文件及日志目录。相对数据库路径基于**进程启动目录**，不是配置文件所在目录。
- 准备一个专用测试媒体库：至少一份同名配对的 `.strm` 与可解析 NFO，以及你有权访问的真实 HTTP(S) 媒体直链。不要用生产库第一次试扫；扫描会写 SQLite 索引，并按磁盘现状清理缺失条目的索引。
- 18080 端口未被占用。浏览器可以访问服务；直接播放时，**客户端**也必须能访问直链。服务端代理播放则需要**服务端**能访问直链。
- Redis **不是必需项**，留空或不可达会回退进程内缓存。`ffprobe` 只用于可选的媒体信息探测；MetaTube/翻译不属于本次最短路径。缓存回退不支持多实例共享。
- 仅当你执行后面的 API 示例时，才需要 `curl` 与 `jq`；本文不要求你安装任何依赖。

> **安全边界**：当前 `port` 只支持数字，实际绑定 `:<port>`（全部网卡），不能用 `port: 127.0.0.1:18080` 或未实现的 `listen` 选项绑定回环。首次尝试请在隔离主机或已有防火墙限制的环境中操作，阻止不可信网络访问该端口。初始化来源白名单只保护初始化，不等于整个服务仅本机可达。公网发布前还需要独立评审 TLS、反向代理及访问控制；本指南不作生产就绪承诺。

## Web 最短路径

### 1. 构建二进制

在仓库根目录执行：

```bash
go build -o metatube ./cmd/metatube
```

**预期结果**：命令退出码为 0，生成可执行二进制。本文没有运行该命令；如果工具链不满足要求，先停止并检查 Go 版本，不要忽略错误继续启动。

### 2. 准备私有配置

确认没有现存的私人配置后再复制，避免覆盖已有配置：

```bash
if [ -e config.yaml ]; then
  printf '%s\n' 'config.yaml 已存在，请检查它，不要覆盖。'
else
  cp config.example.yaml config.yaml
fi
```

编辑复制得到的私有配置，至少检查这些项。下面是要设置的字段，不是另一个必须覆盖写入的完整配置：

```yaml
port: 18080
db_path: "quickstart.db"  # 选择未使用的测试数据库名
redis_addr: ""           # 最短路径不依赖 Redis
session_ttl_days: 30
setup_code: "替换为你自行生成的随机初始化验证码"
initialize_allow_from: ["127.0.0.1/32", "::1/128"]
```

此示例要求浏览器直接从服务所在主机访问；远程访问或反向代理的 TCP 来源不同，需按实际来源审慎配置白名单。保留验证码为你自己生成的值，不要使用上面的占位文本。

**预期结果**：你有一份只用于测试的私有配置。[配置示例](<../config.example.yaml>) 警告不要直接用示例文件启动，因为启动可能回写 `server_id`。其中“Redis 必选”的旧注释与实现不一致；实际回退逻辑见 [server.go](<../internal/server/server.go#L168-L210>)。

### 3. 启动并初始化

```bash
./metatube -c ./config.yaml
```

保持该终端运行。在同一主机浏览器打开 `http://127.0.0.1:18080/`：

1. 首次使用空数据库时，按初始化页面创建管理员：账号至少 3 个字符，密码至少 9 个字符，填写你设置的初始化验证码。
2. 初始化成功后登录。已有管理员的数据库应直接登录，不要再次初始化，也不要为绕过错误删除数据库。
3. 登录后进入 `/admin` 管理后台。

**预期结果**：首次初始化返回 HTTP **204，无响应体**；登录返回 HTTP **200** 并包含 `AccessToken`、`User.Id`、`User.Name`。未初始化状态可通过 `GET /api/auth/status` 的 `initialized` 判断；`setup_code_required` 表示是否需要验证码。

可选：另开终端检查 readiness（无需 token）：

```bash
curl -sS -i http://127.0.0.1:18080/healthz
```

接纳请求时预期 HTTP **200**，响应体为 `{"status":"ok"}`。这只表示进程接纳请求，**不证明**媒体源、Redis、MetaTube 或播放能力可用。

### 4. 登记已有测试媒体库

准备的目录结构应类似下面这样。这里的名称只是示意，不是仓库自带的可播放资源：

```text
测试媒体库/
└── DEMO-001/
    ├── DEMO-001.strm  # 一行你有权使用的真实 http(s) 媒体 URL
    ├── DEMO-001.nfo   # 同名且可解析的 Kodi/Emby 风格 NFO
    └── poster.jpg     # 可选，不提供则不保证有封面
```

一份只用于理解格式的最小 NFO 示例（真实测试时请使用你自己的元数据）：

```xml
<movie>
  <title>你的测试影片</title>
  <year>2026</year>
</movie>
```

在后台「媒体库」填写名称和**服务器进程能访问的绝对目录路径**，然后点击添加。不要填写浏览器所在电脑上的路径；如果用容器，填写容器内挂载路径。

**预期结果**：新媒体库出现在列表中。添加接口成功只证明登记成功，**不验证目录存在或可读，也不会自动完成扫描**。

### 5. 扫描并检查分类

点击该媒体库的扫描按钮，等待进度结束，然后打开「媒体墙」。

**预期结果**：后台使用 `POST /api/admin/tasks/scan?library_id=...` 提交异步任务，收到 **202 只表示接受任务，不表示扫描完成**。正常结束时任务状态为 `succeeded`，`result` 中给出 `success`、`pending`、`skipped`、`failed` 数量；具体数值取决于你的文件，不保证是某个固定输出。

- HTTP(S) 直链 + 可解析 NFO：进入可见影片列表；已有人工录入项保留 `manual`。
- HTTP(S) 直链缺失 NFO 或 NFO 解析失败：记为待补录 `pending`，不进入 Emby 可见影片列表。
- 非 HTTP(S) 源：计入 `skipped`，不作为可播放影片入库。
- 任务 `failed` 或 `skipped`：检查错误与占用状态，不要把任务结束等同于成功。

扫描读取直链文本和 NFO，**不验证远端视频实际可播放**。最短路径不需要刮削、探测或重建整个生产库。

### 6. 连接 Emby 客户端

在兼容客户端中填写服务地址 `http://<服务器地址>:18080`（也支持 `/emby` 前缀），使用刚创建的管理员账号登录。与服务同机的客户端才使用 `127.0.0.1`；其他设备填写能到达服务的实际地址。

**预期结果**：你能看到媒体库、扫描成功的影片与详情。选择影片播放时，服务的 `/Videos/{id}/stream` 返回 **302** 到真实源，不做转码。302 不是播放完成证明；编码、源站鉴权、网络与客户端能力仍可能导致播放失败。

当前实现已有普通用户和媒体库授权；普通用户需由管理员分配库权限，默认无授权。不要硬编码用户 ID 为 `1`，也不要给普通用户分发管理员 API 密钥。

## API 调用示例

本节是脚本接入示例，不是必做的第二遍初始化。先完成上面的初始化和扫描。所有命令在**同一个 Bash 终端**依次执行，需已有 `curl`、`jq`。失败时先检查 HTTP 状态与 `code`，不要继续用空 token/ID 请求。

### 登录并取得当前用户 ID

```bash
BASE='http://127.0.0.1:18080'
read -r -p '账号: ' EMBY_USER
read -r -s -p '密码: ' EMBY_PASS; printf '\n'
AUTH=$(jq -cn --arg u "$EMBY_USER" --arg p "$EMBY_PASS" '{Username:$u,Pw:$p}' |
  curl --fail -sS -H 'Content-Type: application/json' --data-binary @- \
    "$BASE/Users/AuthenticateByName")
unset EMBY_PASS
TOKEN=$(printf '%s' "$AUTH" | jq -er '.AccessToken | select(length > 0)')
USER_ID=$(printf '%s' "$AUTH" | jq -er '.User.Id | select(length > 0)')
unset AUTH
```

HTTP **200** 时可提取 token 与本人 ID。不要把 token、登录响应或含凭据的播放 URL 粘贴到工单、截图或仓库。示例不打印 token，仍应在可信机器的私人终端执行。

### 查询媒体库与影片

```bash
curl --fail -sS -H "X-Emby-Token: $TOKEN" "$BASE/Users/$USER_ID/Views" | jq .
curl --fail -sS -H "X-Emby-Token: $TOKEN" \
  "$BASE/Users/$USER_ID/Items?Limit=1" | jq .
```

预期 HTTP **200**，响应含 `Items` 与 `TotalRecordCount`。从影片列表实际响应中选取一个 `Items[].Id`，不要使用 Views 中的库 ID 代替影片 ID。空数组可能表示尚未扫描成功，或当前用户没有授权；不代表接口缺失。

### 查询播放信息与重定向

```bash
read -r -p '刚查询到的影片 Id: ' ITEM_ID
curl --fail -sS -H "X-Emby-Token: $TOKEN" \
  "$BASE/Items/$ITEM_ID/PlaybackInfo" | jq .
curl --fail -sS -I -H "X-Emby-Token: $TOKEN" \
  "$BASE/Videos/$ITEM_ID/stream"
```

播放信息预期 HTTP **200**，包含 `MediaSources`、`PlaySessionId`；媒体源的 `SupportsDirectPlay`、`SupportsDirectStream` 为 `true`，`SupportsTranscoding` 为 `false`。使用 `DirectStreamUrl`，**不要把 `MediaSources[].Path` 当成可播放 URL**，后者通常是服务器上的本地路径。

第二条请求使用 HEAD，预期 HTTP **302** 与 `Location`；此头可能包含私有媒体直链，不要公开输出。不使用 `-L`，避免在此诊断步骤下载视频或把自定义 token 头继续带到外部源站。

完成后撤销本次登录会话并清理变量（不会删除 API 密钥）：

```bash
curl --fail -sS -i -X POST -H "X-Emby-Token: $TOKEN" "$BASE/Sessions/Logout"
unset TOKEN USER_ID ITEM_ID EMBY_USER BASE
```

预期 HTTP **204**。Emby 兼容端点可以整体改用 `BASE=http://127.0.0.1:18080/emby`；健康、初始化与管理接口则始终在根路径，不加 `/emby`。

### 脚本管理接口速查

以下供替代 Web 操作时查阅；它们需要**管理员会话或管理员 API 密钥**，首次初始化除外。ID 必须取实际响应，不要猜测。

| 操作 | 请求 | 源码预期 |
|---|---|---|
| 首次初始化（仅一次） | `POST /api/auth/initialize`，JSON `Username`、`Pw`、`Code` | 204，无 token；随后另行登录 |
| 登记媒体库 | `POST /api/admin/libraries`，JSON `Name`、`Path` | 200，返回 `Id`、`Name`、`Path` |
| 提交指定库扫描 | `POST /api/admin/tasks/scan?library_id={库Id}`，无须 JSON body | 202，返回 `task_id`、`status`、`library_id` |
| 查询任务 | `GET /api/admin/tasks/{task_id}` | 200，`status` 为 `queued/running/succeeded/failed/skipped`；结束后检查 `result` 或 `error` |

扫描任务表仅保存在进程内，重启不恢复，完成记录也可能被淘汰；轮询需适当间隔，收到 `TASK_NOT_FOUND` 时先核对重启/淘汰和实例，不要无限重试。旧同步 `POST /api/admin/scan` 已弃用，新接入不要使用它。管理接口不属于 Emby 兼容稳定性承诺，完整字段以当前实现为准。

## 常见错误与排查

JSON 错误通常包含 `error` 和稳定的 `code`，按 `code` 判断，不依赖自然语言文案。图片错误是例外，可能为空响应体。

| 现象 / HTTP 与 code | 你可以怎么做 |
|---|---|
| 构建提示工具链版本不满足要求 | 对照 [go.mod](<../go.mod#L3>) 检查本机 Go；本轮没有验证可获取的工具链或发布二进制 |
| 启动出现 `load config` | 检查 `-c` 文件是否存在、可读、YAML 是否有效；不带 `-c` 时依次读取 `EMBY_CONFIG`、当前目录默认配置 |
| 启动出现 `create server` 或 `server stopped` | 查看本机详细日志，检查 SQLite 路径写权限和端口占用；不要公开含私有路径的原始日志 |
| 初始化/登录 400 `INVALID_REQUEST` | 检查 JSON、字段名、账号与密码长度；初始化字段是 `Username/Pw/Code`，不是 `password` |
| 登录 428 `PRECONDITION_REQUIRED` | 实例尚未初始化；先完成一次初始化 |
| 初始化 403 `SETUP_CODE_INVALID` / `INIT_SOURCE_DENIED` | 检查验证码或 TCP 来源白名单；不要直接移除保护以绕过错误 |
| 初始化 409 `CONFLICT` | 可能已初始化；先查 `/api/auth/status`，不要删除现有库重新尝试 |
| 401 `AUTH_INVALID` | 缺失、过期或已撤销的 token；重新登录并用 `X-Emby-Token` 头传递 |
| 管理接口 403 `ADMIN_REQUIRED` | 当前是普通用户；切换管理员，不要修改业务代码绕过权限 |
| 429 `AUTH_RATE_LIMITED` | 暂停尝试，按 `Retry-After` 退避；不要循环重放密码 |
| 扫描 409 `SCAN_BUSY` 或任务 `skipped` | 已有扫描或 NFO 写入任务占用；等待完成再提交 |
| 202 后没有影片 | 继续查任务终态与计数，核对服务器目录、同名 NFO 解析结果和 `.strm` 协议；202 不是入库成功 |
| 列表为空 / 详情或播放 404 `NOT_FOUND` | 检查影片是否成功入库、ID 是否来自实际影片响应、当前账号是否获库授权；普通用户只使用本人 ID |
| 扫描任务 404 `TASK_NOT_FOUND` | 任务可能因重启丢失或被淘汰；检查实例和任务生命周期 |
| 503 `AUTH_UNAVAILABLE` | 凭据存储暂不可用，不等于密码错误；退避并排查 SQLite 可用性 |
| 播放代理 502 `UPSTREAM_UNAVAILABLE` | 检查服务端访问直链的网络/源站状态；不要把直链或凭据发到公开工单 |
| 302 后仍不能播放 | 检查客户端到源站的连通性、直链有效期、编码与浏览器支持；服务没有转码，网页代理也不解决编码不支持 |

## 权限与图片公开约定

登录会话与管理员 API 密钥不是同一种生命周期：会话默认 30 天并滑动续期；密钥是独立的长期管理员凭据，注销不撤销密钥。需要密钥时只发给可信管理员脚本，优先使用请求头，避免 URL 泄漏。

**保持现有兼容边界**：`/Items/{id}/Images/{kind}`（含可选 index）与 `/api/admin/scrape/image` 保持匿名公开，不因多用户库授权改为登录可见。不要据此推断图片具有与影片详情相同的私密性；媒体流和播放代理仍需鉴权与影片可见性检查。

## 资料来源、差异与缺失信息

本指南优先使用实际路由、处理函数与仓库测试示例。它不把历史审查意见、设计提案或记录为待办的功能当成已实现功能。

| 资料 | 本文使用的证据 |
|---|---|
| [API-CONTRACT.md](<../API-CONTRACT.md>) | 根路径与 `/emby`、认证载体、错误码、图片匿名、扫描弃用；其中 §4 仍有单用户旧描述，§4.1 和当前实现已支持多用户 |
| [main.go](<../cmd/metatube/main.go#L30-L80>)、[config.go](<../internal/config/config.go#L88-L113>) | `-c` / `EMBY_CONFIG` / 默认配置优先级、数字端口、监听地址 |
| [server.go](<../internal/server/server.go#L392-L566>)、[auth.go](<../internal/server/auth.go#L193-L328>) | 路由、初始化、登录字段与返回状态、真实用户 ID |
| [admin.go](<../internal/server/admin.go#L167-L179>)、[store.go](<../internal/store/store.go#L39-L43>) | 添加库请求与响应大小写；登记不校验目录可读性 |
| [scan_tasks.go](<../internal/server/scan_tasks.go>)、[scanner.go](<../internal/scanner/scanner.go#L280-L354>) | 202 / 任务终态、进程内生命周期、扫描分类 |
| [media.go](<../internal/server/media.go#L346-L432>)、[stream 实现](<../internal/server/media.go#L1002-L1023>) | PlaybackInfo 字段与 302，不假称源站可播放 |
| [异步扫描测试示例](<../internal/server/scan_tasks_test.go#L19-L103>)、[验收客户端示例](<../internal/server/acceptance_test.go#L85-L147>) | 初始化、登录、添加库、扫描与 token 请求写法；仅阅读，未执行 |
| [Web 添加库](<../internal/server/web/app.js#L239-L256>)、[Web 扫描](<../internal/server/web/app.js#L2411-L2427>) | 后台最短操作路径及其实际接口 |

**缺失 / 待确认**：

- 本轮没有指定某个更窄的目标接口；本文覆盖 README 所述服务主链路。如果你需要某个集成方的专属教程，仍需提供目标端点、客户端与版本、输入样例及验收标准。
- 未找到 README 提及的 `docs/emby_openapi.json`，也没有独立的 `examples/` 目录；未以缺失资料补造接口。当前接口证据来自上述契约、源码与测试。
- 未提供可公开使用的媒体直链、真实库目录、客户端版本或运行环境；因此没有可承诺成功播放的公共样本，也不能给出真实扫描数量、ID、token 或完整实测响应。
- 工具链可用性、构建结果、平台差异、真实源站与客户端播放结果均未验证；本轮只完成文档与源码契约静态核验。
- [配置示例](<../config.example.yaml#L26-L29>) 的 Redis 必选旧注释、[config.go](<../internal/config/config.go#L96>) 的同类注释仍待维护；本文未修改业务代码和配置示例。
- 仓库 [.gitignore](<../.gitignore#L11-L12>) 忽略整个文档目录。本文件已写入工作区，但默认 Git 状态可能不显示它；本轮不改忽略规则、不暂存、不提交。后续发布时需维护者明确决定如何将本指南纳入版本控制，不能只发布 README 入口。

本轮核验方法：读取仓库规则（未发现适用的 `AGENTS.md`）、契约、路由、处理函数与现有示例；静态检查相对链接、Shell 语法及示例字段。没有安装依赖、连接真实实例、运行会修改数据的验收脚本或启动生产服务。

下一步：查看 [API 契约](<../API-CONTRACT.md>) 了解稳定字段与弃用规则，回到 [README](<../README.md>) 查阅完整目录约定与部署注意事项。
