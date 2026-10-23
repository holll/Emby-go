package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestSanitizeTextRedactsCredentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		bad  []string
		want string
	}{
		{"query", `GET https://media.test/a.mp4?api_key=CANARY_KEY&sig=CANARY_SIG`, []string{"CANARY_KEY", "CANARY_SIG"}, "?[REDACTED]"},
		{"quoted", `password="CANARY PASS WORD" user=bob`, []string{"CANARY PASS WORD"}, `password=[REDACTED]`},
		{"kv", `token=CANARY_TOKEN api_key=CANARY_API`, []string{"CANARY_TOKEN", "CANARY_API"}, "[REDACTED]"},
		{"bearer", `Authorization: Bearer CANARY_BEARER`, []string{"CANARY_BEARER"}, "[REDACTED]"},
		{"newline", "line1\npassword=CANARY_NL\nline2", []string{"CANARY_NL"}, "[REDACTED]"},
		{"userinfo", `https://bob:CANARY_PW@media.test/a.mp4`, []string{"CANARY_PW"}, "[REDACTED]@"},
	}
	for _, tc := range cases {
		got := SanitizeText(tc.in)
		for _, bad := range tc.bad {
			if strings.Contains(got, bad) {
				t.Fatalf("%s leaked %s: %s", tc.name, bad, got)
			}
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%s missing %q: %s", tc.name, tc.want, got)
		}
	}
	if got := SanitizeText("扫描完成 success=12 pending=0"); got != "扫描完成 success=12 pending=0" {
		t.Fatalf("benign text changed: %s", got)
	}
}

func TestSanitizingHandlerCoversAttrsGroupsAndMessage(t *testing.T) {
	var buf bytes.Buffer
	h := newSanitizingHandler(slog.NewTextHandler(&buf, nil))
	logger := slog.New(h).With("component", "probe", "token", "CANARY_WITH_TOKEN")
	logger.LogAttrs(context.Background(), slog.LevelWarn, "probe failed url=https://media.test/x?token=CANARY_MSG",
		slog.String("password", "CANARY_PW"),
		slog.String("safe_error", "connect timeout"),
		slog.Group("nested", slog.String("api_key", "CANARY_NESTED"), slog.Int("count", 3)),
		slog.String("stderr", "CANARY_STDERR"),
	)
	out := buf.String()
	for _, bad := range []string{"CANARY_WITH_TOKEN", "CANARY_MSG", "CANARY_PW", "CANARY_NESTED", "CANARY_STDERR"} {
		if strings.Contains(out, bad) {
			t.Fatalf("handler leaked %s: %s", bad, out)
		}
	}
	for _, want := range []string{"component=probe", "safe_error=\"connect timeout\"", "count=3", "?[REDACTED]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("handler lost %q: %s", want, out)
		}
	}
}

func TestRingWriterUsesSanitizer(t *testing.T) {
	r := NewRing()
	w := &RingWriter{ring: r}
	if _, err := w.Write([]byte("level=INFO msg=\"login\" token=CANARY_RING\n")); err != nil {
		t.Fatal(err)
	}
	entries := r.Snapshot(0)
	if len(entries) != 1 || strings.Contains(entries[0].Message, "CANARY_RING") {
		t.Fatalf("ring leaked: %+v", entries)
	}
}
