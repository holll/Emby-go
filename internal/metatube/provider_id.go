package metatube

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// ParseProviderID reads the official plugin's Provider:escaped-id[:position[:update]]
// identity. Only the ID is URI-decoded, exactly once; '+' is a literal plus.
// The optional plugin bookkeeping suffixes do not participate in lookup.
func ParseProviderID(raw string) (provider, id string, ok bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 2 || len(parts) > 4 {
		return "", "", false
	}
	provider = parts[0]
	var err error
	id, err = url.PathUnescape(parts[1])
	if err != nil || !validProviderIdentity(provider, id) {
		return "", "", false
	}
	if len(parts) > 2 && parts[2] != "" {
		position, err := strconv.ParseFloat(parts[2], 64)
		if err != nil || math.IsNaN(position) || math.IsInf(position, 0) {
			return "", "", false
		}
	}
	if len(parts) > 3 && parts[3] != "" {
		switch strings.ToLower(parts[3]) {
		case "true", "false", "t", "f", "1", "0":
		default:
			return "", "", false
		}
	}
	return provider, id, true
}

// FormatProviderID uses the plugin's Uri.EscapeDataString-compatible ID encoding.
// This deliberately does not add the historical local "metatube:" prefix.
func FormatProviderID(provider, id string) string {
	if !validProviderIdentity(provider, id) {
		return ""
	}
	return provider + ":" + strings.ReplaceAll(url.QueryEscape(id), "+", "%20")
}

func validProviderIdentity(provider, id string) bool {
	if provider == "" || strings.TrimSpace(id) == "" || strings.ContainsFunc(id, unicode.IsControl) {
		return false
	}
	for _, r := range provider {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
