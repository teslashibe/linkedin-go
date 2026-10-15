package linkedin

import (
	"net/http"
	"testing"
	"time"
)

func observationTime() time.Time {
	return time.Date(2026, time.October, 15, 22, 0, 0, 0, time.UTC)
}

func TestParseResponseObservationMissingHeaders(t *testing.T) {
	observed := observationTime()
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			got := ParseResponseObservation(http.MethodPost, status, nil, observed)
			if got.Method != http.MethodPost || got.StatusCode != status || !got.ObservedAt.Equal(observed) {
				t.Fatalf("request identity or observation time lost: %+v", got)
			}
			if got.Date.Present || got.Date.Valid || got.RetryAfter.Present || got.RetryAfter.Valid {
				t.Fatalf("missing headers became present: %+v", got)
			}
			for _, quota := range got.Quotas {
				if quota.Limit.Present || quota.Remaining.Present || quota.Reset.Present {
					t.Fatalf("missing quota became present: %+v", quota)
				}
			}
			if status == http.StatusTooManyRequests {
				if got.DeadlineSource != "fallback" || !got.RetryAt.Equal(observed.Add(time.Minute)) {
					t.Fatalf("429 without usable timing must get bounded fallback: %+v", got)
				}
			} else if got.DeadlineSource != "" || !got.RetryAt.IsZero() {
				t.Fatalf("successful response without timing unexpectedly held: %+v", got)
			}
		})
	}
}

func TestParseResponseObservationRetryAfterSeconds(t *testing.T) {
	observed := observationTime()
	for _, tc := range []struct {
		name  string
		value string
		delay time.Duration
	}{
		{name: "zero is explicit", value: "0", delay: 0},
		{name: "delay", value: "120", delay: 2 * time.Minute},
		{name: "ten year bound", value: "315360000", delay: 10 * 365 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{"Retry-After": []string{tc.value}, "Date": []string{observed.Add(-time.Hour).Format(http.TimeFormat)}}
			got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
			want := observed.Add(tc.delay)
			if !got.RetryAfter.Present || !got.RetryAfter.Valid || got.RetryAfter.Format != "delay_seconds" {
				t.Fatalf("valid delay not retained: %+v", got.RetryAfter)
			}
			if !got.RetryAfter.At.Equal(want) || !got.RetryAt.Equal(want) || got.DeadlineSource != "retry_after" {
				t.Fatalf("relative delay must use observation time, not server clock: %+v", got)
			}
		})
	}
}

func TestParseResponseObservationRejectsAmbiguousRetryAfter(t *testing.T) {
	observed := observationTime()
	for _, tc := range []struct {
		name   string
		values []string
	}{
		{name: "empty", values: []string{""}},
		{name: "negative", values: []string{"-1"}},
		{name: "fraction", values: []string{"0.5"}},
		{name: "overflow", values: []string{"9223372036854775808"}},
		{name: "outside count bound", values: []string{"2147483648"}},
		{name: "outside delay bound", values: []string{"315360001"}},
		{name: "not a date", values: []string{"later"}},
		{name: "duplicate values", values: []string{"60", "120"}},
		{name: "coalesced duplicate", values: []string{"60, 120"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, http.Header{"Retry-After": tc.values}, observed)
			if !got.RetryAfter.Present || got.RetryAfter.Valid || !got.RetryAfter.At.IsZero() {
				t.Fatalf("invalid header must remain distinguishable from absence: %+v", got.RetryAfter)
			}
			if got.DeadlineSource != "fallback" || !got.RetryAt.Equal(observed.Add(time.Minute)) {
				t.Fatalf("invalid header unexpectedly became provider timing: %+v", got)
			}
		})
	}
}

func TestParseResponseObservationHTTPDateAdjustsClockSkew(t *testing.T) {
	observed := observationTime()
	for _, skew := range []time.Duration{-5 * time.Minute, 5 * time.Minute} {
		t.Run(skew.String(), func(t *testing.T) {
			serverDate := observed.Add(skew)
			providerDeadline := serverDate.Add(90 * time.Second)
			h := http.Header{
				"Date":        []string{serverDate.Format(http.TimeFormat)},
				"Retry-After": []string{providerDeadline.Format(http.TimeFormat)},
			}
			got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
			if !got.Date.Present || !got.Date.Valid || !got.Date.At.Equal(serverDate) || got.Date.Format != "http_date" {
				t.Fatalf("server date evidence lost: %+v", got.Date)
			}
			if !got.RetryAfter.Valid || got.RetryAfter.Format != "http_date" || !got.RetryAfter.At.Equal(providerDeadline) {
				t.Fatalf("raw provider deadline evidence lost: %+v", got.RetryAfter)
			}
			if !got.RetryAt.Equal(observed.Add(90*time.Second)) || got.DeadlineSource != "retry_after" {
				t.Fatalf("deadline did not compensate for server clock skew: %+v", got)
			}
		})
	}
}

func TestParseResponseObservationInvalidDateDoesNotAdjustDeadline(t *testing.T) {
	observed := observationTime()
	providerDeadline := observed.Add(2 * time.Minute)
	for _, dates := range [][]string{{"not a date"}, {observed.Format(http.TimeFormat), observed.Add(time.Minute).Format(http.TimeFormat)}} {
		h := http.Header{"Date": dates, "Retry-After": []string{providerDeadline.Format(http.TimeFormat)}}
		got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
		if !got.Date.Present || got.Date.Valid || !got.Date.At.IsZero() {
			t.Fatalf("invalid server Date was trusted: %+v", got.Date)
		}
		if !got.RetryAt.Equal(providerDeadline) {
			t.Fatalf("invalid server Date altered deadline: %+v", got)
		}
	}
}

func TestParseResponseObservationQuotaResetSemantics(t *testing.T) {
	observed := observationTime()
	serverDate := observed.Add(-5 * time.Minute)
	for _, tc := range []struct {
		prefix string
		value  string
		format string
		at     time.Time
	}{
		{prefix: "RateLimit-", value: "90", format: "delay_seconds", at: observed.Add(90 * time.Second)},
		{prefix: "X-RateLimit-", value: "1792101390", format: "unix_seconds", at: serverDate.Add(90 * time.Second)},
		{prefix: "X-Rate-Limit-", value: "1792101390", format: "unix_seconds", at: serverDate.Add(90 * time.Second)},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			h := make(http.Header)
			h.Set("Date", serverDate.Format(http.TimeFormat))
			h.Set(tc.prefix+"Limit", "100")
			h.Set(tc.prefix+"Remaining", "0")
			h.Set(tc.prefix+"Reset", tc.value)
			got := ParseResponseObservation(http.MethodGet, http.StatusOK, h, observed)
			var quota *RateLimitQuota
			for i := range got.Quotas {
				if got.Quotas[i].Prefix == tc.prefix {
					quota = &got.Quotas[i]
				}
			}
			if quota == nil {
				t.Fatalf("quota family absent: %+v", got.Quotas)
			}
			if !quota.Limit.Present || !quota.Limit.Valid || quota.Limit.Value != 100 || !quota.Remaining.Present || !quota.Remaining.Valid || quota.Remaining.Value != 0 {
				t.Fatalf("quota numbers lost: %+v", quota)
			}
			if !quota.Reset.Present || !quota.Reset.Valid || quota.Reset.Format != tc.format || !quota.Reset.At.Equal(tc.at) {
				t.Fatalf("reset semantics or raw evidence incorrect: %+v", quota.Reset)
			}
			if !got.RetryAt.Equal(observed.Add(90*time.Second)) || got.DeadlineSource != "ratelimit_reset" {
				t.Fatalf("zero remaining must hold until clock-adjusted reset: %+v", got)
			}
		})
	}
}

func TestParseResponseObservationResetWithoutExhaustionDoesNotHoldSuccess(t *testing.T) {
	observed := observationTime()
	for _, remaining := range []string{"", "1", "invalid"} {
		t.Run("remaining="+remaining, func(t *testing.T) {
			h := make(http.Header)
			h.Set("RateLimit-Reset", "90")
			if remaining != "" {
				h.Set("RateLimit-Remaining", remaining)
			}
			got := ParseResponseObservation(http.MethodGet, http.StatusOK, h, observed)
			if !got.Quotas[0].Reset.Valid {
				t.Fatalf("reset evidence should remain visible even when it does not impose a hold: %+v", got)
			}
			if !got.RetryAt.IsZero() || got.DeadlineSource != "" {
				t.Fatalf("reset alone must not throttle a successful response: %+v", got)
			}
		})
	}
}

func TestParseResponseObservation429UsesResetEvenWithRemaining(t *testing.T) {
	observed := observationTime()
	h := make(http.Header)
	h.Set("RateLimit-Reset", "90")
	h.Set("RateLimit-Remaining", "25")
	got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
	if !got.RetryAt.Equal(observed.Add(90*time.Second)) || got.DeadlineSource != "ratelimit_reset" {
		t.Fatalf("429 must honor reset despite inconsistent remaining: %+v", got)
	}
}

func TestParseResponseObservationTakesLatestProviderDeadline(t *testing.T) {
	observed := observationTime()
	h := make(http.Header)
	h.Set("Retry-After", "30")
	h.Set("RateLimit-Reset", "60")
	h.Set("X-RateLimit-Reset", "1792101690")
	h.Set("X-Rate-Limit-Reset", "1792101720")
	got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
	if !got.RetryAt.Equal(observed.Add(2*time.Minute)) || got.DeadlineSource != "ratelimit_reset" {
		t.Fatalf("must not retry before any known provider deadline: %+v", got)
	}
	for _, quota := range got.Quotas {
		if !quota.Reset.Present || !quota.Reset.Valid {
			t.Fatalf("conflicting deadline evidence was discarded: %+v", got.Quotas)
		}
	}
}

func TestParseResponseObservationQuotaNumbersRejectInvalidAndRetainZero(t *testing.T) {
	observed := observationTime()
	for _, tc := range []struct {
		name   string
		values []string
		valid  bool
	}{
		{name: "zero", values: []string{"0"}, valid: true},
		{name: "negative", values: []string{"-1"}},
		{name: "fraction", values: []string{"1.5"}},
		{name: "overflow", values: []string{"9223372036854775808"}},
		{name: "empty", values: []string{""}},
		{name: "duplicate", values: []string{"0", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := make(http.Header)
			h[http.CanonicalHeaderKey("RateLimit-Limit")] = tc.values
			h[http.CanonicalHeaderKey("RateLimit-Remaining")] = tc.values
			got := ParseResponseObservation(http.MethodGet, http.StatusOK, h, observed)
			for _, value := range []HeaderNumber{got.Quotas[0].Limit, got.Quotas[0].Remaining} {
				if !value.Present || value.Valid != tc.valid || value.Value != 0 {
					t.Fatalf("invalid versus explicit zero evidence confused: %+v", value)
				}
			}
		})
	}
}

func TestParseResponseObservationDoesNotGuessResetUnits(t *testing.T) {
	observed := observationTime()
	h := make(http.Header)
	h.Set("RateLimit-Reset", "1792101690")
	got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
	if !got.Quotas[0].Reset.Present || got.Quotas[0].Reset.Valid {
		t.Fatalf("standard reset must not guess that an excessive delay is a Unix timestamp: %+v", got.Quotas[0])
	}
	if got.DeadlineSource != "fallback" || !got.RetryAt.Equal(observed.Add(time.Minute)) {
		t.Fatalf("unusable reset should use bounded fallback: %+v", got)
	}
}

func TestParseResponseObservationLegacyResetDelay(t *testing.T) {
	for _, prefix := range []string{"X-RateLimit-", "X-Rate-Limit-"} {
		t.Run(prefix, func(t *testing.T) {
			h := make(http.Header)
			h.Set(prefix+"Reset", "120")
			h.Set(prefix+"Remaining", "0")
			got := ParseResponseObservation(http.MethodPost, http.StatusOK, h, observationTime())
			if !got.RetryAt.Equal(observationTime().Add(2*time.Minute)) || got.DeadlineSource != "ratelimit_reset" {
				t.Fatalf("legacy relative reset was treated as a past epoch: %+v", got)
			}
			for _, q := range got.Quotas {
				if q.Prefix == prefix && q.Reset.Format != "delay_seconds" {
					t.Fatalf("selected reset interpretation was lost: %+v", q)
				}
			}
		})
	}
}

func TestParseResponseObservationRetainsOnlySafeRequestIdentifiers(t *testing.T) {
	observed := observationTime()
	uuid := "47d7e610-f938-489a-9c65-b7e73d71f522"
	h := make(http.Header)
	h.Set("X-Li-Request-Id", "req_ABC-123:=+/")
	h.Set("X-Li-Uuid", uuid)
	got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
	if got.RequestID != "req_ABC-123:=+/" || got.UUID != uuid {
		t.Fatalf("safe bounded identifiers were lost: %+v", got)
	}
	for _, value := range []string{"https://example.test/?token=secret", "https://example.test/token=secret", "someone@example.test", "name with spaces", "invalid\nheader", string(make([]byte, 129))} {
		h.Set("X-Li-Request-Id", value)
		h.Set("X-Li-Uuid", value)
		got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
		if got.RequestID != "" || got.UUID != "" {
			t.Fatal("unsafe or unbounded request identifier was retained")
		}
	}
}

func TestParseResponseObservationRejectsDuplicatesAcrossHeaderCasing(t *testing.T) {
	observed := observationTime()
	h := http.Header{
		"Retry-After":         []string{"60"},
		"retry-after":         []string{"120"},
		"RateLimit-Remaining": []string{"0"},
		"ratelimit-remaining": []string{"5"},
	}
	got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, h, observed)
	if !got.RetryAfter.Present || got.RetryAfter.Valid || !got.Quotas[0].Remaining.Present || got.Quotas[0].Remaining.Valid {
		t.Fatalf("differently cased duplicate headers must stay ambiguous: %+v", got)
	}
	if got.DeadlineSource != "fallback" {
		t.Fatalf("duplicate response must not trust one arbitrary header value: %+v", got)
	}
}

func TestResponseObservationSanitizedRecalculatesUntrustedDeadline(t *testing.T) {
	observed := observationTime()
	got := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, http.Header{"Retry-After": []string{"120"}}, observed)
	got.RetryAt = observed.Add(-time.Hour)
	got.DeadlineSource = "untrusted source text"
	got.ProviderReason = "arbitrary provider response text"
	got.ServiceErrorCode = "contains email@example.test"
	got.RequestID = "contains spaces"
	got.UUID = "invalid UUID"
	got.Quotas[0].Limit = HeaderNumber{Present: false, Valid: true, Value: 3}
	got.Quotas[0].Remaining = HeaderNumber{Present: true, Valid: true, Value: -1}
	got = got.Sanitized()
	if !got.RetryAt.Equal(observed.Add(2*time.Minute)) || got.DeadlineSource != "retry_after" {
		t.Fatalf("saved deadline must be rebuilt from validated evidence: %+v", got)
	}
	if got.ProviderReason != "" || got.ServiceErrorCode != "" || got.RequestID != "" || got.UUID != "" {
		t.Fatal("untrusted strings survived the persistence boundary")
	}
	if got.Quotas[0].Limit != (HeaderNumber{}) || got.Quotas[0].Remaining != (HeaderNumber{Present: true}) {
		t.Fatalf("untrusted number validity survived sanitation: %+v", got.Quotas[0])
	}
}

func TestResponseObservationSanitizedRejectsInvalidEnvelope(t *testing.T) {
	valid := ParseResponseObservation(http.MethodPost, http.StatusTooManyRequests, nil, observationTime())
	for _, tc := range []struct {
		name string
		edit func(*ResponseObservation)
	}{
		{name: "missing time", edit: func(o *ResponseObservation) { o.ObservedAt = time.Time{} }},
		{name: "invalid status", edit: func(o *ResponseObservation) { o.StatusCode = 999 }},
		{name: "unrecognized method", edit: func(o *ResponseObservation) { o.Method = "POST /private/path" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := valid
			tc.edit(&got)
			got = got.Sanitized()
			if got != (ResponseObservation{}) {
				t.Fatal("invalid metadata envelope survived sanitation")
			}
		})
	}
}
