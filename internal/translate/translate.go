// Package translate 内嵌的 DeepL 翻译客户端。
//
// 直连自建 Deepl-Proxy（DeepL v2 兼容协议），**不经 MetaTube 转发、不引第三方库**：
//
//	POST {api_url}/v2/translate
//	Authorization: Bearer <key>
//	{"text":["..."],"target_lang":"ZH"}
//	→ {"translations":[{"detected_source_language":"JA","text":"..."}]}
//
// 该服务自带多 Key 轮询熔断与 SQLite 缓存（TTL 365 天，响应头 X-Cache-Status），
// 重复文本不会重复消耗上游配额。
package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout 单次翻译请求超时。
const DefaultTimeout = 30 * time.Second

// Config 翻译配置。APIURL/APIKey 任一为空即视为「未配置」，调用方应跳过翻译。
type Config struct {
	APIURL     string
	APIKey     string
	TargetLang string
	Timeout    time.Duration
}

// Enabled 判断是否具备发起翻译的条件。
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.APIURL) != "" && strings.TrimSpace(c.APIKey) != ""
}

// Client 翻译客户端。
type Client struct {
	cfg  Config
	http *http.Client
}

// New 创建客户端。出网走 http.ProxyFromEnvironment（Go 默认 Transport），
// 回环地址（自建 Deepl-Proxy 常在本机）有内建豁免，不会被误代理。
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: timeout}}
}

// TargetLang 返回生效的目标语言（默认 ZH）。
func (c *Client) TargetLang() string {
	if lang := strings.TrimSpace(c.cfg.TargetLang); lang != "" {
		return lang
	}
	return "ZH"
}

// Response 一次翻译请求的完整结果，便于调用方按需取用。
type Response struct {
	Texts       []string // 按输入顺序的译文
	DetectedSrc string   // 上游识别的源语言（同批取首条）
	CacheStatus string   // Deepl-Proxy 的 X-Cache-Status（HIT/MISS），便于确认缓存效果
}

// Translate 翻译一批文本。texts 为空时直接返回空结果，不发请求。
//
// 刻意不做语言启发式跳过（需求 B7「宁可多翻」）：日文汉字标题会被语言检测误判为中文，
// 按语言跳过的代价是漏翻。是否需要翻译由调用方的开关决定。
func (c *Client) Translate(ctx context.Context, texts []string) (Response, error) {
	if !c.cfg.Enabled() {
		return Response{}, errors.New("translate: 未配置 api_url/api_key")
	}
	payload := make([]string, 0, len(texts))
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			return Response{}, errors.New("translate: 原文为空")
		}
		payload = append(payload, text)
	}
	if len(payload) == 0 {
		return Response{Texts: []string{}}, nil
	}

	body, err := json.Marshal(map[string]any{"text": payload, "target_lang": c.TargetLang()})
	if err != nil {
		return Response{}, err
	}
	target := strings.TrimRight(strings.TrimSpace(c.cfg.APIURL), "/") + "/v2/translate"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))

	response, err := c.http.Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("translate: 请求失败: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return Response{}, fmt.Errorf("translate: 读取响应失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return Response{}, fmt.Errorf("translate: HTTP %d: %s", response.StatusCode, snippet)
	}

	var decoded struct {
		Translations []struct {
			DetectedSourceLanguage string `json:"detected_source_language"`
			Text                   string `json:"text"`
		} `json:"translations"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Response{}, fmt.Errorf("translate: 解析响应失败: %w", err)
	}
	if len(decoded.Translations) != len(payload) {
		return Response{}, fmt.Errorf("translate: 译文条数 %d 与原文条数 %d 不一致",
			len(decoded.Translations), len(payload))
	}
	out := Response{
		Texts:       make([]string, 0, len(decoded.Translations)),
		CacheStatus: response.Header.Get("X-Cache-Status"),
	}
	for i, item := range decoded.Translations {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			// 单条空译文视为失败：静默写空会把原文顶掉。
			return Response{}, fmt.Errorf("translate: 第 %d 条译文为空", i+1)
		}
		out.Texts = append(out.Texts, text)
		if out.DetectedSrc == "" {
			out.DetectedSrc = item.DetectedSourceLanguage
		}
	}
	return out, nil
}

// Ping 打一次最小翻译，供管理端「测试连接」按钮使用。
func (c *Client) Ping(ctx context.Context) (string, error) {
	response, err := c.Translate(ctx, []string{"hello"})
	if err != nil {
		return "", err
	}
	if len(response.Texts) == 0 {
		return "", errors.New("translate: 无译文返回")
	}
	return response.Texts[0], nil
}
