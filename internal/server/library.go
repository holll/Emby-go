package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"emby-go/internal/avatar"
	"emby-go/internal/imageutil"
	"emby-go/internal/store"
)

// settingAvatarsDir 演员头像目录的设置键；未设置时用 <db 同级>/avatars（需求 A4 #20）。
const settingAvatarsDir = "scrape.avatars_dir"

// avatarsDir 返回头像本地副本目录：设置项优先，否则与数据库文件同级。
func (a *App) avatarsDir() string {
	if dir, err := a.db.Setting(settingAvatarsDir); err == nil && strings.TrimSpace(dir) != "" {
		return strings.TrimSpace(dir)
	}
	return avatar.DefaultDir(a.cfg.DBPath)
}

// personAvatar 返回演员本地头像副本的路径与内容标识。
// 只有在「索引里有该演员」且「本地副本确实存在」时才返回；否则返回空串，
// 调用方回退到既有的「参演影片海报」占位行为。
func (a *App) personAvatar(name string) (string, string) {
	actor, err := a.db.ActorAvatar(name)
	if err != nil || actor.Name == "" {
		return "", ""
	}
	path := avatar.Path(a.avatarsDir(), actor.Name)
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", ""
	}
	tag := actor.AvatarTag
	if tag == "" {
		// 索引里没记标识（如头像文件由外部直接放入）时按文件 mtime 兜底。
		tag = a.posterTag(path)
	}
	return path, tag
}

// entityPosterPath 返回某实体（Genre/Tag/Studio/Person）的代表性海报路径（带短缓存）。
func (a *App) entityPosterPath(kind, name string) string {
	key := "ep:" + kind + ":" + name
	if b, ok := a.cache.Get(key); ok {
		return string(b)
	}
	p, err := a.db.EntityPoster(kind, name, 0, "")
	if err != nil || p == "" {
		a.cache.Set(key, []byte{}, 5*time.Minute)
		return ""
	}
	a.cache.Set(key, []byte(p), 5*time.Minute)
	return p
}

// entityKind 解析 “genre:<base64url>” 这类虚拟实体 id 为规范实体类型与名称。
// 名称 base64url 编码是为了放进 URL 路径时不含 / ? # 等破坏路径的字符。
func entityKind(rawID string) (kind, name string, ok bool) {
	index := strings.IndexByte(rawID, ':')
	if index <= 0 || index == len(rawID)-1 {
		return "", "", false
	}
	switch strings.ToLower(rawID[:index]) {
	case "genre":
		kind = "Genre"
	case "tag":
		kind = "Tag"
	case "studio":
		kind = "Studio"
	case "person":
		kind = "Person"
	default:
		return "", "", false
	}
	encoded := rawID[index+1:]
	if decoded, err := base64.RawURLEncoding.DecodeString(encoded); err == nil {
		return kind, string(decoded), true
	}
	// 兼容旧明文 id（如 genre:Drama）。
	return kind, encoded, true
}

// 合集媒体库（CollectionType=boxsets）在 Views 中使用的固定 Id。
const boxsetViewID = "boxsets"

// 媒体库对外 id = libraryIDBase + 内部库 id。
// 影片 id 是自增小整数，两者相加后永不冲突（对齐 Emby 全局唯一 item id），
// 这样 /Items/{库id}/Images/Primary 能稳定取到库封面而不是撞上同名影片。
const libraryIDBase int64 = 1_000_000_000_000

func externalLibraryID(id int64) string { return strconv.FormatInt(libraryIDBase+id, 10) }

// parseLibraryExternal 判断一个数值 id 是否落在媒体库外部 id 区间；命中返回内部库 id。
// 区间外的数值视为普通影片/旧内部库 id（兼容旧客户端把库 id 直接当 ParentId）。
func parseLibraryExternal(num int64) (int64, bool) {
	if num >= libraryIDBase {
		return num - libraryIDBase, true
	}
	return num, false
}

// boxsetID / parseBoxsetID：单个合集的复合 Id（boxset:<base64(name)>）。
func boxsetID(name string) string {
	return "boxset:" + base64.RawURLEncoding.EncodeToString([]byte(name))
}
func parseBoxsetID(raw string) (string, bool) {
	const prefix = "boxset:"
	if !strings.HasPrefix(raw, prefix) || len(raw) == len(prefix) {
		return "", false
	}
	encoded := raw[len(prefix):]
	if name, err := base64.RawURLEncoding.DecodeString(encoded); err == nil {
		return string(name), true
	}
	return encoded, true // 兼容旧明文 id
}

// parseParentScope 把 ParentId 参数解析为 (媒体库id, 合集作用域)。
// collection：""=不限；"*"=任意合集成员（合集媒体库上下文）；其它=具体合集名。
func parseParentScope(parent string) (int64, string) {
	parent = strings.TrimSpace(parent)
	if parent == "" || parent == "0" {
		return 0, ""
	}
	if id, err := strconv.ParseInt(parent, 10, 64); err == nil {
		internal, _ := parseLibraryExternal(id)
		return internal, ""
	}
	if parent == boxsetViewID {
		return 0, "*"
	}
	if name, ok := parseBoxsetID(parent); ok {
		return 0, name
	}
	return 0, ""
}

// artRatio 依图像文件基础名估算主视觉纵横比：宽图(16:9) vs 海报(2:3)。
// 没有真实解码尺寸时用这个近似值，客户端据此决定卡片比例。
func artRatio(path string) float64 {
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, "fanart") || strings.Contains(base, "backdrop") || strings.Contains(base, "landscape") {
		return 16.0 / 9.0
	}
	return 2.0 / 3.0
}

// posterTag 用海报文件 mtime 生成稳定缓存标签，文件被替换后 tag 变化触发客户端刷新。
// 结果做进程内短缓存，避免列表/图片请求对（可能较慢的）媒体盘反复 stat。
func (a *App) posterTag(path string) string {
	now := time.Now()
	a.tagMu.Lock()
	if a.tags == nil {
		a.tags = make(map[string]tagEntry)
	}
	if e, ok := a.tags[path]; ok {
		ttl := 5 * time.Minute
		if e.neg {
			ttl = 30 * time.Second
		}
		if now.Sub(e.ts) < ttl {
			a.tagMu.Unlock()
			return e.tag
		}
	}
	a.tagMu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		a.tagMu.Lock()
		a.tags[path] = tagEntry{tag: "0", ts: now, neg: true}
		a.tagMu.Unlock()
		return "0"
	}
	tag := strconv.FormatInt(info.ModTime().UnixNano(), 36)
	a.tagMu.Lock()
	a.tags[path] = tagEntry{tag: tag, ts: now}
	a.tagMu.Unlock()
	return tag
}

// invalidateImageTag 立即失效指定图片路径的 tag 缓存。
//
// posterTag 是 5 分钟进程内缓存，且 cache.Clear() 只清 Redis、清不到它——
// 覆盖同名图片（上传海报、写入演员头像）后若不显式失效，
// 客户端会拿着旧的 ImageTag 继续显示旧图，最长 5 分钟。
func (a *App) invalidateImageTag(paths ...string) {
	a.tagMu.Lock()
	defer a.tagMu.Unlock()
	for _, path := range paths {
		delete(a.tags, path)
	}
}

// invalidateNFOStreams 失效指定 NFO 的流信息缓存（刮削/编辑改写 NFO 后调用）。
// probe 的整库任务结束后会整体清空；单条写入必须精确失效，否则详情抽屉
// 与 PlaybackInfo 会继续返回旧参数。
func (a *App) invalidateNFOStreams(nfoPaths ...string) {
	a.nfoMu.Lock()
	defer a.nfoMu.Unlock()
	for _, path := range nfoPaths {
		delete(a.nfos, path)
	}
}

// libraryCoverPath 返回媒体库封面路径：优先库根目录自带的 poster/folder/cover/default 图片
// （webp/jpg/jpeg/png 均可），没有则借用库内最近入库影片的代表图（宽图优先）。
// 解析要多次 stat 媒体盘并可能回查库，按库版本号缓存（空结果同样缓存）。
func (a *App) libraryCoverPath(l store.Library) string {
	key := "libcover:" + a.db.Version("g:version") + ":" + strconv.FormatInt(l.ID, 10)
	if b, ok := a.cache.Get(key); ok {
		return string(b)
	}
	path := imageutil.FindPoster(l.Path)
	if path == "" {
		path, _ = a.db.RepresentativeArt(l.ID)
	}
	a.cache.Set(key, []byte(path), 5*time.Minute)
	return path
}

// libraryCoverPathByID 按库内部 id 解析封面；库不存在返回空串。
func (a *App) libraryCoverPathByID(libraryID int64) string {
	l, err := a.db.Library(libraryID)
	if err != nil {
		return ""
	}
	return a.libraryCoverPath(l)
}

// coverRatio 优先返回图片真实宽高比（webp/jpg/png 均可读），失败再按文件名猜测。
func coverRatio(path string) float64 {
	if ratio := imageutil.AspectRatio(path); ratio > 0 {
		return ratio
	}
	return artRatio(path)
}

func zeroUserData() gin.H {
	return gin.H{"PlaybackPositionTicks": 0, "PlayCount": 0, "IsFavorite": false, "Played": false}
}

// collectionFolderDTO 把内部媒体库渲染为 Emby 的 CollectionFolder（电影库）BaseItemDto。
// Id 用外部命名空间（libraryIDBase+内部id），与影片 id 全局不冲突。
func (a *App) collectionFolderDTO(l store.Library) gin.H {
	unplayed, _ := a.db.UnplayedInLibrary(l.ID)
	item := gin.H{
		"Id":                   externalLibraryID(l.ID),
		"Name":                 l.Name,
		"Type":                 "CollectionFolder",
		"CollectionType":       "movies",
		"ServerId":             a.serverID,
		"DisplayPreferencesId": strconv.FormatInt(l.ID, 10),
		"IsFolder":             true,
		"BackdropImageTags":    []string{},
		"UserData": gin.H{
			"UnplayedItemCount": unplayed, "PlaybackPositionTicks": 0,
			"IsFavorite": false, "Played": false,
		},
	}
	if cover := a.libraryCoverPath(l); cover != "" {
		item["ImageTags"] = gin.H{"Primary": a.posterTag(cover)}
		item["PrimaryImageAspectRatio"] = coverRatio(cover)
	}
	return item
}

// boxsetFolderDTO 返回「合集」媒体库文件夹项（Views / 详情共用）。
func (a *App) boxsetFolderDTO(collections []string) gin.H {
	item := gin.H{
		"Id": boxsetViewID, "Name": "合集", "Type": "CollectionFolder",
		"CollectionType": "boxsets", "ServerId": a.serverID,
		"DisplayPreferencesId": boxsetViewID, "IsFolder": true,
		"BackdropImageTags": []string{},
		"UserData":          gin.H{"UnplayedItemCount": len(collections), "PlaybackPositionTicks": 0, "IsFavorite": false, "Played": false},
	}
	for _, name := range collections {
		if poster, _ := a.db.CollectionPoster(name); poster != "" {
			item["ImageTags"] = gin.H{"Primary": a.posterTag(poster)}
			item["PrimaryImageAspectRatio"] = artRatio(poster)
			break
		}
	}
	return item
}

// boxsetItemDTO 返回单个合集（BoxSet）的 BaseItemDto。
func (a *App) boxsetItemDTO(name string) gin.H {
	count, genres, _ := a.db.CollectionSummary(name)
	poster, _ := a.db.CollectionPoster(name)
	return a.boxsetItemFrom(name, store.CollectionStat{Count: count, Poster: poster, Genres: genres})
}

// boxsetItemFrom 用已取回的聚合信息渲染 BoxSet DTO（列表页批量取数时复用）。
func (a *App) boxsetItemFrom(name string, stat store.CollectionStat) gin.H {
	item := gin.H{
		"Id": boxsetID(name), "Name": name, "Type": "BoxSet", "IsFolder": true,
		"ServerId": a.serverID, "ChildCount": stat.Count,
		"BackdropImageTags": []string{},
		"UserData":          zeroUserData(),
	}
	if stat.Poster != "" {
		item["ImageTags"] = gin.H{"Primary": a.posterTag(stat.Poster)}
		item["PrimaryImageAspectRatio"] = artRatio(stat.Poster)
	}
	if len(stat.Genres) > 0 {
		sort.Strings(stat.Genres)
		item["Genres"] = stat.Genres
		item["GenreItems"] = nameObjects("Genre", stat.Genres)
	}
	return item
}

func (a *App) counts(c *gin.Context) {
	_, total, err := a.db.SearchFiltered(0, "", "", "", false, "title", false, 1, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	collections, _ := a.db.Collections()
	c.JSON(http.StatusOK, gin.H{
		"MovieCount": total, "SeriesCount": 0, "EpisodeCount": 0, "GameCount": 0,
		"ArtistCount": 0, "ProgramCount": 0, "GameSystemCount": 0, "TrailerCount": 0,
		"SongCount": 0, "AlbumCount": 0, "MusicVideoCount": 0, "BoxSetCount": len(collections),
		"BookCount": 0, "ItemCount": 0,
	})
}

// genres 处理 GET /Genres（库内流派实体列表，可按媒体库/合集范围过滤）。
func (a *App) genres(c *gin.Context) {
	if userID := c.Query("UserId"); userID != "" && userID != "1" {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	libraryID, collection := parseParentScope(c.Query("ParentId"))
	a.entityBrowse(c, "Genre", libraryID, collection)
}

func (a *App) resume(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	start, _ := strconv.Atoi(c.DefaultQuery("StartIndex", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("Limit", "30"))
	if start < 0 {
		start = 0
	}
	if limit < 1 || limit > 1000 {
		limit = 30
	}
	movies, total, err := a.db.Resumed(limit, start)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	dataMap, _ := a.db.DataFor(movieIDs(movies))
	actorMap, _ := a.db.ActorsFor(movieIDs(movies))
	items := make([]gin.H, 0, len(movies))
	for _, movie := range movies {
		items = append(items, a.embyItemActors(movie, dataMap[movie.ID], actorMap[movie.ID], true))
	}
	c.JSON(http.StatusOK, gin.H{"Items": items, "TotalRecordCount": total, "StartIndex": start})
}

// nextUp 无剧集库，恒为空结果（Emby 客户端主屏会调用）。
func (a *App) nextUp(c *gin.Context) {
	if userID := c.Query("UserId"); userID != "" && userID != "1" {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	a.emptyItems(c)
}

// similar 返回与当前影片共享 流派/标签/制片商 的近似影片。
func (a *App) similar(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	src, err := a.db.Movie(id)
	if err != nil || !src.IsVisible() {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("Limit", "12"))
	if limit < 1 || limit > 50 {
		limit = 12
	}
	// 相似度扫描整库成本较高，按 (版本,影片,limit) 短缓存，重复进入详情页直接命中。
	simKey := "similar:" + a.db.Version("g:version") + ":" + strconv.FormatInt(id, 10) + ":" + strconv.Itoa(limit)
	if b, ok := a.cache.Get(simKey); ok {
		c.Data(200, "application/json", b)
		return
	}
	movies, _, err := a.db.SearchFiltered(0, "", "", "", false, "title", false, 10000, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 一次取出全部可见影片的演员映射，避免逐候选打库。
	actorMap, err := a.db.AllActors()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	srcActors := actorNames(actorMap[src.ID])
	// 源影片的集合只构建一次，避免对每个候选重复转换。
	srcGenres, srcTags := toSet(src.Genres), toSet(src.Tags)
	srcStudios, srcActorSet := toSet(src.Studios), toSet(srcActors)

	type scored struct {
		movie store.Movie
		score int
	}
	results := make([]scored, 0, 64)
	for _, m := range movies {
		if m.ID == src.ID {
			continue
		}
		// 与 Emby 3.5.2 口径一致：总分需 > 2 才进入相似候选。
		if s := similarityScore(src, m, srcGenres, srcTags, srcStudios, srcActorSet, actorNames(actorMap[m.ID])); s > 2 {
			results = append(results, scored{movie: m, score: s})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].movie.Title < results[j].movie.Title
	})
	if len(results) > limit {
		results = results[:limit]
	}
	ids := make([]int64, 0, len(results))
	for _, r := range results {
		ids = append(ids, r.movie.ID)
	}
	dataMap, _ := a.db.DataFor(ids)
	out := make([]gin.H, 0, len(results))
	for _, r := range results {
		// actorMap 来自 AllActors，已含全部可见影片（无演员的影片缺省 nil，标记 loaded 避免回查）。
		out = append(out, a.embyItemActors(r.movie, dataMap[r.movie.ID], actorMap[r.movie.ID], true))
	}
	body, _ := json.Marshal(gin.H{"Items": out, "TotalRecordCount": len(out), "StartIndex": 0})
	a.cache.Set(simKey, body, 20*time.Second)
	c.Data(http.StatusOK, "application/json", body)
}

// similarityScore 按 Emby 3.5.2 SimilarItemsHelper 权重给两片打分：
// 同分级 +10；每共同 Genre +10；每共同 Tag +10；每共同 Studio +3；
// 同导演 +5；每共同演员 +3；年代差 <5 年 +4、<10 年 +2。
// xGenres/xTags/xStudios/xActors 为源影片预先构建的集合，避免逐候选重复转换。
func similarityScore(x, y store.Movie, xGenres, xTags, xStudios, xActors map[string]struct{}, yActors []string) int {
	score := 0
	if x.OfficialRating != "" && x.OfficialRating == y.OfficialRating {
		score += 10
	}
	score += overlapSet(xGenres, y.Genres, 10)
	score += overlapSet(xTags, y.Tags, 10)
	score += overlapSet(xStudios, y.Studios, 3)
	if x.Director != "" && x.Director == y.Director {
		score += 5
	}
	for _, name := range yActors {
		if _, ok := xActors[name]; ok {
			score += 3
		}
	}
	if x.Year > 0 && y.Year > 0 {
		diff := x.Year - y.Year
		if diff < 0 {
			diff = -diff
		}
		switch {
		case diff < 5:
			score += 4
		case diff < 10:
			score += 2
		}
	}
	return score
}

// overlapSet 统计 y 中命中 set 的项数并乘以权重。
func overlapSet(set map[string]struct{}, y []string, weight int) int {
	count := 0
	for _, v := range y {
		if _, ok := set[v]; ok {
			count++
		}
	}
	return count * weight
}

func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			set[v] = struct{}{}
		}
	}
	return set
}

func (a *App) views(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	libs, e := a.db.Libraries()
	if e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	out := make([]gin.H, 0, len(libs))
	for _, l := range libs {
		out = append(out, a.collectionFolderDTO(l))
	}
	// 有合集（NFO <set>）时追加「合集」媒体库视图（CollectionType=boxsets）。
	if collections, err := a.db.Collections(); err == nil && len(collections) > 0 {
		out = append(out, a.boxsetFolderDTO(collections))
	}
	c.JSON(200, gin.H{"Items": out, "TotalRecordCount": len(out)})
}

// nameObjects 把字符串名列表映射成 Emby 惯用的 {Name, Id} 实体对象。
// Id 使用 URL 安全的复合 id（kind:<base64(name)>），客户端会原样回传用于实体过滤/图片。
func nameObjects(kind string, names []string) []gin.H {
	out := make([]gin.H, 0, len(names))
	for _, name := range names {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		out = append(out, gin.H{"Name": name, "Id": entityId(kind, name)})
	}
	return out
}

// actorNames 取演员姓名列表（相似度打分等只关心名字的场景）。
func actorNames(actors []store.ActorRef) []string {
	out := make([]string, 0, len(actors))
	for _, actor := range actors {
		out = append(out, actor.Name)
	}
	return out
}

// peopleOfActors 组装影片人员：导演（若 NFO 有）+ 演员，Type 与真实 Emby 一致。
// 允许调用方传入批量预取的演员（含头像索引）。
// loaded=false 时 actors 视为未预取，回退单条查询；loaded=true 时即使 actors 为空也不查库。
//
// 有头像时补 PrimaryImageTag：客户端据此请求 Items/{personId}/Images/Primary，
// 缺这个字段它们根本不会去取图（见需求 R3b 契约一项）。
func (a *App) peopleOfActors(m store.Movie, actors []store.ActorRef, loaded bool) []gin.H {
	people := make([]gin.H, 0, 4)
	if name := strings.TrimSpace(m.Director); name != "" {
		people = append(people, gin.H{"Name": name, "Id": entityId("Person", name), "Type": "Director"})
	}
	if !loaded {
		if list, err := a.db.Actors(m.ID); err == nil {
			actors = list
		}
	}
	for _, actor := range actors {
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			continue
		}
		person := gin.H{"Name": name, "Id": entityId("Person", name), "Type": "Actor"}
		if tag := actor.AvatarTag; tag != "" {
			person["PrimaryImageTag"] = tag
		}
		people = append(people, person)
	}
	return people
}

// embyItem 把 DB 影片映射为 BaseItemDto（Movie）。字段名与真实 Emby 对齐，
// 并保证 iPlay 等脆弱客户端必须的 UserData / ImageTags / BackdropImageTags 恒存在。
func (a *App) embyItem(m store.Movie, d store.UserData) gin.H {
	return a.embyItemActors(m, d, nil, false)
}

// embyItemActors 与 embyItem 相同，但允许传入批量预取的演员列表避免逐片查库。
// loaded=true 表示 actors 已由调用方批量取回（可为空，不再单条回查）。
func (a *App) embyItemActors(m store.Movie, d store.UserData, actors []store.ActorRef, loaded bool) gin.H {
	id := strconv.FormatInt(m.ID, 10)
	sortName := m.SortName
	if sortName == "" {
		sortName = m.Title
	}
	v := gin.H{
		"Id": id, "Name": m.Title, "SortName": sortName,
		"Type": "Movie", "MediaType": "Video", "IsFolder": false,
		"ParentId":  strconv.FormatInt(m.LibraryID, 10),
		"ServerId":  a.serverID,
		"Container": m.SourceContainer,
		"CanDelete": false, "CanDownload": false, "SupportsSync": false,
		"UserData": d,
	}
	if m.RuntimeSeconds > 0 {
		v["RunTimeTicks"] = m.RuntimeSeconds * 10000000
	}
	if len(m.AdditionalParts) > 0 {
		v["PartCount"] = len(m.AdditionalParts) + 1
	}
	if m.OriginalTitle != "" {
		v["OriginalTitle"] = m.OriginalTitle
	}
	if m.Plot != "" {
		v["Overview"] = m.Plot
	}
	if m.Year > 0 {
		v["ProductionYear"] = m.Year
	}
	if m.Premiere != "" {
		v["PremiereDate"] = normalizeDate(m.Premiere)
	}
	if m.Rating > 0 {
		v["CommunityRating"] = m.Rating
	}
	if m.OfficialRating != "" {
		v["OfficialRating"] = m.OfficialRating
	}
	if len(m.Taglines) > 0 {
		v["Taglines"] = m.Taglines
	}
	if m.ProviderID != "" {
		v["ProviderIds"] = gin.H{"metatube": m.ProviderID}
	}
	if len(m.Genres) > 0 {
		v["Genres"] = m.Genres
		v["GenreItems"] = nameObjects("Genre", m.Genres)
	}
	if len(m.Tags) > 0 {
		v["Tags"] = m.Tags
		v["TagItems"] = nameObjects("Tag", m.Tags)
	}
	if len(m.Studios) > 0 {
		v["Studios"] = nameObjects("Studio", m.Studios)
	}
	if people := a.peopleOfActors(m, actors, loaded); len(people) > 0 {
		v["People"] = people
	}

	imageTags := gin.H{}
	backdrops := make([]string, 0, 1)
	if m.PosterPath != "" {
		imageTags["Primary"] = a.posterTag(m.PosterPath)
	}
	if m.LandscapePath != "" {
		imageTags["Thumb"] = a.posterTag(m.LandscapePath)
	} else if m.PosterPath != "" {
		// 无宽图时 Thumb 回退主海报，保证给客户端的 Thumb 标记总是可取图。
		imageTags["Thumb"] = a.posterTag(m.PosterPath)
	}
	if m.BackdropPath != "" {
		tag := a.posterTag(m.BackdropPath)
		backdrops = append(backdrops, tag)
		imageTags["Backdrop"] = tag
	}
	// iPlay Android 对 ImageTags / BackdropImageTags 做无条件解引用：
	// 二者必须恒为对象/数组（内容可空），缺失即列表页 NPE。
	v["ImageTags"] = imageTags
	v["BackdropImageTags"] = backdrops
	if m.RuntimeSeconds > 0 && d.PositionTicks > 0 {
		d.PlayedPercentage = float64(d.PositionTicks) / float64(m.RuntimeSeconds*10000000) * 100
		v["UserData"] = d
	}
	return v
}

// normalizeDate 把 NFO 常见的 "2006-01-02" 规整为 Emby 契约的全时间格式；
// 已是完整时间的原样透传，解析失败的字符串原样返回。
func normalizeDate(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05.0000000Z")
		}
	}
	return s
}

func (a *App) rootItems(c *gin.Context) {
	if userID := c.Query("UserId"); userID != "" && userID != "1" {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	a.itemsQuery(c)
}

func (a *App) items(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	a.itemsQuery(c)
}

// publicUsers 登录页探测：本服务单用户，不公开列出。
func (a *App) publicUsers(c *gin.Context) {
	c.JSON(http.StatusOK, []gin.H{})
}

// searchHints 兼容旧客户端 /Search/Hints：实测真机对该端点返回空数组，
// 本服务检索以 /Users/{uid}/Items?SearchTerm= 为准（iPlay/Yamby 走那条）。
func (a *App) searchHints(c *gin.Context) {
	c.JSON(http.StatusOK, []gin.H{})
}

// latest 首页「最新」：真机返回 BaseItemDto 数组。
func (a *App) latest(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	// ParentId 可能是媒体库外部 id（libraryIDBase+内部 id）或旧内部 id，统一换算。
	libraryID, _ := parseParentScope(c.Query("ParentId"))
	limit, _ := strconv.Atoi(c.DefaultQuery("Limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	movies, err := a.db.Latest(libraryID, limit, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	dataMap, _ := a.db.DataFor(movieIDs(movies))
	actorMap, _ := a.db.ActorsFor(movieIDs(movies))
	// Latest 返回裸数组（实测真实 Emby 不是 {Items:...} 包裹），同样支持 ?Fields=。
	fields := requestedFields(c)
	out := make([]gin.H, 0, len(movies))
	for _, m := range movies {
		item := a.embyItemActors(m, dataMap[m.ID], actorMap[m.ID], true)
		a.applyItemFields(item, m, fields, c)
		out = append(out, item)
	}
	c.JSON(http.StatusOK, out)
}

// suggestions 首页推荐：复用最近入库，返回 QueryResult 形态。
func (a *App) suggestions(c *gin.Context) {
	if !a.validUser(c) {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("Limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	movies, err := a.db.Latest(0, limit, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	dataMap, _ := a.db.DataFor(movieIDs(movies))
	actorMap, _ := a.db.ActorsFor(movieIDs(movies))
	out := make([]gin.H, 0, len(movies))
	for _, m := range movies {
		out = append(out, a.embyItemActors(m, dataMap[m.ID], actorMap[m.ID], true))
	}
	c.JSON(http.StatusOK, gin.H{"Items": out, "TotalRecordCount": len(out), "StartIndex": 0})
}

// itemsQuery 处理 GET /Users/{uid}/Items 与 GET /Items。
// 依据 IncludeItemTypes 区分「影片列表」与「流派/标签/制片商/演员实体列表」，
// 不支持的类型（如 Series/Episode）返回空结果而非错把影片当该类型返回。
func (a *App) itemsQuery(c *gin.Context) {
	parentRaw := c.Query("ParentId")
	lib, collection := parseParentScope(parentRaw)
	isBoxsetView := parentRaw == boxsetViewID

	start, _ := strconv.Atoi(c.DefaultQuery("StartIndex", "0"))
	if start < 0 {
		start = 0
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("Limit", "100"))
	if limit < 1 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	include := splitQueryTypes(c.Query("IncludeItemTypes"))
	virtual := make([]string, 0, 1)
	boxsetWanted := false
	movieWanted := false
	unsupportedOnly := true
	for _, t := range include {
		switch t {
		case "Movie", "Video":
			movieWanted = true
			unsupportedOnly = false
		case "BoxSet":
			boxsetWanted = true
			unsupportedOnly = false
		case "Genre", "Tag", "Studio", "Person":
			virtual = append(virtual, t)
			unsupportedOnly = false
		}
	}

	// 实体（Genre/Tag/Studio/Person）浏览：仅在客户端「只要实体」时处理。
	// 若同时请求了影片（如搜索页的 IncludeItemTypes=Movie,Series,Video,Person），
	// 以影片结果为准，否则 SearchTerm 会被整份实体列表顶掉。
	if len(virtual) > 0 && !movieWanted {
		a.entityBrowse(c, virtual[0], lib, collection)
		return
	}

	// 合集媒体库（boxset view）或某个合集内部：以实体之外均按盒集/成员处理。
	if isBoxsetView || (collection != "" && collection != "*") {
		if isBoxsetView {
			a.boxsetListResponse(c, start, limit, c.Query("SearchTerm"))
			return
		}
		// 打开某个合集：列出其成员影片。
		// SortBy 取首键，库用 DateCreated 主排序。
		sortBy := firstSortKey(c.DefaultQuery("SortBy", "SortName"))
		desc := strings.EqualFold(c.DefaultQuery("SortOrder", "Ascending"), "Descending")
		genre := joinEntityNames(c, "Genres", "Genre", "GenreIds")
		tags := joinEntityNames(c, "Tags", "Tag", "TagIds")
		studios := joinEntityNames(c, "Studios", "Studio", "StudioIds")
		person := joinEntityNames(c, "Person", "Person", "PersonIds")
		unplayed := strings.Contains(strings.ToLower(c.Query("Filters")), "isunplayed")
		favorite := strings.Contains(strings.ToLower(c.Query("Filters")), "isfavorite")
		ms, total, e := a.db.SearchScoped(0, collection, c.Query("SearchTerm"), c.Query("Years"), genre, tags, studios, person, unplayed, favorite, sortBy, desc, limit, start)
		if e != nil {
			c.JSON(500, gin.H{"error": e.Error()})
			return
		}
		a.respondItems(c, ms, total, start, "")
		return
	}

	// 根级或媒体库上下文：请求 BoxSet 类型时给出合集列表。
	if boxsetWanted && len(virtual) == 0 && lib == 0 && !movieWanted {
		a.boxsetListResponse(c, start, limit, c.Query("SearchTerm"))
		return
	}

	if len(include) > 0 && !movieWanted {
		// 客户端请求了不支持的纯类型（如 Series/Episode）：给空结果，避免返回 Movie。
		if unsupportedOnly {
			a.emptyItems(c)
			return
		}
	}

	sortBy := firstSortKey(c.DefaultQuery("SortBy", "SortName"))
	desc := strings.EqualFold(c.DefaultQuery("SortOrder", "Ascending"), "Descending")
	genre := joinEntityNames(c, "Genres", "Genre", "GenreIds")
	tags := joinEntityNames(c, "Tags", "Tag", "TagIds")
	studios := joinEntityNames(c, "Studios", "Studio", "StudioIds")
	person := joinEntityNames(c, "Person", "Person", "PersonIds")
	filterLower := strings.ToLower(c.Query("Filters"))
	unplayed := strings.Contains(filterLower, "isunplayed")
	favorite := strings.Contains(filterLower, "isfavorite")

	// Fields 决定响应体内容，须纳入缓存键，否则不同 Fields 的请求会互相串缓存。
	fields := requestedFields(c)
	term, years := c.Query("SearchTerm"), c.Query("Years")
	// 缓存键不需要带请求来源：响应里的流地址是相对路径，与宿主无关。
	key := strings.Join([]string{a.db.Version("g:version"), strconv.FormatInt(lib, 10), term, years, genre, tags, studios, person, c.Query("Filters"), sortBy, c.Query("SortOrder"), fields.key(), strconv.Itoa(start), strconv.Itoa(limit)}, "|")
	if b, ok := a.cache.Get("items:" + key); ok {
		c.Data(200, "application/json", b)
		return
	}
	ms, total, e := a.db.SearchScoped(lib, "", term, years, genre, tags, studios, person, unplayed, favorite, sortBy, desc, limit, start)
	if e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	a.respondItems(c, ms, total, start, key)
}

// itemFields 客户端通过 ?Fields= 显式索取的可选字段集合。
// 真实 Emby 的列表接口默认不返回这些内容，只有点名要才给；
// 详情接口则恒返回（见 item handler）。
type itemFields struct {
	mediaSources bool
	dateCreated  bool
	path         bool
}

// key 返回可用于缓存键的稳定标识。
func (f itemFields) key() string {
	mark := make([]string, 0, 3)
	if f.mediaSources {
		mark = append(mark, "src")
	}
	if f.dateCreated {
		mark = append(mark, "date")
	}
	if f.path {
		mark = append(mark, "path")
	}
	return strings.Join(mark, "+")
}

// requestedFields 解析 ?Fields=a,b,c（大小写不敏感）。
func requestedFields(c *gin.Context) itemFields {
	raw := strings.ToLower(c.Query("Fields"))
	has := func(name string) bool {
		for _, part := range strings.Split(raw, ",") {
			if strings.TrimSpace(part) == name {
				return true
			}
		}
		return false
	}
	return itemFields{
		mediaSources: has("mediasources"),
		dateCreated:  has("datecreated"),
		path:         has("path"),
	}
}

// applyItemFields 按 Fields 补充真实 Emby 需要显式索取才返回的条目字段。
// 实测：Fields=MediaSources 时条目还会额外多出顶层 Bitrate/Container/Size
// （从首个 MediaSource 复制的便捷字段），客户端列表页靠它显示码率与体积。
func (a *App) applyItemFields(item gin.H, m store.Movie, fields itemFields, c *gin.Context) {
	if fields.path {
		item["Path"] = m.SourcePath
	}
	if fields.dateCreated {
		item["DateCreated"] = embyTime(m.CreatedAt)
		item["DateModified"] = embyTime(m.UpdatedAt)
	}
	if !fields.mediaSources {
		return
	}
	sources := []gin.H{a.mediaSource(m, c)}
	item["MediaSources"] = sources
	source := sources[0]
	for _, name := range []string{"Bitrate", "Container", "Size"} {
		if value, ok := source[name]; ok {
			item[name] = value
		}
	}
}

// embyTime 把内部存储的 RFC3339 时间转换为 Emby 契约的 7 位小数 UTC 格式
// （如 2026-03-09T11:35:44.0000000Z）。解析失败时原样返回，避免因格式问题丢字段。
func embyTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

// respondItems 统一输出影片分页结果。cacheKey 非空时写入 15s 缓存（供 itemsQuery 命中复用）。
// UserData 与演员表按整页批量取回，避免逐片 N+1 查询。
func (a *App) respondItems(c *gin.Context, ms []store.Movie, total, start int, cacheKey string) {
	fields := requestedFields(c)
	ids := movieIDs(ms)
	dataMap, _ := a.db.DataFor(ids)
	actorMap, _ := a.db.ActorsFor(ids)
	out := make([]gin.H, 0, len(ms))
	for _, m := range ms {
		item := a.embyItemActors(m, dataMap[m.ID], actorMap[m.ID], true)
		a.applyItemFields(item, m, fields, c)
		out = append(out, item)
	}
	v := gin.H{"Items": out, "TotalRecordCount": total, "StartIndex": start}
	b, _ := json.Marshal(v)
	if cacheKey != "" {
		a.cache.Set("items:"+cacheKey, b, 15*time.Second)
	}
	c.Data(200, "application/json", b)
}

// movieIDs 提取影片列表的 id 切片（供批量查询）。
func movieIDs(ms []store.Movie) []int64 {
	ids := make([]int64, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

// boxsetListResponse 分页返回「合集」媒体库的 BoxSet 列表。
func (a *App) boxsetListResponse(c *gin.Context, start, limit int, term string) {
	stats, err := a.db.CollectionStats()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	term = strings.ToLower(strings.TrimSpace(term))
	names := make([]string, 0, len(stats))
	for name := range stats {
		if term == "" || strings.Contains(strings.ToLower(name), term) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if start > len(names) {
		start = len(names)
	}
	end := start + limit
	if end > len(names) {
		end = len(names)
	}
	items := make([]gin.H, 0, end-start)
	for _, name := range names[start:end] {
		items = append(items, a.boxsetItemFrom(name, stats[name]))
	}
	c.JSON(http.StatusOK, gin.H{"Items": items, "TotalRecordCount": len(names), "StartIndex": start})
}

func splitQueryTypes(v string) []string {
	var out []string
	for _, t := range strings.Split(v, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// firstSortKey 取 SortBy 逗号列表的首项作主排序键。
func firstSortKey(v string) string {
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			return part
		}
	}
	return "SortName"
}

// joinEntityNames 合并「名字参数」与「实体 Id 参数」为逗号分隔的实体名，
// 供 store 按名过滤。实体 Id 是 entityId 生成的 kind:<base64(name)>。
func joinEntityNames(c *gin.Context, nameParam, kind string, idParams ...string) string {
	parts := splitQueryTypes(c.Query(nameParam))
	for _, param := range idParams {
		for _, id := range splitQueryTypes(c.Query(param)) {
			if gotKind, name, ok := entityKind(id); ok && gotKind == kind {
				parts = append(parts, name)
			}
		}
	}
	return strings.Join(parts, ",")
}

func (a *App) emptyItems(c *gin.Context) {
	start, _ := strconv.Atoi(c.DefaultQuery("StartIndex", "0"))
	if start < 0 {
		start = 0
	}
	c.JSON(http.StatusOK, gin.H{"Items": []gin.H{}, "TotalRecordCount": 0, "StartIndex": start})
}

// entityId 把实体（Genre/Tag/Studio/Person）映射成 URL 安全的稳定字符串 Id。
func entityId(kind, name string) string {
	return strings.ToLower(kind) + ":" + base64.RawURLEncoding.EncodeToString([]byte(name))
}

// entityBrowse 对库内影片去重聚合出某类实体列表（Type=kind），按名排序后分页。
// collection 用于合集上下文限定（""/"*"/具体合集名），保证只返回该上下文内数量≥1 的实体。
func (a *App) entityBrowse(c *gin.Context, kind string, libraryID int64, collection string) {
	start, _ := strconv.Atoi(c.DefaultQuery("StartIndex", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("Limit", "100"))
	if start < 0 {
		start = 0
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	// 实体名需扫全库 JSON 列去重，逐项还要查代表海报，故按（版本/类型/范围/分页）缓存。
	cacheKey := "entities:" + a.db.Version("g:version") + ":" + strings.ToLower(kind) + ":" +
		strconv.FormatInt(libraryID, 10) + ":" + collection + ":" + strconv.Itoa(start) + ":" + strconv.Itoa(limit)
	if b, ok := a.cache.Get(cacheKey); ok {
		c.Data(http.StatusOK, "application/json", b)
		return
	}
	var (
		names []string
		err   error
	)
	switch strings.ToLower(kind) {
	case "genre":
		names, err = a.db.Genres(libraryID, collection)
	case "tag":
		names, err = a.db.Tags(libraryID, collection)
	case "studio":
		names, err = a.db.Studios(libraryID, collection)
	case "person":
		names, err = a.db.Persons(libraryID, collection)
	default:
		a.emptyItems(c)
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	sort.Strings(names)
	if start > len(names) {
		start = len(names)
	}
	end := start + limit
	if end > len(names) {
		end = len(names)
	}
	items := make([]gin.H, 0, end-start)
	for _, name := range names[start:end] {
		item := gin.H{
			"Id": entityId(kind, name), "Name": name, "Type": kind,
			"IsFolder": false, "ServerId": a.serverID,
		}
		if poster := a.entityPosterPath(kind, name); poster != "" {
			item["ImageTags"] = gin.H{"Primary": a.posterTag(poster)}
		}
		items = append(items, item)
	}
	body, _ := json.Marshal(gin.H{"Items": items, "TotalRecordCount": len(names), "StartIndex": start})
	a.cache.Set(cacheKey, body, 30*time.Second)
	c.Data(http.StatusOK, "application/json", body)
}

func (a *App) item(c *gin.Context) {
	if c.Param("uid") != "" && !a.validUser(c) {
		return
	}
	rawID := c.Param("id")
	// 合集媒体库文件夹本身（Id="boxsets"）。
	if rawID == boxsetViewID {
		collections, _ := a.db.Collections()
		c.JSON(http.StatusOK, a.boxsetFolderDTO(collections))
		return
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		// 多分段影片的 AdditionalPart（part-<movieID>-<part>）。
		if movieID, part, ok := parseVirtualPartID(rawID); ok {
			if m, e := a.db.Movie(movieID); e == nil && m.IsVisible() && part-2 < len(m.AdditionalParts) {
				c.JSON(http.StatusOK, a.partItem(m, part-2))
				return
			}
			c.JSON(404, gin.H{"error": "not found"})
			return
		}
		// 单个合集（boxset:<b64>）。
		if name, ok := parseBoxsetID(rawID); ok {
			c.JSON(http.StatusOK, a.boxsetItemDTO(name))
			return
		}
		// 虚拟实体（genre:/tag:/studio:/person:）详情：避免 404，返回最小 BaseItemDto。
		if kind, name, ok := entityKind(rawID); ok {
			item := gin.H{
				"Id": rawID, "Name": name, "Type": kind,
				"IsFolder": false, "ServerId": a.serverID,
			}
			if poster := a.entityPosterPath(kind, name); poster != "" {
				item["ImageTags"] = gin.H{"Primary": a.posterTag(poster)}
			}
			c.JSON(http.StatusOK, item)
			return
		}
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	// 媒体库外部 id（libraryIDBase+内部库 id）：返回 CollectionFolder DTO。
	if libID, ok := parseLibraryExternal(id); ok {
		lib, e := a.db.Library(libID)
		if e != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusOK, a.collectionFolderDTO(lib))
		return
	}
	// 详情恒含 MediaSources，但流地址是相对路径，缓存键无需按宿主分桶。
	key := "item:" + a.db.Version("g:version") + ":" + rawID
	if b, ok := a.cache.Get(key); ok {
		c.Data(200, "application/json", b)
		return
	}
	m, e := a.db.Movie(id)
	if e != nil || !m.IsVisible() {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	d, _ := a.db.Data(id)
	b, _ := json.Marshal(a.embyItemDetail(m, d, c))
	a.cache.Set(key, b, time.Hour)
	c.Data(200, "application/json", b)
}

// embyItemDetail 在列表 DTO 之上补齐详情接口恒返回的字段。
// 实测真实 Emby 详情（/Users/{uid}/Items/{id}）无需 Fields 即包含：
// DateCreated/DateModified、Path/FileName、MediaSources、顶层 MediaStreams，
// 以及从媒体源复制出来的 Bitrate/Container/Size/Width/Height/PartCount/Chapters。
// 缺了它们客户端详情页会显示不出入库时间、文件路径与媒体参数。
func (a *App) embyItemDetail(m store.Movie, d store.UserData, c *gin.Context) gin.H {
	item := a.embyItem(m, d)
	item["Path"] = m.SourcePath
	if name := itemFileName(m); name != "" && name != "." {
		item["FileName"] = name
	}
	if created := embyTime(m.CreatedAt); created != "" {
		item["DateCreated"] = created
		// 未单独记录修改时间时回退入库时间，与 Emby 对未改动条目的取值一致。
		modified := embyTime(m.UpdatedAt)
		if modified == "" {
			modified = created
		}
		item["DateModified"] = modified
	}
	if _, ok := item["PartCount"]; !ok {
		item["PartCount"] = 1
	}

	source := a.mediaSourceFor(m, strconv.FormatInt(m.ID, 10), m.SourcePath, c)
	item["MediaSources"] = []gin.H{source}
	// 顶层 MediaStreams 与首个媒体的 MediaStreams 同源（实测两者相等）。
	streams, _ := source["MediaStreams"].([]gin.H)
	item["MediaStreams"] = streams
	for _, name := range []string{"Bitrate", "Container", "Size"} {
		if value, ok := source[name]; ok {
			item[name] = value
		}
	}
	if width, height, ok := videoDimensions(streams); ok {
		item["Width"] = width
		item["Height"] = height
	}
	return item
}

// videoDimensions 取首个视频轨的像素尺寸，供条目顶层 Width/Height 使用。
func videoDimensions(streams []gin.H) (int, int, bool) {
	for _, stream := range streams {
		if stream["Type"] != "Video" {
			continue
		}
		width, okW := stream["Width"].(int)
		height, okH := stream["Height"].(int)
		if okW && okH && width > 0 && height > 0 {
			return width, height, true
		}
	}
	return 0, 0, false
}
