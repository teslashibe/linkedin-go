package linkedin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

func validateOutreachText(text string, limit int) error {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || UTF16Length(text) > limit {
		return ErrInvalidParams
	}
	return nil
}

func (c *Client) requireSender(ctx context.Context, expected string) (*Profile, error) {
	want, err := CanonicalMemberURN(expected)
	if err != nil {
		return nil, err
	}
	me, err := c.GetMe(ctx)
	if err != nil {
		return nil, err
	}
	if me.URN != want {
		return nil, ErrSenderIdentityMismatch
	}
	return me, nil
}

// makeWriteRequest sends exactly one POST. Safe warm-up reads finish before the
// dispatch boundary. Redirects, recovery and HTTP failures never replay a write.
func (c *Client) makeWriteRequest(ctx context.Context, requestURL string, payload []byte) ([]byte, http.Header, error) {
	return c.makeGuardedWriteRequest(ctx, requestURL, payload, nil)
}

func (c *Client) makeGuardedWriteRequest(ctx context.Context, requestURL string, payload []byte, guard func() error) ([]byte, http.Header, error) {
	if guard != nil {
		ctx = context.WithValue(ctx, preparedWriteGuardKey{}, guard)
	}
	if c.auth.LiAt == "" || c.auth.CSRF == "" {
		return nil, nil, ErrInvalidAuth
	}
	if err := c.checkCooldown(); err != nil {
		return nil, nil, err
	}
	release := c.acquireRequestSlot(ctx)
	if release == nil {
		return nil, nil, mapCtxErr(ctx.Err())
	}
	defer release()
	if err := c.warmUp(ctx); err != nil {
		return nil, nil, err
	}
	if err := c.gateBeforeRequest(ctx); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, ErrInvalidParams
	}
	// Disallow transport or redirect replay, including a 307/308 with a body.
	req.GetBody = nil
	headers := map[string]string{}
	c.applyVoyagerHeaders(headers, requestURL, true)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	req.Header.Del("Idempotency-Key")
	req.Header.Del("X-Idempotency-Key")
	hc := *c.httpClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if ctx.Err() != nil {
		return nil, nil, mapCtxErr(ctx.Err())
	}
	if guard != nil {
		if err := guard(); err != nil {
			return nil, nil, err
		}
	}
	resp, err := hc.Do(req)
	if err != nil {
		var denied *RequestGateError
		if errors.As(err, &denied) {
			return nil, nil, denied
		}
		return nil, nil, &WriteError{Cause: ErrWriteUnknown, unknownReason: writeUnknownTransport}
	}
	defer resp.Body.Close()
	c.absorbSetCookies(resp)
	observation := c.observeResponse(http.MethodPost, resp)
	body, err := readResponseBody(resp)
	if err != nil {
		return nil, nil, &WriteError{StatusCode: resp.StatusCode, Cause: ErrWriteUnknown, Observation: observation, unknownReason: writeUnknownResponseBody}
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") || detectRestrictionInBody(body) != nil {
			return nil, nil, &WriteError{StatusCode: resp.StatusCode, Cause: ErrWriteUnknown, Observation: observation, unknownReason: writeUnknownResponseContent}
		}
		return body, resp.Header.Clone(), nil
	}
	cause := error(ErrWriteUnknown)
	wait := observation.RetryAt.Sub(observation.ObservedAt)
	if observation.RetryAt.IsZero() {
		wait = 0
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		cause = ErrUnauthorized
	case http.StatusTooManyRequests:
		cause = ErrRateLimited
		rejection, reason, code := providerRejection(body)
		observation.ProviderReason, observation.ServiceErrorCode = reason, code
		if reason == "write_rejected" {
			observation.ProviderReason = "rate_limited"
		} else {
			cause = errors.Join(ErrRateLimited, rejection)
		}
	case http.StatusNotFound:
		cause = ErrNotFound
	case http.StatusBadRequest, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity:
		cause, observation.ProviderReason, observation.ServiceErrorCode = providerRejection(body)
	}
	c.storeResponse(observation)
	return nil, nil, &WriteError{StatusCode: resp.StatusCode, RetryAfter: wait, Cause: cause, Observation: observation, unknownReason: writeUnknownResponseStatus}
}

func writeRejection(body []byte) error {
	err, _, _ := providerRejection(body)
	return err
}

func unknownReceipt() error { return &WriteError{Cause: ErrWriteUnknown} }

func unknownReceiptReason(reason string) error {
	return &WriteError{Cause: ErrWriteUnknown, unknownReason: reason}
}

// IsWriteUnknown reports a dispatched write whose success cannot be established.
func IsWriteUnknown(err error) bool { return errors.Is(err, ErrWriteUnknown) }
