package linkedin

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// ActivityConversationListParams traverses the provider's INBOX category.
// It is not a delta token or an unread filter. Hosts overlap conversation IDs
// and last-activity times between traversals and retain historical events.
type ActivityConversationListParams struct {
	NextCursor string
	Count      int // default 20, maximum 25
}

// ListActivityConversations reads one INBOX-category page, including read
// conversations. Complete means this category's pagination ended, not that
// archived conversations were included or that a missing thread was deleted.
// Only metadata is returned: fetch changed tracked threads with
// GetActivityMessages after applying the host's authorization and recipient
// filters. Existing GetActivityConversations retains its snapshot contract.
func (c *Client) ListActivityConversations(ctx context.Context, expectedSender string, p ActivityConversationListParams) (*ActivityConversationPage, error) {
	if err := c.activitySender(expectedSender); err != nil {
		return nil, err
	}
	if p.Count == 0 {
		p.Count = 20
	}
	if p.Count < 1 || p.Count > 25 || len(p.NextCursor) > 4096 {
		return nil, ErrInvalidParams
	}
	q := c.activityQueries.ConversationsByCategory
	if q == "" {
		q = defaultActivityQueries.ConversationsByCategory
	}
	variables := "(query:(predicateUnions:List((conversationCategoryPredicate:(category:INBOX)))),count:" + strconv.Itoa(p.Count) + ",mailboxUrn:" + url.QueryEscape(expectedSender)
	if p.NextCursor != "" {
		variables += ",nextCursor:" + url.QueryEscape(p.NextCursor)
	}
	body, err := c.activityQuery(ctx, q, variables+")")
	if err != nil {
		return nil, err
	}
	node, err := activityCategoryNode(body)
	if err != nil {
		return nil, err
	}
	return parseActivityConversationList(node, expectedSender, p)
}

// The category query includes optional last-message and profile decorations.
// Resolve only the collection, conversation metadata and participant identities
// needed here. An unrelated message/media reference must not hide an otherwise
// complete page, and no private message body enters the result.
func activityCategoryNode(body []byte) ([]byte, error) {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || activityError(root) {
		return nil, ErrParseFailed
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
	collection, ok := data["messengerConversationsByCategoryQuery"].(map[string]any)
	if !ok || activityError(collection) {
		return nil, ErrParseFailed
	}
	included, _ := root["included"].([]any)
	if len(included) > 10000 {
		return nil, ErrParseFailed
	}
	resolve := func(ref string) (map[string]any, error) {
		var selected map[string]any
		count := 0
		for _, entry := range included {
			obj, _ := entry.(map[string]any)
			if firstString(obj, "entityUrn") == ref {
				selected, count = obj, count+1
			}
		}
		if count != 1 || activityError(selected) {
			return nil, ErrParseFailed
		}
		return selected, nil
	}
	// These are the only two source-documented collection representations.
	elements := func(obj map[string]any, field string) ([]map[string]any, error) {
		inline, hasInline := obj[field]
		references, hasReferences := obj["*"+field]
		if hasInline == hasReferences {
			return nil, ErrParseFailed
		}
		value := inline
		if hasReferences {
			value = references
		}
		list, ok := value.([]any)
		if !ok {
			return nil, ErrParseFailed
		}
		out := make([]map[string]any, 0, len(list))
		for _, item := range list {
			var entry map[string]any
			if hasReferences {
				ref, ok := item.(string)
				if !ok || ref == "" {
					return nil, ErrParseFailed
				}
				var err error
				entry, err = resolve(ref)
				if err != nil {
					return nil, err
				}
			} else {
				entry, ok = item.(map[string]any)
				if !ok || activityError(entry) {
					return nil, ErrParseFailed
				}
			}
			out = append(out, entry)
		}
		return out, nil
	}
	conversations, err := elements(collection, "elements")
	if err != nil {
		return nil, err
	}
	metadata, ok := collection["metadata"].(map[string]any)
	if !ok || activityError(metadata) {
		return nil, ErrParseFailed
	}
	projected := make([]any, 0, len(conversations))
	for _, conversation := range conversations {
		participants, err := elements(conversation, "conversationParticipants")
		if err != nil {
			return nil, err
		}
		identities := make([]any, 0, len(participants))
		for _, participant := range participants {
			identities = append(identities, map[string]any{"entityUrn": participant["entityUrn"], "hostIdentityUrn": participant["hostIdentityUrn"]})
		}
		projected = append(projected, map[string]any{"entityUrn": conversation["entityUrn"], "groupChat": conversation["groupChat"], "lastActivityAt": conversation["lastActivityAt"], "conversationParticipants": identities})
	}
	return json.Marshal(map[string]any{"elements": projected, "metadata": metadata})
}

func parseActivityConversationList(node []byte, sender string, p ActivityConversationListParams) (*ActivityConversationPage, error) {
	var raw struct {
		Elements *[]struct {
			URN            string `json:"entityUrn"`
			Group          bool   `json:"groupChat"`
			LastActivityAt int64  `json:"lastActivityAt"`
			Participants   *[]struct {
				URN       string `json:"hostIdentityUrn"`
				EntityURN string `json:"entityUrn"`
			} `json:"conversationParticipants"`
		} `json:"elements"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if json.Unmarshal(node, &raw) != nil || raw.Elements == nil || len(*raw.Elements) > p.Count {
		return nil, ErrParseFailed
	}
	out := &ActivityConversationPage{Conversations: []ActivityConversation{}}
	if cursor, present := raw.Metadata["nextCursor"]; present && string(cursor) != "null" {
		if json.Unmarshal(cursor, &out.NextCursor) != nil || len(out.NextCursor) > 4096 {
			return nil, ErrParseFailed
		}
	}
	// The documented category query omits the cursor (or returns null/empty)
	// when exhausted. Unlike message history it has no explicit-null contract.
	out.Complete = out.NextCursor == ""
	if !out.Complete && out.NextCursor == p.NextCursor {
		return nil, ErrParseFailed
	}
	seen := map[string]bool{}
	for _, r := range *raw.Elements {
		if !validActivityURN(r.URN, "msg_conversation") || seen[r.URN] || r.LastActivityAt <= 0 || r.Participants == nil {
			return nil, ErrParseFailed
		}
		seen[r.URN] = true
		conv := ActivityConversation{URN: r.URN, Group: r.Group, LastActivityAt: r.LastActivityAt, Participants: []string{}, Messages: []ActivityMessage{}}
		participants := map[string]bool{}
		for _, participant := range *r.Participants {
			identity := participant.URN
			if identity != "" {
				canonical, err := CanonicalMemberURN(identity)
				if err != nil {
					return nil, ErrIdentityUnavailable
				}
				identity = canonical
			}
			if participant.EntityURN != "" {
				entity := strings.TrimPrefix(participant.EntityURN, "urn:li:msg_messagingParticipant:")
				canonical, err := CanonicalMemberURN(entity)
				if err == nil {
					if identity != "" && identity != canonical {
						return nil, ErrIdentityUnavailable
					}
					identity = canonical
				}
			}
			canonical, err := CanonicalMemberURN(identity)
			if err != nil {
				return nil, ErrIdentityUnavailable
			}
			if participants[canonical] {
				return nil, ErrParseFailed
			}
			participants[canonical] = true
			conv.Participants = append(conv.Participants, canonical)
		}
		if !participants[sender] {
			return nil, ErrSenderIdentityMismatch
		}
		out.Conversations = append(out.Conversations, conv)
	}
	return out, nil
}
