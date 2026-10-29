package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxIPBlockRules = 256
const ipBlockKey = "security:ip-blocklist:v1"

var (
	ErrIPBlockConflict = errors.New("IP block rule overlaps an existing rule")
	ErrIPBlockLimit    = errors.New("IP block rule limit reached")
	ErrIPBlockMissing  = errors.New("IP block rule not found")
	// ErrIPBlockUnsafe marks rules that would cut off the local recovery paths.
	// Administrators must not be able to lock themselves out or block loopback.
	ErrIPBlockUnsafe = errors.New("loopback and all-address ranges cannot be blocked")
)

type IPBlockRule struct {
	Network   string `json:"network"`
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

// NormalizeIPBlockNetwork accepts only IP literals or CIDRs, never host names,
// zones, ranges or URL syntax. Loopback remains a local authenticated recovery
// path; broad ranges overlapping loopback (including /0) fail closed.
func NormalizeIPBlockNetwork(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	var p netip.Prefix
	var err error
	if strings.Contains(raw, "/") {
		p, err = netip.ParsePrefix(raw)
	} else {
		var addr netip.Addr
		addr, err = netip.ParseAddr(raw)
		if err == nil && addr.Zone() == "" {
			addr = addr.Unmap()
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
	}
	if err != nil || !p.IsValid() || p.Addr().Zone() != "" {
		return netip.Prefix{}, errors.New("provide an IP address or CIDR")
	}
	if p.Addr().Is4In6() {
		if p.Bits() < 96 {
			return netip.Prefix{}, errors.New("IPv4-mapped prefix must be /96 or narrower")
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	p = p.Masked()
	for _, local := range []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")} {
		if p.Overlaps(local) {
			return netip.Prefix{}, ErrIPBlockUnsafe
		}
	}
	if p.Addr().IsUnspecified() {
		return netip.Prefix{}, errors.New("unspecified address ranges cannot be blocked")
	}
	return p, nil
}

func validateIPBlockNote(note string) bool {
	return utf8.ValidString(note) && len(note) <= 200 && strings.IndexFunc(note, unicode.IsControl) < 0
}

// decodeIPBlockRules never treats a stored empty value as an empty policy. A
// missing row is handled by readIPBlockRules via sql.ErrNoRows; every committed
// policy is JSON, so an empty or malformed value must fail closed.
func decodeIPBlockRules(raw string) ([]IPBlockRule, error) {
	if raw == "" {
		return nil, errors.New("invalid IP block configuration")
	}
	if len(raw) > 128*1024 {
		return nil, errors.New("IP block configuration too large")
	}
	var rules []IPBlockRule
	if json.Unmarshal([]byte(raw), &rules) != nil || rules == nil || len(rules) > MaxIPBlockRules {
		return nil, errors.New("invalid IP block configuration")
	}
	prefixes := make([]netip.Prefix, 0, len(rules))
	for _, rule := range rules {
		p, err := NormalizeIPBlockNetwork(rule.Network)
		if err != nil || p.String() != rule.Network || !validateIPBlockNote(rule.Note) {
			return nil, errors.New("invalid IP block rule")
		}
		if _, err := time.Parse(time.RFC3339Nano, rule.CreatedAt); err != nil {
			return nil, errors.New("invalid IP block creation time")
		}
		for _, prior := range prefixes {
			if p.Overlaps(prior) {
				return nil, ErrIPBlockConflict
			}
		}
		prefixes = append(prefixes, p)
	}
	return rules, nil
}

func (s *Store) readIPBlockRules(ctx context.Context) ([]IPBlockRule, string, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM kv WHERE key=?", ipBlockKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []IPBlockRule{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	rules, err := decodeIPBlockRules(raw)
	return rules, raw, err
}

func (s *Store) IPBlockRules(ctx context.Context) ([]IPBlockRule, error) {
	rules, _, err := s.readIPBlockRules(ctx)
	return rules, err
}

// MutateIPBlockRules uses a single-statement CAS; failed writes never publish a
// tentative config. Concurrent independent Stores cannot overwrite each other.
// Neither movie metadata nor list/image cache namespaces change.
func (s *Store) MutateIPBlockRules(ctx context.Context, network, note string, remove bool) ([]IPBlockRule, error) {
	prefix, err := NormalizeIPBlockNetwork(network)
	if err != nil {
		return nil, err
	}
	if !validateIPBlockNote(note) {
		return nil, errors.New("note must be at most 200 bytes without control characters")
	}
	network = prefix.String()
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rules, old, err := s.readIPBlockRules(ctx)
		if err != nil {
			return nil, err
		}
		index := -1
		for i, rule := range rules {
			p, _ := netip.ParsePrefix(rule.Network)
			if p == prefix {
				index = i
			}
			if !remove && p.Overlaps(prefix) {
				return nil, ErrIPBlockConflict
			}
		}
		if remove {
			if index < 0 {
				return nil, ErrIPBlockMissing
			}
			rules = append(rules[:index], rules[index+1:]...)
		} else {
			if len(rules) >= MaxIPBlockRules {
				return nil, ErrIPBlockLimit
			}
			rules = append(rules, IPBlockRule{Network: network, Note: strings.TrimSpace(note), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		}
		payload, err := json.Marshal(rules)
		if err != nil {
			return nil, err
		}
		result, err := s.db.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE kv.value=?`, ipBlockKey, string(payload), old)
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n == 1 {
			return rules, nil
		}
	}
	return nil, errors.New("IP block configuration changed concurrently; retry the operation")
}
