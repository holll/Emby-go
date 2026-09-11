package translate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captured 服务端观测到的请求内容（body 只能在这里读，请求结束后原 reader 已关闭）。
type captured struct {
	Path   string
	Auth   string
	Target string
	Texts  []string
}

func newServer(t *testing.T, status int, body string, header map[string]string) (*Client, *captured) {
	t.Helper()
	seen := &captured{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Path = r.URL.Path
		seen.Auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		var payload struct {
			Text       []string `json:"text"`
			TargetLang string   `json:"target_lang"`
		}
		if err := json.Unmarshal(raw, &payload); err == nil {
			seen.Texts, seen.Target = payload.Text, payload.TargetLang
		}
		for key, value := range header {
			w.Header().Set(key, value)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return New(Config{APIURL: ts.URL, APIKey: "gw-token", TargetLang: "ZH", Timeout: 5 * time.Second}), seen
}

func TestTranslateOK(t *testing.T) {
	client, seen := newServer(t, 200,
		`{"translations":[{"detected_source_language":"JA","text":"中文标题"},{"detected_source_language":"JA","text":"中文简介"}]}`,
		map[string]string{"X-Cache-Status": "MISS"})

	response, err := client.Translate(context.Background(), []string{"日本語タイトル", "日本語の説明"})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(response.Texts) != 2 || response.Texts[0] != "中文标题" || response.Texts[1] != "中文简介" {
		t.Fatalf("译文不对: %+v", response)
	}
	if response.DetectedSrc != "JA" || response.CacheStatus != "MISS" {
		t.Errorf("元信息不对: %+v", response)
	}
	if seen.Path != "/v2/translate" {
		t.Errorf("路径 = %q", seen.Path)
	}
	if seen.Auth != "Bearer gw-token" {
		t.Errorf("鉴权头 = %q", seen.Auth)
	}
	if len(seen.Texts) != 2 || seen.Target != "ZH" {
		t.Errorf("请求体不对: %+v", seen)
	}
}

func TestTranslateSkipsWhenNotConfigured(t *testing.T) {
	client := New(Config{})
	if client.cfg.Enabled() {
		t.Error("未配 api_url/api_key 时应视为未启用")
	}
	if _, err := client.Translate(context.Background(), []string{"x"}); err == nil {
		t.Error("未配置时应报错（由调用方决定跳过）")
	}
}

func TestTranslateRejectsEmptySource(t *testing.T) {
	client, _ := newServer(t, 200, `{"translations":[{"text":"x"}]}`, nil)
	if _, err := client.Translate(context.Background(), []string{"   "}); err == nil {
		t.Error("原文为空时应报错而不是发请求")
	}
}

func TestTranslateErrors(t *testing.T) {
	// HTTP 错误：错误信息要带状态码与响应片段，便于定位。
	client, _ := newServer(t, 429, `{"message":"rate limited"}`, nil)
	_, err := client.Translate(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("错误信息不完整: %v", err)
	}

	// 条数不匹配：宁可报错也不要错位落位。
	client2, _ := newServer(t, 200, `{"translations":[{"text":"only-one"}]}`, nil)
	if _, err := client2.Translate(context.Background(), []string{"a", "b"}); err == nil {
		t.Error("译文条数不一致应报错")
	}

	// 空译文：同样视为失败。
	client3, _ := newServer(t, 200, `{"translations":[{"text":"  "}]}`, nil)
	if _, err := client3.Translate(context.Background(), []string{"a"}); err == nil {
		t.Error("空译文应报错")
	}

	// 非法 JSON。
	client4, _ := newServer(t, 200, `not json`, nil)
	if _, err := client4.Translate(context.Background(), []string{"a"}); err == nil {
		t.Error("非法响应应报错")
	}
}

func TestTargetLangDefault(t *testing.T) {
	if got := New(Config{}).TargetLang(); got != "ZH" {
		t.Errorf("默认目标语言 = %q，期望 ZH", got)
	}
	if got := New(Config{TargetLang: "ZH-HANT"}).TargetLang(); got != "ZH-HANT" {
		t.Errorf("应直用 DeepL 码不做映射，得到 %q", got)
	}
}

func TestPing(t *testing.T) {
	client, _ := newServer(t, 200, `{"translations":[{"detected_source_language":"EN","text":"你好"}]}`, nil)
	got, err := client.Ping(context.Background())
	if err != nil || got != "你好" {
		t.Fatalf("Ping = (%q, %v)", got, err)
	}
}
