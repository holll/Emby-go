// Package metatube 是 MetaTube 后端的 HTTP 客户端。
//
// 只做接口适配，不含业务编排（搜索选哪个结果、写哪个 NFO 由 internal/scraper 决定）。
// 契约以 src/metatube-sdk-go 的 route 实现为准：
//
//	GET /v1/movies/search?q=&provider=&fallback=   （需鉴权）
//	GET /v1/movies/:provider/:id?lazy=true         （需鉴权）
//	GET /v1/actors/search?q=&provider=&fallback=   （需鉴权）
//	GET /v1/images/{primary|thumb|backdrop}/:provider/:id?quality=90  （公开，无需鉴权）
//
// 响应统一为 {"data": ...} / {"error":{"code":N,"message":"..."}}。
package metatube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout 单次请求超时。探测/刮削跑在后台任务里，宁可早失败也不要挂死。
const DefaultTimeout = 30 * time.Second

// Client MetaTube 后端客户端。
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New 创建客户端。baseURL 形如 https://metatube.example.com；超时 <=0 用默认值。
// 出网走 http.ProxyFromEnvironment（Go 的默认 Transport 行为），
// 尊重 HTTP_PROXY/NO_PROXY，回环地址有内建豁免。
func New(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: timeout},
	}
}

// BaseURL 返回服务地址（供管理端展示与连通性测试）。
func (c *Client) BaseURL() string { return c.baseURL }

// MovieSearchResult 搜索结果（MetaTube 的 MovieSearchResult 子集）。
type MovieSearchResult struct {
	ID          string   `json:"id"`
	Number      string   `json:"number"`
	Title       string   `json:"title"`
	Provider    string   `json:"provider"`
	Homepage    string   `json:"homepage"`
	ThumbURL    string   `json:"thumb_url"`
	CoverURL    string   `json:"cover_url"`
	Score       float64  `json:"score"`
	Actors      []string `json:"actors,omitempty"`
	ReleaseDate string   `json:"release_date"`
}

// MovieInfo 影片详情（MetaTube 的 MovieInfo 子集，只保留写 NFO 需要的字段）。
type MovieInfo struct {
	ID          string   `json:"id"`
	Number      string   `json:"number"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Provider    string   `json:"provider"`
	Homepage    string   `json:"homepage"`
	Director    string   `json:"director"`
	Actors      []string `json:"actors"`
	ThumbURL    string   `json:"thumb_url"`
	BigThumbURL string   `json:"big_thumb_url"`
	CoverURL    string   `json:"cover_url"`
	BigCoverURL string   `json:"big_cover_url"`
	Maker       string   `json:"maker"`
	Label       string   `json:"label"`
	Series      string   `json:"series"`
	Genres      []string `json:"genres"`
	Score       float64  `json:"score"`
	Runtime     int      `json:"runtime"`
	ReleaseDate string   `json:"release_date"`
}

// ActorSearchResult 演员搜索结果（Images 首张即头像）。
type ActorSearchResult struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Homepage string   `json:"homepage"`
	Aliases  []string `json:"aliases,omitempty"`
	Images   []string `json:"images"`
}

// APIError MetaTube 返回的结构化错误。
type APIError struct {
	StatusCode int    `json:"-"`
	Code       int    `json:"code"`
	Message    string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("metatube: HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("metatube: HTTP %d", e.StatusCode)
}

// IsNotFound 判断是否为「没有结果」（搜索无命中时后端返回 404）。
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound || apiErr.Code == http.StatusNotFound
	}
	return false
}

// SearchMovies 搜索影片。provider 为空时后端会搜全部来源。
func (c *Client) SearchMovies(ctx context.Context, q, provider string, fallback bool) ([]MovieSearchResult, error) {
	var out []MovieSearchResult
	err := c.get(ctx, "/v1/movies/search", url.Values{
		"q":        {q},
		"provider": {provider},
		"fallback": {strconv.FormatBool(fallback)},
	}, &out)
	if IsNotFound(err) {
		return nil, nil
	}
	return out, err
}

// MovieInfo 取影片详情。lazy=true 时后端优先回本地库（快，且不依赖上游可用性）。
func (c *Client) MovieInfo(ctx context.Context, provider, id string, lazy bool) (MovieInfo, error) {
	var out MovieInfo
	path := "/v1/movies/" + url.PathEscape(provider) + "/" + url.PathEscape(id)
	err := c.get(ctx, path, url.Values{"lazy": {strconv.FormatBool(lazy)}}, &out)
	return out, err
}

// SearchActors 搜索演员（姓名/别名匹配用）。
func (c *Client) SearchActors(ctx context.Context, q, provider string, fallback bool) ([]ActorSearchResult, error) {
	var out []ActorSearchResult
	err := c.get(ctx, "/v1/actors/search", url.Values{
		"q":        {q},
		"provider": {provider},
		"fallback": {strconv.FormatBool(fallback)},
	}, &out)
	if IsNotFound(err) {
		return nil, nil
	}
	return out, err
}

// ImageURL 拼出图片地址。图片端点是公开的（不需要鉴权），由后端裁剪到指定比例。
func (c *Client) ImageURL(kind, provider, id string, quality int, remoteURL string) string {
	query := url.Values{}
	if quality > 0 {
		query.Set("quality", strconv.Itoa(quality))
	}
	if strings.TrimSpace(remoteURL) != "" {
		query.Set("url", remoteURL)
	}
	target := c.baseURL + "/v1/images/" + url.PathEscape(kind) + "/" +
		url.PathEscape(provider) + "/" + url.PathEscape(id)
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	return target
}

// Download 拉取任意绝对 URL（用于下载图片）。limit 为读取上限，<=0 时不限。
func (c *Client) Download(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &APIError{StatusCode: response.StatusCode, Code: response.StatusCode,
			Message: "下载图片失败: " + response.Status}
	}
	reader := io.Reader(response.Body)
	if limit > 0 {
		reader = io.LimitReader(response.Body, limit)
	}
	return io.ReadAll(reader)
}

// Health 探测后端是否可达（GET / 返回应用信息，无需鉴权）。
func (c *Client) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode >= 400 {
		return &APIError{StatusCode: response.StatusCode, Code: response.StatusCode, Message: response.Status}
	}
	return nil
}

// get 发起一次带鉴权的 GET，并把 {"data": ...} 解到 out。
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	if c.baseURL == "" {
		return errors.New("metatube: 未配置服务地址")
	}
	target := c.baseURL + path
	if encoded := query.Encode(); encoded != "" {
		target += "?" + encoded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("metatube: 请求 %s 失败: %w", path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("metatube: 读取响应失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeError(response.StatusCode, body)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("metatube: 解析响应失败: %w", err)
	}
	if len(envelope.Data) == 0 {
		return errors.New("metatube: 响应缺少 data")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("metatube: 解析 data 失败: %w", err)
	}
	return nil
}

// decodeError 把错误响应解成 APIError；解不出来就退回状态码 + 原文片段。
func decodeError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return &APIError{StatusCode: status, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	return &APIError{StatusCode: status, Code: status, Message: snippet}
}
