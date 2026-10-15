package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteRateLimitRetainsStructuredReason(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason, service string
		note                        bool
	}{
		{"numeric service", `{"code":"PERSONALIZED_INVITATION_LIMIT_REACHED","serviceErrorCode":123}`, "note_unavailable", "123", true},
		{"string service", `{"code":"NOTE_UNAVAILABLE","serviceErrorCode":"123"}`, "note_unavailable", "123", true},
		{"named service", `{"serviceErrorCode":"PERSONALIZED_INVITATION_LIMIT_REACHED"}`, "note_unavailable", "", true},
		{"nested service", `{"data":{"code":"NOTE_UNAVAILABLE","serviceErrorCode":9876543210987654321}}`, "note_unavailable", "9876543210987654321", true},
		{"unknown numeric", `{"code":429,"serviceErrorCode":987}`, "rate_limited", "987", false},
		{"conflicting reasons", `{"code":"NOTE_UNAVAILABLE","data":{"code":"ALREADY_CONNECTED"}}`, "rate_limited", "", false},
		{"conflicting service numbers", `{"serviceErrorCode":123,"data":{"serviceErrorCode":321}}`, "rate_limited", "", false},
		{"arbitrary provider text", `{"code":"private.name@example.com","serviceErrorCode":"private profile details","message":"secret provider text"}`, "rate_limited", "", false},
		{"malformed", `{`, "rate_limited", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				r := response(req, http.StatusTooManyRequests, tc.body)
				r.Header.Set("Retry-After", "90")
				return r, nil
			})
			_, _, err := c.makeWriteRequest(context.Background(), apiBase+"/fixture", []byte(`{}`))
			var writeErr *WriteError
			if !errors.As(err, &writeErr) || !errors.Is(err, ErrRateLimited) || errors.Is(err, ErrNoteUnavailable) != tc.note || calls.Load() != 1 || IsWriteUnknown(err) {
				t.Fatalf("unexpected send outcome: calls=%d error=%v", calls.Load(), err)
			}
			o := writeErr.Observation
			if o.ProviderReason != tc.reason || o.ServiceErrorCode != tc.service || o.Method != http.MethodPost || o.StatusCode != 429 || writeErr.RetryAfter != 90*time.Second {
				t.Fatalf("structured rejection lost: %+v", o)
			}
			if o != c.LastResponse() {
				t.Fatal("error and client disagree about the response")
			}
			encoded, _ := json.Marshal(o)
			if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "secret") {
				t.Fatalf("provider payload leaked into diagnostics: %s", encoded)
			}
		})
	}
}

func TestUnknownWriteRetainsHeadersWithoutReclassification(t *testing.T) {
	var calls atomic.Int32
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		r := response(req, 429, "")
		r.Header.Set("Retry-After", "120")
		r.Body = failedRead{}
		return r, nil
	})
	_, _, err := c.makeWriteRequest(context.Background(), apiBase+"/fixture", []byte(`{}`))
	var writeErr *WriteError
	if !errors.As(err, &writeErr) || !IsWriteUnknown(err) || errors.Is(err, ErrRateLimited) || calls.Load() != 1 {
		t.Fatalf("incomplete response must remain unknown and never replay: calls=%d error=%v", calls.Load(), err)
	}
	if writeErr.Observation.StatusCode != 429 || !writeErr.Observation.RetryAfter.Valid || writeErr.Observation.ProviderReason != "" {
		t.Fatalf("header evidence lost or unknown response classified: %+v", writeErr.Observation)
	}
}

func TestLastResponseTracksSuccessAndErrorSnapshotIsImmutable(t *testing.T) {
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		return response(req, 201, `{}`), nil
	})
	_, _, err := c.makeWriteRequest(context.Background(), apiBase+"/fixture", []byte(`{}`))
	if err != nil || c.LastResponse().Method != http.MethodPost || c.LastResponse().StatusCode != 201 {
		t.Fatalf("successful write missing from observation: %+v error=%v", c.LastResponse(), err)
	}
	before := c.LastResponse()
	copy := c.LastResponse()
	copy.Quotas[0].Limit.Value = 99
	if before != c.LastResponse() {
		t.Fatal("returned observation aliases client state")
	}
	c.httpClient.Transport = outreachTransport(func(req *http.Request) (*http.Response, error) { return response(req, 200, `{}`), nil })
	if _, err := c.doRequest(context.Background(), apiBase+"/fixture"); err != nil || c.LastResponse().Method != http.MethodGet || c.LastResponse().StatusCode != 200 {
		t.Fatalf("successful read missing from observation: %+v error=%v", c.LastResponse(), err)
	}
	if before.Method != http.MethodPost || before.StatusCode != 201 {
		t.Fatal("later read changed earlier response snapshot")
	}
}

func TestReadRateLimitHasBodyReasonAndStopsNoteRetries(t *testing.T) {
	var calls atomic.Int32
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(req, 429, `{"serviceErrorCode":"NOTE_UNAVAILABLE"}`), nil
	})
	_, err := c.makeRequest(context.Background(), apiBase+"/fixture")
	if !errors.Is(err, ErrRateLimited) || !errors.Is(err, ErrNoteUnavailable) || calls.Load() != 1 || c.LastResponse().ProviderReason != "note_unavailable" {
		t.Fatalf("known note restriction retried or lost: calls=%d observation=%+v error=%v", calls.Load(), c.LastResponse(), err)
	}
}

func TestRateLimitStatePreservesStrictestIndependentQuota(t *testing.T) {
	now := observationTime()
	h := make(http.Header)
	h.Set("RateLimit-Remaining", "0")
	h.Set("RateLimit-Reset", "90")
	h.Set("X-RateLimit-Remaining", "5")
	h.Set("X-RateLimit-Reset", "1792101630")
	c := New(Auth{LiAt: "fixture", CSRF: "fixture"})
	c.storeResponse(ParseResponseObservation(http.MethodGet, 200, h, now))
	state := c.RateLimit()
	if state.Remaining != 0 || !state.Reset.Equal(now.Add(90*time.Second)) {
		t.Fatalf("positive quota replaced exhausted quota: %+v", state)
	}
	// A later reset-only response must not synthesize zero remaining or extend
	// the existing restriction using an unrelated window.
	c.storeResponse(ParseResponseObservation(http.MethodGet, 200, http.Header{"Ratelimit-Reset": []string{"600"}}, now.Add(time.Second)))
	if got := c.RateLimit(); !got.Reset.Equal(state.Reset) {
		t.Fatalf("reset-only response changed an existing restriction: %+v", got)
	}
	// After expiry, a reset without remaining has no basis for a new hold.
	c.storeResponse(ParseResponseObservation(http.MethodGet, 200, http.Header{"Ratelimit-Reset": []string{"600"}}, now.Add(2*time.Minute)))
	if got := c.RateLimit(); !got.Reset.IsZero() || got.RetryAfter != 0 {
		t.Fatalf("stale implicit zero manufactured a new restriction: %+v", got)
	}
}

func TestProviderDeadlineBlocksWriteWithAnyPacingPolicy(t *testing.T) {
	for _, human := range []bool{false, true} {
		for _, status := range []int{http.StatusCreated, http.StatusTooManyRequests} {
			name := http.StatusText(status)
			if human {
				name += " human pacing"
			}
			t.Run(name, func(t *testing.T) {
				var calls atomic.Int32
				c := offlineClient(func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					r := response(req, status, `{}`)
					r.Header.Set("RateLimit-Remaining", "0")
					r.Header.Set("RateLimit-Reset", "120")
					return r, nil
				})
				if human {
					c.pacer = newPacer(HumanPacing{BaseGap: time.Nanosecond})
				}
				_, _, _ = c.makeWriteRequest(context.Background(), apiBase+"/fixture", []byte(`{}`))
				ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
				defer cancel()
				_, _, err := c.makeWriteRequest(ctx, apiBase+"/fixture", []byte(`{}`))
				if !errors.Is(err, ErrTimeout) || calls.Load() != 1 {
					t.Fatalf("second write bypassed provider deadline: calls=%d error=%v", calls.Load(), err)
				}
			})
		}
	}
}
