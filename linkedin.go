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
	"net/http"
	"time"
)

// Auth holds the LinkedIn session cookies required for Voyager API access.
type Auth struct {
	LiAt       string
	CSRF       string
	JSESSIONID string // optional; defaults to CSRF value
}

// Client is a LinkedIn Voyager API client.
type Client struct {
	auth           Auth
	httpClient     *http.Client
	userAgent      string
	searchQueryID  string
	profileQueryID string
	maxRetries     int
	retryBase      time.Duration
}

const (
	defaultUserAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	defaultSearchQueryID  = "voyagerSearchDashClusters.b0928897b71bd00a5a7291755dcd64f0"
	defaultProfileQueryID = "voyagerIdentityDashProfiles.53d2faade97e29ac0dea5a4f23a4508f"
	defaultMaxRetries     = 3
	defaultRetryBase      = 500 * time.Millisecond
)

// New creates a new LinkedIn client with the given auth credentials and options.
func New(auth Auth, opts ...Option) *Client {
	if auth.JSESSIONID == "" {
		auth.JSESSIONID = auth.CSRF
	}
	c := &Client{
		auth:           auth,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		userAgent:      defaultUserAgent,
		searchQueryID:  defaultSearchQueryID,
		profileQueryID: defaultProfileQueryID,
		maxRetries:     defaultMaxRetries,
		retryBase:      defaultRetryBase,
	}
	for _, o := range opts {
		o(c)
	}
	return c
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

// WithHTTPClient replaces the default http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}
