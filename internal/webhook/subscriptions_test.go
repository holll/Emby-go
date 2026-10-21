package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/config"
)

var supportedEvents = []string{
	ItemDeleted, ItemUpdated, ItemMarkPlayed, ItemMarkUnplayed, ItemRate, ItemUnrate,
	PlaybackStart, PlaybackProgress, PlaybackStop, PlaybackPause, PlaybackUnpause,
	UserAuthenticated, UserLogout,
}

func TestSubscriptions(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count.Add(1) }))
	defer srv.Close()
	for _, events := range [][]string{nil, {}, {ItemUpdated, UserLogout, ItemUpdated}, supportedEvents} {
		s, err := New(config.WebhookConfig{Enabled: true, URL: srv.URL, AllowPrivate: true, Events: events})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		for _, name := range supportedEvents {
			want := len(events) == 0 && name == ItemDeleted
			for _, selected := range events {
				want = want || selected == name
			}
			if s.Subscribed(name) != want {
				t.Fatalf("events=%v event=%s subscribed=%v", events, name, s.Subscribed(name))
			}
			before := count.Load()
			event := deletionEvent()
			event.Event = name
			if err := s.Send(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if (count.Load() == before+1) != want {
				t.Fatalf("events=%v event=%s request mismatch", events, name)
			}
			if !want {
				// Unsubscribed is a no-op even without a media path or context.
				if err := s.Send(nil, Event{Event: name}); err != nil {
					t.Fatalf("non-subscribed event validation: %v", err)
				}
			}
		}
		for _, invalid := range []string{"", "*", "item.future", "ITEM.DELETED", " item.deleted", "secret-token"} {
			if s.Subscribed(invalid) {
				t.Fatal("invalid subscription")
			}
			if err := s.Send(context.Background(), Event{Event: invalid}); !errors.Is(err, ErrInvalidEvent) || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("invalid event: %v", err)
			}
		}
	}
	var nilSender *Sender
	disabled, err := New(config.WebhookConfig{Events: supportedEvents})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Sender{nilSender, {}, disabled} {
		for _, name := range supportedEvents {
			if s.Subscribed(name) {
				t.Fatal("disabled subscribed")
			}
		}
	}
}

func TestGenericPayload(t *testing.T) {
	var payload map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	s, err := New(config.WebhookConfig{Enabled: true, URL: srv.URL, AllowPrivate: true, Events: supportedEvents})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{UserAuthenticated, UserLogout} {
		stamp := time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)
		event := Event{Event: name, Timestamp: stamp, Date: stamp.Add(time.Hour), User: &User{Id: "user-id", Name: "User"}}
		if err := s.Send(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if string(payload["Item"]) != "{}" || payload["Session"] != nil || payload["PlaybackInfo"] != nil {
			t.Fatalf("optional fields: %s", payload)
		}
		for _, field := range []string{"Date", "Timestamp"} {
			var raw string
			if err := json.Unmarshal(payload[field], &raw); err != nil {
				t.Fatal(err)
			}
			got, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil || !got.Equal(stamp) {
				t.Fatalf("%s=%s err=%v", field, raw, err)
			}
		}
		if event.Date.Equal(event.Timestamp) {
			t.Fatal("Send mutated original event")
		}
	}
	before := time.Now().Add(-time.Second)
	if err := s.Send(context.Background(), Event{Event: UserLogout}); err != nil {
		t.Fatal(err)
	}
	var generated time.Time
	if err := json.Unmarshal(payload["Date"], &generated); err != nil || generated.Before(before) || generated.After(time.Now()) || string(payload["Date"]) != string(payload["Timestamp"]) {
		t.Fatalf("generated Date: %s err=%v", payload["Date"], err)
	}
	for _, name := range supportedEvents[:11] {
		if err := s.Send(context.Background(), Event{Event: name}); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("media path required for %s: %v", name, err)
		}
	}
	event := deletionEvent()
	event.Event = PlaybackProgress
	event.Item.LibraryId = "library-id"
	event.Item.RunTimeTicks = 12345
	event.User = &User{Id: "user-id", Name: "User"}
	event.Session = &Session{Id: "session-id", DeviceId: "device-id", DeviceName: "Device", Client: "Client", ApplicationVersion: "1", RemoteEndPoint: "127.0.0.1", PlayState: PlayState{PositionTicks: 42, IsPaused: true, PlayMethod: "DirectPlay"}}
	event.PlaybackInfo = &PlaybackInfo{PositionTicks: 42, PlaySessionId: "play-session-id", IsPaused: true}
	if err := s.Send(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"User", "Session", "PlaybackInfo"} {
		if payload[field] == nil {
			t.Fatalf("missing %s", field)
		}
	}
	var got Event
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Item, event.Item) || *got.User != *event.User || *got.Session != *event.Session || *got.PlaybackInfo != *event.PlaybackInfo {
		t.Fatalf("payload round-trip: %+v", got)
	}
}
