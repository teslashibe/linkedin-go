package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
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
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, &WriteError{Cause: ErrWriteUnknown}
	}
	defer resp.Body.Close()
	c.absorbSetCookies(resp)
	c.updateRateLimit(resp.Header)
	body, err := readResponseBody(resp)
	if err != nil {
		return nil, nil, &WriteError{StatusCode: resp.StatusCode, Cause: ErrWriteUnknown}
	}
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") || detectRestrictionInBody(body) != nil {
			return nil, nil, &WriteError{StatusCode: resp.StatusCode, Cause: ErrWriteUnknown}
		}
		return body, resp.Header.Clone(), nil
	}
	cause := error(ErrWriteUnknown)
	wait := parseRetryAfter(resp.Header.Get("Retry-After"), 0)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		cause = ErrUnauthorized
	case http.StatusTooManyRequests:
		cause = ErrRateLimited
	case http.StatusNotFound:
		cause = ErrNotFound
	case http.StatusBadRequest, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity:
		cause = writeRejection(body)
	}
	return nil, nil, &WriteError{StatusCode: resp.StatusCode, RetryAfter: wait, Cause: cause}
}

func writeRejection(body []byte) error {
	if err := detectRestrictionInBody(body); err != nil {
		return err
	}
	var response struct {
		Code             string `json:"code"`
		ServiceErrorCode string `json:"serviceErrorCode"`
		Data             *struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ErrWriteRejected
	}
	code := response.Code
	if response.Data != nil {
		if code != "" && response.Data.Code != "" && code != response.Data.Code {
			return ErrWriteRejected
		}
		if code == "" {
			code = response.Data.Code
		}
	}
	switch strings.ToUpper(code) {
	case "ALREADY_CONNECTED":
		return ErrAlreadyConnected
	case "INVITATION_PENDING", "ALREADY_INVITED":
		return ErrInvitationPending
	case "EMAIL_REQUIRED":
		return ErrEmailRequired
	case "NOTE_UNAVAILABLE", "PERSONALIZED_INVITATION_LIMIT_REACHED":
		return ErrNoteUnavailable
	case "RECIPIENT_NOT_MESSAGEABLE", "NOT_CONNECTED", "RECIPIENT_NOT_FIRST_DEGREE_CONNECTION":
		return ErrRecipientNotMessageable
	}
	return ErrWriteRejected
}

func unknownReceipt() error { return &WriteError{Cause: ErrWriteUnknown} }

// IsWriteUnknown reports a dispatched write whose success cannot be established.
func IsWriteUnknown(err error) bool { return errors.Is(err, ErrWriteUnknown) }
