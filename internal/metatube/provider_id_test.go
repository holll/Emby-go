package metatube

import "testing"

func TestProviderIDRoundTrip(t *testing.T) {
	for _, id := range []string{"abc00123", "a+b", "a%2Bb", "a:b/c?d#e", "中文 空格", "~!*'()"} {
		raw := FormatProviderID("Provider_1", id)
		p, got, ok := ParseProviderID(raw)
		if !ok || p != "Provider_1" || got != id {
			t.Errorf("round trip %q via %q: %q %q %v", id, raw, p, got, ok)
		}
	}
	if got := FormatProviderID("FANZA", "a+b c"); got != "FANZA:a%2Bb%20c" {
		t.Fatal(got)
	}
	for _, raw := range []string{"FANZA:a+b", "FANZA:a%2Bb", "FANZA:a%2Bb:0.5", "FANZA:a%2Bb:0.5:True", "FANZA:a%2Bb::False", "FANZA:a%2Bb::1"} {
		p, id, ok := ParseProviderID(raw)
		if !ok || p != "FANZA" || id != "a+b" {
			t.Errorf("parse %q: %q %q %v", raw, p, id, ok)
		}
	}
	for _, raw := range []string{"", "FANZA", ":id", "FANZA:", "../bad:id", "FANZA:%zz", "FANZA:%00", "FANZA:id:bad", "FANZA:id:NaN", "FANZA:id:1:no", "FANZA:id:1:true:extra"} {
		if _, _, ok := ParseProviderID(raw); ok {
			t.Errorf("accepted malformed %q", raw)
		}
	}
	if FormatProviderID("bad/provider", "id") != "" || FormatProviderID("FANZA", "") != "" {
		t.Fatal("formatted invalid identity")
	}
}

func TestProviderIDSuffixSemantics(t *testing.T) {
	cases := []struct {
		raw      string
		want     string // 规范化后的官方格式（后缀原样保留）
		position *float64
		update   *bool
		lazy     bool
	}{
		{raw: "FANZA:abc00123", want: "FANZA:abc00123", lazy: true},
		{raw: "FANZA:abc00123:0", want: "FANZA:abc00123:0", lazy: true},
		{raw: "FANZA:abc00123:0.50:true", want: "FANZA:abc00123:0.50:true", update: boolPtr(true), lazy: false},
		{raw: "FANZA:abc00123::t", want: "FANZA:abc00123::t", update: boolPtr(true), lazy: false},
		{raw: "FANZA:abc00123:1.5:false", want: "FANZA:abc00123:1.5:false", update: boolPtr(false), lazy: true},
		// 编码 ID 只解码一次再按官方规则编码，后缀不参与解码。
		{raw: "FANZA:abc%2B00123:0:1", want: "FANZA:abc%2B00123:0:1", update: boolPtr(true), lazy: false},
	}
	for _, tc := range cases {
		pid, ok := ParseProviderIDValue(tc.raw)
		if !ok {
			t.Fatalf("rejected %q", tc.raw)
		}
		if got := pid.String(); got != tc.want {
			t.Errorf("String(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if pid.Lazy() != tc.lazy {
			t.Errorf("Lazy(%q) = %v, want %v", tc.raw, pid.Lazy(), tc.lazy)
		}
		if (pid.Update == nil) != (tc.update == nil) || (pid.Update != nil && *pid.Update != *tc.update) {
			t.Errorf("Update(%q) = %v, want %v", tc.raw, pid.Update, tc.update)
		}
	}
	pid, _ := ParseProviderIDValue("FANZA:abc00123:0.25:true")
	if pid.Position == nil || *pid.Position != 0.25 {
		t.Fatalf("position = %v", pid.Position)
	}
	// 身份与选项分离：后缀永远不进 ID。
	if pid.ID != "abc00123" || pid.Provider != "FANZA" {
		t.Fatalf("identity = %q %q", pid.Provider, pid.ID)
	}
	// 未设置的选项不得伪造默认值。
	plain, _ := ParseProviderIDValue("FANZA:abc00123")
	if plain.Position != nil || plain.Update != nil {
		t.Fatalf("omitted suffix must stay nil: %+v", plain)
	}
}

func boolPtr(v bool) *bool { return &v }
