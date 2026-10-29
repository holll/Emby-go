package server

import (
	"strings"
	"testing"
)

// This inventory fails when a new management write route is registered without
// an explicit authority policy. Read/validation and configuration/file writes
// retain request authentication snapshots. Credential writes recheck in the
// mutation transaction; external work has a short, separately committed admission
// point and may finish after a subsequent revocation. This is not cancellation.
func TestStabilityPermissionsManagementRouteInventory(t *testing.T) {
	a, _, _ := asyncScanFixture(t)
	policies := map[string]string{
		"POST /api/admin/security/ip-blocklist":    "admission",
		"DELETE /api/admin/security/ip-blocklist":  "admission",
		"POST /api/admin/security/unlock":          "reauth-admission",
		"POST /api/admin/libraries":                "request-snapshot",
		"PUT /api/admin/libraries/:id/sprites":     "admission",
		"DELETE /api/admin/libraries/:id":          "request-snapshot",
		"POST /api/admin/libraries/:id/move":       "request-snapshot",
		"POST /api/admin/libraries/:id/cover":      "request-snapshot",
		"DELETE /api/admin/libraries/:id/cover":    "request-snapshot",
		"POST /api/admin/scan":                     "admission",
		"POST /api/admin/tasks/scan":               "admission",
		"POST /api/admin/probe/media":              "admission",
		"POST /api/admin/probe/media/cancel":       "admission",
		"POST /api/admin/reindex":                  "admission",
		"DELETE /api/admin/items/:id":              "request-snapshot",
		"DELETE /api/admin/probe":                  "request-snapshot",
		"POST /api/admin/users":                    "transaction",
		"PUT /api/admin/users/:id":                 "transaction",
		"DELETE /api/admin/users/:id":              "transaction",
		"PUT /api/admin/users/:id/libraries":       "transaction",
		"POST /api/admin/apikeys":                  "transaction",
		"POST /api/admin/apikeys/:key/rotate":      "reauth-transaction",
		"DELETE /api/admin/apikeys/:key":           "transaction",
		"DELETE /api/admin/access-tokens":          "transaction",
		"PUT /api/admin/logs/settings":             "configuration-snapshot",
		"PUT /api/admin/image-cache/settings":      "configuration-snapshot",
		"POST /api/admin/image-cache/trim":         "request-snapshot",
		"PUT /api/admin/webhook/settings":          "configuration-snapshot",
		"PUT /api/admin/webhook/targets":           "configuration-snapshot",
		"PUT /api/admin/scrape/settings":           "configuration-snapshot",
		"POST /api/admin/scrape/test":              "request-snapshot",
		"POST /api/admin/scrape/run":               "admission",
		"POST /api/admin/scrape/avatars":           "admission",
		"POST /api/admin/scrape/cancel":            "admission",
		"POST /api/admin/translate/run":            "admission",
		"POST /api/admin/translate/cancel":         "admission",
		"POST /api/admin/sprites/run":              "admission",
		"POST /api/admin/sprites/cancel":           "admission",
		"POST /api/admin/artwork/run":              "admission",
		"POST /api/admin/artwork/cancel":           "admission",
		"POST /api/admin/scheduled":                "configuration-snapshot",
		"POST /api/admin/scheduled/validate":       "request-snapshot",
		"PUT /api/admin/scheduled/:id":             "configuration-snapshot",
		"PATCH /api/admin/scheduled/:id":           "configuration-snapshot",
		"DELETE /api/admin/scheduled/:id":          "configuration-snapshot",
		"POST /api/admin/scheduled/:id/toggle":     "configuration-snapshot",
		"POST /api/admin/scheduled/:id/run":        "admission",
		"PUT /api/admin/items/:id":                 "request-snapshot",
		"PATCH /api/admin/items/:id":               "request-snapshot",
		"POST /api/admin/items/:id/recover-edit":   "request-snapshot",
		"POST /api/admin/items/:id/reread":         "request-snapshot",
		"POST /api/admin/items/:id/probe":          "admission",
		"POST /api/admin/items/:id/scrape/inspect": "request-snapshot",
		"POST /api/admin/items/:id/scrape":         "admission",
		"POST /api/admin/items/:id/images/:kind":   "request-snapshot",
	}
	seen := map[string]bool{}
	for _, route := range a.router.Routes() {
		if !strings.HasPrefix(route.Path, "/api/admin/") || route.Method == "GET" || route.Method == "HEAD" || route.Method == "OPTIONS" {
			continue
		}
		key := route.Method + " " + route.Path
		if policies[key] == "" {
			t.Errorf("unclassified management write: %s", key)
		}
		seen[key] = true
	}
	for key, policy := range policies {
		if !seen[key] {
			t.Errorf("policy %s has no route: %s", policy, key)
		}
	}
	t.Logf("classified %d management write/validation routes", len(seen))
}
