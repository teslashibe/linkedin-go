// Package linkedin provides a Go client for LinkedIn's Voyager API.
//
// It supports authenticated people search with full UI-parity filters,
// profile scraping, and human-readable entity resolution (locations,
// companies, schools). Zero production dependencies — stdlib only.
//
// Requires session cookies obtained from an authenticated LinkedIn browser
// session (li_at, JSESSIONID/csrf-token).
package linkedin

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Auth holds the LinkedIn session cookies required for Voyager API access.
//
// LiAt and CSRF are required. The richer the ExtraCookies set the more the
// session will look like a real browser to LinkedIn's bot detection — at a
// minimum copy across `bcookie`, `bscookie`, `lidc`, `li_sugr`, `liap`,
// `lang`, `lms_ads`, `lms_analytics`, `UserMatchHistory`, `AnalyticsSyncHistory`
// from a fresh browser session.
type Auth struct {
	LiAt         string
	CSRF         string
	JSESSIONID   string // optional; defaults to CSRF value
	ExtraCookies string // optional; additional browser cookies appended verbatim (e.g. "bcookie=...; bscookie=...")
}

// Role classifies a client by intended use. The library does not enforce
// roles itself — callers use AssertRole at startup to refuse to perform a
// scrape with primary cookies (or vice versa). See the multi-account
// playbook for the strict-separation rationale.
type Role string

const (
	// RoleScraper is for read-only burner accounts that perform people
	// search and profile fetches under stealth pacing.
	RoleScraper Role = "scraper"
	// RolePrimary is for the high-trust account that handles outbound
	// writes (InMails, messages, group posts, accepting connections).
	RolePrimary Role = "primary"
	// RoleUnspecified is the zero value; AssertRole always allows.
	RoleUnspecified Role = ""
)

// Client is a LinkedIn Voyager API client.
type Client struct {
	auth           Auth
	httpClient     *http.Client
	jar            *cookiejar.Jar
	browser        BrowserProfile
	role           Role
	proxyURL       *url.URL
	cooldownUntil  time.Time // operator-imposed cool-off; ErrInCooldown until this passes
	searchQueryID  string
	profileQueryID string
	maxRetries     int
	retryBase      time.Duration
	minGap         time.Duration
	pacer          *pacer

	warmedUp atomic.Bool

	// vanityURN caches vanity-name → member URN resolutions for the life of
	// this warm affine client so GetUserPosts can skip repeated GetProfile hops.
	vanityURN sync.Map // string (lower vanity) → string (urn)

	// userAgent is kept for compatibility with WithUserAgent; if set it
	// overrides browser.UserAgent.
	userAgent string

	rlMu      sync.Mutex
	rlState   RateLimitState
	gapMu     sync.Mutex
	lastReqAt time.Time

	// reqMu serializes the entire authenticated-request lifecycle
	// (pacer wait + HTTP roundtrip + retries + response processing). One
	// LinkedIn API call in flight per Client at a time — anything else
	// looks like a script to LinkedIn's bot detection no matter how good
	// the per-request gap is. Concurrent callers (e.g. an MCP server
	// receiving 10 parallel tool calls from Claude Desktop) queue here.
	//
	// pendingReqs counts goroutines waiting on or holding the lock; used
	// only for observability ("queued behind N").
	reqMu       sync.Mutex
	pendingReqs atomic.Int32
}

const (
	defaultSearchQueryID  = "voyagerSearchDashClusters.7cdf88d3366ad02cc5a3862fb9a24085"
	defaultProfileQueryID = "voyagerIdentityDashProfiles.8ca6ef03f32147a4d49324ed99a3d978"
	defaultMaxRetries     = 3
	defaultRetryBase      = 500 * time.Millisecond
	defaultMinGap         = 300 * time.Millisecond
)

// linkedinBaseURL is reused when seeding the cookie jar with the user's
// initial cookies and when reading server Set-Cookie updates.
var linkedinBaseURL = mustParseURL("https://www.linkedin.com/")

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// New creates a new LinkedIn client with the given auth credentials and options.
func New(auth Auth, opts ...Option) *Client {
	if auth.JSESSIONID == "" {
		auth.JSESSIONID = auth.CSRF
	}
	jar, _ := cookiejar.New(nil)
	c := &Client{
		auth:           auth,
		httpClient:     &http.Client{Timeout: 30 * time.Second, Jar: jar},
		jar:            jar,
		browser:        MacChromePT(),
		searchQueryID:  defaultSearchQueryID,
		profileQueryID: defaultProfileQueryID,
		maxRetries:     defaultMaxRetries,
		retryBase:      defaultRetryBase,
		minGap:         defaultMinGap,
	}
	for _, o := range opts {
		o(c)
	}

	if c.userAgent != "" {
		c.browser.UserAgent = c.userAgent
	}

	// Seed the jar with the cookies the caller provided.
	c.seedJar()

	// If WithHTTPClient was used, ensure the user's client has our jar
	// attached so Set-Cookie updates roundtrip correctly. We don't clobber a
	// jar the user explicitly set.
	if c.httpClient.Jar == nil {
		c.httpClient.Jar = c.jar
	} else {
		// Use the user-supplied jar as the canonical store.
		c.jar = jarFromClient(c.httpClient)
		c.seedJar()
	}

	// Wire the proxy through the http.Client's transport, if configured.
	// We clone the existing transport to avoid mutating shared state and
	// preserve any TLS / connection pooling tuning the caller set.
	if c.proxyURL != nil {
		c.applyProxy()
	}
	c.ensureJSESSIONQuoteTransport()

	return c
}

// applyProxy attaches c.proxyURL to the http.Client's transport, cloning
// http.DefaultTransport when none is configured. Safe to call multiple times
// — subsequent calls overwrite the Proxy func without disturbing other
// transport settings.
func (c *Client) applyProxy() {
	var base *http.Transport
	if existing, ok := c.httpClient.Transport.(*http.Transport); ok && existing != nil {
		base = existing.Clone()
	} else {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	base.Proxy = http.ProxyURL(c.proxyURL)
	c.httpClient.Transport = base
}

func jarFromClient(hc *http.Client) *cookiejar.Jar {
	if j, ok := hc.Jar.(*cookiejar.Jar); ok {
		return j
	}
	// Fallback: replace it with one of ours.
	j, _ := cookiejar.New(nil)
	hc.Jar = j
	return j
}

// seedJar pushes Auth.LiAt + Auth.JSESSIONID + parsed ExtraCookies into the
// jar so subsequent requests carry them automatically. Call once after
// constructing the client.
//
// Cookie.Value must NOT contain `"` — Go's net/http strips quoted bytes and
// logs "invalid byte '"' in Cookie.Value", which corrupts LinkedIn session
// cookies (especially bcookie / quoted Playwright exports). JSESSIONID is
// re-quoted on the wire by jsessionQuoteTransport.
func (c *Client) seedJar() {
	if c.jar == nil {
		return
	}
	jsid := strings.ReplaceAll(c.auth.JSESSIONID, `"`, "")
	liAt := strings.ReplaceAll(c.auth.LiAt, `"`, "")
	cookies := []*http.Cookie{
		{Name: "li_at", Value: liAt, Domain: ".linkedin.com", Path: "/", Secure: true, HttpOnly: true},
		{Name: "JSESSIONID", Value: jsid, Domain: ".linkedin.com", Path: "/", Secure: true},
	}
	for _, kv := range parseCookieString(c.auth.ExtraCookies) {
		// Skip li_at / JSESSIONID if the caller accidentally repeats them.
		if kv.Name == "li_at" || kv.Name == "JSESSIONID" {
			continue
		}
		cookies = append(cookies, &http.Cookie{
			Name:   kv.Name,
			Value:  strings.ReplaceAll(kv.Value, `"`, ""),
			Domain: ".linkedin.com",
			Path:   "/",
			Secure: true,
		})
	}
	c.jar.SetCookies(linkedinBaseURL, cookies)
}

type cookieKV struct{ Name, Value string }

// parseCookieString parses "name=value; name2=value2" into ordered KV pairs.
// Values containing semicolons in quoted strings are not currently supported;
// LinkedIn cookies don't use that. Surrounding quotes are stripped so values
// are safe for net/http Cookie.Value.
func parseCookieString(s string) []cookieKV {
	var out []cookieKV
	for _, raw := range strings.Split(s, ";") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			continue
		}
		out = append(out, cookieKV{
			Name:  strings.TrimSpace(raw[:eq]),
			Value: strings.ReplaceAll(strings.TrimSpace(raw[eq+1:]), `"`, ""),
		})
	}
	return out
}

// jsessionQuoteTransport rewrites the outbound Cookie header so JSESSIONID is
// quoted on the wire (LinkedIn's historical format) without putting `"` into
// http.Cookie.Value (which Go rejects).
type jsessionQuoteTransport struct {
	base http.RoundTripper
}

func (t *jsessionQuoteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if h := req.Header.Get("Cookie"); h != "" {
		req.Header.Set("Cookie", quoteJSESSIONIDCookie(h))
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func quoteJSESSIONIDCookie(h string) string {
	parts := strings.Split(h, ";")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) < 11 || !strings.EqualFold(p[:10], "JSESSIONID") || p[10] != '=' {
			continue
		}
		v := strings.Trim(p[11:], `"`)
		parts[i] = `JSESSIONID="` + v + `"`
	}
	return strings.Join(parts, "; ")
}

func (c *Client) ensureJSESSIONQuoteTransport() {
	base := c.httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if _, ok := base.(*jsessionQuoteTransport); ok {
		return
	}
	c.httpClient.Transport = &jsessionQuoteTransport{base: base}
}

// Option configures a Client.
type Option func(*Client)

// WithUserAgent overrides the default browser User-Agent string.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithQueryIDs overrides the Voyager GraphQL query IDs used for search and
// profile endpoints. Useful when LinkedIn rotates these during deploys.
func WithQueryIDs(searchID, profileID string) Option {
	return func(c *Client) {
		if searchID != "" {
			c.searchQueryID = searchID
		}
		if profileID != "" {
			c.profileQueryID = profileID
		}
	}
}

// WithRetry configures retry behaviour. Set maxAttempts to 0 to disable retries.
// Default: 3 attempts, 500ms exponential base (500ms → 1s → 2s).
func WithRetry(maxAttempts int, base time.Duration) Option {
	return func(c *Client) {
		c.maxRetries = maxAttempts
		c.retryBase = base
	}
}

// WithHTTPClient replaces the default http.Client. Nil is ignored.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithMinRequestGap sets the minimum delay between consecutive requests.
// Default: 300ms. Ignored when WithHumanPacing is also provided.
func WithMinRequestGap(d time.Duration) Option {
	return func(c *Client) { c.minGap = d }
}

// WithBrowserProfile pins the request fingerprint (User-Agent, sec-ch-ua,
// timezone, display dimensions, x-li-track) to a specific browser profile.
// Use MacChromePT() / WindowsChromeET() / a custom BrowserProfile.
//
// Default: MacChromePT(). Recommended: pin per burner account so the
// fingerprint stays consistent across runs.
func WithBrowserProfile(bp BrowserProfile) Option {
	return func(c *Client) { c.browser = bp }
}

// WithHumanPacing enables stochastic, human-like request pacing — randomised
// gap, periodic reading pauses, occasional long distractions, working-hours
// window, and a daily request budget. Strongly recommended when scraping with
// any single account, especially burner accounts.
//
// Pass DefaultBurnerPacing(loc) for a sensible starting point, or build a
// HumanPacing manually for custom rhythms.
func WithHumanPacing(p HumanPacing) Option {
	return func(c *Client) { c.pacer = newPacer(p) }
}

// WithProxy routes all client traffic through the given proxy URL. Use to
// pin a burner account to a specific egress IP (residential proxy, mobile
// proxy, etc.) — LinkedIn fingerprints the IP separately from cookies, so
// multiple burners on one IP all get correlated by their bot detection.
//
// Supports user:pass auth in the URL: http://user:pass@host:port. Composes
// cleanly with WithHTTPClient — the proxy is applied to the client's
// transport, preserving any TLS/timeout/connection-pool tuning.
func WithProxy(proxyURL *url.URL) Option {
	return func(c *Client) { c.proxyURL = proxyURL }
}

// WithRole tags the client by intended use. The library does not enforce
// roles itself — callers use AssertRole at startup to refuse to perform a
// scrape with primary cookies (or vice versa).
//
// Strongly recommended for any deployment that has both a primary outreach
// account and burner scraping accounts: it makes "wrong cookies in the
// wrong binary" a startup error instead of a quiet account-restriction.
func WithRole(r Role) Option {
	return func(c *Client) { c.role = r }
}

// WithCooldownUntil refuses every authenticated request with ErrInCooldown
// until the given time passes. Use after a LinkedIn restriction to prevent
// re-poking the account before the auto-lift window — even an accidental
// agent retry can reset LinkedIn's restriction timer.
//
// A zero time disables the cooldown (default). The check happens before
// any HTTP work, so cooldown'd requests are essentially free.
func WithCooldownUntil(t time.Time) Option {
	return func(c *Client) { c.cooldownUntil = t }
}

// CooldownUntil returns the configured cooldown deadline (zero if none).
func (c *Client) CooldownUntil() time.Time { return c.cooldownUntil }

// InCooldown reports whether the client is currently refusing requests.
func (c *Client) InCooldown() bool {
	return !c.cooldownUntil.IsZero() && time.Now().Before(c.cooldownUntil)
}

// Role returns the role this client was tagged with at construction.
// Returns RoleUnspecified if WithRole was not used.
func (c *Client) Role() Role { return c.role }

// AssertRole returns an error if the client's role does not match want.
// Returns nil if the client has no role set (RoleUnspecified) or if the
// roles match. Designed to be called at command startup:
//
//	if err := client.AssertRole(linkedin.RoleScraper); err != nil {
//	    log.Fatalf("refusing to scrape with non-scraper credentials: %v", err)
//	}
func (c *Client) AssertRole(want Role) error {
	if c.role == RoleUnspecified || c.role == want {
		return nil
	}
	return fmt.Errorf("%w: client role is %q, caller requires %q",
		ErrRoleMismatch, c.role, want)
}

// RateLimit returns a snapshot of the most recently observed rate-limit state.
func (c *Client) RateLimit() RateLimitState {
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	return c.rlState
}

// RequestsRemaining returns how many calls are left under the daily budget,
// or 0 if pacing/budget are not configured.
func (c *Client) RequestsRemaining() int {
	if c.pacer == nil {
		return 0
	}
	return c.pacer.requestsRemaining()
}

// BudgetExhausted reports whether the human pacer's daily request budget is
// configured and already consumed. False when pacing/budget are disabled.
func (c *Client) BudgetExhausted() bool {
	if c == nil || c.pacer == nil || c.pacer.policy.DailyBudget <= 0 {
		return false
	}
	return c.pacer.requestsRemaining() <= 0
}

// WithProxyURL is WithProxy for a raw proxy URL string
// (http://user:pass@host:port). Invalid URLs are ignored.
func WithProxyURL(raw string) Option {
	return func(c *Client) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return
		}
		c.proxyURL = u
	}
}
