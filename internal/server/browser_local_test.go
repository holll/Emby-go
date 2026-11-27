package server

import (
	"os"
	"testing"
)

// Real browser tests are local-only, even if the Actions runner has Chromium.
func requireLocalBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("GITHUB_ACTIONS") == "true" || os.Getenv("EMBY_GO_BROWSER_TESTS") != "1" {
		t.Skip("real browser tests are local-only; set EMBY_GO_BROWSER_TESTS=1 locally")
	}
}
