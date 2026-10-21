package webhook

import (
	"context"
	"emby-go/internal/config"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbyFormAndJSONUserData(t *testing.T) {
	for _, format := range []string{"", "json", "form"} {
		t.Run(format, func(t *testing.T) {
			got := make(chan Event, 1)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var event Event
				if format == "form" {
					if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Error("form content type")
					}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if len(r.PostForm) != 1 {
						t.Error("expected only data field")
					}
					if err := json.Unmarshal([]byte(r.PostForm.Get("data")), &event); err != nil {
						t.Error(err)
					}
				} else {
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("JSON content type")
					}
					if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
						t.Error(err)
					}
				}
				got <- event
			}))
			defer target.Close()
			s, err := New(config.WebhookConfig{Enabled: true, URL: target.URL, AllowPrivate: true, Format: format, Events: []string{ItemRate}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			e := deletionEvent()
			e.Event = ItemRate
			e.Item.Name = "影片 & + = %"
			e.Item.UserData = map[string]any{"IsFavorite": true}
			if err := s.Send(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			actual := <-got
			if actual.Event != ItemRate || actual.Item.Name != e.Item.Name || actual.Item.UserData["IsFavorite"] != true || !actual.Date.Equal(actual.Timestamp) {
				t.Fatalf("wrong payload %+v", actual)
			}
		})
	}
	for _, f := range []string{"FORM", "xml", "private-canary"} {
		if err := (config.WebhookConfig{Format: f}).Validate(); err == nil || err.Error() != "webhook: invalid format configuration" {
			t.Fatalf("invalid format err=%v", err)
		}
	}
}
