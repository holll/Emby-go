# Emby-go

一个用 **Go** 重写的、兼容 **Emby API** 的轻量媒体库服务端，面向 **AV / FC2 类 NFO + `.strm` 媒体库**（海报墙浏览走 Emby 客户端）。

- 元数据真源 = **现成 NFO**（`<title>/<genre>/<studio>/<actor>/<set>` 等），不内置刮削器
- 媒体只支持 `http(s)://` 的 `.strm`；播放走 **302 直拉**（服务端零带宽，不转码）
- 单二进制、单端口、零外部依赖（除可选 Redis 缓存）；内置深色 Web 管理后台
- 兼容端点覆盖：认证、媒体库/影片墙、详情、图片、合集(BoxSet)、进度/续播、搜索/过滤等 Emby 客户端浏览主链路

## 快速开始

### 构建

```bash
go build -o metatube ./cmd/metatube
```

### 运行

```bash
# 默认读取 ./config.yaml；也可用 -c 指定其它配置文件
./metatube
./metatube -c /etc/emby-go/config.yaml
./metatube -version
```

`config.yaml` 属本地私人配置（已在 .gitignore 中，不入库）；首次使用复制示例并修改：

```bash
cp config.example.yaml config.yaml
```

监听端口在配置文件的 `port`（或 `listen`）中设置，命令行只负责选择配置文件。浏览器打开 `http://127.0.0.1:<port>` 走初始化向导（创建管理员账号）。

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
- 手动补录（http(s) 直链 + 字段 → 生成 strm/NFO 即时入库）
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

```text
服务器地址：http://<host>:18080          # 或 http://<host>:18080/emby
账号/密码：  初始化向导创建的管理员
```

已对齐的浏览主链路：认证 → Views（媒体库 + 合集）→ Latest/Resume/Suggestions → Items（排序/过滤/分页/搜索）→ 详情/图片 → 相似推荐 → PlaybackInfo → `/Videos/{id}/stream`（302 直拉）→ 进度/收藏/评分上报。

### API 密钥

后台「API 密钥」创建后，可用密钥直接调用 Emby API（等价于登录令牌，长期有效）：

```bash
curl -H "X-Emby-Token: <key>" http://<host>:18080/Users/1/Views
# 或 http://<host>:18080/Users/1/Views?api_key=<key>
```

## 配置（config.yaml）

```yaml
port: 18080               # 监听端口
debug: false              # true 时启用 gin 调试 + 详细请求体日志
db_path: "emby-go.db"     # SQLite 文件
server_name: "Emby-go"    # 对外站名
server_id: ""             # 留空自动生成稳定 UUID
redis_addr: "127.0.0.1:6379"   # Redis 必选缓存后端
redis_password: ""
redis_db: 0
server_domains: []        # 前端“服务器域名切换”候选
```

`server_id` 会写入配置文件以保持稳定；`redis_addr` 必填，连接失败将拒绝启动。

## 开发

```bash
go vet ./...
go test ./...
```

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

单用户、仅 `http(s)-strm` 直拉播放；不做转码/HLS/刮削器/多用户/直播音乐等（见 `dev-plan.md` Roadmap）。
