package nfo

import (
	"encoding/xml"
	"os"
	"strconv"
	"strings"
)

type Actor struct {
	Name       string `xml:"name"`
	Type       string `xml:"type"`
	MetaTubeID string `xml:"metatubeid"`
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
type VideoStream struct {
	Codec           string  `xml:"codec"`
	CodecTag        string  `xml:"micodec"`
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
	Codec        string `xml:"codec"`
	CodecTag     string `xml:"micodec"`
	Bitrate      int64  `xml:"bitrate"`
	Language     string `xml:"language"`
	Channels     int    `xml:"channels"`
	SamplingRate int    `xml:"samplingrate"`
	Default      string `xml:"default"`
	Forced       string `xml:"forced"`
}

type StreamDetails struct {
	Video *VideoStream `xml:"video,omitempty"`
	Audio *AudioStream `xml:"audio,omitempty"`
}

type FileInfo struct {
	StreamDetails *StreamDetails `xml:"streamdetails,omitempty"`
}

type MovieMeta struct {
	Number        string     `xml:"num"`
	Title         string     `xml:"title"`
	OriginalTitle string     `xml:"originaltitle"`
	Plot          string     `xml:"plot"`
	Outline       string     `xml:"outline"`
	Year          int        `xml:"year"`
	Premiered     string     `xml:"premiered"`
	ReleaseDate   string     `xml:"releasedate"`
	DateAdded     string     `xml:"dateadded"`
	Rating        float64    `xml:"rating"`
	Mpaa          string     `xml:"mpaa"`
	SortTitle     string     `xml:"sorttitle"`
	Director      string     `xml:"director"`
	Series        string     `xml:"series"`
	Maker         string     `xml:"maker"`
	Label         string     `xml:"label"`
	LockData      string     `xml:"lockdata"`
	MetaTubeID    string     `xml:"metatubeid"`
	TrailerURLID  string     `xml:"trailerurlid"`
	Set           *MovieSet  `xml:"set,omitempty"`
	Genres        []string   `xml:"genre"`
	Tags          []string   `xml:"tag"`
	Studios       []string   `xml:"studio"`
	Taglines      []string   `xml:"tagline"`
	UniqueIDs     []UniqueID `xml:"uniqueid"`
	Runtime       int64      `xml:"runtime"`
	FileInfo      *FileInfo  `xml:"fileinfo,omitempty"`
	Actors        []Actor    `xml:"actor"`
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
func ParseRuntime(v string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64); return n }
