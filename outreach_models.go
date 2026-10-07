package linkedin

import (
	"errors"
	"fmt"
	"time"
	"unicode/utf16"
)

// These conservative application limits are not claims about LinkedIn's maximums.
const (
	MessageLimit        = 1000
	CommentLimit        = 1000
	InvitationNoteLimit = 200
)

// UTF16Length counts browser-style text units without changing the text.
func UTF16Length(text string) int { return len(utf16.Encode([]rune(text))) }

// Member is a resolved profile and the authenticated sender's observed relationship.
// Zero/unknown values never establish sending eligibility.
type Member struct {
	URN              string `json:"urn"`
	PublicID         string `json:"publicId"`
	ProfileURL       string `json:"profileUrl"`
	ConnectionDegree int    `json:"connectionDegree"`
	InvitationState  string `json:"invitationState"`
}

type CommentTarget struct {
	PostURN          string `json:"postUrn"`
	ActivityURN      string `json:"activityUrn"`
	ParentCommentURN string `json:"parentCommentUrn,omitempty"`
}

type DirectMessageParams struct {
	RecipientURN        string
	RecipientProfileURL string
	ExpectedSenderURN   string
	Body                string
}

type MessageReceipt struct {
	MessageURN      string `json:"messageUrn"`
	ConversationURN string `json:"conversationUrn"`
}

type CreateCommentParams struct {
	PostURN           string
	ActivityURN       string
	ExpectedSenderURN string
	ParentCommentURN  string
	Text              string
}

type InvitationParams struct {
	RecipientURN        string
	RecipientProfileURL string
	ExpectedSenderURN   string
	Note                string
}

type InvitationReceipt struct {
	InvitationURN string `json:"invitationUrn"`
	RecipientURN  string `json:"recipientUrn"`
}

var (
	ErrIdentityUnavailable       = errors.New("linkedin: authenticated identity unavailable")
	ErrSenderIdentityMismatch    = errors.New("linkedin: authenticated sender identity changed")
	ErrRecipientIdentityMismatch = errors.New("linkedin: resolved recipient identity changed")
	ErrRecipientNotMessageable   = errors.New("linkedin: recipient is not an eligible first-degree connection")
	ErrAlreadyConnected          = errors.New("linkedin: recipient is already connected")
	ErrInvitationPending         = errors.New("linkedin: invitation is already pending")
	ErrEmailRequired             = errors.New("linkedin: recipient email is required")
	ErrNoteUnavailable           = errors.New("linkedin: personalized invitation note is unavailable")
	ErrEligibilityUnknown        = errors.New("linkedin: recipient eligibility unavailable")
	ErrWriteUnknown              = errors.New("linkedin: provider write outcome is unknown; do not repeat")
	ErrWriteRejected             = errors.New("linkedin: provider rejected write")
)

// WriteError contains safe provider outcome metadata, never a response body.
// Once dispatch starts, an unproven failure unwraps to ErrWriteUnknown.
type WriteError struct {
	StatusCode int
	RetryAfter time.Duration
	Cause      error
}

func (e *WriteError) Error() string { return fmt.Sprintf("%v (HTTP %d)", e.Cause, e.StatusCode) }
func (e *WriteError) Unwrap() error { return e.Cause }
