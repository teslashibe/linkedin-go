package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProviderCodeRetainsOnlyMachineCodes(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"nested invitation code", `{"data":{"code":"CANT_RESEND_YET","status":429}}`, "CANT_RESEND_YET"},
		{"unrecognized nested code", `{"data":{"code":"SOME_NEW_LIMIT_2"},"included":[]}`, "SOME_NEW_LIMIT_2"},
		{"top-level code wins", `{"code":"FIRST_CODE","data":{"code":"SECOND_CODE"}}`, "FIRST_CODE"},
		{"named service code", `{"serviceErrorCode":"PERSONALIZED_INVITATION_LIMIT_REACHED"}`, "PERSONALIZED_INVITATION_LIMIT_REACHED"},
		{"email address", `{"code":"private.name@example.com"}`, ""},
		{"free text", `{"data":{"code":"Too many requests"}}`, ""},
		{"lower-case word", `{"data":{"code":"private_member"}}`, ""},
		{"numeric code", `{"code":429,"serviceErrorCode":123}`, ""},
		{"single letter", `{"code":"A"}`, ""},
		{"leading digit", `{"code":"9_LIMIT"}`, ""},
		{"too long", `{"code":"` + strings.Repeat("A", 65) + `"}`, ""},
		{"malformed", `{`, ""},
		{"empty", ``, ""},
	} {
		if got := providerCode([]byte(tc.body)); got != tc.want {
			t.Fatalf("%s: providerCode=%q want %q", tc.name, got, tc.want)
		}
	}
}

func TestInvitationCodesClassifyAndRetainCode(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason, code, outcome string
		status                            int
		want                              error
	}{
		{"resend cooldown on 429", `{"data":{"code":"CANT_RESEND_YET","status":429},"included":[]}`, "cannot_resend_yet", "CANT_RESEND_YET", "classified", 429, ErrCannotResendYet},
		{"weekly cap on 429", `{"data":{"code":"CANT_INVITE_CONNECTION_LIMIT_REACHED"}}`, "invitation_limit", "CANT_INVITE_CONNECTION_LIMIT_REACHED", "classified", 429, ErrInvitationLimit},
		{"short cap on 429", `{"data":{"code":"MAX_INVITATION_SENT"}}`, "invitation_limit", "MAX_INVITATION_SENT", "classified", 429, ErrInvitationLimit},
		{"unknown code on 429", `{"data":{"code":"SOME_NEW_LIMIT"},"included":[]}`, "rate_limited", "SOME_NEW_LIMIT", "unknown_codes", 429, nil},
		{"resend cooldown on 400", `{"data":{"code":"CANT_RESEND_YET","status":400}}`, "cannot_resend_yet", "CANT_RESEND_YET", "", 400, ErrCannotResendYet},
		{"weekly cap on 400", `{"data":{"code":"WEEKLY_CONNECTIONS_LIMIT_REACHED"}}`, "invitation_limit", "WEEKLY_CONNECTIONS_LIMIT_REACHED", "", 400, ErrInvitationLimit},
		{"unknown code on 422", `{"data":{"code":"SOME_NEW_REFUSAL"}}`, "write_rejected", "SOME_NEW_REFUSAL", "", 422, ErrWriteRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				return response(req, tc.status, tc.body), nil
			})
			_, _, err := c.makeWriteRequest(context.Background(), apiBase+"/voyagerRelationshipsDashMemberRelationships?action=verifyQuotaAndCreateV2", []byte(`{}`))
			var writeErr *WriteError
			if !errors.As(err, &writeErr) || IsWriteUnknown(err) || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.status == 429) != errors.Is(err, ErrRateLimited) {
				t.Fatalf("unexpected outcome: %v", err)
			}
			o := writeErr.Observation
			if o.ProviderReason != tc.reason || o.ProviderCode != tc.code || o.Diagnostics.ParseOutcome != tc.outcome {
				t.Fatalf("reason=%q code=%q outcome=%q", o.ProviderReason, o.ProviderCode, o.Diagnostics.ParseOutcome)
			}
			if c.LastResponse().ProviderCode != tc.code {
				t.Fatal("client snapshot lost the provider code")
			}
			var decoded ResponseObservation
			raw, _ := json.Marshal(o)
			if json.Unmarshal(raw, &decoded) != nil || decoded.Sanitized().ProviderCode != tc.code {
				t.Fatalf("provider code did not survive persistence: %s", raw)
			}
		})
	}
}

func TestProviderCodeSanitizing(t *testing.T) {
	base := ResponseObservation{ObservedAt: time.Now(), Method: http.MethodPost, StatusCode: 429, ProviderCode: "CANT_RESEND_YET"}
	if base.Sanitized().ProviderCode != "CANT_RESEND_YET" {
		t.Fatal("safe code dropped")
	}
	for _, mutate := range []func(*ResponseObservation){
		func(o *ResponseObservation) { o.ProviderCode = "private.name@example.com" },
		func(o *ResponseObservation) { o.ProviderCode = "lower_case" },
		func(o *ResponseObservation) { o.ProviderCode = strings.Repeat("A", 65) },
		func(o *ResponseObservation) { o.StatusCode = 201 },
	} {
		o := base
		mutate(&o)
		if o.Sanitized().ProviderCode != "" {
			t.Fatalf("unsafe provider code retained: %+v", o)
		}
	}
}
