package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GetConversations returns paginated conversations for the authenticated user.
func (c *Client) GetConversations(ctx context.Context, p ConversationParams) ([]Conversation, error) {
	count := p.Count
	if count <= 0 {
		count = 20
	}
	reqURL := fmt.Sprintf("%s/messaging/conversations?keyVersion=LEGACY_INBOX&start=%d&count=%d",
		apiBase, p.Start, count)

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp conversationListResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	convos := make([]Conversation, 0, len(resp.Elements))
	for _, elem := range resp.Elements {
		conv := Conversation{
			URN:            elem.EntityURN,
			LastActivityAt: elem.LastActivityAt,
			Unread:         !elem.Read,
		}

		for _, pe := range elem.Participants {
			part := Participant{}
			if pe.MiniProfile != nil {
				part.URN = pe.MiniProfile.EntityURN
				part.FirstName = pe.MiniProfile.FirstName
				part.LastName = pe.MiniProfile.LastName
				part.Headline = pe.MiniProfile.Occupation
			}
			if part.URN != "" {
				conv.Participants = append(conv.Participants, part)
			}
		}

		if len(elem.Events) > 0 {
			last := elem.Events[0]
			msg := &Message{
				URN:       last.EntityURN,
				SenderURN: last.From,
				SentAt:    last.CreatedAt,
			}
			if last.EventContent != nil && last.EventContent.MessageEvent != nil {
				msg.Body = last.EventContent.MessageEvent.Body
			}
			conv.LastMessage = msg
		}

		convos = append(convos, conv)
	}

	return convos, nil
}

// GetMessages returns paginated messages for a specific conversation.
func (c *Client) GetMessages(ctx context.Context, p MessageListParams) ([]Message, error) {
	if p.ConversationURN == "" {
		return nil, fmt.Errorf("%w: conversation URN required", ErrInvalidParams)
	}
	count := p.Count
	if count <= 0 {
		count = 20
	}

	convID := extractURNID(p.ConversationURN)
	reqURL := fmt.Sprintf("%s/messaging/conversations/%s/events?start=%d&count=%d",
		apiBase, convID, p.Start, count)

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Elements []messageEvent `json:"elements"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	msgs := make([]Message, 0, len(resp.Elements))
	for _, ev := range resp.Elements {
		msg := Message{
			URN:       ev.EntityURN,
			SenderURN: ev.From,
			SentAt:    ev.CreatedAt,
		}
		if ev.EventContent != nil && ev.EventContent.MessageEvent != nil {
			msg.Body = ev.EventContent.MessageEvent.Body
		}
		msgs = append(msgs, msg)
	}

	return msgs, nil
}

// SendMessage sends a plain-text message. Specify either ConversationURN
// (existing thread) or Recipients (new conversation), not both.
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) error {
	if p.Body == "" {
		return ErrMessageEmpty
	}
	if p.ConversationURN == "" && len(p.Recipients) == 0 {
		return ErrNoRecipients
	}

	msgCreate := map[string]interface{}{
		"body":             p.Body,
		"attachments":      []interface{}{},
		"attributedBody":   map[string]interface{}{"text": p.Body, "attributes": []interface{}{}},
		"mediaAttachments": []interface{}{},
	}

	eventCreate := map[string]interface{}{
		"value": map[string]interface{}{
			"com.linkedin.voyager.messaging.create.MessageCreate": msgCreate,
		},
	}

	if p.ConversationURN != "" {
		convID := extractURNID(p.ConversationURN)
		reqURL := fmt.Sprintf("%s/messaging/conversations/%s/events?action=create",
			apiBase, convID)

		payload := map[string]interface{}{"eventCreate": eventCreate}
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRequestFailed, err)
		}

		_, err = c.makePostRequest(ctx, reqURL, data)
		return err
	}

	reqURL := fmt.Sprintf("%s/messaging/conversations?action=create", apiBase)
	payload := map[string]interface{}{
		"keyVersion": "LEGACY_INBOX",
		"conversationCreate": map[string]interface{}{
			"eventCreate": eventCreate,
			"recipients":  p.Recipients,
			"subtype":     "MEMBER_TO_MEMBER",
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	_, err = c.makePostRequest(ctx, reqURL, data)
	return err
}

func extractURNID(urn string) string {
	parts := strings.Split(urn, ":")
	if len(parts) == 0 {
		return urn
	}
	return parts[len(parts)-1]
}
