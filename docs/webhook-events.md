# 通用 Webhook 与 Emby 风格事件

本项目在保留 SmartStrm 删除联动的基础上，支持显式订阅以下 13 种事件。默认仍全局关闭。管理员可在 **管理后台 → 设置 → Webhook** 编辑并即时生效、持久保存；含 token 的接收地址只写不回显，留空保留，显式清除需关闭通知。保存不会试发请求，开启删除订阅需确认整目录删除风险。多人同时编辑出现 412 冲突时保留草稿，需重新加载核对，不自动覆盖。

以下 YAML 仅为没有后台持久配置时的后备值；后台保存后数据库配置优先，YAML 不会在重启时覆盖它。后台事件必须至少选择一项，不能以空列表关闭；YAML 历史空列表语义仍为仅 item.deleted。关闭通知请使用启用开关。详见[WebUI 配置指南](<webhook-webui.md>)。

## 1. 配置

```yaml
webhook:
  enabled: true
  url: "https://receiver.example/webhook"
  events:
    - playback.start
    - playback.stop
    - user.authenticated
    - user.logout
    - item.rate
    - item.unrate
    - item.markplayed
    - item.markunplayed
    - item.updated
  format: json
  timeout_seconds: 5
  allow_private: false
```

- 显式 events **替换**默认集合，并非追加。上述例子没有 item.deleted，不发送删除命令。
- 缺省或空列表等同仅 item.deleted；空列表不表示关闭。要关闭使用 enabled: false。
- 事件严格区分大小写，不接受未知值、通配符或前后空白；重复值不重复投递。
- format 缺省/json 为 application/json；form 为 application/x-www-form-urlencoded，唯一 data 字段的值是完整事件 JSON。不是 multipart，也不是 JSON 字符串的再编码。
- 一个配置对应一个接收地址/一种编码，不支持多目标分流。
- 本机/私网目标需显式 allow_private: true；统一出站地址策略仍生效，禁止重定向，禁止 URL userinfo/fragment。
- URL 中的 token 是敏感配置，不提交版本库、不分享。不要把本站用户密码或访问令牌配置给接收器。

## 2. 事件及实际触发边界

| Event | 触发位置 | 说明 |
|---|---|---|
| item.deleted | 管理员明确删除单部影片，DB 提交后 | 仅绝对 .strm 源路径；扫描清理、删除媒体库不触发 |
| item.updated | 人工编辑/摘要恢复完成索引，或人工上传影片图片完成索引 | partial_success 不发送；不覆盖扫描、刮削、探测等所有后台变更 |
| item.markplayed | 手动标为已看成功 | 不等同自动播放结束 |
| item.markunplayed | 手动取消已看成功 | Item.UserData.Played=false |
| item.rate | 收藏、取消收藏；设置喜欢或不喜欢成功 | 用 Item.UserData.IsFavorite/Likes 区分，不把取消收藏命名为 unrate |
| item.unrate | 清除喜欢/不喜欢成功 | 与取消收藏不同 |
| playback.start | Sessions/Playing 的 start 成功提交 | 不是仅请求 PlaybackInfo 或打开流地址 |
| playback.progress | Sessions/Playing/Progress 成功提交 | 高频，默认不订阅；无 Pause/Unpause 标记时使用此事件 |
| playback.pause | Progress 上报 EventName=Pause | 大小写不敏感；不推测 IsPaused 变化，不自动检测转态 |
| playback.unpause | Progress 上报 EventName=Unpause | IsPaused=false |
| playback.stop | Sessions/Playing/Stopped 首次成功提交 | 重复 stop、已停止后的迟到 progress 不发送；沿用现有会话识别规则 |
| user.authenticated | 用户密码登录并保存会话成功 | 不包括失败登录、初始化、每次 token 验证或自动续期 |
| user.logout | 当前 Logout 请求成功撤销提交后 | 不包括管理员撤销全部会话、禁用用户或会话自然过期；不保证 API key 被此请求撤销 |

个人状态为既有写入请求通知，重复设置同值仍可能通知；start/progress 不是全局去重事件。Pause/Unpause 只是明确 Progress 标记映射，不更改数据库播放状态机。重复 stop/迟到 progress 的抑制来自本次实际提交结果，不是新的永久去重协议。

浏览器直接访问视频流但不调用播放上报 API 时不会生成播放事件。本功能不为播放器新增上报，也不引入开机、关机、用户创建/删除、库扫描、任务完成、自动收藏等尚未接入的通知。

## 3. JSON 示例

```json
{
  "Event": "playback.start",
  "Date": "2026-10-20T13:00:00Z",
  "Timestamp": "2026-10-20T13:00:00Z",
  "Server": {"Id": "server-id", "Name": "Emby-go"},
  "Item": {
    "Id": "42",
    "Name": "示例影片",
    "Path": "/strm/example/movie.strm",
    "Type": "Movie",
    "LibraryId": "1",
    "RunTimeTicks": 72000000000
  },
  "User": {"Id": "2", "Name": "viewer"},
  "Session": {
    "Id": "play-session-id",
    "DeviceId": "device-id",
    "PlayState": {"PositionTicks": 0, "IsPaused": false}
  },
  "PlaybackInfo": {
    "PositionTicks": 0,
    "PlaySessionId": "play-session-id",
    "IsPaused": false
  }
}
```

字段按场景提供，示例不是所有事件的必填结构：

- Event 为固定名字；Date 与保留的 Timestamp 同值，UTC RFC3339，可含小数秒。
- Id 是字符串。Item.Path 是本地索引源路径，不是 STRM 文件里的媒体地址；片名、用户名、本地路径与播放进度仍属于隐私数据，只交给信任的接收端。
- 用户事件没有媒体时 Item={}，不附 Session/PlaybackInfo；不发送完整用户对象、密码、访问 token、请求头、登录响应 DTO 或媒体直链。
- 状态事件附 Item.UserData；收藏/评分使用现有个人态字段，已看事件提供 Played。不要假设每种事件都有完整 Emby BaseItemDto。
- 当前 Session.Id 使用客户端上报的 PlaySessionId；不是独立的完整 Emby 在线会话目录。DeviceId 来自上报，未获取时省略；DeviceName、Client 等不保证有值，PlayMethod 未推断。
- item.updated 在索引成功后读取当前影片构造载荷，不是与通知共用同一提交快照，可能包含更晚的并发元数据；读取失败仅报告通知失败。
- 删除事件使用同一删除事务返回的原路径/标题，继续保持独立的高风险路径防护，不经普通事件转换逻辑。

参考的公开接收器 [emby-webhooks](https://github.com/tsredanovic/emby-webhooks) 使用上述事件命名，其 [main.py](https://github.com/tsredanovic/emby-webhooks/blob/main/main.py) 从 form 的 data 字段解析 JSON。因此提供 form 选项，但没有声称所有 Emby 插件/版本或第三方接收器均完全兼容。请按接收器检查编码及实际需要的字段。

## 4. 投递语义与风险

- 业务事务成功后才尝试通知；一次同步 POST，包含响应读取的总超时默认 5 秒，可配置 1–30 秒。
- 无后台队列、持久 outbox、自动重试、事件 ID 或顺序承诺。网络错误、进程退出、响应丢失都可能丢通知；不能用作可靠账本。
- X-Webhook-Status 为 sent/failed/disabled；删除路径额外有 skipped。sent 只是 2xx 接收确认，明确 ignored/未启用回复判失败。最多读取不足 4KiB 的回复，达到上限保守判失败；不回显接收端内容。
- 失败不回滚登录、收藏、播放等已提交业务，也不改变这些业务既有成功响应。播放停止原有后置版本刷新仍可能失败返回 500：本次已提交的 stop 通知会先尝试，重试不会再次发同一 stop。
- 发送使用应用生命周期 context，客户端断开不保证中断通知；关闭应用会取消。没有为发送另起不受管 goroutine。
- playback.progress 每次上报都可能等待接收端，故默认不订阅；慢接收器会增加上报延迟。高频用途请先接入低延迟网关评估，不要将“同步限时”误当零开销异步。
- 即使配置非删除事件，也须审计接收端如何处理它。原版 SmartStrm 只支持其删除事件分支，不会因此获得播放/用户通知功能，可能返回 ignored 并被标记失败。需要普通通知时使用合适接收器。

> item.deleted 可能让 SmartStrm 删除远端整个目录，不是只删除一个文件。默认未自动扩大发送范围。超时不证明远端未执行，不要盲目重发。详见[删除联动安全指南](<webhook-smartstrm.md>)。

## 5. 实现与验证

- [发送器与载荷](<../internal/webhook/webhook.go>)、[订阅/格式验证](<../internal/config/config.go>)。
- [服务端映射](<../internal/server/webhook.go>)、[播放提交结果](<../internal/store/playback_sessions.go>)。
- [JSON/form 编码测试](<../internal/webhook/form_test.go>)、[订阅测试](<../internal/webhook/subscriptions_test.go>)、[真实 API 事件测试](<../internal/server/webhook_events_test.go>)。

本轮验证结果：

- go test ./... -count=1 全部通过（Server 70.257s、Store 29.791s）。
- 前端 VM 测试 91/91 通过。
- Webhook/Config 全包 race 各 3 遍通过；Server Webhook/既有播放定向 race 3 遍通过（120.876s）；Store 播放/删除快照定向 race 3 遍通过（50.942s）。并非本轮 Server/Store 全量 race。
- go vet ./... 与 git diff --check 通过。
- 独立只读审查发现停止通知排在可能失败的版本刷新之后，已主会话修正并补真实 API 故障测试；只读 overlay 恢复旧顺序，准确检出 missing playback.stop。修后未再做第二次独立复审。

测试只访问本机 httptest 接收器/临时数据库，没有修改部署配置、试发真实通知或调用真实 SmartStrm 远端删除。覆盖登录失败不发/成功与退出、收藏评分及已看映射、人工索引成功/部分失败、播放五类及重复 stop/迟到 progress、不订阅删除时不投递、后置版本错误仍尝试一次 stop、接收失败不回滚个人状态、两种编码和默认删除订阅兼容。真实第三方接收器联调、浏览器交互、并发跨请求的投递顺序和所有故障阶段不是本轮已验收内容。
