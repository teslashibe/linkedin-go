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
				return nil, ctx.Err()
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
	c.waitForGap(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/vnd.linkedin.normalized+json+2.1")
	req.Header.Set("Accept-Language", "en-GB,en-US;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("csrf-token", c.auth.CSRF)
	req.Header.Set("x-li-lang", "en_US")
	req.Header.Set("x-restli-protocol-version", "2.0.0")
	req.Header.Set("x-li-track", `{"clientVersion":"1.13.35368","mpVersion":"1.13.35368","osName":"web","timezoneOffset":0,"timezone":"Etc/UTC","deviceFormFactor":"DESKTOP","mpName":"voyager-web","displayDensity":1,"displayWidth":1920,"displayHeight":1080}`)
	req.Header.Set("x-li-page-instance", "urn:li:page:d_flagship3_search_srp_people;0")
	req.Header.Set("x-li-pem-metadata", "Voyager - People SRP=search-results")
	req.Header.Set("Cookie", fmt.Sprintf(`li_at=%s; JSESSIONID="%s"`, c.auth.LiAt, c.auth.JSESSIONID))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()

	c.updateRateLimit(resp.Header)
	switch {
	case resp.StatusCode == http.StatusOK:
		// success
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := parseRetryAfter(resp.Header.Get("Retry-After"), 60*time.Second)
		c.rlMu.Lock()
		c.rlState.Remaining = 0
		c.rlState.RetryAfter = wait
		if c.rlState.Reset.IsZero() || time.Until(c.rlState.Reset) < wait {
			c.rlState.Reset = time.Now().Add(wait)
		}
		c.rlMu.Unlock()
		c.gapMu.Lock()
		if earliest := time.Now().Add(wait); c.lastReqAt.Before(earliest) {
			c.lastReqAt = earliest
		}
		c.gapMu.Unlock()
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
				return nil, ctx.Err()
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
	c.waitForGap(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/vnd.linkedin.normalized+json+2.1")
	req.Header.Set("csrf-token", c.auth.CSRF)
	req.Header.Set("x-li-lang", "en_US")
	req.Header.Set("x-restli-protocol-version", "2.0.0")
	req.Header.Set("x-li-track", `{"clientVersion":"1.13.9814","mpVersion":"1.13.9814","osName":"web","timezoneOffset":0,"timezone":"Etc/UTC","deviceFormFactor":"DESKTOP","mpName":"voyager-web","displayDensity":1,"displayWidth":1920,"displayHeight":1080}`)
	req.Header.Set("x-li-page-instance", "urn:li:page:d_flagship3_search_srp_people;0")
	req.Header.Set("Cookie", fmt.Sprintf(`li_at=%s; JSESSIONID="%s"`, c.auth.LiAt, c.auth.JSESSIONID))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()

	c.updateRateLimit(resp.Header)
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		// success
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := parseRetryAfter(resp.Header.Get("Retry-After"), 60*time.Second)
		c.rlMu.Lock()
		c.rlState.Remaining = 0
		c.rlState.RetryAfter = wait
		if c.rlState.Reset.IsZero() || time.Until(c.rlState.Reset) < wait {
			c.rlState.Reset = time.Now().Add(wait)
		}
		c.rlMu.Unlock()
		c.gapMu.Lock()
		if earliest := time.Now().Add(wait); c.lastReqAt.Before(earliest) {
			c.lastReqAt = earliest
		}
		c.gapMu.Unlock()
		return nil, &retryAfterError{wait: wait, err: ErrRateLimited}
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)
	default:
		return nil, &nonRetryableError{fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)}
	}

	return readResponseBody(resp)
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
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInvalidAuth) ||
		errors.Is(err, ErrInvalidParams)
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
