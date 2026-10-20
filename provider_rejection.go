package linkedin

import (
	"encoding/json"
	"errors"
	"strings"
)

type providerRejectionEnvelope struct {
	Code             json.RawMessage `json:"code"`
	ServiceErrorCode json.RawMessage `json:"serviceErrorCode"`
	Data             *struct {
		Code             json.RawMessage `json:"code"`
		ServiceErrorCode json.RawMessage `json:"serviceErrorCode"`
	} `json:"data"`
}

// providerRejection never returns arbitrary provider strings. Numeric service
// codes are retained for correlation, without assigning undocumented meaning.
func providerRejection(body []byte) (error, string, string) {
	if err := detectRestrictionInBody(body); err != nil {
		if errors.Is(err, ErrChallengeRequired) {
			return err, "challenge_required", ""
		}
		return err, "account_restricted", ""
	}
	var response providerRejectionEnvelope
	if len(body) > 64*1024 || json.Unmarshal(body, &response) != nil {
		return ErrWriteRejected, "write_rejected", ""
	}
	codes := []json.RawMessage{response.Code, response.ServiceErrorCode}
	services := []json.RawMessage{response.ServiceErrorCode}
	if response.Data != nil {
		codes = append(codes, response.Data.Code, response.Data.ServiceErrorCode)
		services = append(services, response.Data.ServiceErrorCode)
	}
	numeric := ""
	for _, raw := range services {
		code := scalarProviderCode(raw)
		if digits(code) && len(code) <= 20 {
			if numeric != "" && numeric != code {
				numeric = ""
				break
			}
			numeric = code
		}
	}
	var selected error
	reason := "write_rejected"
	for _, raw := range codes {
		err, known := knownRejection(scalarProviderCode(raw))
		if err == nil {
			continue
		}
		if selected != nil && !errors.Is(selected, err) {
			return ErrWriteRejected, "write_rejected", numeric
		}
		selected, reason = err, known
	}
	if selected == nil {
		selected = ErrWriteRejected
	}
	return selected, reason, numeric
}

func scalarProviderCode(raw json.RawMessage) string {
	if len(raw) == 0 || len(raw) > 128 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.ToUpper(value)
	}
	value = string(raw)
	if digits(value) {
		return value
	}
	return ""
}

func knownRejection(code string) (error, string) {
	switch code {
	case "ALREADY_CONNECTED":
		return ErrAlreadyConnected, "already_connected"
	case "INVITATION_PENDING", "ALREADY_INVITED":
		return ErrInvitationPending, "invitation_pending"
	case "EMAIL_REQUIRED":
		return ErrEmailRequired, "email_required"
	case "NOTE_UNAVAILABLE", "PERSONALIZED_INVITATION_LIMIT_REACHED":
		return ErrNoteUnavailable, "note_unavailable"
	case "RECIPIENT_NOT_MESSAGEABLE", "NOT_CONNECTED", "RECIPIENT_NOT_FIRST_DEGREE_CONNECTION":
		return ErrRecipientNotMessageable, "recipient_not_messageable"
	// Invitation codes observed by other Voyager clients, notably Waalaxy's
	// connect error handling: a per-recipient resend cooldown, and weekly or
	// short-term sender caps.
	case "CANT_RESEND_YET", "CANNOT_RESEND_YET":
		return ErrCannotResendYet, "cannot_resend_yet"
	case "CANT_INVITE_CONNECTION_LIMIT_REACHED", "WEEKLY_CONNECTIONS_LIMIT_REACHED", "MAX_INVITATION_SENT":
		return ErrInvitationLimit, "invitation_limit"
	}
	return nil, ""
}

// providerCode retains one machine error code exactly as LinkedIn sent it.
// Only short upper-case identifiers qualify, so free text, emails, numbers and
// lower-case words are never retained.
func providerCode(body []byte) string {
	var response providerRejectionEnvelope
	if len(body) > rejectionBodyLimit || json.Unmarshal(body, &response) != nil {
		return ""
	}
	fields := []json.RawMessage{response.Code}
	if response.Data != nil {
		fields = append(fields, response.Data.Code)
	}
	fields = append(fields, response.ServiceErrorCode)
	if response.Data != nil {
		fields = append(fields, response.Data.ServiceErrorCode)
	}
	for _, raw := range fields {
		var value string
		if len(raw) == 0 || len(raw) > 128 || json.Unmarshal(raw, &value) != nil {
			continue
		}
		if safeProviderCode(value) {
			return value
		}
	}
	return ""
}

func safeProviderCode(v string) bool {
	if len(v) < 2 || len(v) > 64 || v[0] < 'A' || v[0] > 'Z' {
		return false
	}
	for _, ch := range v {
		if !(ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_') {
			return false
		}
	}
	return true
}

func knownProviderReason(reason string) bool {
	switch reason {
	case "", "write_rejected", "rate_limited", "already_connected", "invitation_pending", "email_required", "note_unavailable", "recipient_not_messageable", "account_restricted", "challenge_required", "cannot_resend_yet", "invitation_limit":
		return true
	}
	return false
}
