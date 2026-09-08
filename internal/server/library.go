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

	"emby-go/internal/store"
)

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

// posterTag 用海报文件 mtime 生成稳定缓存标签，文件被替换后 tag 变化触发客户端刷新。
func posterTag(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "0"
	}
	return strconv.FormatInt(info.ModTime().UnixNano(), 36)
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

// libraryCoverPath 决定媒体库在 Views 卡片上“实际可被取到”的主图路径。
// libraryCoverPath 返回媒体库主视觉路径（优先宽图 fanart，无则回退海报）。
func (a *App) libraryCoverPath(libraryID int64) string {
	path, _ := a.db.RepresentativeArt(libraryID)
	return path
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
	if cover := a.libraryCoverPath(l.ID); cover != "" {
		item["ImageTags"] = gin.H{"Primary": posterTag(cover)}
		item["PrimaryImageAspectRatio"] = artRatio(cover)
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
			item["ImageTags"] = gin.H{"Primary": posterTag(poster)}
			item["PrimaryImageAspectRatio"] = artRatio(poster)
			break
		}
	}
	return item
}

// boxsetItemDTO 返回单个合集（BoxSet）的 BaseItemDto。
func (a *App) boxsetItemDTO(name string) gin.H {
	count, genres, _ := a.db.CollectionSummary(name)
	item := gin.H{
		"Id": boxsetID(name), "Name": name, "Type": "BoxSet", "IsFolder": true,
		"ServerId": a.serverID, "ChildCount": count,
		"BackdropImageTags": []string{},
		"UserData":          zeroUserData(),
	}
	if poster, _ := a.db.CollectionPoster(name); poster != "" {
		item["ImageTags"] = gin.H{"Primary": posterTag(poster)}
		item["PrimaryImageAspectRatio"] = artRatio(poster)
	}
	if len(genres) > 0 {
		sort.Strings(genres)
		item["Genres"] = genres
		item["GenreItems"] = nameObjects("Genre", genres)
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
	movies, _, err := a.db.SearchFiltered(0, "", "", "", false, "title", false, 1000, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	items := make([]gin.H, 0)
	for _, movie := range movies {
		data, err := a.db.Data(movie.ID)
		if err == nil && data.PositionTicks > 0 && !data.HideFromResume {
			items = append(items, a.embyItem(movie, data))
		}
	}
	if start > len(items) {
		start = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	c.JSON(http.StatusOK, gin.H{"Items": items[start:end], "TotalRecordCount": len(items), "StartIndex": start})
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
	srcActors := actorMap[src.ID]

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
		if s := similarityScore(src, m, srcActors, actorMap[m.ID]); s > 2 {
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
	out := make([]gin.H, 0, len(results))
	for _, r := range results {
		d, _ := a.db.Data(r.movie.ID)
		out = append(out, a.embyItem(r.movie, d))
	}
	c.JSON(http.StatusOK, gin.H{"Items": out, "TotalRecordCount": len(out), "StartIndex": 0})
}

// similarityScore 按 Emby 3.5.2 SimilarItemsHelper 权重给两片打分：
// 同分级 +10；每共同 Genre +10；每共同 Tag +10；每共同 Studio +3；
// 同导演 +5；每共同演员 +3；年代差 <5 年 +4、<10 年 +2。
func similarityScore(x, y store.Movie, xActors, yActors []string) int {
	score := 0
	if x.OfficialRating != "" && x.OfficialRating == y.OfficialRating {
		score += 10
	}
	score += overlapWeight(x.Genres, y.Genres, 10)
	score += overlapWeight(x.Tags, y.Tags, 10)
	score += overlapWeight(x.Studios, y.Studios, 3)

	if x.Director != "" && x.Director == y.Director {
		score += 5
	}
	if set := toSet(xActors); set != nil {
		for _, name := range yActors {
			if _, ok := set[name]; ok {
				score += 3
			}
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

// overlapWeight 统计 y 列表中与 x 集合重叠的去重项数并乘以权重。
func overlapWeight(x, y []string, weight int) int {
	set := toSet(x)
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

// peopleOf 组装影片人员：导演（若 NFO 有）+ 演员。Type 与真实 Emby 一致。
func (a *App) peopleOf(m store.Movie) []gin.H {
	people := make([]gin.H, 0, 4)
	if name := strings.TrimSpace(m.Director); name != "" {
		people = append(people, gin.H{"Name": name, "Id": entityId("Person", name), "Type": "Director"})
	}
	if names, err := a.db.Actors(m.ID); err == nil {
		for _, name := range names {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			people = append(people, gin.H{"Name": name, "Id": entityId("Person", name), "Type": "Actor"})
		}
	}
	return people
}

// embyItem 把 DB 影片映射为 BaseItemDto（Movie）。字段名与真实 Emby 对齐，
// 并保证 iPlay 等脆弱客户端必须的 UserData / ImageTags / BackdropImageTags 恒存在。
func (a *App) embyItem(m store.Movie, d store.UserData) gin.H {
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
	if people := a.peopleOf(m); len(people) > 0 {
		v["People"] = people
	}

	imageTags := gin.H{}
	backdrops := make([]string, 0, 1)
	if m.PosterPath != "" {
		imageTags["Primary"] = posterTag(m.PosterPath)
	}
	if m.LandscapePath != "" {
		imageTags["Thumb"] = posterTag(m.LandscapePath)
	} else if m.PosterPath != "" {
		// 无宽图时 Thumb 回退主海报，保证给客户端的 Thumb 标记总是可取图。
		imageTags["Thumb"] = posterTag(m.PosterPath)
	}
	if m.BackdropPath != "" {
		tag := posterTag(m.BackdropPath)
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
	libraryID, _ := strconv.ParseInt(c.Query("ParentId"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("Limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	movies, err := a.db.Latest(libraryID, limit, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]gin.H, 0, len(movies))
	for _, m := range movies {
		d, _ := a.db.Data(m.ID)
		out = append(out, a.embyItem(m, d))
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
	out := make([]gin.H, 0, len(movies))
	for _, m := range movies {
		d, _ := a.db.Data(m.ID)
		out = append(out, a.embyItem(m, d))
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

	// 实体（Genre/Tag/Studio/Person）浏览：无论在哪一层上下文都先处理，
	// 并按合集范围过滤，保证只返回该上下文内数量≥1 的类型。
	if len(virtual) > 0 {
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

	key := strings.Join([]string{a.db.Version("g:version"), strconv.FormatInt(lib, 10), termOf(c), c.Query("Years"), genre, tags, studios, person, c.Query("Filters"), sortBy, c.Query("SortOrder"), strconv.Itoa(start), strconv.Itoa(limit)}, "|")
	if b, ok := a.cache.Get("items:" + key); ok {
		c.Data(200, "application/json", b)
		return
	}
	ms, total, e := a.db.SearchScoped(lib, "", c.Query("SearchTerm"), c.Query("Years"), genre, tags, studios, person, unplayed, favorite, sortBy, desc, limit, start)
	if e != nil {
		c.JSON(500, gin.H{"error": e.Error()})
		return
	}
	a.respondItems(c, ms, total, start, key)
}

func termOf(c *gin.Context) string { return c.Query("SearchTerm") }

// respondItems 统一输出影片分页结果。cacheKey 非空时写入 15s 缓存（供 itemsQuery 命中复用）。
func (a *App) respondItems(c *gin.Context, ms []store.Movie, total, start int, cacheKey string) {
	wantSources := strings.Contains(strings.ToLower(c.Query("Fields")), "mediasources")
	out := make([]gin.H, 0, len(ms))
	for _, m := range ms {
		d, _ := a.db.Data(m.ID)
		item := a.embyItem(m, d)
		if wantSources {
			item["MediaSources"] = []gin.H{a.mediaSource(m, c)}
		}
		out = append(out, item)
	}
	v := gin.H{"Items": out, "TotalRecordCount": total, "StartIndex": start}
	b, _ := json.Marshal(v)
	if cacheKey != "" {
		a.cache.Set("items:"+cacheKey, b, 15*time.Second)
	}
	c.Data(200, "application/json", b)
}

// boxsetListResponse 分页返回「合集」媒体库的 BoxSet 列表。
func (a *App) boxsetListResponse(c *gin.Context, start, limit int, term string) {
	names, err := a.db.Collections()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	term = strings.ToLower(strings.TrimSpace(term))
	if term != "" {
		filtered := names[:0]
		for _, n := range names {
			if strings.Contains(strings.ToLower(n), term) {
				filtered = append(filtered, n)
			}
		}
		names = filtered
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
		items = append(items, a.boxsetItemDTO(name))
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
	start, _ := strconv.Atoi(c.DefaultQuery("StartIndex", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("Limit", "100"))
	if start < 0 {
		start = 0
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
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
			item["ImageTags"] = gin.H{"Primary": posterTag(poster)}
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"Items": items, "TotalRecordCount": len(names), "StartIndex": start})
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
				item["ImageTags"] = gin.H{"Primary": posterTag(poster)}
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
	b, _ := json.Marshal(a.embyItem(m, d))
	a.cache.Set(key, b, time.Hour)
	c.Data(200, "application/json", b)
}
