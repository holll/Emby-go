package metatube

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// ProviderID is a parsed plugin identity: the lookup pair plus the optional
// bookkeeping suffix the plugin writes next to it.
//
// The suffix is plugin state, not part of the movie identity. It never enters
// the lookup pair, so a position or refresh flag can never change which movie
// is addressed. tail keeps the suffix text exactly as stored: we consume the
// typed values but do not own the plugin's number formatting.
type ProviderID struct {
	Provider string
	ID       string
	Position *float64 // image crop position; nil when the plugin omitted it
	Update   *bool    // force a fresh upstream lookup; nil when omitted
	tail     string   // raw suffix text after "Provider:ID:", empty when absent
}

// ParseProviderIDValue reads Provider:escaped-id[:position[:update]].
// Only the ID is URI-decoded, exactly once; '+' is a literal plus.
// A malformed suffix rejects the whole identity instead of being ignored.
func ParseProviderIDValue(raw string) (ProviderID, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) < 2 || len(parts) > 4 {
		return ProviderID{}, false
	}
	pid := ProviderID{Provider: parts[0]}
	id, err := url.PathUnescape(parts[1])
	if err != nil || !validProviderIdentity(pid.Provider, id) {
		return ProviderID{}, false
	}
	pid.ID = id
	if len(parts) > 2 {
		pid.tail = strings.Join(parts[2:], ":")
	}
	if len(parts) > 2 && parts[2] != "" {
		position, err := strconv.ParseFloat(parts[2], 64)
		if err != nil || math.IsNaN(position) || math.IsInf(position, 0) {
			return ProviderID{}, false
		}
		pid.Position = &position
	}
	if len(parts) > 3 && parts[3] != "" {
		var update bool
		switch strings.ToLower(parts[3]) {
		case "true", "t", "1":
			update = true
		case "false", "f", "0":
			update = false
		default:
			return ProviderID{}, false
		}
		pid.Update = &update
	}
	return pid, true
}

// Lazy reports whether a precise lookup may be served from the upstream cache.
// Only an explicit Update=true asks for a fresh lookup; an absent flag keeps the
// cache-first behaviour that existing markers rely on.
func (p ProviderID) Lazy() bool { return p.Update == nil || !*p.Update }

// String renders the official Provider:escaped-ID[:position[:update]] form.
// The suffix is copied verbatim, so re-saving a marker cannot silently rewrite it.
func (p ProviderID) String() string {
	base := FormatProviderID(p.Provider, p.ID)
	if p.tail == "" {
		return base
	}
	return base + ":" + p.tail
}

// ParseProviderID reads the same identity and keeps the lookup pair only.
func ParseProviderID(raw string) (provider, id string, ok bool) {
	pid, ok := ParseProviderIDValue(raw)
	return pid.Provider, pid.ID, ok
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
