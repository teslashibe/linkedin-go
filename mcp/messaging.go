package mcp

import (
	"context"

	linkedin "github.com/teslashibe/linkedin-go"
	"github.com/teslashibe/mcptool"
)

// GetConversationsInput is the typed input for linkedin_get_conversations.
type GetConversationsInput struct {
	Start int `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count int `json:"count,omitempty" jsonschema:"description=results per page,minimum=1,maximum=50,default=20"`
}

func getConversations(ctx context.Context, c *linkedin.Client, in GetConversationsInput) (any, error) {
	res, err := c.GetConversations(ctx, linkedin.ConversationParams{Start: in.Start, Count: in.Count})
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 20
	}
	return mcptool.PageOf(res, "", limit), nil
}

// GetMessagesInput is the typed input for linkedin_get_messages.
type GetMessagesInput struct {
	ConversationURN string `json:"conversation_urn" jsonschema:"description=conversation URN from linkedin_get_conversations,required"`
	Start           int    `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count           int    `json:"count,omitempty" jsonschema:"description=results per page,minimum=1,maximum=50,default=20"`
}

func getMessages(ctx context.Context, c *linkedin.Client, in GetMessagesInput) (any, error) {
	res, err := c.GetMessages(ctx, linkedin.MessageListParams{
		ConversationURN: in.ConversationURN,
		Start:           in.Start,
		Count:           in.Count,
	})
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 20
	}
	return mcptool.PageOf(res, "", limit), nil
}

// SendMessageInput is the typed input for linkedin_send_message. Pass either
// ConversationURN (existing thread) OR Recipients (new conversation), not both.
type SendMessageInput struct {
	ConversationURN string   `json:"conversation_urn,omitempty" jsonschema:"description=existing conversation URN (mutually exclusive with recipients)"`
	Recipients      []string `json:"recipients,omitempty" jsonschema:"description=profile URNs for a new conversation (mutually exclusive with conversation_urn)"`
	Body            string   `json:"body" jsonschema:"description=plain-text message body,required"`
}

func sendMessage(ctx context.Context, c *linkedin.Client, in SendMessageInput) (any, error) {
	if err := c.SendMessage(ctx, linkedin.SendMessageParams{
		ConversationURN: in.ConversationURN,
		Recipients:      in.Recipients,
		Body:            in.Body,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

var messagingTools = []mcptool.Tool{
	mcptool.Define[*linkedin.Client, GetConversationsInput](
		"linkedin_get_conversations",
		"List recent LinkedIn conversations for the authenticated user",
		"GetConversations",
		getConversations,
	),
	mcptool.Define[*linkedin.Client, GetMessagesInput](
		"linkedin_get_messages",
		"Fetch messages within a specific LinkedIn conversation",
		"GetMessages",
		getMessages,
	),
	mcptool.Define[*linkedin.Client, SendMessageInput](
		"linkedin_send_message",
		"Send a LinkedIn DM in an existing conversation or to new recipients",
		"SendMessage",
		sendMessage,
	),
}
