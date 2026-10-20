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

func TestRateLimitDiagnosticOutcomes(t *testing.T) {
	cases := []struct{ name, body, contentType, format, outcome string }{
		{"empty", "", "application/json", "empty", "empty"},
		{"malformed", "{", "application/json", "json", "invalid_json"},
		{"HTML", "<html>private.name@example.test</html>", "text/html", "html", "invalid_json"},
		{"no codes", `{"message":"private response text"}`, "application/json", "json", "missing_codes"},
		{"unknown code", `{"code":"PRIVATE_CODE","serviceErrorCode":123}`, "application/json", "json", "unknown_codes"},
		{"known code", `{"code":"NOTE_UNAVAILABLE"}`, "application/json", "json", "classified"},
		{"conflicting", `{"code":"NOTE_UNAVAILABLE","data":{"code":"ALREADY_CONNECTED"}}`, "application/json", "json", "conflicting_codes"},
		{"numeric conflict", `{"serviceErrorCode":123,"data":{"serviceErrorCode":456}}`, "application/json", "json", "conflicting_codes"},
		{"oversized", strings.Repeat("x", rejectionBodyLimit+200), "application/json", "json", "body_too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{"GET", "POST"} {
				var calls atomic.Int32
				c := offlineClient(func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					r := response(req, 429, tc.body)
					r.Header.Set("Content-Type", tc.contentType)
					return r, nil
				})
				endpoint := apiBase + "/voyagerRelationshipsDashMemberRelationships?action=private-query"
				var err error
				if method == "POST" {
					_, _, err = c.makeWriteRequest(context.Background(), endpoint, []byte(`{"private":"payload"}`))
				} else {
					_, err = c.doRequest(context.Background(), endpoint)
				}
				if !errors.Is(err, ErrRateLimited) || calls.Load() != 1 {
					t.Fatalf("classification/dispatch changed: calls=%d err=%v", calls.Load(), err)
				}
				o := c.LastResponse()
				d := o.Diagnostics
				if d.Format != tc.format || d.ParseOutcome != tc.outcome || d.Endpoint != "invitation" || d.Version != 1 || d.BodyBytes != min(len(tc.body), rejectionBodyLimit+1) || d.Truncated != (len(tc.body) > rejectionBodyLimit) {
					t.Fatalf("bad diagnostic: %+v", d)
				}
				if d != o.Sanitized().Diagnostics {
					t.Fatalf("diagnostic lost to sanitizer: %+v", d)
				}
				raw, _ := json.Marshal(o)
				for _, secret := range []string{"private.name", "private response", "PRIVATE_CODE", "private-query", "payload"} {
					if strings.Contains(string(raw), secret) {
						t.Fatalf("leaked %q: %s", secret, raw)
					}
				}
			}
		})
	}
}

func TestDiagnosticTypesAndSanitizing(t *testing.T) {
	d := rejectionDiagnostics([]byte(`{"code":null,"serviceErrorCode":12,"data":{"code":"unknown","serviceErrorCode":{}}}`), "application/json", apiBase+"/fixture", time.Second, nil)
	if d.CodeType != "null" || d.ServiceCodeType != "number" || d.NestedCodeType != "string" || d.NestedServiceCodeType != "other" || d.DurationMillis != 1000 {
		t.Fatalf("wrong field types: %+v", d)
	}
	for _, mutate := range []func(*RejectionDiagnostics){
		func(d *RejectionDiagnostics) { d.ParseOutcome = "private error" }, func(d *RejectionDiagnostics) { d.Endpoint = "https://private" }, func(d *RejectionDiagnostics) { d.CodeType = "private-code" }, func(d *RejectionDiagnostics) { d.BodyBytes = rejectionBodyLimit + 2 }, func(d *RejectionDiagnostics) { d.DurationMillis = -1 },
	} {
		bad := d
		mutate(&bad)
		if bad.sanitized() != (RejectionDiagnostics{}) {
			t.Fatal("unsafe diagnostic accepted")
		}
	}
	d = rejectionDiagnostics(nil, "application/json", apiBase+"/fixture", time.Second, errors.New("private reader error"))
	if d.ParseOutcome != "read_error" {
		t.Fatalf("read failure lost: %+v", d)
	}
}
