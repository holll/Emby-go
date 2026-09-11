package server

import "testing"

func TestQuietPath(t *testing.T) {
	cases := []struct {
		path  string
		quiet bool
	}{
		{"/web/app.js", true},
		{"/web/vendor/artplayer.min.js", true},
		{"/favicon.ico", true},
		{"/Items/abc/Images/Primary", true},
		{"/emby/Items/abc/Images/Primary/0", true},
		{"/Videos/abc/stream", true},
		{"/Videos/abc/stream.mkv", true},
		{"/emby/videos/abc/proxy", true},
		{"/audio/abc/stream", true},

		// 需要保留记录的接口
		{"/System/Info/Public", false},
		{"/Users/AuthenticateByName", false},
		{"/api/admin/items", false},
		{"/", false},
		{"/Items/abc/PlaybackInfo", false},
		{"/Users/uid/Items", false},
		{"/web", false}, // 管理端首页本体，非静态资源
	}
	for _, tc := range cases {
		if got := quietPath(tc.path); got != tc.quiet {
			t.Errorf("quietPath(%q) = %v，期望 %v", tc.path, got, tc.quiet)
		}
	}
}
