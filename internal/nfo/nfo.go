package nfo

import (
	"bytes"
	"encoding/xml"
	"errors"
	"os"
	"strconv"
	"strings"
)

// Actor 对应 <actor>：Thumb 是头像的远端地址，头像真源就在这里——
// 删库重建后靠它重新下载本地副本（见需求 A7 #44）。
type Actor struct {
	Name string `xml:"name"`
	// 可选字段带 omitempty：新建 NFO 时不该出现空的 <thumb></thumb>
	//（空 thumb 与「没有 thumb」在读取侧等价，但空标签会让人误以为刮过而失败）。
	Type       string `xml:"type,omitempty"`
	MetaTubeID string `xml:"metatubeid,omitempty"`
	Thumb      string `xml:"thumb,omitempty"`
}

// MovieSet 对应 Kodi/Emby 的 <set><name>：合集（BoxSet）归属。
type MovieSet struct {
	Name string `xml:"name"`
}

// UniqueID 对应 <uniqueid type="metatube|trailerurl">值</uniqueid>。
type UniqueID struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// VideoStream / AudioStream 对应 <fileinfo><streamdetails> 下的音视频轨信息。
// Profile/Level/PixelFormat/BitDepth/RefFrames 等在 Kodi 之外的实现里用于还原
// Emby MediaStream 的技术参数，缺失时留空/为零即可。
type VideoStream struct {
	Codec           string  `xml:"codec"`
	CodecTag        string  `xml:"micodec"`
	Profile         string  `xml:"profile"`
	Level           int     `xml:"level"`
	PixelFormat     string  `xml:"pixelformat"`
	BitDepth        int     `xml:"bitdepth"`
	RefFrames       int     `xml:"reframes"`
	ColorTransfer   string  `xml:"colortransfer"`
	ColorPrimaries  string  `xml:"colorprimaries"`
	ColorSpace      string  `xml:"colorspace"`
	ColorRange      string  `xml:"colorrange"`
	Bitrate         int64   `xml:"bitrate"`
	Width           int     `xml:"width"`
	Height          int     `xml:"height"`
	AspectRatio     string  `xml:"aspectratio"`
	Framerate       float64 `xml:"framerate"`
	Language        string  `xml:"language"`
	DurationMinutes int     `xml:"duration"`
	DurationSeconds int64   `xml:"durationinseconds"`
	ScanType        string  `xml:"scantype"`
	Default         string  `xml:"default"`
	Forced          string  `xml:"forced"`
}

// AudioStream 对应 <audio> 音轨信息。
type AudioStream struct {
	Codec         string `xml:"codec"`
	CodecTag      string `xml:"micodec"`
	Profile       string `xml:"profile"`
	ChannelLayout string `xml:"channellayout"`
	Bitrate       int64  `xml:"bitrate"`
	Language      string `xml:"language"`
	Channels      int    `xml:"channels"`
	SamplingRate  int    `xml:"samplingrate"`
	Default       string `xml:"default"`
	Forced        string `xml:"forced"`
}

// SubtitleStream 对应 <subtitle> 字幕轨。
type SubtitleStream struct {
	Codec           string `xml:"codec"`
	CodecTag        string `xml:"micodec"`
	Language        string `xml:"language"`
	Title           string `xml:"title"`
	Default         string `xml:"default"`
	Forced          string `xml:"forced"`
	HearingImpaired string `xml:"hearingimpaired"`
	External        string `xml:"external"`
}

type StreamDetails struct {
	Video     *VideoStream     `xml:"video,omitempty"`
	Audio     *AudioStream     `xml:"audio,omitempty"`
	Subtitles []SubtitleStream `xml:"subtitle,omitempty"`
}

type FileInfo struct {
	// Size 是媒体文件的字节数，对应 Emby 的 BaseItemDto/MediaSourceInfo.Size。
	// Kodi/Emby 自身的 NFO 都不写这一项（Emby 把体积存在自己的库里），
	// 本服务以 NFO 为真源，故在 <fileinfo> 下用 <size> 承载；对其它工具是未知元素，会被忽略。
	Size int64 `xml:"size,omitempty"`
	// ProbeVersion 记录本服务写入该 <fileinfo> 时的探测版本。
	// 用于在全库探测时精确判定「是否已探测过」——不能只看 <streamdetails> 是否存在，
	// 因为刮削器也会写 streamdetails（且常常不完整），那样会把该补齐的条目永久跳过。
	// 0（缺省）表示未经本服务探测；探测逻辑新增字段时提升版本即可让旧条目自动重探。
	ProbeVersion int `xml:"probeversion,omitempty"`
	// ProbeURL 记录探测时的直链。.strm 换源后（换片源、修失效链接）旧参数即失效，
	// 必须凭它识别出来并重探，否则会一直沿用错误的分辨率/码率。
	// 空值表示该条目由旧版本探测写入、未记录直链，此时仅凭 ProbeVersion 判定。
	ProbeURL      string         `xml:"probeurl,omitempty"`
	StreamDetails *StreamDetails `xml:"streamdetails,omitempty"`
}

// FileInfoMeta 写入 <fileinfo> 的探测元信息。
type FileInfoMeta struct {
	Size         int64
	ProbeVersion int
	ProbeURL     string
}

// MovieMeta 既是 NFO 的解析目标，也是整体重写（Save/SaveAtomic）的输出源。
//
// 可选字段一律带 omitempty：整体重写新文件时不应写出 `<outline></outline>` 这类空标签——
// 空 <lockdata>、空 <outline> 会被其它工具当成「显式写了空值」，也让文件难以人工核对。
// 刮削存量文件走的是 internal/nfo/update.go 的标签级更新器，不受这里影响。
type MovieMeta struct {
	Number        string     `xml:"num,omitempty"`
	Title         string     `xml:"title,omitempty"`
	OriginalTitle string     `xml:"originaltitle,omitempty"`
	Plot          string     `xml:"plot,omitempty"`
	Outline       string     `xml:"outline,omitempty"`
	Year          int        `xml:"year,omitempty"`
	Premiered     string     `xml:"premiered,omitempty"`
	ReleaseDate   string     `xml:"releasedate,omitempty"`
	DateAdded     string     `xml:"dateadded,omitempty"`
	Rating        float64    `xml:"rating,omitempty"`
	Mpaa          string     `xml:"mpaa,omitempty"`
	SortTitle     string     `xml:"sorttitle,omitempty"`
	Director      string     `xml:"director,omitempty"`
	Series        string     `xml:"series,omitempty"`
	Maker         string     `xml:"maker,omitempty"`
	Label         string     `xml:"label,omitempty"`
	LockData      string     `xml:"lockdata,omitempty"`
	MetaTubeID    string     `xml:"metatubeid,omitempty"`
	TrailerURLID  string     `xml:"trailerurlid,omitempty"`
	Set           *MovieSet  `xml:"set,omitempty"`
	Genres        []string   `xml:"genre,omitempty"`
	Tags          []string   `xml:"tag,omitempty"`
	Studios       []string   `xml:"studio,omitempty"`
	Taglines      []string   `xml:"tagline,omitempty"`
	UniqueIDs     []UniqueID `xml:"uniqueid,omitempty"`
	Runtime       int64      `xml:"runtime,omitempty"`
	FileInfo      *FileInfo  `xml:"fileinfo,omitempty"`
	Actors        []Actor    `xml:"actor,omitempty"`
}

// Collection 返回 <set><name> 的合集名（去空白），无 set 时为空串。
func (m MovieMeta) Collection() string {
	if m.Set == nil {
		return ""
	}
	return strings.TrimSpace(m.Set.Name)
}

// uniqueID 按 type 取值（大小写不敏感），找不到返回空串。
func (m MovieMeta) uniqueID(typ string) string {
	for _, id := range m.UniqueIDs {
		if strings.EqualFold(strings.TrimSpace(id.Type), typ) {
			return strings.TrimSpace(id.Value)
		}
	}
	return ""
}

// ProviderID 返回刮削源 ID（uniqueid type=metatube，回退顶层 <metatubeid>）。
func (m MovieMeta) ProviderID() string {
	if v := m.uniqueID("metatube"); v != "" {
		return v
	}
	return strings.TrimSpace(m.MetaTubeID)
}

// TrailerURL 返回预告片地址（uniqueid type=trailerurl，回退顶层 <trailerurlid>）。
func (m MovieMeta) TrailerURL() string {
	if v := m.uniqueID("trailerurl"); v != "" {
		return v
	}
	return strings.TrimSpace(m.TrailerURLID)
}

// TaglineList 返回去空白的标语列表。
func (m MovieMeta) TaglineList() []string {
	var out []string
	for _, t := range m.Taglines {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// RuntimeSeconds 返回秒为单位的片长：
// <runtime>（分钟）→ fileinfo 的 durationinseconds → video duration（分钟）。
func (m MovieMeta) RuntimeSeconds() int64 {
	if m.Runtime > 0 {
		return m.Runtime * 60
	}
	if m.FileInfo != nil && m.FileInfo.StreamDetails != nil && m.FileInfo.StreamDetails.Video != nil {
		if v := m.FileInfo.StreamDetails.Video.DurationSeconds; v > 0 {
			return v
		}
		if v := int64(m.FileInfo.StreamDetails.Video.DurationMinutes); v > 0 {
			return v * 60
		}
	}
	return 0
}

func Read(path string) (MovieMeta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return MovieMeta{}, err
	}
	var m MovieMeta
	if err := xml.Unmarshal(b, &m); err != nil {
		return MovieMeta{}, err
	}
	return m, nil
}
func Save(path string, m MovieMeta) error {
	b, err := xml.MarshalIndent(struct {
		XMLName xml.Name `xml:"movie"`
		MovieMeta
	}{MovieMeta: m}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), b...), 0644)
}
func FromFields(title string, year int) MovieMeta { return MovieMeta{Title: title, Year: year} }
func SaveAtomic(path string, m MovieMeta) error {
	tmp := path + ".tmp"
	if err := Save(tmp, m); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SaveFileInfo 写入 NFO 的 <fileinfo> 块（含媒体体积、探测版本与 <streamdetails>）；
// 已存在 <fileinfo> 则整块替换，不存在则插到 </movie> 之前。
//
// 这里刻意不做「读成 MovieMeta → 整体重写」：NFO 是元数据真源，整文件重写会把
// 结构体未建模的元素（刮削器写入的扩展标签、属性等）静默丢掉。
// 因此只做块级替换，其余内容字节级保留（含文件头 BOM 与原有缩进风格）。
func SaveFileInfo(path string, meta FileInfoMeta, details *StreamDetails) error {
	if details == nil {
		return errors.New("nfo: 流信息为空")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	block := renderFileInfo(meta, details)
	text := string(raw)
	if start := strings.Index(text, "<fileinfo"); start >= 0 {
		if rel := strings.Index(text[start:], "</fileinfo>"); rel >= 0 {
			end := start + rel + len("</fileinfo>")
			// 匹配从 "<fileinfo" 起，不含它所在行的缩进；若不一并吃掉，
			// 新块自带的缩进会与残留缩进叠加（<fileinfo> 被顶到 4 空格）。
			lineStart := start
			for lineStart > 0 && (text[lineStart-1] == ' ' || text[lineStart-1] == '\t') {
				lineStart--
			}
			return writeFileAtomic(path, text[:lineStart]+block+text[end:])
		}
	}
	index := strings.LastIndex(text, "</movie>")
	if index < 0 {
		return errors.New("nfo: 未找到 </movie>，无法写入流信息")
	}
	return writeFileAtomic(path, text[:index]+block+"\n"+text[index:])
}

// renderFileInfo 按 Metatube/Kodi 的既有缩进风格（2 空格逐层）渲染 <fileinfo> 块。
func renderFileInfo(meta FileInfoMeta, details *StreamDetails) string {
	var builder strings.Builder
	builder.WriteString("  <fileinfo>\n")
	writeNumber(&builder, 4, "size", meta.Size)
	writeNumber(&builder, 4, "probeversion", int64(meta.ProbeVersion))
	writeValue(&builder, 4, "probeurl", meta.ProbeURL)
	builder.WriteString("    <streamdetails>\n")
	if video := details.Video; video != nil {
		builder.WriteString("      <video>\n")
		writeValue(&builder, 8, "codec", video.Codec)
		writeValue(&builder, 8, "micodec", video.CodecTag)
		writeValue(&builder, 8, "profile", video.Profile)
		writeNumber(&builder, 8, "level", int64(video.Level))
		writeValue(&builder, 8, "pixelformat", video.PixelFormat)
		writeNumber(&builder, 8, "bitdepth", int64(video.BitDepth))
		writeNumber(&builder, 8, "reframes", int64(video.RefFrames))
		writeValue(&builder, 8, "colortransfer", video.ColorTransfer)
		writeValue(&builder, 8, "colorprimaries", video.ColorPrimaries)
		writeValue(&builder, 8, "colorspace", video.ColorSpace)
		writeValue(&builder, 8, "colorrange", video.ColorRange)
		writeNumber(&builder, 8, "bitrate", video.Bitrate)
		writeNumber(&builder, 8, "width", int64(video.Width))
		writeNumber(&builder, 8, "height", int64(video.Height))
		writeValue(&builder, 8, "aspectratio", video.AspectRatio)
		writeValue(&builder, 8, "aspect", video.AspectRatio)
		writeDecimal(&builder, 8, "framerate", video.Framerate)
		writeValue(&builder, 8, "language", video.Language)
		writeValue(&builder, 8, "scantype", video.ScanType)
		writeValue(&builder, 8, "default", video.Default)
		writeValue(&builder, 8, "forced", video.Forced)
		writeNumber(&builder, 8, "duration", int64(video.DurationMinutes))
		writeNumber(&builder, 8, "durationinseconds", video.DurationSeconds)
		builder.WriteString("      </video>\n")
	}
	if audio := details.Audio; audio != nil {
		builder.WriteString("      <audio>\n")
		writeValue(&builder, 8, "codec", audio.Codec)
		writeValue(&builder, 8, "micodec", audio.CodecTag)
		writeValue(&builder, 8, "profile", audio.Profile)
		writeNumber(&builder, 8, "bitrate", audio.Bitrate)
		writeValue(&builder, 8, "language", audio.Language)
		writeValue(&builder, 8, "channellayout", audio.ChannelLayout)
		writeNumber(&builder, 8, "channels", int64(audio.Channels))
		writeNumber(&builder, 8, "samplingrate", int64(audio.SamplingRate))
		writeValue(&builder, 8, "default", audio.Default)
		writeValue(&builder, 8, "forced", audio.Forced)
		builder.WriteString("      </audio>\n")
	}
	for _, subtitle := range details.Subtitles {
		builder.WriteString("      <subtitle>\n")
		writeValue(&builder, 8, "codec", subtitle.Codec)
		writeValue(&builder, 8, "micodec", subtitle.CodecTag)
		writeValue(&builder, 8, "language", subtitle.Language)
		writeValue(&builder, 8, "title", subtitle.Title)
		writeValue(&builder, 8, "default", subtitle.Default)
		writeValue(&builder, 8, "forced", subtitle.Forced)
		writeValue(&builder, 8, "hearingimpaired", subtitle.HearingImpaired)
		writeValue(&builder, 8, "external", subtitle.External)
		builder.WriteString("      </subtitle>\n")
	}
	builder.WriteString("    </streamdetails>\n  </fileinfo>")
	return builder.String()
}

// writeValue 写入字符串元素；空值跳过（不写空标签，保持与刮削器输出一致）。
func writeValue(builder *strings.Builder, indent int, name, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	builder.WriteString(strings.Repeat(" ", indent))
	builder.WriteString("<" + name + ">" + escaped.String() + "</" + name + ">\n")
}

// writeNumber 写入整数元素；非正值跳过（0 会被误读为真实取值）。
func writeNumber(builder *strings.Builder, indent int, name string, value int64) {
	if value <= 0 {
		return
	}
	builder.WriteString(strings.Repeat(" ", indent))
	builder.WriteString("<" + name + ">" + strconv.FormatInt(value, 10) + "</" + name + ">\n")
}

// writeDecimal 写入小数元素（帧率保留 5 位，与 Metatube 的 29.97003 风格一致）。
func writeDecimal(builder *strings.Builder, indent int, name string, value float64) {
	if value <= 0 {
		return
	}
	builder.WriteString(strings.Repeat(" ", indent))
	builder.WriteString("<" + name + ">" + strconv.FormatFloat(value, 'f', 5, 64) + "</" + name + ">\n")
}

// writeFileAtomic 先写临时文件再改名，避免中途失败留下半截 NFO。
func writeFileAtomic(path string, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
