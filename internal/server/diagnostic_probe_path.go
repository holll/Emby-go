package server

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// safeDiagnosticProbePath is only for unknown-endpoint diagnostics. It retains
// known static API segments and numeric IDs, never free-form names, filenames,
// credentials or query strings. Hash markers distinguish unknown actions without
// persisting their plaintext. The request logger deliberately keeps its stricter
// family-only policy.
func safeDiagnosticProbePath(path string) string {
	redacted := func(value string) string {
		digest := sha256.Sum256([]byte(value))
		return fmt.Sprintf("[redacted:%x]", digest[:6])
	}
	if len(path) > 2048 {
		return "/" + redacted(path)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 12 {
		return "/" + redacted(path)
	}
	for i, part := range parts {
		lower := strings.ToLower(part)
		switch lower {
		case "emby", "users", "items", "videos", "sessions", "system", "persons", "audio", "activity", "log", "branding", "configuration", "public", "info", "capabilities", "playbackinfo", "playing", "progress", "stopped", "views", "latest", "resume", "suggestions", "similar", "ancestors", "children", "metadata", "images", "primary", "backdrop", "thumb", "subtitles", "stream", "download", "authenticatebyname", "authentication", "quickconnect", "connect", "enabled", "displaypreferences", "localization", "countries", "cultures", "ratings", "virtualfolders", "library", "refresh", "search", "hints", "genres", "studios", "artists", "albums", "collections", "boxsets", "trailers", "extras", "specialfeatures", "additionalparts", "intros", "themevideos", "themesongs", "channels", "livetv", "programs", "recordings", "scheduledtasks", "environment", "devices", "notifications", "plugins", "packages", "updates", "ping":
			parts[i] = lower
		default:
			if !diagnosticIDNumber(part) {
				parts[i] = redacted(part)
			}
		}
	}
	return "/" + strings.Join(parts, "/")
}
