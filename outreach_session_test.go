package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

const threadURN = "urn:li:msg_conversation:(urn:li:fsd_profile:SELF," + createdConversationID + ")"

// threadClient answers reads as fixtureRead does, for a recipient who is not a
// first-degree connection, and each createMessage with value(payload).
func threadClient(t *testing.T, posts *atomic.Int32, value func(message map[string]any) map[string]any) *Client {
	return offlineClient(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			return fixtureRead(req, 2), nil
		}
		posts.Add(1)
		if req.URL.Path != "/voyager/api/voyagerMessagingDashMessengerMessages" || req.URL.Query().Get("action") != "createMessage" || req.GetBody != nil {
			t.Fatal("wrong or replayable message request")
		}
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		message, ok := payload["message"].(map[string]any)
		if !ok || payload["mailboxUrn"] != "urn:li:fsd_profile:SELF" || message["conversationUrn"] != threadURN || valueText(message["body"]) != "exact reply" {
			t.Fatalf("changed sender, conversation or text: %v", payload)
		}
		if _, exists := payload["hostRecipientUrns"]; exists {
			t.Fatal("an in-thread message names no new recipients")
		}
		body, err := json.Marshal(map[string]any{"value": value(message)})
		if err != nil {
			t.Fatal(err)
		}
		return response(req, http.StatusOK, string(body)), nil
	})
}

func threadParams() ConversationMessageParams {
	return ConversationMessageParams{ConversationURN: threadURN, RecipientURN: "urn:li:fsd_profile:RECIPIENT", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Body: "exact reply"}
}

func TestConversationMessageSendsOnceIntoTheSendersThread(t *testing.T) {
	var posts atomic.Int32
	c := threadClient(t, &posts, func(message map[string]any) map[string]any {
		return createMessageValue(message["originToken"].(string), message["body"])
	})
	op, err := c.OpenOutreach(context.Background(), "urn:li:fsd_profile:SELF")
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	h, err := op.PrepareConversationMessage(context.Background(), threadParams())
	if err != nil || h.Conversation() != threadURN {
		t.Fatalf("prepared=%v err=%v", h, err)
	}
	receipt, err := h.Send(context.Background())
	if err != nil || receipt == nil || receipt.MessageURN != createdMessageURN || receipt.ConversationURN != threadURN || posts.Load() != 1 {
		t.Fatalf("receipt=%+v err=%v posts=%d", receipt, err, posts.Load())
	}
	if _, err := h.Send(context.Background()); !errors.Is(err, ErrInvalidParams) || posts.Load() != 1 {
		t.Fatalf("a prepared message sends once: err=%v posts=%d", err, posts.Load())
	}
}

func TestConversationMessageRefusesBeforeAnyWrite(t *testing.T) {
	other := "urn:li:msg_conversation:(urn:li:fsd_profile:OTHER," + createdConversationID + ")"
	for _, tc := range []struct {
		name   string
		mutate func(*ConversationMessageParams)
		want   error
	}{
		{"another sender", func(p *ConversationMessageParams) { p.ExpectedSenderURN = "urn:li:fsd_profile:OTHER" }, ErrSenderIdentityMismatch},
		{"another mailbox's conversation", func(p *ConversationMessageParams) { p.ConversationURN = other }, ErrInvalidParams},
		{"a bare conversation id", func(p *ConversationMessageParams) { p.ConversationURN = "urn:li:msg_conversation:" + createdConversationID }, ErrInvalidParams},
		{"a malformed conversation", func(p *ConversationMessageParams) { p.ConversationURN = "urn:li:msg_conversation:(urn:li:fsd_profile:SELF,a b)" }, ErrInvalidParams},
		{"no recipient", func(p *ConversationMessageParams) { p.RecipientURN = "" }, ErrInvalidParams},
		{"the sender as recipient", func(p *ConversationMessageParams) { p.RecipientURN = "urn:li:fsd_profile:SELF" }, ErrInvalidParams},
		{"blank text", func(p *ConversationMessageParams) { p.Body = " \n" }, ErrInvalidParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts, reads atomic.Int32
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPost {
					posts.Add(1)
				} else {
					reads.Add(1)
				}
				return fixtureRead(req, 2), nil
			})
			op, err := c.OpenOutreach(context.Background(), "urn:li:fsd_profile:SELF")
			if err != nil {
				t.Fatal(err)
			}
			defer op.Close()
			before := reads.Load()
			p := threadParams()
			tc.mutate(&p)
			if _, err := op.PrepareConversationMessage(context.Background(), p); !errors.Is(err, tc.want) || posts.Load() != 0 || reads.Load() != before {
				t.Fatalf("err=%v want=%v posts=%d reads=%d", err, tc.want, posts.Load(), reads.Load()-before)
			}
		})
	}
	t.Run("a closed session", func(t *testing.T) {
		var posts atomic.Int32
		c := threadClient(t, &posts, func(message map[string]any) map[string]any {
			return createMessageValue(message["originToken"].(string), message["body"])
		})
		op, err := c.OpenOutreach(context.Background(), "urn:li:fsd_profile:SELF")
		if err != nil {
			t.Fatal(err)
		}
		h, err := op.PrepareConversationMessage(context.Background(), threadParams())
		if err != nil {
			t.Fatal(err)
		}
		op.Close()
		if _, err := h.Send(context.Background()); err == nil || posts.Load() != 0 {
			t.Fatalf("err=%v posts=%d", err, posts.Load())
		}
	})
}

func TestConversationMessageAcknowledgmentMustMatch(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(map[string]any)
	}{
		{"another conversation", "request_mismatch", func(v map[string]any) {
			v["conversationUrn"] = "urn:li:msg_conversation:(urn:li:fsd_profile:SELF,2-other)"
			delete(v, "backendConversationUrn")
		}},
		{"no message", "message_receipt", func(v map[string]any) { delete(v, "entityUrn"); delete(v, "backendUrn") }},
		{"changed text", "request_mismatch", func(v map[string]any) { v["body"] = map[string]any{"text": "changed"} }},
		{"another sender", "identity_mismatch", func(v map[string]any) { v["senderUrn"] = "urn:li:msg_messagingParticipant:urn:li:fsd_profile:OTHER" }},
		{"another recipient", "identity_mismatch", func(v map[string]any) { v["recipientUrn"] = "urn:li:fsd_profile:OTHER" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			c := threadClient(t, &posts, func(message map[string]any) map[string]any {
				value := createMessageValue(message["originToken"].(string), message["body"])
				tc.mutate(value)
				return value
			})
			op, err := c.OpenOutreach(context.Background(), "urn:li:fsd_profile:SELF")
			if err != nil {
				t.Fatal(err)
			}
			defer op.Close()
			h, err := op.PrepareConversationMessage(context.Background(), threadParams())
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := h.Send(context.Background())
			if receipt != nil || !IsWriteUnknown(err) || WriteUnknownReason(err) != tc.reason || posts.Load() != 1 {
				t.Fatalf("receipt=%+v err=%v reason=%q posts=%d", receipt, err, WriteUnknownReason(err), posts.Load())
			}
		})
	}
}
