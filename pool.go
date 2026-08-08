package linkedin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrNoAccountAvailable is returned when every pool member is exhausted,
// cooling down, in use, or otherwise unavailable.
var ErrNoAccountAvailable = errors.New("linkedin: no account available in pool")

// Account describes one LinkedIn identity bound to a sticky egress proxy.
//
// Cookie session and proxy IP must stay paired: rotating cookies across IPs
// (or many accounts through one IP) correlates them in LinkedIn's risk systems.
type Account struct {
	// ID is a durable operator label (e.g. "burner-1"). Required.
	ID string
	// Auth is the browser session for this account. Required.
	Auth Auth
	// ProxyURL is the sticky residential/mobile proxy for this account
	// (http://user:pass@host:port). Required when the pool has more than
	// one account; strongly recommended even for a single account.
	ProxyURL string
	// Role defaults to RoleScraper when empty.
	Role Role
	// Pacing overrides DefaultBurnerPacing when non-nil.
	Pacing *HumanPacing
	// Browser overrides the default fingerprint when non-nil.
	Browser *BrowserProfile
	// CooldownUntil refuses traffic until this time (operator cool-off).
	CooldownUntil time.Time
}

// AccountStatus is a snapshot of one pool member for operator UIs.
type AccountStatus struct {
	ID                string    `json:"id"`
	Available         bool      `json:"available"`
	InUse             bool      `json:"in_use"`
	InCooldown        bool      `json:"in_cooldown"`
	BudgetExhausted   bool      `json:"budget_exhausted"`
	RequestsRemaining int       `json:"requests_remaining"`
	ProxyHost         string    `json:"proxy_host,omitempty"`
	SkipUntil         time.Time `json:"skip_until,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
}

// Pool rotates work across N Clients. Each member owns its own Auth, cookie
// jar, pacer/daily budget, browser fingerprint, and sticky proxy — never
// shared across members.
type Pool struct {
	mu      sync.Mutex
	members []*poolMember
	next    int
}

type poolMember struct {
	id        string
	client    *Client
	proxyHost string
	inUse     bool
	skipUntil time.Time
	lastErr   string
}

// Lease is a temporary exclusive claim on one pool account. Hold it for the
// full multi-call unit of work (e.g. profile + posts + comments) so affinity
// stays on one identity + IP.
type Lease struct {
	ID     string
	Client *Client
	pool   *Pool
	member *poolMember
	once   sync.Once
}

// NewPool builds one Client per Account. Common opts (retry, etc.) apply to
// every member; per-account proxy/pacing/browser/role always win.
func NewPool(accounts []Account, common ...Option) (*Pool, error) {
	if len(accounts) == 0 {
		return nil, fmt.Errorf("linkedin: pool requires at least one account")
	}
	multi := len(accounts) > 1
	seen := map[string]struct{}{}
	p := &Pool{members: make([]*poolMember, 0, len(accounts))}
	for i, acc := range accounts {
		id := strings.TrimSpace(acc.ID)
		if id == "" {
			return nil, fmt.Errorf("linkedin: account[%d] missing ID", i)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("linkedin: duplicate account ID %q", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(acc.Auth.LiAt) == "" || strings.TrimSpace(acc.Auth.CSRF) == "" {
			return nil, fmt.Errorf("linkedin: account %q missing LiAt/CSRF", id)
		}
		proxyRaw := strings.TrimSpace(acc.ProxyURL)
		if multi && proxyRaw == "" {
			return nil, fmt.Errorf("linkedin: account %q requires ProxyURL when pool size > 1", id)
		}
		var proxyURL *url.URL
		var proxyHost string
		if proxyRaw != "" {
			u, err := url.Parse(proxyRaw)
			if err != nil || u.Host == "" {
				return nil, fmt.Errorf("linkedin: account %q invalid ProxyURL: %q", id, proxyRaw)
			}
			proxyURL = u
			proxyHost = u.Host
		}

		role := acc.Role
		if role == RoleUnspecified {
			role = RoleScraper
		}
		pacing := DefaultBurnerPacing(nil)
		if acc.Pacing != nil {
			pacing = *acc.Pacing
		}
		opts := append([]Option{}, common...)
		opts = append(opts,
			WithRole(role),
			WithHumanPacing(pacing),
		)
		if proxyURL != nil {
			opts = append(opts, WithProxy(proxyURL))
		}
		if acc.Browser != nil {
			opts = append(opts, WithBrowserProfile(*acc.Browser))
		} else if i%2 == 1 {
			// Alternate fingerprints across burners when caller doesn't pin one.
			bp := WindowsChromeET()
			opts = append(opts, WithBrowserProfile(bp))
		}
		if !acc.CooldownUntil.IsZero() {
			opts = append(opts, WithCooldownUntil(acc.CooldownUntil))
		}
		client := New(acc.Auth, opts...)
		p.members = append(p.members, &poolMember{
			id:        id,
			client:    client,
			proxyHost: proxyHost,
		})
	}
	return p, nil
}

// Len returns the number of accounts in the pool.
func (p *Pool) Len() int {
	if p == nil {
		return 0
	}
	return len(p.members)
}

// Acquire claims the next available account. The lease must be Released.
// Blocks until ctx is done if every member is temporarily in use but at
// least one is otherwise healthy; returns ErrNoAccountAvailable when none
// can serve traffic (budget / cooldown / skip).
func (p *Pool) Acquire(ctx context.Context) (*Lease, error) {
	if p == nil || len(p.members) == 0 {
		return nil, ErrNoAccountAvailable
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if lease, err := p.tryAcquire(); lease != nil || (err != nil && !errors.Is(err, errAllBusy)) {
			return lease, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

var errAllBusy = errors.New("linkedin: all pool accounts busy")

func (p *Pool) tryAcquire() (*Lease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.members)
	busyOnly := true
	anyHealthy := false
	for i := 0; i < n; i++ {
		idx := (p.next + i) % n
		m := p.members[idx]
		reason := m.unavailableLocked(time.Now())
		if reason == "" {
			anyHealthy = true
			if m.inUse {
				continue
			}
			busyOnly = false
			m.inUse = true
			p.next = (idx + 1) % n
			return &Lease{ID: m.id, Client: m.client, pool: p, member: m}, nil
		}
		if reason == "in_use" {
			continue
		}
		// budget/cooldown/skip — not merely busy
		busyOnly = false
	}
	if anyHealthy && busyOnly {
		return nil, errAllBusy
	}
	return nil, ErrNoAccountAvailable
}

func (m *poolMember) softBlockedLocked(now time.Time) string {
	if !m.skipUntil.IsZero() && now.Before(m.skipUntil) {
		return "skip"
	}
	if m.client.InCooldown() {
		return "cooldown"
	}
	if m.client.BudgetExhausted() {
		return "budget"
	}
	return ""
}

func (m *poolMember) unavailableLocked(now time.Time) string {
	if m.inUse {
		return "in_use"
	}
	return m.softBlockedLocked(now)
}

// Release returns the account to the pool. Safe to call multiple times.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.pool == nil || l.member == nil {
			return
		}
		l.pool.mu.Lock()
		l.member.inUse = false
		l.pool.mu.Unlock()
	})
}

// ReportError marks the leased account unavailable when err indicates the
// identity should rest (daily budget, restriction, challenge, cooldown).
func (l *Lease) ReportError(err error) {
	if l == nil || l.pool == nil || l.member == nil || err == nil {
		return
	}
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	l.member.lastErr = err.Error()
	switch {
	case errors.Is(err, ErrDailyBudget):
		l.member.skipUntil = endOfLocalDay(time.Now())
	case errors.Is(err, ErrAccountRestricted), errors.Is(err, ErrChallengeRequired):
		l.member.skipUntil = time.Now().Add(24 * time.Hour)
	case errors.Is(err, ErrInCooldown):
		if t := l.Client.CooldownUntil(); !t.IsZero() {
			l.member.skipUntil = t
		} else {
			l.member.skipUntil = time.Now().Add(time.Hour)
		}
	case errors.Is(err, ErrUnauthorized):
		l.member.skipUntil = time.Now().Add(6 * time.Hour)
	}
}

func endOfLocalDay(now time.Time) time.Time {
	loc := now.Location()
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

// Status returns per-account availability for dashboards.
func (p *Pool) Status() []AccountStatus {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := make([]AccountStatus, 0, len(p.members))
	for _, m := range p.members {
		blocked := m.softBlockedLocked(now)
		st := AccountStatus{
			ID:                m.id,
			InUse:             m.inUse,
			InCooldown:        m.client.InCooldown(),
			BudgetExhausted:   m.client.BudgetExhausted(),
			RequestsRemaining: m.client.RequestsRemaining(),
			ProxyHost:         m.proxyHost,
			SkipUntil:         m.skipUntil,
			LastError:         m.lastErr,
			Available:         !m.inUse && blocked == "",
		}
		out = append(out, st)
	}
	return out
}

// Available reports how many accounts can accept new work right now.
func (p *Pool) Available() int {
	n := 0
	for _, st := range p.Status() {
		if st.Available && !st.InUse {
			n++
		}
	}
	return n
}
