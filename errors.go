package linkedin

import "errors"

var (
	ErrInvalidAuth       = errors.New("linkedin: invalid auth credentials")
	ErrUnauthorized      = errors.New("linkedin: unauthorized")
	ErrAccountRestricted = errors.New("linkedin: account restricted by LinkedIn — do not retry")
	ErrChallengeRequired = errors.New("linkedin: checkpoint/challenge required — solve in a real browser")
	ErrDailyBudget       = errors.New("linkedin: daily request budget exhausted")
	ErrOutsideHours      = errors.New("linkedin: outside configured working hours")
	ErrRoleMismatch      = errors.New("linkedin: client role does not match required role")
	ErrInCooldown        = errors.New("linkedin: client is in operator-imposed cooldown — refusing to make any request")
	ErrRateLimited       = errors.New("linkedin: rate limited")
	ErrNotFound          = errors.New("linkedin: not found")
	ErrInvalidParams     = errors.New("linkedin: invalid parameters")
	ErrRequestFailed     = errors.New("linkedin: request failed")
	ErrParseFailed       = errors.New("linkedin: failed to parse response")
	// ErrTimeout is a typed retryable deadline/budget failure. Hosts should map
	// it to timeout/transient_error instead of surfacing raw context errors.
	ErrTimeout       = errors.New("linkedin: request timed out")
	ErrMessageEmpty  = errors.New("linkedin: message body is empty")
	ErrNoRecipients  = errors.New("linkedin: no recipients or conversation specified")
	ErrNotMember     = errors.New("linkedin: not a member of this group")
	ErrAlreadyMember = errors.New("linkedin: already a member of this group")
	ErrPostEmpty     = errors.New("linkedin: post text is empty")
)
