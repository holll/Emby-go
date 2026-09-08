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

### 合集（BoxSet）

读 NFO `<set><name>` 自动聚合。Views 会出现「合集」媒体库（`CollectionType=boxsets`），合集内类型筛选只显示该合集数量≥1 的类型。

## Web 管理后台

`http://127.0.0.1:18080/admin` 提供：
- 总览统计、媒体库管理、扫描/重建索引
- 影片记录（状态/协议筛选、搜索、重读源、删索引）
- 手动补录（http(s) 直链 + 字段 → 生成 strm/NFO 即时入库）
- 任务日志、未知接口探针、设置

> 浏览海报墙请用 **Yamby / iPlay** 等 Emby 客户端连接同一地址；后台不是影院浏览端。

## Emby 客户端接入

Emby 兼容 API 同时注册在根路径与 `/emby` 前缀下：

```text
服务器地址：http://<host>:18080          # 或 http://<host>:18080/emby
账号/密码：  初始化向导创建的管理员
```

已对齐的浏览主链路：认证 → Views（媒体库 + 合集）→ Latest/Resume/Suggestions → Items（排序/过滤/分页/搜索）→ 详情/图片 → 相似推荐 → PlaybackInfo → `/Videos/{id}/stream`（302 直拉）→ 进度/收藏/评分上报。

## 配置（config.yaml）

```yaml
listen: ":18080"          # 监听地址
db_path: "emby-go.db"     # SQLite 文件
server_name: "Emby-go"    # 对外站名
server_id: ""             # 留空自动生成稳定 UUID
redis_addr: ""            # 可选 Redis；留空使用进程内内存缓存
server_domains: []        # 前端“服务器域名切换”候选
```

`server_id` 会写入配置文件以保持稳定；`redis_addr` 为空时自动退化为进程内 LRU 缓存。

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
