# Emby API 兼容性审查与修复（2026-09-08）

> 对照物：真实 Emby `https://emby.hollc.top/emby`（4.9.5.0，库为 AV/FC2）、
> `docs/emby_openapi.json`、开源 `src/Emby-3.5.2.0` 与客户端 `src/iPlay-1.0.1154`。
> 方法：对真机逐个探测并比对响应结构；读 iPlay 源码确定客户端强依赖字段。

## 一、审查结论摘要

本项目（Emby-go）的核心浏览链路（认证→Views→Latest/Items/Resume→详情→图→播放）已能工作，
但存在三类问题：**未实现但客户端会探的端点**、**响应结构与真机不符**、**少数该有值却空/错值**。

## 二、修复清单

### A. 系统层
| 端点 | 问题 | 修复 |
|---|---|---|
| `/System/Info/Public` | 用 `LocalAddress`(string) 且无地址数组 | 对齐真机：`LocalAddresses/RemoteAddresses` 数组 + `Version=4.9.5.0` |
| `/System/Info` | 只有 3 字段 | 补齐 ServerId/OS/本地地址等客户端探针常用字段 |
| `/System/Ping` | 404 入探针 | 返回 `200 "Emby Server"`（GET/POST） |
| `/System/Endpoint` | 404 | 返回 `{IsLocal:false,IsInNetwork:false}` |
| `/Sessions/Playing/Ping`、`/Sessions/Capabilities[/Full]`、`/Sessions/Logout` | 404 | 204 探针占位 |
| `/DisplayPreferences/{id}` | 404 | 最小 DisplayPreferencesDto |
| `/Search/Hints` | 404 | 实测真机对旧搜索端点返回 `[]`，同样返回 `[]`（检索主走 Items?SearchTerm） |

### B. 浏览/条目
| 项 | 问题 | 修复 |
|---|---|---|
| Items 排序 | `SortBy=DateCreated,SortName` 整串不识别→按标题排 | 取首个支持键；新增 `DateCreated→id` 排序；`SortBy=IsFavoriteOrLiked,Random` 兜底 |
| Genre/Studio/Tag/Person 过滤 | 只支持名字参数 | 支持 `GenreIds/StudioIds/TagIds/PersonIds`（解析复合 Id `kind:<base64(name)>`）并按名字 OR 过滤；store 对 genres 也支持多值 |
| 条目字段 `Studios` | 返回字符串数组 | 对齐真机改为 `[{Name,Id}]`；新增 `GenreItems/TagItems`、`People` 带 Id/Type（含 Director） |
| 条目字段 `ImageTags` | 值写死 `"local"`，缺 BackdropImageTags | 用文件 mtime 做 tag；`Primary/Thumb`（Thumb 缺宽图回退海报）；`BackdropImageTags` 恒为数组（iPlay 对缺失 NPE） |
| 条目 `UserData` | — | 恒存在（含 IsFavorite），iPlay 无条件解引用 |
| `PremiereDate` | NFO `2006-01-02` 直接透传 | 规整为 `2006-01-02T00:00:00.0000000Z` |
| Views（媒体库卡片） | 缺封面、ServerId、DisplayPreferencesId | 用库内最近带海报影片做封面；补 ServerId/DisplayPreferencesId |
| 库封面图片 | `/Items/{libId}/Images/Primary` 404 | image 路由支持“影片缺失时回退库代表海报” |
| ImageType `Thumb` | `/Images/Thumb` 404 | image 路由映射 Thumb→landscape（缺则 poster）；`/Images` 清单类型与真机一致 Primary/Thumb/Backdrop |
| NFO `<runtime>` | 单位是分钟，却按秒入库 | `runtime_seconds = runtime(分钟)×60`（真机 RunTimeTicks 佐证） |

### C. 播放
| 项 | 问题 | 修复 |
|---|---|---|
| `PlaybackInfo` MediaSource | 缺 `MediaStreams`/`DirectStreamUrl`；Path 相对无前缀 | Path/DirectStreamUrl 指向本服务流端点并尊重 `/emby` 前缀；`MediaStreams` 恒为数组；客户端按 Path/DirectStreamUrl 播放，缺则播放失败 |

### D. 客户端结论（iPlay）
- Android/鸿蒙调用大写驼峰路径，与 Gin 路由一致；token 走 query `X-Emby-Token` 或 header（均已支持）。
- 强依赖：`ImageTags.Primary/Thumb`、`BackdropImageTags`（数组）、`UserData`（含 IsFavorite）、
  `PlaybackInfo.MediaSources[].MediaStreams` 与可播地址——任一缺失会 NPE 或无法起播。
- 媒体库浏览一律 `ParentId=viewId&Recursive=true&IncludeItemTypes=Series,Movie`（无需目录平铺模型）。
- 演员浏览走 `Items?PersonIds=<Id>&IncludeItemTypes=Movie,Series`。
- 不调用 `/Search/Hints`、不解析评分；`Genre` 仅展示不可点。

## 三、验收
`go vet ./... && go test ./...` 通过；`internal/server/server_test.go` 增补了对
NFO 分钟换算、BackdropImageTags/UserData/ImageTags 恒存在、Studios 对象化、PersonIds/GenreIds 过滤、
PlaybackInfo 字段、System/Ping、Search/Hints、DisplayPreferences、Public Info 的断言。
