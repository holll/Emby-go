// Package webhook sends explicitly subscribed Emby-style event notifications.
// Sending is synchronous, best-effort and non-durable: no retries, queue, or
// background goroutines exist. Deletion notifications must never represent scans
// or database removal. A nil error acknowledges receipt only, not remote effects.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"emby-go/internal/config"
	"emby-go/internal/outbound"
)

const (
	ItemDeleted       = "item.deleted"
	ItemUpdated       = "item.updated"
	ItemMarkPlayed    = "item.markplayed"
	ItemMarkUnplayed  = "item.markunplayed"
	ItemRate          = "item.rate"
	ItemUnrate        = "item.unrate"
	PlaybackStart     = "playback.start"
	PlaybackProgress  = "playback.progress"
	PlaybackStop      = "playback.stop"
	PlaybackPause     = "playback.pause"
	PlaybackUnpause   = "playback.unpause"
	UserAuthenticated = "user.authenticated"
	UserLogout        = "user.logout"
	maxResponseBytes  = 4 << 10
)

// Event preserves the original value-based Item API. User events may have a zero
// Item (encoded as {}). Timestamp and Date encode as RFC3339; Send mirrors the
// canonical Timestamp into Date. Media Item.Path is the original local path,
// never a STRM content URL. SmartStrm may delete its entire mapped remote folder.
// These types intentionally contain no access tokens or media content URLs.
type Event struct {
	Event        string        `json:"Event"`
	Item         Item          `json:"Item"`
	Server       Server        `json:"Server"`
	Timestamp    time.Time     `json:"Timestamp"`
	Date         time.Time     `json:"Date"`
	User         *User         `json:"User,omitempty"`
	Session      *Session      `json:"Session,omitempty"`
	PlaybackInfo *PlaybackInfo `json:"PlaybackInfo,omitempty"`
}

type Item struct {
	Id           string         `json:"Id,omitempty"`
	Name         string         `json:"Name,omitempty"`
	Path         string         `json:"Path,omitempty"`
	Type         string         `json:"Type,omitempty"`
	LibraryId    string         `json:"LibraryId,omitempty"`
	RunTimeTicks int64          `json:"RunTimeTicks,omitempty"`
	UserData     map[string]any `json:"UserData,omitempty"`
}

type User struct {
	Id   string `json:"Id,omitempty"`
	Name string `json:"Name,omitempty"`
}

type Session struct {
	Id                 string    `json:"Id,omitempty"`
	DeviceId           string    `json:"DeviceId,omitempty"`
	DeviceName         string    `json:"DeviceName,omitempty"`
	Client             string    `json:"Client,omitempty"`
	ApplicationVersion string    `json:"ApplicationVersion,omitempty"`
	RemoteEndPoint     string    `json:"RemoteEndPoint,omitempty"`
	PlayState          PlayState `json:"PlayState"`
}

type PlayState struct {
	PositionTicks int64  `json:"PositionTicks"`
	IsPaused      bool   `json:"IsPaused"`
	PlayMethod    string `json:"PlayMethod,omitempty"`
}

type PlaybackInfo struct {
	PositionTicks int64  `json:"PositionTicks"`
	PlaySessionId string `json:"PlaySessionId,omitempty"`
	IsPaused      bool   `json:"IsPaused"`
}

type Server struct {
	Id   string `json:"Id"`
	Name string `json:"Name"`
}

var (
	ErrInvalidEvent     = errors.New("webhook: invalid event")
	ErrSend             = errors.New("webhook: request failed")
	ErrReadResponse     = errors.New("webhook: response read failed")
	ErrResponseTooLarge = errors.New("webhook: response exceeds acknowledgment limit")
	ErrIgnored          = errors.New("webhook: receiver ignored notification")
)

// StatusError contains only the numeric HTTP status, never a URL or response body.
type StatusError struct{ StatusCode int }

func (e *StatusError) Error() string { return fmt.Sprintf("webhook: HTTP status %d", e.StatusCode) }

// Sender is safe for concurrent Send calls; it has no worker or durable queue.
// Its URL is private to avoid accidentally exposing a token through logs.
type Sender struct {
	client *http.Client
	url    string
	events map[string]struct{}
	format string
}

// New validates configuration even when constructed outside config.Load.
// Disabled configuration creates a no-op sender and no HTTP client.
func New(cfg config.WebhookConfig) (*Sender, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return &Sender{}, nil
	}
	client := outbound.NewClient(outbound.Options{Timeout: cfg.Timeout(), AllowPrivate: cfg.AllowPrivate})
	// outbound's zero redirect count means its default, so override explicitly.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	events := make(map[string]struct{}, len(cfg.Events))
	if len(cfg.Events) == 0 {
		events[ItemDeleted] = struct{}{}
	} else {
		for _, event := range cfg.Events {
			events[event] = struct{}{}
		}
	}
	return &Sender{client: client, url: cfg.URL, events: events, format: cfg.Format}, nil
}

// Subscribed reports whether the sender is enabled and explicitly subscribes to
// a valid event. The historical empty subscription defaults only to item.deleted.
// The subscription set is copied at construction and safe for concurrent reads.
func (s *Sender) Subscribed(event string) bool {
	if s == nil || s.client == nil || !config.ValidWebhookEvent(event) {
		return false
	}
	_, ok := s.events[event]
	return ok
}

// Send performs one POST using the application lifecycle context supplied by main
// (not a soon-to-be-cancelled incoming request context). It does not retry.
// A disabled sender returns nil without validating or transmitting the event.
// A successful response means receiver acknowledgment only, not remote proof.
func (s *Sender) Send(ctx context.Context, event Event) error {
	if s == nil || s.client == nil {
		return nil
	}
	if !config.ValidWebhookEvent(event.Event) {
		return ErrInvalidEvent
	}
	if !s.Subscribed(event.Event) {
		return nil
	}
	isUserEvent := event.Event == UserAuthenticated || event.Event == UserLogout
	if ctx == nil || (!isUserEvent && strings.TrimSpace(event.Item.Path) == "") {
		return ErrInvalidEvent
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	event.Date = event.Timestamp
	body, err := json.Marshal(event)
	if err != nil {
		return ErrInvalidEvent
	}
	var payload io.Reader = bytes.NewReader(body)
	contentType := "application/json"
	if s.format == "form" {
		payload = strings.NewReader(url.Values{"data": {string(body)}}.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, payload)
	if err != nil {
		return ErrSend
	}
	req.Header.Set("Content-Type", contentType)
	// outbound's routing transport does not expose CloseIdleConnections. Disable
	// reuse so no pooled connection survives Send; Close remains lifecycle-safe.
	req.Close = true
	resp, err := s.client.Do(req)
	if err != nil {
		return ErrSend
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{StatusCode: resp.StatusCode}
	}
	// The client total timeout and caller context cover this read as well as Do.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ErrReadResponse
	}
	// Treat a full buffer conservatively: do not accept an unknown/truncated ack.
	if len(data) == maxResponseBytes {
		return ErrResponseTooLarge
	}
	ack := strings.ToLower(string(data))
	if strings.Contains(ack, "ignored") || strings.Contains(ack, "未启用") {
		return ErrIgnored
	}
	return nil
}

// Close releases idle connections. Requests use Connection: close because the
// shared outbound transport currently has no idle-pool closing hook. Call after
// application users have stopped; Close is idempotent and does not cancel Send.
func (s *Sender) Close() {
	if s != nil && s.client != nil {
		s.client.CloseIdleConnections()
	}
}
