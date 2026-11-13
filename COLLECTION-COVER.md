# 合集自动拼接封面

复刻 Emby 历史 `StripCollageBuilder.BuildThumbCollage` 的横版拼贴：把成员的竖版海报
横向拼成一张横版图。**不是九宫格、不是瀑布流、也不是把整张竖版海报缩小后并排。**

这套拼贴在上游是 **Thumb（横版）/ 媒体库型**算法，同一份代码被两个地方用：

| 用途 | 输出尺寸 | 上游依据 |
| --- | --- | --- |
| 媒体库文件夹封面（`CollectionFolder`） | 960×540 | `BaseDynamicImageProvider.CreateThumbCollage(item, items, outputPath, 960, 540)` |
| 用户视图 / 播放列表（`UserView` 等） | 960×540 | `DynamicImageProvider`（Jellyfin 同名实现） |
| `ImageType.Thumb` 动态图 | 640×360 | `CreateImage` 里 `ImageType.Thumb → CreateThumbCollage` |

本项目把同一算法用在两处：**合集（BoxSet）Primary 640×360**、**媒体库文件夹 Primary 960×540**。
前者是"把媒体库型拼贴套到合集上"的兼容做法（见 §2 的调用矩阵与 §10 的偏差说明），
后者与上游媒体库封面的用途一致。

## 1. 依据：以源码为准

布局常量全部取自 Emby 历史源码 `Emby.Drawing.Skia/StripCollageBuilder.cs`
（该文件最后一次提交 `042b15c9a180c1303fbde65a343a2bc1d5d82434`，2018-09-20），
`BuildThumbCollage` 的入口尺寸由 `Emby.Server.Implementations/Images/BaseDynamicImageProvider.cs`
的 `CreateThumbCollage` 给出 640×360。

| 量 | 源码写法 | 640×360 时 |
| --- | --- | --- |
| 画布 | 黑底，`width` × `height` | 640×360 |
| 列数 | `for (int i = 0; i < 4; i++)` | 固定 4 |
| 单列宽度 | `Convert.ToInt32(width * 0.23475)` | 150 |
| 海报主体高度 | `Convert.ToInt32(height * .70)` | 252 |
| 水平留距 | `Convert.ToInt32(width * 0.0125)` | 8 |
| 顶部/主体-倒影间距 | `Convert.ToInt32(height * 0.011111…)` | 4 |
| 编码质量 | `BuildThumbCollage` 用 90 | 90 |
| 倒影 | 裁剪块缩到 `(width, height/2)` → 垂直翻转 → 黑色渐变 alpha `{128,208,240,255}` | 可见高 100 |

列左上角：`(8,4) (166,4) (324,4) (482,4)`；主体 `y=4..255`；倒影起点 `y=260`。

取整必须对齐 C# `Convert.ToInt32` 的中点取偶语义（Go 用 `math.RoundToEven`），
否则 0.5 边界上会和原生差 1 像素。

### 已实现与源码一致的细节

- 缩放：按主体高度等比缩放，源比例保持，Lanczos3 重采样（`iWidth = iHeight * W / H` 整数除法）。
- 裁剪：`ix = |(iWidth - iSlice) / 2|`，水平居中裁出 `iSlice` 宽。
- 降级分支：`ix + iSlice > iWidth` 时只画缩放后的整图、**跳过该列倒影**（源码 `if (subset == null) continue`）。
- 循环补位：`GetNextValidImage` 环形取图、最多试 `len(paths)` 次、跳过解码失败的项。
  1 张 `AAAA`、2 张 `ABAB`、3 张 `ABCA`、4 张 `ABCD`。
- EXIF 方向：源码在 Skia 解码层按 codec origin 纠正方向，本项目在 `decodeOriented` 里
  支持 Orientation 1..8。

### 刻意不做

- `BuildSquareCollage`（2×2 网格）不实现。
- `BuildPosterCollage`（源码里是 `// @todo` 空实现）不实现。
- 源码里声明但从未使用的 `int iTrans = Convert.ToInt32(height * .25)` 不作为参数。

## 2. 已证实 / 未证实

**已证实（可核对源码）**：四槽位布局、所有比例常量、缩放与裁剪方式、倒影构造、
少图循环补位、编码质量 90、只接受本地文件（`ImageType.Primary` 且 `IsLocalFile`，
否则退到 `ImageType.Thumb` 且 `IsLocalFile`）。

### 上游调用矩阵（Emby 3.3 公开源码）

| 上游位置 | 行为 |
| --- | --- |
| `Emby.Drawing.Skia/SkiaEncoder.CreateImageCollage` | 按比例派发：`ratio >= 1.4` → `BuildThumbCollage`；`>= .9` → `BuildSquareCollage`；`< .9` 也是 `BuildSquareCollage`（源码注释 `// @todo create Poster collage capability`） |
| `BaseDynamicImageProvider.CreateImage` | `Thumb` → `CreateThumbCollage` 640×360；`Primary` 时 `UserView`/`Playlist`/`MusicGenre`/`Genre`/`GameGenre`/`PhotoAlbum` 走 `CreateSquareCollage` 600×600，**其余一律 `CreatePosterCollage` 400×600** |
| `BaseDynamicImageProvider.GetStripCollageImagePaths` | 只有 Primary → Thumb 两级回退，没有 backdrop 分支 |
| `Collections/CollectionImageProvider.CreateImage` | BoxSet 走 `CreateSingleImage(..., ImageType.Primary)`：**从成员里挑一张本地 Primary 原文件拷贝，根本不拼贴**；成员顺序 `OrderBy(i => Guid.NewGuid())` 随机；`Supports()` 要求 `item.IsLocked` |

**由此得到的结论**：Emby 3.3 的合集封面不是拼贴，拼贴是 Thumb / 媒体库那一侧的行为。
本项目把 `BuildThumbCollage` 用到合集 Primary 上，是**兼容实现**（视觉与参考截图一致），
不是对 3.3 行为的事实复刻；对媒体库文件夹（960×540）则与上游用途一致。

**未证实**：
- 现代 Emby（4.x 闭源）的合集封面算法。仅有 changelog 证据：
  `4.10.0.40` 的 `fix collections not getting images on a fresh installation`、
  `improve single item collages`（出现两次）、`4.11` 的 `Update to libvips 8.18` /
  `Support creating image collage with libvips` —— 说明确实会给合集做拼贴、且"单条目拼贴"
  被单独优化过（对应本项目 1 张时的 `AAAA`），但算法已迁到 libvips，无法逐行核对。
- BoxSet 成员传入的顺序与候选上限。`GetItemsWithImages` 是抽象方法，从这两个文件看不出顺序。

因此本项目**不宣称等同于当前 Emby 合集 Primary 的完整生成链路**，只宣称复现了
已核实的这段历史横版拼接设计。成员顺序取 `ORDER BY id DESC`（最新入库优先），
是兼容选择，不是已证实的原生排序。

## 3. 代码位置

| 文件 | 职责 |
| --- | --- |
| `internal/collage/collage.go` | 布局几何、四列拼接、倒影、WebP 输出 |
| `internal/collage/lanczos.go` | Lanczos3 可分离两趟重采样（缩小按 filter scale 扩核抗混叠） |
| `internal/collage/exif.go` | 有界解码 + EXIF Orientation 1..8 |
| `internal/server/collection_cover.go` | 取成员、缓存键、并发生成去重、原子写、生效记录（与媒体库共用） |
| `internal/server/library_cover.go` | 媒体库封面：960×540、库根目录自定义封面保护、生效记录 |
| `internal/server/collection_cover_admin.go` | 合集管理端点、批量任务状态机（媒体库复用 status/cancel） |
| `internal/server/library_cover_admin.go` | 媒体库管理端点 |
| `internal/server/collection_cover_auto.go` | 两类自动触发开关、统一任务入口 |
| `internal/store/migration_collection_covers.go` | migration 23：`collection_covers` 表 |
| `internal/store/collection_cover.go` | 合集生效记录读写 |
| `internal/store/migration_library_covers.go` | migration 24：`library_covers` 表 |
| `internal/store/library_cover.go` | 媒体库生效记录读写 |

不新增第三方依赖：Lanczos3 是自己实现的，未引入 `golang.org/x/image`。

## 4. 接口

七个端点都在 `/api/admin` 组下（`requireAuth` + `requireAdmin` + 审计）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/admin/collection-cover/run` | 批量生成/发布，202 + 任务快照 |
| GET | `/api/admin/collection-cover/status` | 轮询（`task_id` + `instance_id`） |
| POST | `/api/admin/collection-cover/cancel` | 取消，只停后续 |
| GET | `/api/admin/collection-cover/preview` | `?collection=<名称>`，直接返回 WebP |
| POST | `/api/admin/collection-cover/revert` | 删除生效记录，回退代表海报 |
| GET | `/api/admin/collection-cover/settings` | 读自动触发开关：`{"auto":bool}` |
| PUT | `/api/admin/collection-cover/settings` | 写自动触发开关，body 必须是 `{"auto":bool}` |

媒体库封面是同一套语义的另一组端点（`status`/`cancel` 与合集**共用同一个任务槽**）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/admin/library-cover/run` | body 用 `libraries: [<id>]` 或 `all: true` |
| GET | `/api/admin/library-cover/status` | 复用合集任务状态（响应里的 `kind` 为 `library`） |
| POST | `/api/admin/library-cover/cancel` | 复用合集取消 |
| GET | `/api/admin/library-cover/preview` | `?library=<id>`，直接返回 WebP |
| POST | `/api/admin/library-cover/revert` | body `{library:<id>}` 或 `{all:true}` |
| GET | `/api/admin/library-cover/settings` | 读 `library.cover_auto` |
| PUT | `/api/admin/library-cover/settings` | 写 `library.cover_auto` |

管理前端「设置 → 合集与媒体库封面」提供两个开关，以及「立即生成全部合集」与
「立即生成全部媒体库」两个按钮。

请求体（`run`）：

```json
{
  "collections": ["系列A"],
  "apply": false,
  "overwrite": false,
  "quality": 90,
  "width": 640,
  "height": 360,
  "reflection": true
}
```

- `collections` 与 `all` 二选一，同时给或都不给都是 400。
- `quality` 1..100，`width`/`height` 64..4096；越界一律 400，不静默兜底。
- `apply` 缺省为 `false`：**默认只生成预览文件，不写库、不改 Emby 数据**。
- `overwrite` 缺省为 `false`：已有自定义封面（`algorithm` 不是 `emby-strip-4col-v1`）时跳过，
  并在任务结果里记 `skipped` + `custom cover kept`。

### 自动触发（默认开）

`collection.cover_auto` / `library.cover_auto`（两个独立的 DB 设置项，默认都开）为
`true` 时，`scanLibraries` 在**扫描无错且 `IndexChanged` 为真**之后排一个后台 apply 任务
（这是扫描的副作用，**不单独记一条任务历史**，`status` 里的 `task_id` 为 0；只有手动
`run` 才记任务行），
覆盖全部可见合集 / 全部媒体库。因为同一时刻只允许一个封面任务，两类目标会**合并进同一个
任务**（而不是各排一个，否则第二个必然撞任务槽被整批跳过）。内容没变的目标在生成阶段按
`SourceHash` 直接判 `up to date`，不重写记录、不失效图片缓存；已有自定义封面的照旧跳过。
手动任务正在跑时自动触发让位（不排队、不报错）。

出厂即开启（对齐 Emby 自己给封面出图的行为），读不到设置时也按开启处理；只有显式写入
`false` 才关。关掉：

```bash
curl -X PUT -H "X-Emby-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"auto":false}' http://127.0.0.1:18080/api/admin/collection-cover/settings
```

注意：自动触发只在「扫描提交了索引变更」时排任务。开关从关改到开时，已经存在的合集不会
自己变（库早就扫完了、成员没动），需要点一次设置页的「立即生成全部合集」或调
`POST /api/admin/collection-cover/run` 做存量回填。

### 媒体库封面（960×540）

生成参数与合集相同（同一 `coverOptions`，默认尺寸换成 960×540），取片用
`LatestVisible(libraryID, …)` 取库内最新 8 部有图的影片，同样走 Primary → Thumb 回退。

生效后媒体库封面的解析优先级（`internal/server/library.go` 的 `libraryCoverPath`）：

1. 管理端上传的库封面（库根目录 `cover.*`）
2. 库根目录自带的 `poster/folder/cover/default` 图片
3. **自动拼贴封面**（`library_covers` 生效记录，960×540）
4. 库内最新入库影片的代表图

第 1、2 项属用户自定义封面，`apply` 阶段遇到它们会直接跳过并记
`skipped` + `custom cover kept`，不生成也不写库。第 3 项的文件名即缓存键，
`libraryCoverPath` 的缓存键里带 `SourceHash`，换封面自然换键，不需要遍历删旧键。

```bash
# 全部媒体库，只落预览文件
curl -H "X-Emby-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"all":true}' http://127.0.0.1:18080/api/admin/library-cover/run

# 发布 + 回退
curl -H "X-Emby-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"all":true,"apply":true}' http://127.0.0.1:18080/api/admin/library-cover/run
curl -H "X-Emby-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"all":true}' http://127.0.0.1:18080/api/admin/library-cover/revert
```

## 5. 批量生成

```bash
TOKEN=<管理员 token>

# 全部合集，只落预览文件
curl -H "X-Emby-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"all":true}' http://127.0.0.1:18080/api/admin/collection-cover/run

# 发布（不动已有自定义封面）
curl -H "X-Emby-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"all":true,"apply":true}' http://127.0.0.1:18080/api/admin/collection-cover/run

# 全部回退
curl -H "X-Emby-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"all":true}' http://127.0.0.1:18080/api/admin/collection-cover/revert
```

同一时刻只跑一个合集封面任务（占用返回 409）；任务经 `beginWorker` 登记，
关停时会等待或在准入前拒绝，不会留下「Running 但没人跑」的状态。

## 6. 缓存与增量

缓存键 = `sha256(算法标识 | WxH | quality | reflection | 作用域 | 每个成员的 id:图mtime tag)`，
取前 32 位十六进制，文件名 `<key>.webp`。作用域对合集是合集名（与改造前字节一致，
既有缓存不失效）、对媒体库是 `library:<id>`；落盘目录分别是数据库同级
`collection-covers/` 与 `library-covers/`。

- 成员增减 → 键变 → 重新生成。
- 海报被替换（mtime 变）→ `posterTag` 变 → 键变 → 重新生成。
- 参数变化 → 键变。
- 都没变 → `os.Stat` 命中直接复用，不重新渲染。

写文件走 `writeFileAtomic`（同目录临时文件 → `os.Rename`），中断不会留下半张图。
同一 key 的并发请求用 `coverFlight` 合并：只有一个 leader 渲染，其余等结果，
不会重复计算。

## 7. 异常处理

| 情况 | 行为 |
| --- | --- |
| 合集为空 / 无成员有海报 | `skipped`，`reason = "no posters"`，不写生效记录 |
| 只有一张有效海报 | 正常生成，四列都填这张（`AAAA`） |
| 海报全部解码失败 | `skipped`，`reason = "no decodable posters"` |
| 部分海报损坏 | 跳过损坏项，其余照常拼；不中断整批 |
| 单张解码 panic | `decodeOriented` 内 recover 该次解码，计为跳过 |
| 图片尺寸异常（宽或高 ≤0、像素超上限） | 跳过该列，不参与拼接 |
| 输出目录不可写 | 该合集记 `failed`，其余继续 |
| WebP 编码失败 | 同上，记 `failed` |
| 批量中单条失败 | `failed++`，继续下一条 |
| 已结束的任务再次取消 | 409 |

## 8. 测试

```
go test ./internal/collage/ ./internal/server/ ./internal/store/ -count=1
```

- `internal/collage`：几何常量、C# 取整语义、列分配、1/2/3/4/6/10 张、不同宽高比、
  缺失与损坏输入、像素级布局、关闭倒影、重复执行结果一致、窄图跳过倒影、
  源文件不被修改。
- `internal/server`（`collection_cover_test.go`，10 个用例）：640×360 WebP 输出、
  缓存命中不落新文件、海报 mtime 变化后重新生成、apply 后 `/Items/boxset:.../Images/Primary`
  字节与生成文件一致且 `PrimaryImageAspectRatio ≈ 1.7778`、自定义封面默认保留（显式
  `overwrite` 才替换）、revert 回退代表海报、无海报与损坏海报只跳过不失败、
  源海报字节与 mtime 不变、8 种非法请求体 + 空名 + 不存在合集、
  4 并发预览结果字节一致且只落 1 个文件。
- `internal/server`（`library_cover_test.go`，4 个用例）：`/library-cover/settings` 读写往返与
  三种坏体 400、两个开关互不影响、生成后 960×540 落盘且 `Library/MediaFolders` 暴露
  `ImageTags.Primary` 与 `PrimaryImageAspectRatio ≈ 1.7778`、重复生成复用同一文件、
  库根目录有 `poster.png` 时 `custom cover kept` 且不写生效记录、
  成员只有 `landscape.png` 时按 Thumb 回退参与拼贴。
- `internal/store`：`collection_covers` / `library_covers` 表迁移与读写、重开库后仍在。

全量：`go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿。

## 9. 示例输出

仓库内 `internal/collage/testdata/samples/`：`s1` `s2` `s3` `s4` `s6` `s10` 分别是 1/2/3/4/6/10 张输入的结果
（`s4.png` 同一布局的 PNG 便于直接打开）。用 `COLLAGE_SAMPLES=1 go test ./internal/collage/ -run TestSamples` 可重新生成到 `/tmp/collage-samples/`。输入是合成的 2:3 竖版海报，带序号便于核对列顺序。

四列的实际填充（按列做像素比对，容差 <2/255）：

| 输入张数 | 1 | 2 | 3 | 4 | 6 | 10 |
| --- | --- | --- | --- | --- | --- | --- |
| 四列内容 | `AAAA` | `ABAB` | `ABCA` | `ABCD` | `ABCD` | `ABCD` |

`s4`、`s6`、`s10` 字节相同是预期行为：只画四列，第 5 张及以后不参与。

## 10. 与参考截图的差异

参考截图里是一张约 236×133 的 16:9 卡片（比例 1.7744）。逐项核对：

| 检查项 | 参考截图实测 | 本项目 640×360 | 结论 |
| --- | --- | --- | --- |
| 画面宽高比 | 1.7744 | 1.7778 | 一致（截图缩放误差） |
| 横向拼接数量 | 4 列 | 4 列 | 一致 |
| 左右留边 | 卡片内 x 0–2.9 为纯黑（实测绝对 x 33–36） | 0–7（映射到截图 = 33.0–36.0） | 一致 |
| 列间空隙 | 实测纯黑列 91–94 / 150–152 / 208–211 | 158–165 / 316–323 / 474–481，映射到截图 = 91.3–94.2 / 149.5–152.5 / 207.8–210.7 | 一致，每处 8px 黑缝 |
| 主体高度 | — | 主体 `y=4..255` | — |
| 主体-倒影间隔 | 卡片内相对行 244–260 为纯黑带 | `y=256..259`（映射到截图 = 134–135） | 一致 |
| 倒影起点 | 实测相对行 260.3 开始出现倒影内容 | `y=260` | 一致 |
| 底部渐隐倒影 | 有，向下变暗 | 有，alpha `{128,208,240,255}` | 一致 |
| 排列顺序 | 逐列不同海报 | 按成员顺序逐列填充 | 顺序规则未证实，见 §2 |

**倒影衰减实测**（反射行亮度 / 对应主体镜像行亮度，只在海报列内取样）：

| 反射起点后行数 | 源码公式保留系数 | 参考截图实测 | 本项目实测 |
| --- | --- | --- | --- |
| 0 | 0.498 | — | 0.491 |
| 10 | 0.404 | 0.339 | 0.395 |
| 20 | 0.310 | 0.250 | 0.300 |
| 30 | 0.216 | 0.169 | 0.206 |
| 50 | 0.122 | 0.107 | 0.116 |
| 70 | 0.053 | 0.046 | 0.046 |
| 90 | 0.018 | 0.010 | 0.012 |

本项目曲线贴合公式（误差 ≤0.01），参考截图整体再暗 0.02–0.06 —— 这是截图经过
缩放和再压缩后的损失，不是算法差异。**倒影可见高度取 `min(ReflectionHeight, BodyHeight/2)`：
640×360 下 `ReflectionHeight = 100`，`BodyHeight/2 = 126`，所以实际可见 100 行，与源码
`ClipRect(width, remainingHeight)` 一致。**

**已知差异**：

1. **倒影渐变的实现方式**。源码用 Skia 的线性渐变 shader 叠在黑图上；本项目用
   `image.Alpha` 蒙版 + `draw.DrawMask` 做同一组 4 个停靠点的分段线性插值。
   停靠点与端点 alpha 相同，但中间插值不会和 Skia 逐像素完全相同，
   倒影区亮度曲线可能有几个灰度级的偏差。
2. **重采样实现**。源码用 Skia `Lanczos3`，本项目是自写的可分离 Lanczos3，
   权重归一化与抗混叠策略一致但浮点细节不同，缩放后的高频细节会有极小差异。
3. **成员顺序**。参考截图无法反推出原生取片顺序；本项目取 `id DESC`。
   如果实际原生按年份或标题排序，同一合集的列顺序会不同。
4. **上游选择范围**。源码只接受本地文件，远端 URL 不参与；本项目同样只读本地，
   并实现了 Primary → Thumb 回退（`coverSourcePath`：`PosterPath` 缺失时用
   `LandscapePath`，即扫描器认的 `landscape.*` 横图），与上游
   `GetStripCollageImagePaths` 的两级回退一致。
5. **算法归属**。上游把 `BuildThumbCollage` 用于 Thumb / 媒体库 / UserView，
   3.3 的合集 Primary 是"挑一张成员海报拷贝"（见 §2 调用矩阵）。本项目把该算法用于
   合集 Primary 是兼容选择，视觉与参考截图一致，但不宣称是 3.3 的事实行为。
