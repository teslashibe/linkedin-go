package linkedin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// RequestGate reserves host-owned capacity immediately before every HTTP attempt,
// including warm-up, redirects and retries. It must be safe for concurrent use.
// Returning an error prevents the request. Reservations are not refunded: a
// transport failure cannot prove that the provider did not receive the request.
type RequestGate func(context.Context) error

// WithRequestGate installs a host's durable allowance and schedule check. The
// gate runs without URLs, cookies or bodies and never replaces the proxy transport.
func WithRequestGate(gate RequestGate) Option {
	return func(c *Client) { c.requestGate = gate }
}

type gatedTransport struct {
	base http.RoundTripper
	gate RequestGate
}

type preparedWriteGuardKey struct{}

func (t *gatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.gate(req.Context()); err != nil {
		return nil, &RequestGateError{cause: err}
	}
	// A host gate can wait while the operation closes or its proof changes.
	// Recheck prepared writes after that wait and before transport dispatch.
	if guard, ok := req.Context().Value(preparedWriteGuardKey{}).(func() error); ok {
		if err := guard(); err != nil {
			return nil, &RequestGateError{cause: err}
		}
	}
	return t.base.RoundTrip(req)
}

// RequestGateError proves the host refused this HTTP attempt before transport
// dispatch. Its message excludes URLs and the host error, which can be private.
// Hosts can use errors.Is/As on Unwrap to choose their next eligible check.
type RequestGateError struct{ cause error }

func (e *RequestGateError) Error() string { return "linkedin: host request gate refused dispatch" }
func (e *RequestGateError) Unwrap() error { return e.cause }

func requestTransportError(err error) error {
	var denied *RequestGateError
	if errors.As(err, &denied) {
		return denied
	}
	return fmt.Errorf("%w: %v", ErrRequestFailed, err)
}
