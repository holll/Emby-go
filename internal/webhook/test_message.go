package webhook

import (
	"context"
	"encoding/json"
	"time"
)

// TestEvent is intentionally not a subscribable business event.
const TestEvent = "webhook.test"

// SendTest sends exactly one harmless configuration probe, regardless of event
// subscriptions. It cannot accept Item/User data or simulate a business event.
// Callers must construct an enabled Sender from the explicit draft destination.
func (s *Sender) SendTest(ctx context.Context, server Server) error {
	if s == nil || s.client == nil || ctx == nil {
		return ErrInvalidEvent
	}
	now := time.Now().UTC()
	payload := struct {
		Event     string    `json:"Event"`
		Message   string    `json:"Message"`
		Server    Server    `json:"Server"`
		Timestamp time.Time `json:"Timestamp"`
		Date      time.Time `json:"Date"`
	}{TestEvent, "Webhook configuration test only; no media or user operation occurred.", server, now, now}
	body, err := json.Marshal(payload)
	if err != nil {
		return ErrInvalidEvent
	}
	return s.sendPayload(ctx, body)
}

// DeliveryErrorCode returns a stable sanitized classification, never raw network
// errors, destination credentials or receiver response contents.
func DeliveryErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return classifyTargetError(err)
}
