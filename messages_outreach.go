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
	if member.URN != recipient {
		return nil, ErrRecipientIdentityMismatch
	}
	if member.ConnectionDegree != 1 {
		return nil, ErrRecipientNotMessageable
	}
	if recipient == me.URN {
		return nil, ErrInvalidParams
	}
	origin, tracking, err := messageToken()
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"message":    map[string]any{"body": map[string]any{"attributes": []any{}, "text": p.Body}, "renderContentUnions": []any{}, "originToken": origin},
		"mailboxUrn": me.URN, "trackingId": tracking, "dedupeByClientGeneratedToken": false,
		"hostRecipientUrns": []string{recipient},
	})
	body, _, err := c.makeWriteRequest(ctx, apiBase+"/voyagerMessagingDashMessengerMessages?action=createMessage", payload)
	if err != nil {
		return nil, err
	}
	value, err := createdValue(body)
	if err != nil {
		return nil, err
	}
	message, messageOK := selectMessagingReceipt(value, []string{"backendUrn", "entityUrn"}, me.URN, "urn:li:msg_message:", "urn:li:messagingMessage:")
	conversation, conversationOK := selectMessagingReceipt(value, []string{"conversationUrn", "backendConversationUrn", "*conversation"}, me.URN, "urn:li:msg_conversation:", "urn:li:messagingThread:")
	if !messageOK || !conversationOK {
		return nil, unknownReceipt()
	}
	for key, expected := range map[string]string{"originToken": origin, "mailboxUrn": me.URN, "senderUrn": me.URN, "recipientUrn": recipient} {
		if observed, exists := value[key]; exists && observed != expected {
			return nil, unknownReceipt()
		}
	}
	for _, key := range []string{"*sender", "*actor"} {
		if observed, exists := value[key]; exists {
			raw, ok := observed.(string)
			raw = strings.TrimPrefix(raw, "urn:li:msg_messagingParticipant:")
			member, err := CanonicalMemberURN(raw)
			if !ok || err != nil || member != me.URN {
				return nil, unknownReceipt()
			}
		}
	}
	if observed, exists := value["hostRecipientUrns"]; exists {
		recipients, ok := observed.([]any)
		if !ok || len(recipients) != 1 || recipients[0] != recipient {
			return nil, unknownReceipt()
		}
	}
	if observed, exists := value["body"]; exists && valueText(observed) != p.Body {
		return nil, unknownReceipt()
	}
	return &MessageReceipt{MessageURN: message, ConversationURN: conversation}, nil
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
