package linkedin

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

const rejectionBodyLimit = 64 * 1024

// RejectionDiagnostics explains classification without retaining provider text.
// BodyBytes is a bounded observed count, not the size of a truncated response.
type RejectionDiagnostics struct {
	Version               int    `json:"version"`
	Format                string `json:"format"`
	BodyBytes             int    `json:"body_bytes"`
	Truncated             bool   `json:"truncated"`
	ParseOutcome          string `json:"parse_outcome"`
	CodeType              string `json:"code_type"`
	ServiceCodeType       string `json:"service_code_type"`
	NestedCodeType        string `json:"nested_code_type"`
	NestedServiceCodeType string `json:"nested_service_code_type"`
	Endpoint              string `json:"endpoint"`
	DurationMillis        int64  `json:"duration_ms"`
}

func rejectionDiagnostics(body []byte, contentType, requestURL string, elapsed time.Duration, readErr error) RejectionDiagnostics {
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed > 24*time.Hour {
		elapsed = 24 * time.Hour
	}
	d := RejectionDiagnostics{Version: 1, BodyBytes: min(len(body), rejectionBodyLimit+1),
		Truncated: len(body) > rejectionBodyLimit, Endpoint: diagnosticEndpoint(requestURL),
		DurationMillis: elapsed.Milliseconds(),
		CodeType:       "absent", ServiceCodeType: "absent", NestedCodeType: "absent", NestedServiceCodeType: "absent"}
	trimmed := bytes.TrimSpace(body)
	switch {
	case len(trimmed) == 0:
		d.Format = "empty"
	case bytes.HasPrefix(trimmed, []byte("<")):
		d.Format = "html"
	case json.Valid(body) || strings.Contains(strings.ToLower(contentType), "json"):
		d.Format = "json"
	default:
		d.Format = "other"
	}
	switch {
	case readErr != nil:
		d.ParseOutcome = "read_error"
		return d
	case d.Truncated:
		d.ParseOutcome = "body_too_large"
		return d
	case len(trimmed) == 0:
		d.ParseOutcome = "empty"
		return d
	}
	var response providerRejectionEnvelope
	if json.Unmarshal(body, &response) != nil {
		d.ParseOutcome = "invalid_json"
		return d
	}
	fields := []json.RawMessage{response.Code, response.ServiceErrorCode}
	if response.Data != nil {
		fields = append(fields, response.Data.Code, response.Data.ServiceErrorCode)
	}
	d.CodeType, d.ServiceCodeType = diagnosticCodeType(response.Code), diagnosticCodeType(response.ServiceErrorCode)
	if response.Data != nil {
		d.NestedCodeType, d.NestedServiceCodeType = diagnosticCodeType(response.Data.Code), diagnosticCodeType(response.Data.ServiceErrorCode)
	}
	d.ParseOutcome = "missing_codes"
	known := ""
	service := ""
	conflict := false
	for i, field := range fields {
		if len(field) == 0 || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			continue
		}
		d.ParseOutcome = "unknown_codes"
		value := scalarProviderCode(field)
		_, reason := knownRejection(value)
		if reason != "" {
			if known != "" && known != reason {
				conflict = true
			}
			known = reason
		}
		if i%2 == 1 && digits(value) && len(value) <= 20 {
			if service != "" && service != value {
				conflict = true
			}
			service = value
		}
	}
	if known != "" {
		d.ParseOutcome = "classified"
	}
	if _, reason, _ := providerRejection(body); reason == "account_restricted" || reason == "challenge_required" {
		d.ParseOutcome = "classified"
	}
	if conflict {
		d.ParseOutcome = "conflicting_codes"
	}
	return d
}

func diagnosticCodeType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "absent"
	}
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return "null"
	}
	if len(raw) > 0 && raw[0] == '"' {
		return "string"
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil && len(n) > 0 {
		return "number"
	}
	return "other"
}

func diagnosticEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "www.linkedin.com" {
		return "other"
	}
	switch u.Path {
	case "/voyager/api/voyagerRelationshipsDashMemberRelationships":
		return "invitation"
	case "/voyager/api/voyagerMessagingDashMessengerMessages":
		return "message"
	case "/voyager/api/voyagerSocialDashNormComments":
		return "comment"
	default:
		return "other"
	}
}

func (d RejectionDiagnostics) sanitized() RejectionDiagnostics {
	if d.Version != 1 {
		return RejectionDiagnostics{}
	}
	switch d.Format {
	case "json", "html", "empty", "other":
	default:
		return RejectionDiagnostics{}
	}
	switch d.ParseOutcome {
	case "read_error", "body_too_large", "empty", "invalid_json", "missing_codes", "unknown_codes", "classified", "conflicting_codes":
	default:
		return RejectionDiagnostics{}
	}
	switch d.Endpoint {
	case "invitation", "message", "comment", "other":
	default:
		return RejectionDiagnostics{}
	}
	for _, kind := range []string{d.CodeType, d.ServiceCodeType, d.NestedCodeType, d.NestedServiceCodeType} {
		switch kind {
		case "absent", "null", "string", "number", "other":
		default:
			return RejectionDiagnostics{}
		}
	}
	if d.BodyBytes < 0 || d.BodyBytes > rejectionBodyLimit+1 || d.DurationMillis < 0 || d.DurationMillis > int64((24*time.Hour)/time.Millisecond) || d.Truncated != (d.BodyBytes > rejectionBodyLimit) {
		return RejectionDiagnostics{}
	}
	return d
}
