package linkedin

import (
	"encoding/json"
	"errors"
	"strings"
)

// providerRejection never returns arbitrary provider strings. Numeric service
// codes are retained for correlation, without assigning undocumented meaning.
func providerRejection(body []byte) (error, string, string) {
	if err := detectRestrictionInBody(body); err != nil {
		if errors.Is(err, ErrChallengeRequired) {
			return err, "challenge_required", ""
		}
		return err, "account_restricted", ""
	}
	var response struct {
		Code             json.RawMessage `json:"code"`
		ServiceErrorCode json.RawMessage `json:"serviceErrorCode"`
		Data             *struct {
			Code             json.RawMessage `json:"code"`
			ServiceErrorCode json.RawMessage `json:"serviceErrorCode"`
		} `json:"data"`
	}
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
	}
	return nil, ""
}

func knownProviderReason(reason string) bool {
	switch reason {
	case "", "write_rejected", "rate_limited", "already_connected", "invitation_pending", "email_required", "note_unavailable", "recipient_not_messageable", "account_restricted", "challenge_required":
		return true
	}
	return false
}
