package linkedin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxRateLimitDelay = 10 * 365 * 24 * time.Hour
	maxRateLimitCount = 1<<31 - 1
)

// HeaderTime records presence separately from validity. At retains the parsed
// value: delay headers are relative to ObservedAt, absolute headers are UTC.
// No unvalidated header text is retained.
type HeaderTime struct {
	Present bool      `json:"present"`
	Valid   bool      `json:"valid"`
	At      time.Time `json:"at,omitempty"`
	Format  string    `json:"format,omitempty"`
}

// HeaderNumber distinguishes an explicit zero from a missing or invalid value.
type HeaderNumber struct {
	Present bool  `json:"present"`
	Valid   bool  `json:"valid"`
	Value   int64 `json:"value"`
}

type RateLimitQuota struct {
	Prefix    string       `json:"prefix"`
	Limit     HeaderNumber `json:"limit"`
	Remaining HeaderNumber `json:"remaining"`
	Reset     HeaderTime   `json:"reset"`
}

// ResponseObservation is bounded, safe response metadata. It contains no URL,
// request or response body, cookies, credentials, or arbitrary provider text.
// A deadline is a provider hint, not proof that an operation will succeed then.
type ResponseObservation struct {
	Diagnostics      RejectionDiagnostics `json:"diagnostics,omitzero"`
	ObservedAt       time.Time            `json:"observed_at"`
	Method           string               `json:"method"`
	StatusCode       int                  `json:"status_code"`
	Date             HeaderTime           `json:"date"`
	RetryAfter       HeaderTime           `json:"retry_after"`
	Quotas           [3]RateLimitQuota    `json:"quotas"`
	RetryAt          time.Time            `json:"retry_at,omitempty"`
	DeadlineSource   string               `json:"deadline_source,omitempty"`
	ProviderReason   string               `json:"provider_reason,omitempty"`
	ServiceErrorCode string               `json:"service_error_code,omitempty"`
	RequestID        string               `json:"request_id,omitempty"`
	UUID             string               `json:"uuid,omitempty"`
}

var rateLimitPrefixes = [3]string{"RateLimit-", "X-RateLimit-", "X-Rate-Limit-"}

// ParseResponseObservation interprets Retry-After according to HTTP: numeric
// values are delay-seconds, never guessed Unix timestamps. RateLimit-Reset is
// delay-seconds. Legacy X-RateLimit-Reset variants retain the client's dual
// numeric forms: bounded delay-seconds or epoch-sized Unix seconds. The chosen
// interpretation is retained in Format. HTTP dates are also accepted.
// Duplicate, negative, overflowing and oversized values are present but invalid.
func ParseResponseObservation(method string, status int, h http.Header, observedAt time.Time) ResponseObservation {
	o := ResponseObservation{ObservedAt: observedAt.UTC(), Method: method, StatusCode: status}
	o.Date = parseHeaderTime(h, "Date", "http_date", observedAt)
	o.RetryAfter = parseHeaderTime(h, "Retry-After", "delay_seconds", observedAt)
	for i, prefix := range rateLimitPrefixes {
		format := "unix_seconds"
		if i == 0 {
			format = "delay_seconds"
		}
		o.Quotas[i] = RateLimitQuota{Prefix: prefix, Limit: parseHeaderNumber(h, prefix+"Limit"), Remaining: parseHeaderNumber(h, prefix+"Remaining"), Reset: parseHeaderTime(h, prefix+"Reset", format, observedAt)}
	}
	o.RequestID = headerIdentifier(h, "X-Li-Request-Id", false)
	o.UUID = headerIdentifier(h, "X-Li-Uuid", true)
	o.setDeadline()
	return o.Sanitized()
}

// Sanitized returns a value safe to persist even after decoding external JSON.
// It does not expose arbitrary strings, header values or response content.
func (o ResponseObservation) Sanitized() ResponseObservation {
	if !validObservationTime(o.ObservedAt) || (o.Method != http.MethodGet && o.Method != http.MethodPost) || o.StatusCode < 100 || o.StatusCode > 599 {
		return ResponseObservation{}
	}
	o.ObservedAt = o.ObservedAt.UTC()
	o.Date = sanitizeHeaderTime(o.Date, o.ObservedAt, true)
	o.RetryAfter = sanitizeHeaderTime(o.RetryAfter, o.ObservedAt, false)
	for i, prefix := range rateLimitPrefixes {
		o.Quotas[i].Prefix = prefix
		o.Quotas[i].Limit = sanitizeHeaderNumber(o.Quotas[i].Limit)
		o.Quotas[i].Remaining = sanitizeHeaderNumber(o.Quotas[i].Remaining)
		o.Quotas[i].Reset = sanitizeHeaderTime(o.Quotas[i].Reset, o.ObservedAt, false)
	}
	if !knownProviderReason(o.ProviderReason) {
		o.ProviderReason = ""
	}
	if !digits(o.ServiceErrorCode) || len(o.ServiceErrorCode) > 20 {
		o.ServiceErrorCode = ""
	}
	if !safeIdentifier(o.RequestID, false) {
		o.RequestID = ""
	}
	if !safeIdentifier(o.UUID, true) {
		o.UUID = ""
	}
	o.Diagnostics = o.Diagnostics.sanitized()
	if o.StatusCode != http.StatusTooManyRequests {
		o.Diagnostics = RejectionDiagnostics{}
	}
	o.setDeadline()
	return o
}

func (o *ResponseObservation) setDeadline() {
	o.RetryAt, o.DeadlineSource = time.Time{}, ""
	choose := func(h HeaderTime, source string) {
		if !h.Valid {
			return
		}
		deadline := h.At
		if h.Format != "delay_seconds" && o.Date.Valid {
			deadline = o.ObservedAt.Add(h.At.Sub(o.Date.At))
		}
		if deadline.Before(o.ObservedAt) {
			deadline = o.ObservedAt
		}
		if deadline.Sub(o.ObservedAt) > maxRateLimitDelay {
			return
		}
		if o.RetryAt.IsZero() || deadline.After(o.RetryAt) {
			o.RetryAt, o.DeadlineSource = deadline, source
		}
	}
	choose(o.RetryAfter, "retry_after")
	for _, quota := range o.Quotas {
		if o.StatusCode == http.StatusTooManyRequests || (quota.Remaining.Valid && quota.Remaining.Value == 0) {
			choose(quota.Reset, "ratelimit_reset")
		}
	}
	if o.RetryAt.IsZero() && o.StatusCode == http.StatusTooManyRequests {
		o.RetryAt, o.DeadlineSource = o.ObservedAt.Add(time.Minute), "fallback"
	}
}

func parseHeaderTime(h http.Header, name, numericFormat string, now time.Time) HeaderTime {
	v, present, valid := singleHeader(h, name)
	r := HeaderTime{Present: present}
	if !valid {
		return r
	}
	if numericFormat != "http_date" && digits(v) {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return r
		}
		if numericFormat == "unix_seconds" && n <= 1_000_000_000 {
			numericFormat = "delay_seconds"
		}
		if numericFormat == "delay_seconds" {
			if n > int64(maxRateLimitDelay/time.Second) {
				return r
			}
			r.At = now.UTC().Add(time.Duration(n) * time.Second)
		} else {
			r.At = time.Unix(n, 0).UTC()
			if !validObservationTime(r.At) || r.At.Sub(now) > maxRateLimitDelay {
				return HeaderTime{Present: present}
			}
		}
		r.Valid, r.Format = true, numericFormat
		return r
	}
	if parsed, err := http.ParseTime(v); err == nil && validObservationTime(parsed) {
		r.Valid, r.At, r.Format = true, parsed.UTC(), "http_date"
	}
	return r
}

func parseHeaderNumber(h http.Header, name string) HeaderNumber {
	v, present, valid := singleHeader(h, name)
	r := HeaderNumber{Present: present}
	if valid && digits(v) {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n <= maxRateLimitCount {
			r.Valid, r.Value = true, n
		}
	}
	return r
}

func singleHeader(h http.Header, name string) (string, bool, bool) {
	var values []string
	present := false
	for key, entries := range h {
		if strings.EqualFold(key, name) {
			present = true
			values = append(values, entries...)
		}
	}
	if len(values) != 1 || len(values[0]) > 128 {
		return "", present, false
	}
	v := strings.TrimSpace(values[0])
	return v, present, v != ""
}

func digits(v string) bool {
	if v == "" {
		return false
	}
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func sanitizeHeaderNumber(h HeaderNumber) HeaderNumber {
	if !h.Present {
		return HeaderNumber{}
	}
	if !h.Valid || h.Value < 0 || h.Value > maxRateLimitCount {
		return HeaderNumber{Present: true}
	}
	return h
}

func sanitizeHeaderTime(h HeaderTime, now time.Time, date bool) HeaderTime {
	if !h.Present {
		return HeaderTime{}
	}
	if !h.Valid || !validObservationTime(h.At) || (h.Format != "delay_seconds" && h.Format != "http_date" && h.Format != "unix_seconds") || (date && h.Format != "http_date") || (h.Format == "delay_seconds" && (h.At.Before(now) || h.At.Sub(now) > maxRateLimitDelay)) {
		return HeaderTime{Present: true}
	}
	h.At = h.At.UTC()
	return h
}

func validObservationTime(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999
}

func headerIdentifier(h http.Header, name string, uuid bool) string {
	v, _, valid := singleHeader(h, name)
	if valid && safeIdentifier(v, uuid) {
		return v
	}
	return ""
}

func safeIdentifier(v string, uuid bool) bool {
	if v == "" {
		return true
	}
	if len(v) > 128 || strings.Contains(v, "://") || (uuid && len(v) != 32 && len(v) != 36) {
		return false
	}
	for i, ch := range v {
		if uuid {
			if len(v) == 36 && (i == 8 || i == 13 || i == 18 || i == 23) {
				if ch != '-' {
					return false
				}
				continue
			}
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		} else if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || strings.ContainsRune("-_:.=+/", ch)) {
			return false
		}
	}
	return true
}

// LastResponse returns the latest received HTTP response's safe metadata.
// A blocked or transport-failed request does not fabricate a new observation.
func (c *Client) LastResponse() ResponseObservation {
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	return c.lastResponse
}

func (c *Client) observeResponse(method string, resp *http.Response) ResponseObservation {
	o := ParseResponseObservation(method, resp.StatusCode, resp.Header, time.Now())
	c.storeResponse(o)
	return o
}

func (c *Client) storeResponse(o ResponseObservation) {
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	c.lastResponse = o
	// Quotas belong to this response. Never combine a fresh reset with a stale
	// implicit zero or overwrite an exhausted quota with a different family.
	var state RateLimitState
	for _, q := range o.Quotas {
		if q.Limit.Valid {
			state.Limit = int(q.Limit.Value)
		}
		if q.Remaining.Valid {
			state.Remaining = int(q.Remaining.Value)
			if q.Reset.Valid {
				state.Reset = q.Reset.At
				if q.Reset.Format != "delay_seconds" && o.Date.Valid {
					state.Reset = o.ObservedAt.Add(q.Reset.At.Sub(o.Date.At))
				}
			}
		}
	}
	if !o.RetryAt.IsZero() {
		state.RetryAfter = o.RetryAt.Sub(o.ObservedAt)
		state.Remaining, state.Reset = 0, o.RetryAt
	}
	// A later response without quota headers must not clear an existing wait.
	if c.rlState.Remaining == 0 && c.rlState.Reset.After(state.Reset) && c.rlState.Reset.After(o.ObservedAt) {
		state.Remaining, state.Reset = 0, c.rlState.Reset
		state.RetryAfter = state.Reset.Sub(o.ObservedAt)
	}
	c.rlState = state
}

func (c *Client) rateLimitFailure(resp *http.Response) error {
	return c.rateLimitFailureAt(resp, time.Now())
}

func (c *Client) rateLimitFailureAt(resp *http.Response, started time.Time) error {
	o := c.LastResponse()
	cause := error(ErrRateLimited)
	body, err := readResponseBodyLimit(resp, rejectionBodyLimit+1)
	requestURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		requestURL = resp.Request.URL.String()
	}
	o.Diagnostics = rejectionDiagnostics(body, resp.Header.Get("Content-Type"), requestURL, time.Since(started), err)
	if err == nil {
		rejection, reason, code := providerRejection(body)
		o.ProviderReason, o.ServiceErrorCode = reason, code
		if reason != "write_rejected" {
			cause = errors.Join(ErrRateLimited, rejection)
		}
	}
	if o.ProviderReason == "" || o.ProviderReason == "write_rejected" {
		o.ProviderReason = "rate_limited"
	}
	c.storeResponse(o)
	wait := o.RetryAt.Sub(o.ObservedAt)
	c.pacer.applyServerBackoff(wait)
	c.gapMu.Lock()
	if c.lastReqAt.Before(o.RetryAt) {
		c.lastReqAt = o.RetryAt
	}
	c.gapMu.Unlock()
	return &retryAfterError{wait: wait, err: cause}
}
