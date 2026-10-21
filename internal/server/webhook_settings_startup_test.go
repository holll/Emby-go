package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"emby-go/internal/cache"
	"emby-go/internal/config"
)

func TestWebhookSettingsStartupAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, raw      string
		present, valid bool
		validFallback  bool
	}{
		{name: "absent invalid fallback"},
		{name: "disabled override wins", raw: `{"version":1,"config":{"Enabled":false,"URL":"","Events":["item.rate"],"Format":"json","TimeoutSeconds":1}}`, present: true, valid: true},
		{name: "empty override", present: true},
		{name: "empty override must not revive enabled fallback", present: true, validFallback: true},
		{name: "corrupt override", raw: "private-corruption-canary", present: true},
		{name: "unknown version", raw: `{"version":9,"config":{"Enabled":false}}`, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, path := authorityApp(t)
			if tc.present {
				if err := a.db.SetSetting(settingWebhook, tc.raw); err != nil {
					t.Fatal(err)
				}
			}
			a.Close()
			file := filepath.Join(t.TempDir(), "config.yaml")
			// Semantically invalid YAML fallback is irrelevant if DB has a valid override;
			// structural/type-invalid YAML remains an ordinary configuration parse error.
			yaml := "webhook: {enabled: true, url: 'file:///private-yaml-canary'}\n"
			if tc.validFallback {
				// No request is made: this valid enabled fallback exists only to catch
				// the unsafe interpretation of an existing empty row as no override.
				yaml = "webhook: {enabled: true, url: 'https://receiver.example/private-yaml-canary'}\n"
			}
			if err := os.WriteFile(file, []byte(yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(file)
			if err != nil {
				t.Fatal("fallback validated before override lookup", err)
			}
			cfg.DBPath = path
			app, err := newApp(cfg, cache.NewMemory(16))
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				app.startupWG.Wait()
				if app.webhookConfig.Enabled || app.webhookConfig.URL != "" || app.webhookSender().Subscribed("item.deleted") {
					t.Fatal("override did not remain disabled")
				}
			} else {
				if err == nil {
					app.Close()
					t.Fatal("invalid settings accepted")
				}
				if strings.Contains(err.Error(), "canary") {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestWebhookSettingsDeletionConfirmationAndClear(t *testing.T) {
	f := newAdminDeleteFixture(t)
	body := webhookSettingsBody("https://receiver.example/delete-secret-canary", true, "item.deleted")
	tag := webhookSettingsTag(t, f.a)
	w := webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, body)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "WEBHOOK_DELETE_CONFIRMATION_REQUIRED") {
		t.Fatal(w.Code, w.Body.String())
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	req["confirm_delete"] = true
	raw, _ := json.Marshal(req)
	w = webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, string(raw))
	if w.Code != 200 || !f.a.webhookSender().Subscribed("item.deleted") {
		t.Fatal(w.Code, w.Body.String())
	}
	tag = webhookSettingsTag(t, f.a)
	req["url"] = ""
	req["clear_url"] = true
	raw, _ = json.Marshal(req)
	w = webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, string(raw))
	if w.Code != 400 {
		t.Fatal("enabled configuration must not lose its URL", w.Code, w.Body.String())
	}
	req["enabled"] = false
	raw, _ = json.Marshal(req)
	w = webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", tag, string(raw))
	if w.Code != 200 || f.a.webhookSender().Subscribed("item.deleted") {
		t.Fatal(w.Code, w.Body.String())
	}
	f.a.BeginShutdown()
	w = webhookSettingsCall(f.a, "PUT", "fixture-admin-delete", webhookSettingsTag(t, f.a), string(raw))
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}
