package nfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveFileInfoOnlyDirectMovieElement(t *testing.T) {
	meta := FileInfoMeta{Size: 123, ProbeVersion: 1}
	block := renderFileInfo(meta, sampleDetails())
	tests := []struct {
		name, before, target string
	}{
		{"nested extension", "<movie>\n<vendor><fileinfo><size>7</size></fileinfo></vendor>\n</movie>", ""},
		{"comment", "<movie>\n<!-- <fileinfo><size>7</size></fileinfo> -->\n</movie>", ""},
		{"cdata", "<movie>\n<plot><![CDATA[<fileinfo>leave alone</fileinfo>]]></plot>\n</movie>", ""},
		{"namespace", `<movie xmlns:v="urn:vendor"><v:fileinfo><v:size>7</v:size></v:fileinfo></movie>`, ""},
		{"default namespace extension", `<movie><fileinfo xmlns="urn:vendor"><size>7</size></fileinfo></movie>`, ""},
		{"self closing", "<movie>\n  <fileinfo/>\n</movie>", "  <fileinfo/>"},
		{"direct after nested", "<movie>\n<vendor><fileinfo><size>7</size></fileinfo></vendor>\n  <fileinfo><size>9</size></fileinfo>\n</movie>", "  <fileinfo><size>9</size></fileinfo>"},
		{"direct after comment", "<movie>\n<!-- <fileinfo>leave alone</fileinfo> -->\n  <fileinfo><size>9</size></fileinfo>\n</movie>", "  <fileinfo><size>9</size></fileinfo>"},
		{"similarly named extension", "<movie><fileinformation><fileinfo>keep</fileinfo></fileinformation>\n</movie>", ""},
		{"trailing closing marker comment", "<movie><title>T</title></movie>\n<!-- </movie> -->", ""},
		{"outside xml whitespace and pi", " \t\r\n<?vendor keep?>\n<!--before--><movie><fileinfo/></movie>\r\n<?vendor after?><!--after-->\t", "<fileinfo/>"},
		{"bom crlf attributes", "\ufeff<?xml version=\"1.0\" encoding=\"utf-8\"?>\r\n<movie a=\"keep\">\r\n  <fileinfo a=\"old\"/>\r\n  <vendor attr=\"&amp;\"/>\r\n</movie>", "  <fileinfo a=\"old\"/>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "movie.nfo")
			if err := os.WriteFile(path, []byte(tt.before), 0644); err != nil {
				t.Fatal(err)
			}
			if err := SaveFileInfo(path, meta, sampleDetails()); err != nil {
				t.Fatal(err)
			}
			want := ""
			if tt.target == "" {
				want = strings.Replace(tt.before, "</movie>", block+"\n</movie>", 1)
			} else {
				want = strings.Replace(tt.before, tt.target, block, 1)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != want {
				t.Fatalf("unexpected edit outside direct fileinfo\ngot: %s\nwant: %s", after, want)
			}
			parsed, err := Read(path)
			if err != nil || parsed.FileInfo == nil || parsed.FileInfo.Size != meta.Size {
				t.Fatalf("new fileinfo not readable: %+v, %v", parsed.FileInfo, err)
			}
			// A second write replaces the same node, rather than accumulating blocks.
			if err := SaveFileInfo(path, meta, sampleDetails()); err != nil {
				t.Fatal(err)
			}
			again, err := os.ReadFile(path)
			if err != nil || string(again) != want {
				t.Fatalf("repeated write changed preserved bytes: %v", err)
			}
		})
	}
}

func TestSaveFileInfoRejectsInvalidXMLWithoutWriting(t *testing.T) {
	for _, original := range []string{
		`<movie><fileinfo><size>7</size></fileinfo>`,
		`<movie><fileinfo/></movie><movie/>`,
		`<movie><fileinfo></movie>`,
		`<tvshow><fileinfo><size>7</size></fileinfo></tvshow>`,
		`<movie xmlns="urn:vendor"><fileinfo/></movie>`,
		`<movie/>`,
		`<movie><!-- </movie> -->`,
		`LEADING_GARBAGE<movie><fileinfo><size>7</size></fileinfo></movie>`,
		`<movie><fileinfo><size>7</size></fileinfo></movie>TRAILING_GARBAGE`,
		"\u00a0<movie><fileinfo/></movie>",
		"<movie><fileinfo/></movie>\ufeff",
		`<![CDATA[]]><movie><fileinfo/></movie>`,
		`<movie><fileinfo/></movie><![CDATA[ ]]>`,
	} {
		t.Run(original, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "movie.nfo")
			if err := os.WriteFile(path, []byte(original), 0644); err != nil {
				t.Fatal(err)
			}
			if err := SaveFileInfo(path, FileInfoMeta{Size: 123}, sampleDetails()); err == nil {
				t.Fatal("invalid document accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != original {
				t.Fatalf("rejected document changed: %v", err)
			}
			if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
				t.Fatalf("unexpected temporary file: %v", err)
			}
		})
	}
}
