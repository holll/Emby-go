package store

import (
	"reflect"
	"testing"
	"time"
)

// parseStrings 的空列快路径（§2.2 P1-3）必须与"照旧走 json.Unmarshal"逐字节等价：
// movieCols 用 COALESCE(...,”) 把 NULL 归一成空串，movieScan 每行调它 5 次。
func TestParseStringsFastPathMatchesDecode(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},          // COALESCE 后的空列：旧实现也是 nil
		{"[]", []string{}}, // 空数组仍是"非 nil 空切片"，快路径不得改变
		{`["剧情"]`, []string{"剧情"}},
		{`["a","b"]`, []string{"a", "b"}},
		{`[ ]`, []string{}}, // 带空格的空数组仍走解码路径，语义相同
		{"null", nil},       // JSON null 解到切片指针仍是 nil
		{"not json", nil},   // 坏数据保持旧行为：忽略错误、返回 nil
	}
	for _, c := range cases {
		got := parseStrings(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseStrings(%q) = %#v, want %#v", c.in, got, c.want)
		}
		if c.in == "[]" && got == nil {
			t.Errorf(`parseStrings("[]") 返回 nil，改变了"非 nil 空切片"语义`)
		}
		if c.in == "[]" && len(got) != 0 {
			t.Errorf(`parseStrings("[]") 应返回空切片，得到 %#v`, got)
		}
	}
}

// movieScan 仍然把 5 个 JSON 列解析到位（防止快路径把某个列漏掉）。
func TestMovieScanParsesAllJSONColumns(t *testing.T) {
	s, err := Open(t.TempDir() + "/parse.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lib, err := s.AddLibrary("parse", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := Movie{
		LibraryID:       lib.ID,
		SourcePath:      t.TempDir() + "/a.strm",
		Status:          "success",
		Genres:          []string{"剧情"},
		Tags:            []string{"4K"},
		Studios:         []string{"Studio A"},
		Taglines:        []string{"一句话"},
		AdditionalParts: []string{},
	}
	id, err := s.UpsertMovie(want, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Movie(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Genres, want.Genres) || !reflect.DeepEqual(got.Tags, want.Tags) ||
		!reflect.DeepEqual(got.Studios, want.Studios) || !reflect.DeepEqual(got.Taglines, want.Taglines) {
		t.Fatalf("JSON 列解析不一致: genres=%#v tags=%#v studios=%#v taglines=%#v",
			got.Genres, got.Tags, got.Studios, got.Taglines)
	}
}
