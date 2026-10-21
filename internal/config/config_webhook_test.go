package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebhookDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Webhook.Enabled || cfg.Webhook.AllowPrivate || cfg.Webhook.URL != "" || cfg.Webhook.TimeoutSeconds != 5 || cfg.Webhook.Timeout() != 5*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg.Webhook)
	}
	if (WebhookConfig{}).Timeout() != 5*time.Second {
		t.Fatal("zero timeout must default")
	}
}

func TestWebhookLoadValidation(t *testing.T) {
	for _, tc := range []struct {
		name, yaml string
		valid      bool
		timeout    int
	}{
		{"absent", "port: 9000\n", true, 5},
		{"disabled empty", "webhook: {enabled: false}\n", true, 5},
		{"enabled empty", "webhook: {enabled: true}\n", false, 0},
		{"enabled type secret", "webhook: {enabled: 'https://receiver.example/secret-token'}\n", false, 0},
		{"timeout type secret", "webhook: {timeout_seconds: 'https://receiver.example/secret-token'}\n", false, 0},
		{"private type secret", "webhook: {allow_private: 'https://receiver.example/secret-token'}\n", false, 0},
		{"valid", "webhook: {enabled: true, url: 'https://receiver.example/webhook/secret-token', timeout_seconds: 30}\n", true, 30},
		{"default zero", "webhook: {enabled: true, url: 'https://receiver.example', timeout_seconds: 0}\n", true, 5},
		{"timeout negative", "webhook: {timeout_seconds: -1}\n", false, 0},
		{"timeout excessive", "webhook: {timeout_seconds: 31}\n", false, 0},
		{"private", "webhook: {enabled: true, url: 'http://127.0.0.1/webhook/secret-token'}\n", false, 0},
		{"private allowed", "webhook: {enabled: true, url: 'http://127.0.0.1/webhook/secret-token', allow_private: true}\n", true, 5},
		{"empty events", "webhook: {events: []}\n", true, 5},
		{"null events", "webhook: {events: null}\n", true, 5},
		{"explicit events", "webhook: {events: [item.updated, user.logout, item.updated]}\n", true, 5},
		{"wildcard", "webhook: {events: ['*']}\n", false, 0},
		{"unknown secret", "webhook: {events: ['https://receiver.example/secret-token']}\n", false, 0},
		{"events scalar secret", "webhook: {events: 'https://receiver.example/secret-token'}\n", false, 0},
		{"events map secret", "webhook: {events: [{secret-token: secret-token}]}\n", false, 0},
		{"events empty string", "webhook: {events: ['']}\n", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(p, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(p)
			// Loading checks YAML types; the server validates the effective fallback
			// only after checking for an authoritative WebUI database override.
			if err == nil {
				err = cfg.Webhook.Validate()
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if err != nil && (strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "http")) {
				t.Fatal("configuration error exposes URL")
			}
			if tc.valid && cfg.Webhook.TimeoutSeconds != tc.timeout {
				t.Fatalf("timeout=%d", cfg.Webhook.TimeoutSeconds)
			}
		})
	}
}

func TestWebhookEventsValidation(t *testing.T) {
	for _, event := range []string{
		"item.deleted", "item.updated", "item.markplayed", "item.markunplayed", "item.rate", "item.unrate",
		"playback.start", "playback.progress", "playback.stop", "playback.pause", "playback.unpause",
		"user.authenticated", "user.logout",
	} {
		if !ValidWebhookEvent(event) {
			t.Fatalf("valid event rejected: %s", event)
		}
		if err := (WebhookConfig{Events: []string{event, event}}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []string{"*", "item.*", "", "item.future", "ITEM.DELETED", " item.deleted", "https://secret-token.invalid"} {
		if ValidWebhookEvent(event) {
			t.Fatal("unknown event accepted")
		}
		for _, enabled := range []bool{false, true} {
			err := (WebhookConfig{Enabled: enabled, Events: []string{event}}).Validate()
			if err == nil || err.Error() != "webhook: invalid events configuration" {
				t.Fatalf("expected fixed sanitized error: %v", err)
			}
		}
	}
}

func TestWebhookURLValidation(t *testing.T) {
	for _, value := range []string{"", "relative/path", "ftp://receiver.example/secret-token", "http:///missing", "https://user:secret-token@receiver.example", "https://receiver.example/#secret-token", "https://receiver.example/#", "https://receiver.example:0/secret-token", "https://receiver.example:65536/secret-token", "https://receiver.example:bad/secret-token", "http://localhost/secret-token", "http://LOCALHOST./secret-token", "http://foo.localhost/secret-token", "http://10.0.0.1/secret-token", "http://[::1]/secret-token", "https://receiver.example/\nsecret-token"} {
		t.Run(value, func(t *testing.T) {
			err := (WebhookConfig{Enabled: true, URL: value}).Validate()
			if err == nil {
				t.Fatal("expected rejection")
			}
			if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), value) && value != "" {
				t.Fatal("error leaks configuration")
			}
		})
	}
	// A disabled sender does not need a URL and does not resolve any hostname.
	if err := (WebhookConfig{URL: "not a URL"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
