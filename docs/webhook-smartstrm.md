# Emby-go → SmartStrm Webhook 删除联动

## 1. 接收原理与危险边界

本指南按当前本机 SmartStrm 源码核对协议，而不是假设完整 Emby Webhook 兼容：

1. SmartStrm 接受 `POST /webhook/<token>` 或 `POST /webhook/emby/<token>`，token 不符返回 403。
2. JSON 中读取 `Event`、`Item.Path`。删除分支接受 `item.deleted`、`ItemDeleted`、`library.deleted`；Emby-go 只发送第一种。
3. 接收端核对删除同步开关、STRM 根前缀，把 `Item.Path` 相对其 `strm_in_emby` 的路径接到 `storage_path_map`。
4. **如果路径后缀是 `.strm`，先去掉文件名，再删除映射后的远端整个目录。** 最后还要通过 `allowed_prefix` 白名单，并使用 `storage` 对应的驱动删除。

例如：

```text
Emby-go 实际索引路径： /media/strm/Movies/Example/Example.strm
SmartStrm strm_in_emby：/media/strm
storage_path_map：      /RemoteLibrary
最终删除目标：          /RemoteLibrary/Movies/Example
```

因此不是“只删一个 STRM 或远端视频”。同目录的其它影片、字幕、封面都可能受影响。STRM 直接放在根目录时，目标甚至可能是映射根目录；不要把根路径作为宽泛删除白名单，也不要在多个影片共享的目录上试用。容器挂载不同时，SmartStrm 的 `strm_in_emby` 必须匹配 **Emby-go 看到的路径**，不是 SmartStrm 容器里另一套路径。

本次未访问真实 SmartStrm 实例、网盘或远端存储，未执行真实远端删除。

## 2. 配置发送端

推荐在 **管理后台 → 设置 → Webhook** 中配置并保存，即时生效、重启保留。接收地址包含 token，只写不回显；留空保留，清除时先关闭。删除订阅启用需明确确认远端整目录风险，保存不会试发。也可订阅其它 Emby 风格事件，见[通用事件指南](<webhook-events.md>)；SmartStrm 删除联动保持 JSON 和仅删除订阅，不要把普通事件假定为 SmartStrm 已支持。以下私有 YAML 仅是没有后台配置时的后备值：

```yaml
webhook:
  enabled: false
  events: [item.deleted]
  format: json
  url: ""
  timeout_seconds: 5
  allow_private: false
```

| 配置 | 语义 |
| --- | --- |
| `enabled` | 默认 false；确认接收端映射与风险后才显式 true。 |
| `url` | 完整 HTTP(S) 接收地址，包含接收端要求的 token 路径；不接受 URL 用户名密码或 fragment。 |
| `timeout_seconds` | 请求总预算，涵盖连接和读取响应体；1–30 秒，0 使用默认 5 秒。 |
| `allow_private` | 默认 false。使用本机、Docker 内网、局域网地址时显式 true；仍只填写受信任接收端。 |

示意配置如下，**占位符必须替换，首次必须使用独立测试环境**：

```yaml
webhook:
  enabled: true
  url: "http://smartstrm-host:PORT/webhook/emby/REPLACE_WITH_PRIVATE_TOKEN"
  timeout_seconds: 5
  allow_private: true
```

先重新编译并运行包含本次改动的版本。后台保存后无需重启，以数据库配置为准；首次没有后台配置时才读取 YAML 后备。无后台覆盖时启用的 YAML 配置无效会阻止初始化；已有后台配置损坏或不可读时同样拒绝启动，不回退启用旧 YAML。配置解析、发送错误与 Emby-go 新增日志不输出 URL/token/响应正文，但这不替代整个系统的日志审计。SmartStrm 自身会记录收到的事件和路径，反向代理也可能记录带 token 的 URL，需自行限制日志与配置访问权限。远程公网接收端建议使用 HTTPS。

## 3. 哪些动作发送

| 动作 | 是否发送 |
| --- | --- |
| 管理员 `DELETE /api/admin/items/:id`，影片删除事务成功提交，且原索引路径为绝对 `.strm` 路径 | 配置启用时发送一次。 |
| 同一影片重复删除、未知 ID、无权限、数据库写入或 COMMIT 失败 | 不发送。 |
| 扫描发现源文件缺失、扫描清理旧索引、重建或其它内部 Store 删除 | 不发送。 |
| 删除整个媒体库 | 不发送，避免批量远端误删。 |
| 播放、收藏、编辑、下载头像等 | 不发送，本次不提供这些事件。 |

路径取自同一事务中 `DELETE ... RETURNING` 返回的实际删除行，不是删除前另读的陈旧路径；可空标题映射为空名称。事务失败不返回可发送快照。路径仅做“绝对路径且 `.strm` 后缀”的语法门槛，不解析 STRM 中的播放 URL，不反推远端路径，也不声称验证了源文件类型/目录布局或远端目标安全；真正的路径映射和删除白名单由 SmartStrm 校验。

## 4. 事件格式

```json
{
  "Event": "item.deleted",
  "Item": {
    "Id": "123",
    "Name": "Example",
    "Path": "/media/strm/Movies/Example/Example.strm",
    "Type": "Movie"
  },
  "Server": {
    "Id": "example-server-id",
    "Name": "Emby-go"
  },
  "Timestamp": "2026-10-20T12:00:00Z"
}
```

发送方式为单次 `POST`、`Content-Type: application/json`。当前 SmartStrm 删除逻辑只依赖 `Event` 与 `Item.Path`，其它字段用于兼容与诊断，不是身份签名或幂等键。

## 5. 投递和 HTTP 结果

数据库提交 → 清除本地响应缓存 → 最多一次同步发送 → 返回原空响应 `204`。通知失败不回滚已经提交的本地删除，不能通过重新删除同一个 ID 重发。请求断开不主动取消已提交后的通知；应用生命周期取消仍会取消发送。

| `X-Webhook-Status` | 解释 |
| --- | --- |
| `disabled` | 配置关闭或发送器不存在，没有发送。 |
| `skipped` | 配置启用，但源路径不满足绝对 `.strm` 语法门槛，没有发送。 |
| `sent` | 接收端返回被接受的 2xx 确认；不是远端结果的独立证明。 |
| `failed` | 发送、读取、状态码或接收确认失败。本地索引已删除。 |

前端确认框说明远端整个目录风险；删除后按这些状态提示。`failed/skipped` 不提示远端成功，`sent` 也提示核对远端。

- 禁止跟随任何重定向，避免把敏感事件转发到其它地址。
- 沿用项目统一 outbound 目的地址策略；默认阻止私网，显式允许私网才连接内网接收端。既有代理/DNS 策略不因此获得额外安全保证。
- 响应读取上限 4 KiB，读满保守判失败；非 2xx、`ignored`、包含“未启用”的确认也判失败。不是所有 2xx 都代表真正删除。
- 没有自动重试、后台队列、持久 outbox、交付保证或远端幂等协议。数据库提交后进程崩溃可能丢通知；接收端已经执行但响应丢失/超时，也会显示 failed。
- **超时或失败后先人工核对远端，切勿盲目重发破坏性事件。** 重扫可能重新建立索引，但不是通知重放或远端恢复。

## 6. 安全接入步骤

1. 保持 `enabled: false`，备份配置与索引，核对两端看到的 STRM 路径及大小写。
2. 使用只记录 JSON、固定返回 `200 ok` 的 mock 接收器验证载荷，不能把生产 SmartStrm URL 用作第一次连通性测试。
3. 真正启用 SmartStrm 删除同步前，准备独立、可丢弃的远端目录，目录里不要混放其它影片；将 `allowed_prefix` 限制到这个测试子树。
4. 先核对路径推导结果，再只删除一个测试条目；确认发出的 `Item.Path` 是原本地 STRM 路径，确认远端只影响预期目录。
5. 验证接收端禁用、token 不符、网络失败时，本地已提交的删除仍是 204，后台提示与响应头可见；人工确认远端状态后再决定后续操作。
6. 审核通过后才切正式配置。停用时设置 `enabled: false` 并重启，不需要修改数据库结构。

不要把文档里的示意事件直接 curl 到真实接收器；它是有副作用的删除命令。

## 7. 实现与验证范围

关键实现：

- [配置与校验](<../internal/config/config.go>)、[配置示例](<../config.example.yaml>)。
- [发送器与协议](<../internal/webhook/webhook.go>)，单次发送、不跟随重定向、响应限制和固定错误。
- [提交后通知入口](<../internal/server/webhook.go>)，只由明确管理删除调用。
- [事务删除快照](<../internal/store/index_delete.go>)，实际路径/标题与版本提交一致。
- [API 集成测试](<../internal/server/webhook_test.go>)、[快照测试](<../internal/store/delete_snapshot_test.go>)、[发送器测试](<../internal/webhook/webhook_test.go>)、[前端 VM 测试](<../internal/server/web_test.cjs>)。

自动测试全部使用本机 mock 或临时 SQLite，包含鉴权、COMMIT 失败不发送、重复删除、默认关闭、路径跳过、500/ignored/取消、重定向、超时、响应大小、错误脱敏与 UI 状态；不代表真实 SmartStrm/远端、DNS/proxy/TLS/HTTP2、断电/强杀、真实浏览器或并发压力验收。新增表结构、持久队列与其它事件类型均不在本次范围。
