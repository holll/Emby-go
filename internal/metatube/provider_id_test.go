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
