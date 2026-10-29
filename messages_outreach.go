package linkedin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

// SendMessageWithReceipt permits a single first-degree recipient, using an
// explicit profile-to-member match and the live authenticated sender identity.
func (c *Client) SendMessageWithReceipt(ctx context.Context, p DirectMessageParams) (*MessageReceipt, error) {
	if err := validateOutreachText(p.Body, MessageLimit); err != nil {
		return nil, err
	}
	recipient, err := CanonicalMemberURN(p.RecipientURN)
	if err != nil {
		return nil, ErrInvalidParams
	}
	me, err := c.requireSender(ctx, p.ExpectedSenderURN)
	if err != nil {
		return nil, err
	}
	member, err := c.ResolveMember(ctx, p.RecipientProfileURL)
	if err != nil {
		return nil, err
	}
	if err := messageRecipient(member, recipient, me.URN); err != nil {
		return nil, err
	}
	return c.sendDirectMessage(ctx, p, recipient, me.URN, nil)
}

func messageRecipient(member *Member, recipient, sender string) error {
	if member.URN != recipient {
		return ErrRecipientIdentityMismatch
	}
	if member.ConnectionDegree != 1 {
		return ErrRecipientNotMessageable
	}
	if recipient == sender {
		return ErrInvalidParams
	}
	return nil
}

func (c *Client) sendDirectMessage(ctx context.Context, p DirectMessageParams, recipient, sender string, guard func() error) (*MessageReceipt, error) {
	origin, tracking, err := messageToken()
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"message":    map[string]any{"body": map[string]any{"attributes": []any{}, "text": p.Body}, "renderContentUnions": []any{}, "originToken": origin},
		"mailboxUrn": sender, "trackingId": tracking, "dedupeByClientGeneratedToken": false,
		"hostRecipientUrns": []string{recipient},
	})
	body, _, err := c.makeGuardedWriteRequest(ctx, apiBase+"/voyagerMessagingDashMessengerMessages?action=createMessage", payload, guard)
	if err != nil {
		return nil, err
	}
	value, err := createdValue(body)
	if err != nil {
		return nil, unknownReceiptReason(writeUnknownCreationValue)
	}
	message, messageOK := selectMessagingReceipt(value, []string{"entityUrn", "backendUrn"}, sender, "urn:li:msg_message:", "urn:li:messagingMessage:")
	conversation, conversationOK := selectMessagingReceipt(value, []string{"conversationUrn", "backendConversationUrn", "*conversation"}, sender, "urn:li:msg_conversation:", "urn:li:messagingThread:")
	if !messageOK {
		return nil, unknownReceiptReason(writeUnknownMessageReceipt)
	}
	if !conversationOK {
		return nil, unknownReceiptReason(writeUnknownConversationReceipt)
	}
	if observed, exists := value["originToken"]; exists && observed != origin {
		return nil, unknownReceiptReason(writeUnknownRequestMismatch)
	}
	if !messageParticipantsMatch(value, sender, recipient) {
		return nil, unknownReceiptReason(writeUnknownIdentityMismatch)
	}
	if observed, exists := value["body"]; exists && valueText(observed) != p.Body {
		return nil, unknownReceiptReason(writeUnknownRequestMismatch)
	}
	return &MessageReceipt{MessageURN: message, ConversationURN: conversation}, nil
}

// messageParticipantsMatch accepts the approved sender and recipient as profile
// URNs or as the messaging-participant URNs that wrap them. A present identity
// that does not parse, or names another member, fails closed.
func messageParticipantsMatch(value map[string]any, sender, recipient string) bool {
	matches := func(observed any, member string) bool {
		raw, ok := observed.(string)
		canonical, err := CanonicalMemberURN(strings.TrimPrefix(raw, "urn:li:msg_messagingParticipant:"))
		return ok && err == nil && canonical == member
	}
	for _, key := range []string{"mailboxUrn", "senderUrn", "*sender", "*actor"} {
		if observed, exists := value[key]; exists && !matches(observed, sender) {
			return false
		}
	}
	if observed, exists := value["recipientUrn"]; exists && !matches(observed, recipient) {
		return false
	}
	if observed, exists := value["hostRecipientUrns"]; exists {
		recipients, ok := observed.([]any)
		if !ok || len(recipients) != 1 || !matches(recipients[0], recipient) {
			return false
		}
	}
	return true
}

func messageToken() (string, string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	raw[6] = raw[6]&15 | 64
	raw[8] = raw[8]&63 | 128
	origin := fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	runes := make([]rune, len(raw))
	for i, b := range raw {
		runes[i] = rune(b)
	}
	return origin, string(runes), nil
}
