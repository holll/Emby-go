# 合集自动拼接封面

复刻 Emby 历史 `StripCollageBuilder` 的横版合集封面：把合集成员的竖版海报横向拼成一张
640×360 的横版图。**不是九宫格、不是瀑布流、也不是把整张竖版海报缩小后并排。**

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
少图循环补位、编码质量 90、只接受本地海报文件（`ImageType.Primary` 且 `IsLocalFile`，
否则退到 `ImageType.Thumb` 且 `IsLocalFile`）。

**未证实**：
- BoxSet 成员传入的顺序与候选列表上限。`BaseDynamicImageProvider.GetItemsWithImages`
  是抽象方法，从这两个文件里看不出 BoxSet 的取片顺序。
- 当前 Emby 版本是否仍走这条 2018 年的链路（该文件此后未再改动，但没有拿到现行
  BoxSet 专属 provider 作为证据）。

因此本项目**不宣称等同于当前 Emby BoxSet Primary 的完整生成链路**，只宣称复现了
已核实的这段历史横版拼接设计。成员顺序在本项目里取 `ORDER BY id DESC`（最新入库优先），
这是兼容选择，不是已证实的原生排序；`LatestVisible` 目前也没有年份/标题排序入口。

## 3. 代码位置

| 文件 | 职责 |
| --- | --- |
| `internal/collage/collage.go` | 布局几何、四列拼接、倒影、WebP 输出 |
| `internal/collage/lanczos.go` | Lanczos3 可分离两趟重采样（缩小按 filter scale 扩核抗混叠） |
| `internal/collage/exif.go` | 有界解码 + EXIF Orientation 1..8 |
| `internal/server/collection_cover.go` | 取成员、缓存键、并发生成去重、原子写、生效记录 |
| `internal/server/collection_cover_admin.go` | 5 个管理端点、批量任务状态机 |
| `internal/store/migration_collection_covers.go` | migration 23：`collection_covers` 表 |
| `internal/store/collection_cover.go` | 生效记录读写 |

不新增第三方依赖：Lanczos3 是自己实现的，未引入 `golang.org/x/image`。

## 4. 接口

五个端点都在 `/api/admin` 组下（`requireAuth` + `requireAdmin` + 审计）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/admin/collection-cover/run` | 批量生成/发布，202 + 任务快照 |
| GET | `/api/admin/collection-cover/status` | 轮询（`task_id` + `instance_id`） |
| POST | `/api/admin/collection-cover/cancel` | 取消，只停后续 |
| GET | `/api/admin/collection-cover/preview` | `?collection=<名称>`，直接返回 WebP |
| POST | `/api/admin/collection-cover/revert` | 删除生效记录，回退代表海报 |

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

缓存键 = `sha256(算法标识 | WxH | quality | reflection | 合集名 | 每个成员的 id:海报mtime tag)`，
取前 32 位十六进制，文件名 `<key>.webp`，落在数据库同级 `collection-covers/`。

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
- `internal/store`：`collection_covers` 表迁移与读写、重开库后仍在。

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
4. **上游选择范围**。源码只接受本地海报文件，远端 URL 不参与；本项目同样只读本地
   `PosterPath`，但没有实现「Primary 缺失时回退 Thumb」这一层（本项目库里
   `PosterPath` 已经是扫描/刮削后的最终结果）。
