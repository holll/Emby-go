package metatube

import "testing"

// Ported 行为的回归用例：覆盖常见文件名噪声，确保归一化后能命中真实番号。
func TestTrimNormalizesFilenames(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ABF-018", "ABF-018"},
		{"ABF-018.mp4", "ABF-018"},
		{"[FHD] ABF-018 uncensored.mp4", "ABF-018"},
		{"ABF-018-C.mp4", "ABF-018"},
		{"FC2-PPV-1234567.mp4", "FC2-1234567"},
		{"fc2ppv_1234567.mp4", "FC2-1234567"},
		{"SSIS001.mp4", "SSIS001"},
		// 归一化取的是「第一个带分隔符的编号段」，前置的 1080p 不会被单独剥离——
		// 这是上游既有行为，本项目逐条照搬（服务端搜索用同一套逻辑，结果一致）。
		{"1080p_HD_SSIS-001_4K.mkv", "1080p_SSIS-001"},
		{"Tokyo-Hot-n1234.mp4", "n1234"},
	}
	for _, tc := range cases {
		if got := Trim(tc.in); got != tc.want {
			t.Errorf("Trim(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeAndSameNumber(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"ABF-018", "ABF-018", true},
		{"abf-018", "ABF_018", true},
		{"ABF018", "ABF-018", true},
		{"ABF-018-c", "ABF-018", false}, // 未归一化的噪声不参与比较
		{"ABF-018", "ABF-019", false},
		{"", "ABF-018", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := SameNumber(tc.a, tc.b); got != tc.same {
			t.Errorf("SameNumber(%q, %q) = %v，期望 %v", tc.a, tc.b, got, tc.same)
		}
	}
}

// 归一化 + 比较连起来用：文件名里的番号要能和接口返回的番号对上。
func TestTrimThenSameNumber(t *testing.T) {
	if !SameNumber(Trim("[FHD] ABF-018 uncensored.mp4"), "ABF-018") {
		t.Error("从文件名提取的番号应能与接口番号匹配")
	}
	if SameNumber(Trim("完全无关的文件名.mp4"), "ABF-018") {
		t.Error("无关文件名不应误判为命中")
	}
}
