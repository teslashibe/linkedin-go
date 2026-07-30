package linkedin

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	baseURL = "https://www.linkedin.com"
	apiBase = baseURL + "/voyager/api"
)

// makeRequest performs an authenticated Voyager API request with configurable
// retry. Retries trigger on 429 and 5xx only; 401/403/404 fail immediately.
func (c *Client) makeRequest(ctx context.Context, requestURL string) ([]byte, error) {
	if c.auth.LiAt == "" || c.auth.CSRF == "" {
		return nil, ErrInvalidAuth
	}
	if err := c.checkCooldown(); err != nil {
		return nil, err
	}

	release := c.acquireRequestSlot(ctx)
	if release == nil {
		return nil, mapCtxErr(ctx.Err())
	}
	defer release()

	if err := c.warmUp(ctx); err != nil {
		// warm-up failure is fatal — without it Voyager calls almost always
		// trip the bot detector, since they look like out-of-context API hits.
		return nil, err
	}

	attempts := c.maxRetries
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			wait := c.backoff(i)
			if lastErr != nil {
				if ra, ok := lastErr.(*retryAfterError); ok && ra.wait > wait {
					wait = ra.wait
				}
			}
			select {
			case <-ctx.Done():
				return nil, mapCtxErr(ctx.Err())
			case <-time.After(wait):
			}
		}

		body, err := c.doRequest(ctx, requestURL)
		if err == nil {
			return body, nil
		}
		if isNonRecoverable(err) {
			return nil, err
		}
		lastErr = err
	}

	return nil, lastErr
}

func (c *Client) doRequest(ctx context.Context, requestURL string) ([]byte, error) {
	if err := c.gateBeforeRequest(ctx); err != nil {
		return nil, mapCtxErr(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	headers := make(map[string]string, 16)
	c.applyVoyagerHeaders(headers, requestURL, false)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()

	c.absorbSetCookies(resp)
	c.updateRateLimit(resp.Header)

	if err := c.classifyResponse(resp, requestURL); err != nil {
		return nil, err
	}

	body, rerr := readResponseBody(resp)
	if rerr != nil {
		return nil, rerr
	}

	if err := detectRestrictionInBody(body); err != nil {
		return nil, err
	}
	return body, nil
}

// doPublicGet performs an unauthenticated GET against a LinkedIn public endpoint
// (no Voyager headers, no session cookies). Honours the client's pacing.
func (c *Client) doPublicGet(ctx context.Context, requestURL string) ([]byte, error) {
	if err := c.checkCooldown(); err != nil {
		return nil, err
	}
	release := c.acquireRequestSlot(ctx)
	if release == nil {
		return nil, ctx.Err()
	}
	defer release()

	if err := c.gateBeforeRequest(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	req.Header.Set("User-Agent", c.browser.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", c.browser.AcceptLanguage)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Referer", referrerFor(requestURL))
	req.Header.Set("sec-ch-ua", c.browser.SecChUA)
	req.Header.Set("sec-ch-ua-mobile", c.browser.SecChUAMobile)
	req.Header.Set("sec-ch-ua-platform", c.browser.SecChUAPlatform)
	req.Header.Set("sec-fetch-dest", "empty")
	req.Header.Set("sec-fetch-mode", "cors")
	req.Header.Set("sec-fetch-site", "same-origin")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()

	c.absorbSetCookies(resp)

	switch {
	case resp.StatusCode == http.StatusOK:
		// fall through
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := parseRetryAfter(resp.Header.Get("Retry-After"), 60*time.Second)
		c.pacer.applyServerBackoff(wait)
		return nil, &retryAfterError{wait: wait, err: ErrRateLimited}
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)
	default:
		return nil, &nonRetryableError{fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)}
	}

	return readResponseBody(resp)
}

func readResponseBody(resp *http.Response) ([]byte, error) {
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("%w: gzip: %v", ErrRequestFailed, err)
		}
		defer gr.Close()
		reader = gr
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("%w: reading response body: %v", ErrRequestFailed, err)
	}
	return body, nil
}

// makePostRequest performs an authenticated Voyager API POST request with the
// same retry semantics as makeRequest.
func (c *Client) makePostRequest(ctx context.Context, requestURL string, payload []byte) ([]byte, error) {
	if c.auth.LiAt == "" || c.auth.CSRF == "" {
		return nil, ErrInvalidAuth
	}
	if err := c.checkCooldown(); err != nil {
		return nil, err
	}

	release := c.acquireRequestSlot(ctx)
	if release == nil {
		return nil, mapCtxErr(ctx.Err())
	}
	defer release()

	if err := c.warmUp(ctx); err != nil {
		return nil, err
	}

	attempts := c.maxRetries
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			wait := c.backoff(i)
			if lastErr != nil {
				if ra, ok := lastErr.(*retryAfterError); ok && ra.wait > wait {
					wait = ra.wait
				}
			}
			select {
			case <-ctx.Done():
				return nil, mapCtxErr(ctx.Err())
			case <-time.After(wait):
			}
		}

		body, err := c.doPostRequest(ctx, requestURL, payload)
		if err == nil {
			return body, nil
		}
		if isNonRecoverable(err) {
			return nil, err
		}
		lastErr = err
	}

	return nil, lastErr
}

func (c *Client) doPostRequest(ctx context.Context, requestURL string, payload []byte) ([]byte, error) {
	if err := c.gateBeforeRequest(ctx); err != nil {
		return nil, mapCtxErr(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	headers := make(map[string]string, 16)
	c.applyVoyagerHeaders(headers, requestURL, true)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()

	c.absorbSetCookies(resp)
	c.updateRateLimit(resp.Header)

	if err := c.classifyResponse(resp, requestURL); err != nil {
		return nil, err
	}

	body, rerr := readResponseBody(resp)
	if rerr != nil {
		return nil, rerr
	}
	if err := detectRestrictionInBody(body); err != nil {
		return nil, err
	}
	return body, nil
}

// gateBeforeRequest applies humanized pacing (or the legacy minGap path),
// returning errors from the daily budget / working-hours guard.
func (c *Client) gateBeforeRequest(ctx context.Context) error {
	if c.pacer != nil {
		return mapCtxErr(c.pacer.wait(ctx))
	}
	c.waitForGap(ctx)
	if ctx.Err() != nil {
		return mapCtxErr(ctx.Err())
	}
	return nil
}

// checkCooldown returns ErrInCooldown (with the deadline embedded in the
// message) when an operator-imposed cooldown is active. The message format
// is stable so MCP/UI layers can grep for it without parsing wrapped errors.
func (c *Client) checkCooldown() error {
	if c.cooldownUntil.IsZero() || !time.Now().Before(c.cooldownUntil) {
		return nil
	}
	return fmt.Errorf("%w: until %s (%s remaining)",
		ErrInCooldown,
		c.cooldownUntil.Format(time.RFC3339),
		time.Until(c.cooldownUntil).Round(time.Second),
	)
}

// acquireRequestSlot enforces "one request in flight per Client at a time".
// Concurrent callers (e.g. an MCP server fielding 10 parallel tool calls)
// queue here. The mutex is held through pacer-wait + HTTP roundtrip + retries
// + response processing — releasing it earlier would let the next caller
// fire its HTTP request before this one finished, defeating stealth pacing.
//
// Returns a release function the caller MUST call (typically via defer), or
// nil if ctx was already cancelled. Cancellation while waiting is honoured;
// callers that timed out cleanly hand the slot to the next waiter.
//
// pendingReqs is incremented on entry and decremented on release; it's
// observation-only (read via PendingRequests for status reporting).
func (c *Client) acquireRequestSlot(ctx context.Context) func() {
	if ctx.Err() != nil {
		return nil
	}
	c.pendingReqs.Add(1)

	// Try a fast lock first; fall back to a context-aware wait so an upstream
	// timeout/cancellation doesn't get stuck behind a long pacer sleep.
	done := make(chan struct{})
	go func() {
		c.reqMu.Lock()
		close(done)
	}()

	select {
	case <-done:
		return func() {
			c.reqMu.Unlock()
			c.pendingReqs.Add(-1)
		}
	case <-ctx.Done():
		// We won't actually hold the lock — but the goroutine above will
		// eventually acquire it, so we have to drain it to keep the count
		// correct. Spin off a release as soon as it lands.
		go func() {
			<-done
			c.reqMu.Unlock()
			c.pendingReqs.Add(-1)
		}()
		return nil
	}
}

// PendingRequests reports the number of goroutines currently waiting on or
// holding the per-client request slot. 0 means idle; >1 means concurrent
// callers are queued (e.g. the host application fired parallel tool calls).
// Useful for surfacing "queued behind N" in MCP / status output.
func (c *Client) PendingRequests() int {
	return int(c.pendingReqs.Load())
}

// classifyResponse maps an HTTP response to one of our sentinel errors,
// detecting account restrictions, checkpoints, redirects to the login wall,
// and rate-limit responses. Returns nil if the response is a normal 2xx.
func (c *Client) classifyResponse(resp *http.Response, requestURL string) error {
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		// LinkedIn sometimes returns the restriction page as 200 OK with HTML
		// even for Voyager API URLs. Catch it before downstream parsing
		// chokes on non-JSON.
		if strings.Contains(ct, "text/html") {
			return ErrAccountRestricted
		}
		return nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc := resp.Header.Get("Location")
		switch {
		case strings.Contains(loc, "/checkpoint/"):
			return ErrChallengeRequired
		case strings.Contains(loc, "/uas/login"), strings.Contains(loc, "/login"):
			return ErrUnauthorized
		case strings.Contains(loc, "account-restricted"), strings.Contains(loc, "/restricted"):
			return ErrAccountRestricted
		default:
			return ErrUnauthorized
		}
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		// 403 on Voyager often means "account is restricted" — distinguish
		// from a generic auth failure by sniffing the response body.
		body, _ := readResponseBody(resp)
		if detectRestrictionInBody(body) != nil {
			return ErrAccountRestricted
		}
		return ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := parseRetryAfter(resp.Header.Get("Retry-After"), 60*time.Second)
		c.rlMu.Lock()
		c.rlState.Remaining = 0
		c.rlState.RetryAfter = wait
		if c.rlState.Reset.IsZero() || time.Until(c.rlState.Reset) < wait {
			c.rlState.Reset = time.Now().Add(wait)
		}
		c.rlMu.Unlock()
		c.pacer.applyServerBackoff(wait)
		c.gapMu.Lock()
		if earliest := time.Now().Add(wait); c.lastReqAt.Before(earliest) {
			c.lastReqAt = earliest
		}
		c.gapMu.Unlock()
		return &retryAfterError{wait: wait, err: ErrRateLimited}
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)
	default:
		return &nonRetryableError{fmt.Errorf("%w: HTTP %d (%s)", ErrRequestFailed, resp.StatusCode, requestURL)}
	}
}

// detectRestrictionInBody returns ErrAccountRestricted / ErrChallengeRequired
// if the response body contains the canonical restriction or checkpoint
// markers. Used to upgrade ambiguous 200/403 responses.
func detectRestrictionInBody(body []byte) error {
	if len(body) == 0 {
		return nil
	}
	low := strings.ToLower(string(body[:min(4096, len(body))]))
	if strings.Contains(low, "your account is temporarily restricted") ||
		strings.Contains(low, "account-restricted") ||
		strings.Contains(low, "we detected the use of") ||
		strings.Contains(low, "we've restricted your account") ||
		strings.Contains(low, "weve restricted your account") {
		return ErrAccountRestricted
	}
	if strings.Contains(low, "/checkpoint/challenge") ||
		strings.Contains(low, "security verification") ||
		strings.Contains(low, "let's do a quick security check") {
		return ErrChallengeRequired
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// absorbSetCookies refreshes the cookie jar from the response so JSESSIONID
// rotation, lidc updates, etc. are picked up automatically. Runs even when
// the http.Client already has a Jar attached so user-supplied transports
// (which sometimes drop the jar) still update our authoritative copy.
func (c *Client) absorbSetCookies(resp *http.Response) {
	if c.jar == nil {
		return
	}
	cks := resp.Cookies()
	for _, ck := range cks {
		ck.Value = strings.ReplaceAll(ck.Value, `"`, "")
	}
	c.jar.SetCookies(linkedinBaseURL, cks)
}

// HealthCheck verifies the authenticated session is live by opening /feed/.
// Prefer this over Voyager list endpoints (messaging/groups) for login verify —
// those return opaque 404/500 for new or lightly-used accounts even when the
// browser session is valid.
func (c *Client) HealthCheck(ctx context.Context) error {
	return c.warmUp(ctx)
}

// warmUp performs a one-time GET /feed/ on first authenticated use to look
// like a real session opening. Without this Voyager API hits look like a
// process talking directly to the API with no UI context, which LinkedIn's
// detection penalises heavily.
func (c *Client) warmUp(ctx context.Context) error {
	if c.warmedUp.Load() {
		return nil
	}
	if err := c.gateBeforeRequest(ctx); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/feed/", nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	bp := c.browser
	req.Header.Set("User-Agent", bp.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", bp.AcceptLanguage)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("sec-ch-ua", bp.SecChUA)
	req.Header.Set("sec-ch-ua-mobile", bp.SecChUAMobile)
	req.Header.Set("sec-ch-ua-platform", bp.SecChUAPlatform)
	req.Header.Set("sec-fetch-dest", "document")
	req.Header.Set("sec-fetch-mode", "navigate")
	req.Header.Set("sec-fetch-site", "none")
	req.Header.Set("sec-fetch-user", "?1")
	req.Header.Set("upgrade-insecure-requests", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return wrapWarmUpTransportErr(err)
	}
	defer resp.Body.Close()
	c.absorbSetCookies(resp)

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	// If the warm-up itself trips a restriction page, fail fast — the account
	// is already burned and there's no point hitting Voyager.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := resp.Header.Get("Location")
		switch {
		case strings.Contains(loc, "/checkpoint/"):
			return ErrChallengeRequired
		case strings.Contains(loc, "/uas/login"), strings.Contains(loc, "/login"):
			return ErrUnauthorized
		case strings.Contains(loc, "account-restricted"), strings.Contains(loc, "/restricted"):
			return ErrAccountRestricted
		}
	}
	if resp.StatusCode == http.StatusOK {
		if err := detectRestrictionInBody(body); err != nil {
			return err
		}
	}

	c.warmedUp.Store(true)
	return nil
}

func (c *Client) backoff(attempt int) time.Duration {
	return c.retryBase * time.Duration(math.Pow(2, float64(attempt-1)))
}

// retryAfterError wraps a sentinel and carries the server-suggested wait.
type retryAfterError struct {
	wait time.Duration
	err  error
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

// nonRetryableError marks errors from non-recoverable HTTP statuses (non-429 4xx).
type nonRetryableError struct {
	err error
}

func (e *nonRetryableError) Error() string { return e.err.Error() }
func (e *nonRetryableError) Unwrap() error { return e.err }

func isNonRecoverable(err error) bool {
	var nre *nonRetryableError
	return errors.As(err, &nre) ||
		errors.Is(err, ErrUnauthorized) ||
		errors.Is(err, ErrAccountRestricted) ||
		errors.Is(err, ErrChallengeRequired) ||
		errors.Is(err, ErrDailyBudget) ||
		errors.Is(err, ErrOutsideHours) ||
		errors.Is(err, ErrInCooldown) ||
		errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInvalidAuth) ||
		errors.Is(err, ErrInvalidParams)
}

// mapCtxErr promotes context deadline failures to ErrTimeout so hosts can map
// a typed retryable class instead of surfacing raw "context deadline exceeded".
func mapCtxErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTimeout) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	return err
}

// wrapWarmUpTransportErr maps warm-up /feed/ deadline and EOF transport failures
// to ErrTimeout (same retryable budget class as deadline preflight).
func wrapWarmUpTransportErr(err error) error {
	if err == nil {
		return nil
	}
	if mapped := mapCtxErr(err); errors.Is(mapped, ErrTimeout) {
		return fmt.Errorf("%w: warm-up: %v", ErrTimeout, err)
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "client.timeout") {
		return fmt.Errorf("%w: warm-up: %v", ErrTimeout, err)
	}
	return fmt.Errorf("%w: warm-up: %v", ErrRequestFailed, err)
}

func parseRetryAfter(val string, fallback time.Duration) time.Duration {
	if val == "" {
		return fallback
	}
	trimmed := strings.TrimSpace(val)
	if n, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		if n > 1_000_000_000 {
			if d := time.Until(time.Unix(n, 0)); d > 0 {
				return d
			}
			return fallback
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(trimmed); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return fallback
}

// updateRateLimit reads standard rate-limit headers and updates tracked state.
func (c *Client) updateRateLimit(h http.Header) {
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	if v := rlHeader(h, "Limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.rlState.Limit = n
		}
	}
	if v := rlHeader(h, "Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.rlState.Remaining = n
		}
	}
	if v := rlHeader(h, "Reset"); v != "" {
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
			if ts > 1_000_000_000 {
				c.rlState.Reset = time.Unix(ts, 0)
			} else {
				c.rlState.Reset = time.Now().Add(time.Duration(ts) * time.Second)
			}
		}
	}
}

// rlHeader returns the value of a rate-limit header, checking four common prefix variants.
func rlHeader(h http.Header, suffix string) string {
	for _, p := range []string{"X-RateLimit-", "X-Rate-Limit-", "X-Ratelimit-", "RateLimit-"} {
		if v := strings.TrimSpace(h.Get(p + suffix)); v != "" {
			return v
		}
	}
	return ""
}

// adaptiveGap returns the delay before the next request based on rate-limit state.
func (c *Client) adaptiveGap() time.Duration {
	c.rlMu.Lock()
	rs := c.rlState
	c.rlMu.Unlock()

	if rs.Remaining == 0 && !rs.Reset.IsZero() {
		if d := time.Until(rs.Reset); d > 0 {
			return d + 50*time.Millisecond
		}
	}
	if rs.Remaining > 0 && !rs.Reset.IsZero() {
		if d := time.Until(rs.Reset); d > 0 {
			spread := d / time.Duration(float64(rs.Remaining)*0.9)
			if spread > c.minGap {
				return spread
			}
		}
	}
	return c.minGap
}

// waitForGap enforces the min request gap, honouring rate-limit state adaptively.
// Used as the legacy fallback when WithHumanPacing is not configured.
func (c *Client) waitForGap(ctx context.Context) {
	gap := c.adaptiveGap()
	c.gapMu.Lock()
	now := time.Now()
	next := c.lastReqAt.Add(gap)
	if now.After(next) {
		next = now
	}
	c.lastReqAt = next
	c.gapMu.Unlock()

	if wait := time.Until(next); wait > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	c.rlMu.Lock()
	c.rlState.RetryAfter = 0
	c.rlMu.Unlock()
}
