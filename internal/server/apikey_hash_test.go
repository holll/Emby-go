package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIKeyListHidesPlaintext(t *testing.T) {
	a, _, _ := authorityApp(t)
	const token = "fixture-session"
	if err := a.db.SaveAccessToken(token); err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "/api/admin/apikeys", strings.NewReader(`{"name":"script"}`))
	create.Header.Set("X-Emby-Token", token)
	create.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, create)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		ID        string `json:"id"`
		Key       string `json:"key"`
		KeyPrefix string `json:"key_prefix"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.Key) != 48 || len(created.ID) != 64 || len(created.KeyPrefix) != 12 {
		t.Fatalf("create response=%s", w.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, "/api/admin/apikeys", nil)
	list.Header.Set("X-Emby-Token", token)
	lw := httptest.NewRecorder()
	a.Handler().ServeHTTP(lw, list)
	if lw.Code != http.StatusOK || strings.Contains(lw.Body.String(), created.Key) {
		t.Fatalf("list leaks plaintext status=%d body=%s", lw.Code, lw.Body.String())
	}
	var listed struct {
		Items []struct {
			ID        string `json:"id"`
			Key       string `json:"key"`
			KeyPrefix string `json:"key_prefix"`
		} `json:"items"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("list body=%s err=%v", lw.Body.String(), err)
	}
	if listed.Items[0].Key != "" || listed.Items[0].ID != created.ID || listed.Items[0].KeyPrefix != created.KeyPrefix {
		t.Fatalf("listed item=%+v", listed.Items[0])
	}
	// 列表返回的 ID 可直接用于删除。
	del := httptest.NewRequest(http.MethodDelete, "/api/admin/apikeys/"+created.ID, nil)
	del.Header.Set("X-Emby-Token", token)
	dw := httptest.NewRecorder()
	a.Handler().ServeHTTP(dw, del)
	if dw.Code != http.StatusNoContent {
		t.Fatalf("delete by id status=%d body=%s", dw.Code, dw.Body.String())
	}
}
