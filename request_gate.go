package linkedin

import (
	"context"
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

func (t *gatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.gate(req.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}
