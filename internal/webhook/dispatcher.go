package webhook

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"emby-go/internal/config"
)

// R3：多目标投递。每个事件按目标独立判断、独立发送；总体有界预算，目标
// 失败不短路其它目标。结果只含稳定 ID/名称与安全分类，不含 URL/响应正文。
const (
	MaxTargets             = 10
	DefaultEventBudget     = 5 * time.Second
	maxConcurrentTargets   = 4
	minBudgetAttemptMargin = 50 * time.Millisecond

	TargetDisabled      = "disabled"
	TargetSent          = "sent"
	TargetFailed        = "failed"
	TargetSkippedBudget = "skipped_budget"

	ResultDisabled = "disabled"
	ResultSent     = "sent"
	ResultFailed   = "failed"
	ResultPartial  = "partial"
)

// NewTargetID 生成服务端稳定的目标 ID；客户端不能伪造或复用其它配置的 ID。
func NewTargetID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "wh_" + hex.EncodeToString(buf[:]), nil
}

// Target 是已验证、可投递的单目标配置。URL 不参与日志与响应。
type Target struct {
	ID             string
	Name           string
	Enabled        bool
	URL            string
	Events         []string
	Format         string
	TimeoutSeconds int
	AllowPrivate   bool
}

// TargetOutcome 是单目标结果；ErrorCode 为稳定分类，不含 URL 或响应原文。
type TargetOutcome struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

// Result 是单事件聚合结果。
type Result struct {
	Status  string          `json:"status"`
	Targets []TargetOutcome `json:"targets"`
}

type dispatchTarget struct {
	spec   Target
	sender *Sender
}

// Dispatcher 持有不可变目标快照与发送器；配置替换整体发布，旧快照可完成
// 已开始的投递。Send 不使用请求上下文以外的后台队列。
type Dispatcher struct {
	targets []dispatchTarget
	budget  time.Duration
	slots   chan struct{}
}

// NewDispatcher 在规定上限内构建所有发送器；任一目标无效则整体失败，调用
// 方不得发布部分配置。合法目标集合可以为空（全部关闭）。
func NewDispatcher(targets []Target, budget time.Duration) (*Dispatcher, error) {
	if budget <= 0 {
		budget = DefaultEventBudget
	}
	if len(targets) > MaxTargets {
		return nil, fmt.Errorf("webhook: at most %d targets", MaxTargets)
	}
	d := &Dispatcher{budget: budget, slots: make(chan struct{}, maxConcurrentTargets)}
	for _, target := range targets {
		spec, err := normalizeTarget(target)
		if err != nil {
			d.Close()
			return nil, err
		}
		cfg := config.WebhookConfig{
			Enabled: spec.Enabled, URL: spec.URL, Events: append([]string(nil), spec.Events...),
			Format: spec.Format, TimeoutSeconds: spec.TimeoutSeconds, AllowPrivate: spec.AllowPrivate,
		}
		sender, err := New(cfg)
		if err != nil {
			d.Close()
			return nil, fmt.Errorf("webhook: invalid target configuration")
		}
		d.targets = append(d.targets, dispatchTarget{spec: spec, sender: sender})
	}
	return d, nil
}

func normalizeTarget(target Target) (Target, error) {
	target.ID = strings.TrimSpace(target.ID)
	target.Name = strings.TrimSpace(target.Name)
	if target.ID == "" || len(target.ID) > 64 {
		return Target{}, errors.New("webhook: target id required")
	}
	if target.Name == "" || len([]rune(target.Name)) > 80 {
		return Target{}, errors.New("webhook: target name must be 1-80 characters")
	}
	if len(target.Events) == 0 || len(target.Events) > 13 {
		return Target{}, errors.New("webhook: target needs explicit events")
	}
	events := append([]string(nil), target.Events...)
	sort.Strings(events)
	for i, event := range events {
		if !config.ValidWebhookEvent(event) {
			return Target{}, errors.New("webhook: invalid event")
		}
		if i > 0 && events[i-1] == event {
			return Target{}, errors.New("webhook: duplicate event")
		}
	}
	target.Events = events
	if target.Format == "" {
		target.Format = "json"
	}
	if target.Format != "json" && target.Format != "form" {
		return Target{}, errors.New("webhook: invalid format")
	}
	if target.TimeoutSeconds == 0 {
		target.TimeoutSeconds = 5
	}
	if target.TimeoutSeconds < 1 || target.TimeoutSeconds > 30 {
		return Target{}, errors.New("webhook: timeout_seconds must be 1-30")
	}
	target.URL = strings.TrimSpace(target.URL)
	if len(target.URL) > 4096 {
		return Target{}, errors.New("webhook: url too long")
	}
	if target.URL != "" {
		// 离线校验；即使目标暂时禁用，草稿地址也必须合法。
		probe := config.WebhookConfig{Enabled: true, URL: target.URL, Events: target.Events,
			Format: target.Format, TimeoutSeconds: target.TimeoutSeconds, AllowPrivate: target.AllowPrivate}
		if err := probe.Validate(); err != nil {
			return Target{}, errors.New("webhook: invalid target url")
		}
	}
	if target.Enabled && target.URL == "" {
		return Target{}, errors.New("webhook: enabled target requires a url")
	}
	return target, nil
}

// Subscribed reports whether any enabled target explicitly subscribes to event.
func (d *Dispatcher) Subscribed(event string) bool {
	if d == nil || !config.ValidWebhookEvent(event) {
		return false
	}
	for _, target := range d.targets {
		if !target.spec.Enabled {
			continue
		}
		if slices.Contains(target.spec.Events, event) {
			return true
		}
	}
	return false
}

// Send 在总预算内并发投递；未获得预算的目标标记 skipped_budget，不算成功。
func (d *Dispatcher) Send(ctx context.Context, event Event) Result {
	result := Result{Status: ResultDisabled, Targets: []TargetOutcome{}}
	if d == nil {
		return result
	}
	matched := make([]dispatchTarget, 0, len(d.targets))
	for _, target := range d.targets {
		if !target.spec.Enabled || !target.sender.Subscribed(event.Event) {
			continue
		}
		matched = append(matched, target)
	}
	if len(matched) == 0 {
		return result
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(d.budget)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	outcomes := make([]TargetOutcome, len(matched))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, target := range matched {
		remaining := time.Until(deadline)
		if remaining <= minBudgetAttemptMargin {
			outcomes[i] = TargetOutcome{ID: target.spec.ID, Name: target.spec.Name, Status: TargetSkippedBudget, ErrorCode: "budget_exhausted"}
			continue
		}
		select {
		case d.slots <- struct{}{}:
		case <-time.After(min(remaining, 250*time.Millisecond)):
			// 全局发送槽也计入预算；等不到槽不能假装成功。
			if remaining <= minBudgetAttemptMargin {
				outcomes[i] = TargetOutcome{ID: target.spec.ID, Name: target.spec.Name, Status: TargetSkippedBudget, ErrorCode: "budget_exhausted"}
				continue
			}
			if tryAcquire(d.slots, deadline) != nil {
				outcomes[i] = TargetOutcome{ID: target.spec.ID, Name: target.spec.Name, Status: TargetSkippedBudget, ErrorCode: "budget_exhausted"}
				continue
			}
		}
		wg.Add(1)
		go func(i int, target dispatchTarget) {
			defer wg.Done()
			defer func() { <-d.slots }()
			remaining := time.Until(deadline)
			if remaining <= minBudgetAttemptMargin {
				outcomes[i] = TargetOutcome{ID: target.spec.ID, Name: target.spec.Name, Status: TargetSkippedBudget, ErrorCode: "budget_exhausted"}
				return
			}
			targetBudget := min(time.Duration(target.spec.TimeoutSeconds)*time.Second, remaining)
			runCtx, cancel := context.WithTimeout(ctx, targetBudget)
			err := target.sender.Send(runCtx, event)
			cancel()
			outcome := TargetOutcome{ID: target.spec.ID, Name: target.spec.Name, Status: TargetSent}
			if err != nil {
				outcome.Status = TargetFailed
				outcome.ErrorCode = classifyTargetError(err)
			}
			mu.Lock()
			outcomes[i] = outcome
			mu.Unlock()
		}(i, target)
	}
	wg.Wait()
	sent, failed := 0, 0
	for _, outcome := range outcomes {
		switch outcome.Status {
		case TargetSent:
			sent++
		case TargetFailed:
			failed++
		}
	}
	switch {
	case sent == len(outcomes):
		result.Status = ResultSent
	case sent > 0:
		result.Status = ResultPartial
	default:
		result.Status = ResultFailed
	}
	result.Targets = append(result.Targets, outcomes...)
	return result
}

func tryAcquire(slots chan struct{}, deadline time.Time) error {
	for {
		select {
		case slots <- struct{}{}:
			return nil
		default:
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// classifyTargetError 只返回稳定分类；绝不透出 URL、DNS 或响应正文。
func classifyTargetError(err error) string {
	var status *StatusError
	switch {
	case errors.As(err, &status):
		return "status_" + fmt.Sprint(status.StatusCode)
	case errors.Is(err, ErrInvalidEvent):
		return "invalid_event"
	case errors.Is(err, ErrReadResponse):
		return "read_response"
	case errors.Is(err, ErrResponseTooLarge):
		return "response_too_large"
	case errors.Is(err, ErrIgnored):
		return "ignored"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "request_failed"
	}
}

// Close 释放各发送器连接；幂等，不取消已开始投递。
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	for _, target := range d.targets {
		target.sender.Close()
	}
	d.targets = nil
}
