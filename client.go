package linkedin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/vnd.linkedin.normalized+json+2.1")
	req.Header.Set("csrf-token", c.auth.CSRF)
	req.Header.Set("x-li-lang", "en_US")
	req.Header.Set("x-restli-protocol-version", "2.0.0")
	req.AddCookie(&http.Cookie{Name: "li_at", Value: c.auth.LiAt})
	req.AddCookie(&http.Cookie{Name: "JSESSIONID", Value: `"` + c.auth.JSESSIONID + `"`})

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		// success
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, &retryAfterError{wait: wait, err: ErrRateLimited}
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)
	default:
		return nil, fmt.Errorf("%w: HTTP %d", ErrRequestFailed, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading response body: %v", ErrRequestFailed, err)
	}
	return body, nil
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

func isNonRecoverable(err error) bool {
	return errors.Is(err, ErrUnauthorized) ||
		errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInvalidAuth) ||
		errors.Is(err, ErrInvalidParams)
}

func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 0
}
