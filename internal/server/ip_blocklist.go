package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"emby-go/internal/store"
	"github.com/gin-gonic/gin"
)

type ipBlockEntry struct {
	rule   store.IPBlockRule
	prefix netip.Prefix
	hits   atomic.Uint64
}
type ipBlockSnapshot struct{ entries []*ipBlockEntry }
type ipBlockState struct {
	edit     sync.Mutex
	snapshot atomic.Pointer[ipBlockSnapshot]
	total    atomic.Uint64
}

// Only immutable snapshots are published, after SQLite has committed. Request
// matching never waits for configuration writes or performs any disk I/O.
func (s *ipBlockState) publish(rules []store.IPBlockRule) {
	previous := make(map[string]*ipBlockEntry)
	if old := s.snapshot.Load(); old != nil {
		for _, e := range old.entries {
			previous[e.rule.Network] = e
		}
	}
	next := &ipBlockSnapshot{entries: make([]*ipBlockEntry, 0, len(rules))}
	for _, rule := range rules {
		if old := previous[rule.Network]; old != nil && old.rule == rule {
			next.entries = append(next.entries, old)
			continue
		}
		p, _ := netip.ParsePrefix(rule.Network)
		next.entries = append(next.entries, &ipBlockEntry{rule: rule, prefix: p})
	}
	s.snapshot.Store(next)
}

// blockClientAddress keeps Gin's explicit proxy trust policy. Gin's net.ParseIP
// cannot represent scoped IPv6 peers; only then fall back to the actual TCP
// remote address, never an untrusted forwarding header.
func blockClientAddress(c *gin.Context) netip.Addr {
	client := c.ClientIP()
	if addr, err := netip.ParseAddr(client); err == nil {
		return addr.WithZone("").Unmap()
	}
	if client == "" {
		if peer, err := netip.ParseAddrPort(c.Request.RemoteAddr); err == nil {
			return peer.Addr().WithZone("").Unmap()
		}
	}
	return netip.Addr{}
}

func (a *App) blocklistedIP() gin.HandlerFunc {
	return func(c *gin.Context) {
		snapshot := a.ipBlocks.snapshot.Load()
		if snapshot == nil || len(snapshot.entries) == 0 {
			c.Next()
			return
		}
		// Gin trusts forwarding headers only from explicitly configured proxies.
		// Never inspect X-Forwarded-For or X-Real-IP ourselves.
		addr := blockClientAddress(c)
		if addr.IsValid() {
			for _, entry := range snapshot.entries {
				if entry.prefix.Contains(addr) {
					entry.hits.Add(1)
					a.ipBlocks.total.Add(1)
					c.Header("Cache-Control", "no-store")
					c.AbortWithStatusJSON(403, gin.H{"error": "access denied", "code": "IP_BLOCKED"})
					return
				}
			}
		}
		c.Next()
	}
}

func (a *App) adminIPBlocklist(c *gin.Context) {
	items := make([]gin.H, 0)
	if snapshot := a.ipBlocks.snapshot.Load(); snapshot != nil {
		for _, entry := range snapshot.entries {
			items = append(items, gin.H{"network": entry.rule.Network, "note": entry.rule.Note, "created_at": entry.rule.CreatedAt, "blocked": entry.hits.Load()})
		}
	}
	clientIP := ""
	if addr := blockClientAddress(c); addr.IsValid() {
		clientIP = addr.String()
	}
	c.JSON(200, gin.H{"items": items, "total_blocked": a.ipBlocks.total.Load(), "stats_since": a.startAt.UTC().Format(time.RFC3339Nano), "client_ip": clientIP, "max_rules": store.MaxIPBlockRules})
}

func (a *App) adminMutateIPBlocklist(c *gin.Context) {
	data, fields, err := artworkObject(c.Request.Body)
	remove := c.Request.Method == "DELETE"
	valid := err == nil && fields["network"] != nil && len(fields) <= 2
	for name := range fields {
		if name != "network" && (name != "note" || remove) {
			valid = false
		}
	}
	if !valid {
		apiError(c, 400, "provide network and an optional note")
		return
	}
	var req struct {
		Network string `json:"network"`
		Note    string `json:"note"`
	}
	if json.Unmarshal(data, &req) != nil {
		apiError(c, 400, "invalid network or note")
		return
	}
	if len(req.Note) > 200 || strings.IndexFunc(req.Note, unicode.IsControl) >= 0 {
		apiError(c, 400, "note must be at most 200 bytes without control characters")
		return
	}
	prefix, err := store.NormalizeIPBlockNetwork(req.Network)
	if errors.Is(err, store.ErrIPBlockUnsafe) {
		// 409 与“会误封当前访问 IP / 与已有规则冲突”同类：规则本身合法但被拒绝。
		apiError(c, 409, err.Error())
		return
	}
	if err != nil {
		apiError(c, 400, err.Error())
		return
	}
	if !remove {
		addr := blockClientAddress(c)
		if !addr.IsValid() {
			apiError(c, 400, "current client IP is unavailable; cannot safely add a rule")
			return
		}
		if prefix.Contains(addr) {
			apiError(c, 409, "cannot block the IP of this administrator request")
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	for !a.ipBlocks.edit.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			apiError(c, 503, "IP rules are busy; try again")
			return
		case <-timer.C:
		}
	}
	defer a.ipBlocks.edit.Unlock()
	// The body and edit-lock wait are untrusted delay points. Recheck the actor
	// immediately before admission, not just in the route's initial middleware.
	// The same deadline bounds the admission wait so a cancelled request cannot
	// keep the cross-request edit lock while waiting for the SQLite writer.
	if !a.admitAdminOperationContext(c, ctx) {
		return
	}
	rules, err := a.db.MutateIPBlockRules(ctx, prefix.String(), req.Note, remove)
	switch {
	case errors.Is(err, store.ErrIPBlockConflict), errors.Is(err, store.ErrIPBlockLimit), errors.Is(err, store.ErrIPBlockUnsafe):
		apiError(c, 409, err.Error())
		return
	case errors.Is(err, store.ErrIPBlockMissing):
		apiError(c, 404, "IP rule not found")
		return
	case err != nil:
		// Database details must never reach clients.
		apiError(c, 500, "IP rule could not be saved")
		return
	}
	a.ipBlocks.publish(rules)
	if remove {
		c.JSON(200, gin.H{"ok": true})
		return
	}
	for _, rule := range rules {
		if rule.Network == prefix.String() {
			c.JSON(201, gin.H{"network": rule.Network, "note": rule.Note, "created_at": rule.CreatedAt, "blocked": 0})
			return
		}
	}
	apiError(c, 500, "IP rule could not be confirmed")
}
