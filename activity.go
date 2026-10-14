package linkedin

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Activity reads use the same Voyager messenger queries as the web client.
// Query IDs rotate independently of the SDK; callers may override them with
// reviewed values. No endpoint override is accepted.
type ActivityQueries struct{ Conversations, Messages, ByRecipients string }

var defaultActivityQueries = ActivityQueries{
	Conversations: "voyagerMessagingDashMessengerConversations.85aab4c1cdc6bf22aeb28edc93d5bf74",
	Messages:      "voyagerMessagingDashMessengerMessages.073958b6fdfe5f5ceeb4d0416523317e",
}

func WithActivityQueries(q ActivityQueries) Option { return func(c *Client) { c.activityQueries = q } }

type ActivityConversation struct {
	URN              string            `json:"urn"`
	Participants     []string          `json:"participants"`
	Group            bool              `json:"group"`
	LastActivityAt   int64             `json:"lastActivityAt"`
	Messages         []ActivityMessage `json:"messages"`
	PreviousCursor   string            `json:"previousCursor,omitempty"`
	MessagesComplete bool              `json:"messagesComplete"`
}
type ActivityMessage struct {
	URN             string `json:"urn"`
	ConversationURN string `json:"conversationUrn"`
	SenderURN       string `json:"senderUrn,omitempty"`
	Body            string `json:"body"`
	SentAt          int64  `json:"sentAt"`
	// Kind is human, system or unknown. Only human may count as a reply.
	Kind          string `json:"kind"`
	HasAttachment bool   `json:"hasAttachment"`
}
type ActivityConversationPage struct {
	Conversations []ActivityConversation `json:"conversations"`
	SyncToken     string                 `json:"syncToken,omitempty"`
	// Initial sync is a bounded provider snapshot, not proof of full history.
	Complete bool `json:"complete"`
}
type ActivityMessagePage struct {
	Messages       []ActivityMessage `json:"messages"`
	PreviousCursor string            `json:"previousCursor,omitempty"`
	Complete       bool              `json:"complete"`
}

// GetActivityConversations returns the bounded initial mailbox snapshot. Hosts
// must not interpret a missing conversation as deleted or as a failed send.
func (c *Client) GetActivityConversations(ctx context.Context, expectedSender string) (*ActivityConversationPage, error) {
	if err := c.activitySender(expectedSender); err != nil {
		return nil, err
	}
	q := c.activityQueries.Conversations
	if q == "" {
		q = defaultActivityQueries.Conversations
	}
	body, err := c.activityQuery(ctx, q, "(mailboxUrn:"+url.QueryEscape(expectedSender)+")")
	if err != nil {
		return nil, err
	}
	node, err := activityNode(body, "messengerConversationsBySyncToken")
	if err != nil {
		return nil, err
	}
	var page struct {
		Elements []rawActivityConversation `json:"elements"`
		Metadata struct {
			NewSyncToken string `json:"newSyncToken"`
		} `json:"metadata"`
	}
	if json.Unmarshal(node, &page) != nil || page.Elements == nil {
		return nil, ErrParseFailed
	}
	out := &ActivityConversationPage{Conversations: make([]ActivityConversation, 0, len(page.Elements)), SyncToken: page.Metadata.NewSyncToken}
	for _, raw := range page.Elements {
		conv, err := raw.parse()
		if err != nil {
			return nil, err
		}
		out.Conversations = append(out.Conversations, conv)
	}
	return out, nil
}

// GetActivityMessages follows an opaque backwards cursor for one conversation.
// Empty pages may have a next cursor; Complete requires explicit null cursor.
func (c *Client) GetActivityMessages(ctx context.Context, expectedSender, conversationURN, previousCursor string, count int) (*ActivityMessagePage, error) {
	if err := c.activitySender(expectedSender); err != nil {
		return nil, err
	}
	if !validActivityURN(conversationURN, "msg_conversation") || count < 1 || count > 100 || len(previousCursor) > 4096 {
		return nil, ErrInvalidParams
	}
	q := c.activityQueries.Messages
	if q == "" {
		q = defaultActivityQueries.Messages
	}
	variables := "(conversationUrn:" + url.QueryEscape(conversationURN) + ",count:" + strconv.Itoa(count)
	if previousCursor != "" {
		variables += ",prevCursor:" + url.QueryEscape(previousCursor)
	}
	body, err := c.activityQuery(ctx, q, variables+")")
	if err != nil {
		return nil, err
	}
	node, err := activityNode(body, "messengerMessagesByConversation")
	if err != nil {
		return nil, err
	}
	var page struct {
		Elements []rawActivityMessage       `json:"elements"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if json.Unmarshal(node, &page) != nil || page.Elements == nil {
		return nil, ErrParseFailed
	}
	rawCursor, ok := page.Metadata["prevCursor"]
	if !ok {
		return nil, ErrParseFailed
	}
	out := &ActivityMessagePage{Messages: make([]ActivityMessage, 0, len(page.Elements)), Complete: string(rawCursor) == "null"}
	if !out.Complete && (json.Unmarshal(rawCursor, &out.PreviousCursor) != nil || out.PreviousCursor == "" || out.PreviousCursor == previousCursor) {
		return nil, ErrParseFailed
	}
	for _, raw := range page.Elements {
		m, err := raw.parse(conversationURN)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, m)
	}
	return out, nil
}
func (c *Client) activitySender(expected string) error {
	canonical, err := CanonicalMemberURN(expected)
	if err != nil || expected != canonical {
		return ErrInvalidParams
	}
	if c.VerifiedMemberURN() != expected {
		return ErrSenderIdentityMismatch
	}
	return nil
}
func (c *Client) activityQuery(ctx context.Context, query, variables string) ([]byte, error) {
	if query == "" || strings.ContainsAny(query, "/?&#\r\n") {
		return nil, ErrInvalidParams
	}
	// Variables are Rest.li encoded above. Encoding the query map again would
	// double-encode URNs and cursors.
	return c.makeRequest(ctx, apiBase+"/voyagerMessagingGraphQL/graphql?queryId="+url.QueryEscape(query)+"&variables="+variables)
}

// activityNode accepts ordinary GraphQL and Voyager's normalized data/data +
// included envelope. References must resolve uniquely; no partial page is
// returned when a referenced entity is missing or the schema has drifted.
func activityNode(body []byte, key string) (json.RawMessage, error) {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || activityError(root) {
		return nil, ErrParseFailed
	}
	refs := map[string]any{}
	if included, ok := root["included"].([]any); ok {
		if len(included) > 10000 {
			return nil, ErrParseFailed
		}
		for _, raw := range included {
			m, ok := raw.(map[string]any)
			if !ok {
				return nil, ErrParseFailed
			}
			id, _ := m["entityUrn"].(string)
			if id == "" {
				continue
			}
			if _, ok := refs[id]; ok {
				return nil, ErrParseFailed
			}
			refs[id] = m
		}
	}
	data, _ := root["data"].(map[string]any)
	if data == nil || activityError(data) {
		return nil, ErrParseFailed
	}
	if inner, ok := data["data"].(map[string]any); ok {
		data = inner
	}
	if activityError(data) {
		return nil, ErrParseFailed
	}
	node, ok := data[key]
	if !ok {
		return nil, ErrParseFailed
	}
	var expand func(any, int) (any, error)
	expand = func(v any, depth int) (any, error) {
		if depth > 20 {
			return nil, ErrParseFailed
		}
		switch x := v.(type) {
		case map[string]any:
			if activityError(x) {
				return nil, ErrParseFailed
			}
			out := map[string]any{}
			for k, v := range x {
				// A message's conversation is a backlink to the same collection
				// that contains it. Retain its identity without walking the cycle.
				if x["$type"] == "com.linkedin.messenger.Message" && (k == "*conversation" || k == "conversation") && v != nil {
					if _, duplicate := x["conversation"]; k == "*conversation" && duplicate {
						return nil, ErrParseFailed
					}
					bound := v
					if k == "*conversation" {
						ref, ok := v.(string)
						if !ok {
							return nil, ErrParseFailed
						}
						bound = refs[ref]
					}
					conversation, ok := bound.(map[string]any)
					id, valid := conversation["entityUrn"].(string)
					if !ok || !valid || activityError(conversation) || !validActivityURN(id, "msg_conversation") {
						return nil, ErrParseFailed
					}
					out["conversation"] = map[string]any{"entityUrn": id}
					continue
				}
				if strings.HasPrefix(k, "*") {
					key := strings.TrimPrefix(k, "*")
					if _, exists := x[key]; exists {
						return nil, ErrParseFailed
					}
					switch ref := v.(type) {
					case nil:
						v = nil
					case string:
						resolved, ok := refs[ref]
						if !ok {
							return nil, ErrParseFailed
						}
						v = resolved
					case []any:
						list := []any{}
						for _, id := range ref {
							refID, ok := id.(string)
							if !ok {
								return nil, ErrParseFailed
							}
							resolved, ok := refs[refID]
							if !ok {
								return nil, ErrParseFailed
							}
							list = append(list, resolved)
						}
						v = list
					default:
						return nil, ErrParseFailed
					}
					k = key
				}
				resolved, e := expand(v, depth+1)
				if e != nil {
					return nil, e
				}
				out[k] = resolved
			}
			return out, nil
		case []any:
			out := make([]any, 0, len(x))
			for _, v := range x {
				r, e := expand(v, depth+1)
				if e != nil {
					return nil, e
				}
				out = append(out, r)
			}
			return out, nil
		default:
			return v, nil
		}
	}
	expanded, e := expand(node, 0)
	if e != nil {
		return nil, e
	}
	return json.Marshal(expanded)
}

func validActivityURN(urn, kind string) bool {
	prefix := "urn:li:" + kind + ":"
	id, ok := strings.CutPrefix(urn, prefix)
	return ok && id != "" && len(id) <= 1024 && !strings.ContainsAny(id, "\r\n /?#&")
}

type rawActivityMessage struct {
	URN          string `json:"entityUrn"`
	Conversation string `json:"backendConversationUrn"`
	Thread       struct {
		URN string `json:"entityUrn"`
	} `json:"conversation"`
	Sender struct {
		URN string `json:"hostIdentityUrn"`
	} `json:"sender"`
	Body *struct {
		Text string `json:"text"`
	} `json:"body"`
	DeliveredAt   int64             `json:"deliveredAt"`
	RenderContent []json.RawMessage `json:"renderContent"`
	Actor         json.RawMessage   `json:"actor"`
}

func (raw rawActivityMessage) parse(conversation string) (ActivityMessage, error) {
	if !validActivityURN(raw.URN, "msg_message") || raw.DeliveredAt <= 0 || raw.Thread.URN != "" && raw.Thread.URN != conversation {
		return ActivityMessage{}, ErrParseFailed
	}
	m := ActivityMessage{URN: raw.URN, ConversationURN: conversation, Body: "", SentAt: raw.DeliveredAt, Kind: "unknown"}
	sender, err := CanonicalMemberURN(raw.Sender.URN)
	if err == nil {
		m.SenderURN = sender
	}
	if raw.Body != nil {
		m.Body = raw.Body.Text
	}

	// Known generated cards are never human replies, even when they carry text.
	// Unrecognized render variants stay unknown until their semantics are reviewed.
	generated, unknown := false, false
	for _, content := range raw.RenderContent {
		var fields map[string]json.RawMessage
		if json.Unmarshal(content, &fields) != nil {
			return ActivityMessage{}, ErrParseFailed
		}
		for key, value := range fields {
			if string(value) == "null" || key == "$type" || key == "$recipeTypes" {
				continue
			}
			switch key {
			case "awayMessage", "callMessageContent", "messageAdRenderContent", "conversationAdsMessageContent", "sponsoredMessageOption", "videoMeeting":
				generated = true
			case "audio", "externalMedia", "file", "vectorImage", "video":
				m.HasAttachment = true
			case "forwardedMessageContent", "repliedMessageContent":
			default:
				unknown = true
			}
		}
	}
	actorPresent := len(raw.Actor) > 0 && string(raw.Actor) != "null"
	actorIsSender := false
	if actorPresent {
		var actor struct {
			URN string `json:"hostIdentityUrn"`
		}
		if json.Unmarshal(raw.Actor, &actor) == nil {
			identity, e := CanonicalMemberURN(actor.URN)
			actorIsSender = e == nil && identity == m.SenderURN
		}
	}
	// Normal messages include an actor equal to their sender. An unrelated or
	// unresolved actor still cannot establish a human reply.
	if actorPresent && !actorIsSender || generated {
		m.Kind = "system"
	} else if err == nil && !unknown && ((raw.Body != nil && strings.TrimSpace(raw.Body.Text) != "") || m.HasAttachment) {
		m.Kind = "human"
	}

	return m, nil
}

type rawActivityConversation struct {
	URN            string `json:"entityUrn"`
	Group          bool   `json:"groupChat"`
	LastActivityAt int64  `json:"lastActivityAt"`
	Participants   []struct {
		URN string `json:"hostIdentityUrn"`
	} `json:"conversationParticipants"`
	Messages struct {
		Elements []rawActivityMessage       `json:"elements"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	} `json:"messages"`
}

func (raw rawActivityConversation) parse() (ActivityConversation, error) {
	if !validActivityURN(raw.URN, "msg_conversation") || raw.LastActivityAt <= 0 || raw.Participants == nil {
		return ActivityConversation{}, ErrParseFailed
	}
	out := ActivityConversation{URN: raw.URN, Group: raw.Group, LastActivityAt: raw.LastActivityAt, Participants: []string{}, Messages: []ActivityMessage{}}
	for _, p := range raw.Participants {
		urn, err := CanonicalMemberURN(p.URN)
		if err != nil {
			out.Group = true
			continue
		}
		out.Participants = append(out.Participants, urn)
	}
	for _, rawMessage := range raw.Messages.Elements {
		m, err := rawMessage.parse(out.URN)
		if err != nil {
			return ActivityConversation{}, err
		}
		out.Messages = append(out.Messages, m)
	}
	if cursor, ok := raw.Messages.Metadata["prevCursor"]; ok && string(cursor) == "null" {
		out.MessagesComplete = true
	}
	if cursor, ok := raw.Messages.Metadata["prevCursor"]; ok && string(cursor) != "null" {
		if json.Unmarshal(cursor, &out.PreviousCursor) != nil {
			return ActivityConversation{}, ErrParseFailed
		}
	}
	return out, nil
}

// FindActivityConversation checks one approved recipient explicitly. An empty
// result means the provider reported no matching conversation at this instant;
// absence from GetActivityConversations alone never provides that evidence.
func (c *Client) FindActivityConversation(ctx context.Context, expectedSender, recipient string) (*ActivityConversation, error) {
	if err := c.activitySender(expectedSender); err != nil {
		return nil, err
	}
	canonical, err := CanonicalMemberURN(recipient)
	if err != nil || canonical != recipient || recipient == expectedSender {
		return nil, ErrInvalidParams
	}
	q := c.activityQueries.ByRecipients
	if q == "" {
		q = "voyagerMessagingDashMessengerConversations.03f1c8258bef74ea493d767a1191e8d5"
	}
	body, err := c.activityQuery(ctx, q, "(mailboxUrn:"+url.QueryEscape(expectedSender)+",recipients:List("+url.QueryEscape(recipient)+"))")
	if err != nil {
		return nil, err
	}
	node, err := activityNode(body, "messengerConversationsByRecipients")
	if err != nil {
		return nil, err
	}
	var result struct {
		Elements []rawActivityConversation `json:"elements"`
	}
	if json.Unmarshal(node, &result) != nil || result.Elements == nil || len(result.Elements) > 1 {
		return nil, ErrParseFailed
	}
	if len(result.Elements) == 0 {
		return nil, nil
	}
	conv, err := result.Elements[0].parse()
	if err != nil {
		return nil, err
	}
	if conv.Group || len(conv.Participants) != 2 {
		return nil, ErrEligibilityUnknown
	}
	foundSelf, foundOther := false, false
	for _, p := range conv.Participants {
		foundSelf = foundSelf || p == expectedSender
		foundOther = foundOther || p == recipient
	}
	if !foundSelf || !foundOther {
		return nil, ErrRecipientIdentityMismatch
	}
	return &conv, nil
}

func activityError(obj map[string]any) bool {
	for _, key := range []string{"error", "errors"} {
		v, ok := obj[key]
		if !ok || v == nil {
			continue
		}
		if list, ok := v.([]any); ok && len(list) == 0 {
			continue
		}
		return true
	}
	if status, ok := obj["status"].(float64); ok && status >= 400 {
		return true
	}
	return false
}
