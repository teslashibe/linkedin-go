package linkedin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func gzipFixture(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

const (
	createdMessageID       = "2-MTc2MTY4NjYyMDQxMGI1MzA0OC0wMDQmZml4dHVyZQ=="
	createdConversationID  = "2-ZjM5YjZlZjYtZml4dHVyZS10aHJlYWQ="
	createdMessageURN      = "urn:li:msg_message:(urn:li:fsd_profile:SELF," + createdMessageID + ")"
	createdConversationURN = "urn:li:msg_conversation:(urn:li:fsd_profile:SELF," + createdConversationID + ")"
)

// createMessageValue has the shape of LinkedIn's 200 createMessage value: the
// sender as a messaging participant, entity and backend aliases, and the echoed
// origin token and body. The identifiers are synthetic.
func createMessageValue(origin string, body any) map[string]any {
	return map[string]any{
		"_type": "com.linkedin.messenger.Message", "renderContentUnions": []any{}, "deliveredAt": 1761686620410, "messageBodyRenderFormat": "DEFAULT",
		"entityUrn": createdMessageURN, "backendUrn": "urn:li:messagingMessage:" + createdMessageID,
		"conversationUrn": createdConversationURN, "backendConversationUrn": "urn:li:messagingThread:" + createdConversationID,
		"senderUrn": "urn:li:msg_messagingParticipant:urn:li:fsd_profile:SELF", "*sender": "urn:li:msg_messagingParticipant:urn:li:fsd_profile:SELF",
		"originToken": origin, "body": body,
	}
}

func TestOutreachGzipAcknowledgmentSingleDispatch(t *testing.T) {
	const text = "  exact 😀\n "
	for _, tc := range []struct {
		name, reason string
		mutate       func(map[string]any)
	}{
		{name: "confirmed"},
		{name: "corrupt_gzip", reason: "response_body"},
		{name: "truncated_gzip", reason: "response_body"},
		{name: "unsupported_encoding", reason: "response_body"},
		{name: "missing_creation_value", reason: "creation_value"},
		{name: "wrong_origin", reason: "request_mismatch", mutate: func(value map[string]any) { value["originToken"] = "other-origin" }},
		{name: "changed_text", reason: "request_mismatch", mutate: func(value map[string]any) { value["body"] = map[string]any{"text": "changed"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Accept-Encoding") != "gzip" {
					t.Fatalf("unsupported encoding advertised: %q", req.Header.Get("Accept-Encoding"))
				}
				if req.Method != http.MethodPost {
					return fixtureRead(req, 1), nil
				}
				writes.Add(1)
				if req.URL.Path != "/voyager/api/voyagerMessagingDashMessengerMessages" || req.URL.Query().Get("action") != "createMessage" || req.GetBody != nil {
					t.Fatal("wrong or replayable DM request")
				}
				var payload map[string]any
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				message, ok := payload["message"].(map[string]any)
				if !ok || valueText(message["body"]) != text || payload["mailboxUrn"] != "urn:li:fsd_profile:SELF" {
					t.Fatal("changed approved text or sender")
				}
				recipients, ok := payload["hostRecipientUrns"].([]any)
				if !ok || len(recipients) != 1 || recipients[0] != "urn:li:fsd_profile:RECIPIENT" {
					t.Fatal("changed approved recipient")
				}
				origin, ok := message["originToken"].(string)
				if !ok || origin == "" {
					t.Fatal("missing origin token")
				}
				value := createMessageValue(origin, message["body"])
				if tc.mutate != nil {
					tc.mutate(value)
				}
				body, err := json.Marshal(map[string]any{"value": value})
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "missing_creation_value" {
					body = []byte(`{}`)
				}
				encoded := gzipFixture(t, body)
				encoding := "gzip"
				switch tc.name {
				case "corrupt_gzip":
					encoded = []byte("not a gzip acknowledgment")
				case "truncated_gzip":
					encoded = encoded[:len(encoded)-8]
				case "unsupported_encoding":
					encoding, encoded = "br", body
				}
				r := response(req, http.StatusOK, "")
				r.Header.Set("Content-Encoding", encoding)
				r.Body = io.NopCloser(bytes.NewReader(encoded))
				return r, nil
			})
			receipt, err := c.SendMessageWithReceipt(context.Background(), DirectMessageParams{
				RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient",
				ExpectedSenderURN: "urn:li:fsd_profile:SELF", Body: text,
			})
			if writes.Load() != 1 {
				t.Fatalf("POST dispatched %d times", writes.Load())
			}
			if tc.reason == "" {
				if err != nil || receipt == nil || receipt.MessageURN != createdMessageURN || receipt.ConversationURN != createdConversationURN || WriteUnknownReason(err) != "" {
					t.Fatalf("receipt=%+v err=%v", receipt, err)
				}
			} else if receipt != nil || !IsWriteUnknown(err) || WriteUnknownReason(err) != tc.reason {
				t.Fatalf("receipt=%+v reason=%q err=%v", receipt, WriteUnknownReason(err), err)
			}
		})
	}
}

// Both DM send paths must confirm LinkedIn's own receipt shape and keep the
// entity URNs that inbox reads return. Wrong-member echoes are checked in both
// the plain profile form and the messaging-participant form.
func TestOutreachCreateMessageReceipt(t *testing.T) {
	const otherProfile = "urn:li:fsd_profile:OTHER"
	const other = "urn:li:msg_messagingParticipant:" + otherProfile
	for _, tc := range []struct {
		name, reason, detail string
		mutate               func(map[string]any)
	}{
		{name: "confirmed"},
		{name: "profile_sender", mutate: func(value map[string]any) { value["senderUrn"] = "urn:li:fsd_profile:SELF" }},
		{name: "participant_recipient", mutate: func(value map[string]any) {
			value["hostRecipientUrns"] = []any{"urn:li:msg_messagingParticipant:urn:li:fsd_profile:RECIPIENT"}
		}},
		{name: "backend_message_only", mutate: func(value map[string]any) { delete(value, "entityUrn") }},
		{name: "backend_conversation_only", mutate: func(value map[string]any) { delete(value, "conversationUrn") }},
		{name: "other_mailbox", reason: "identity_mismatch", detail: "mailbox", mutate: func(value map[string]any) { value["mailboxUrn"] = other }},
		{name: "other_profile_mailbox", reason: "identity_mismatch", detail: "mailbox", mutate: func(value map[string]any) { value["mailboxUrn"] = otherProfile }},
		{name: "other_sender", reason: "identity_mismatch", detail: "sender", mutate: func(value map[string]any) { value["senderUrn"] = other }},
		{name: "other_profile_sender", reason: "identity_mismatch", detail: "sender", mutate: func(value map[string]any) { value["senderUrn"] = otherProfile }},
		{name: "unparseable_sender", reason: "identity_mismatch", detail: "sender", mutate: func(value map[string]any) {
			value["senderUrn"] = "urn:li:msg_messagingParticipant:urn:li:fsd_company:1"
		}},
		{name: "other_sender_reference", reason: "identity_mismatch", detail: "sender_ref", mutate: func(value map[string]any) { value["*sender"] = other }},
		{name: "other_actor", reason: "identity_mismatch", detail: "actor", mutate: func(value map[string]any) { value["*actor"] = otherProfile }},
		{name: "other_recipient", reason: "identity_mismatch", detail: "recipient", mutate: func(value map[string]any) { value["recipientUrn"] = other }},
		{name: "other_profile_recipient", reason: "identity_mismatch", detail: "recipient", mutate: func(value map[string]any) { value["recipientUrn"] = otherProfile }},
		{name: "other_host_recipient", reason: "identity_mismatch", detail: "host_recipients", mutate: func(value map[string]any) { value["hostRecipientUrns"] = []any{other} }},
		{name: "other_profile_host_recipient", reason: "identity_mismatch", detail: "host_recipients", mutate: func(value map[string]any) {
			value["hostRecipientUrns"] = []any{otherProfile}
		}},
		{name: "extra_host_recipient", reason: "identity_mismatch", detail: "host_recipients", mutate: func(value map[string]any) {
			value["hostRecipientUrns"] = []any{"urn:li:fsd_profile:RECIPIENT", other}
		}},
		{name: "backend_id_conflict", reason: "message_receipt", mutate: func(value map[string]any) { value["backendUrn"] = "urn:li:messagingMessage:2-other==" }},
		{name: "missing_message_ids", reason: "message_receipt", mutate: func(value map[string]any) {
			delete(value, "entityUrn")
			delete(value, "backendUrn")
		}},
		{name: "missing_conversation_ids", reason: "conversation_receipt", mutate: func(value map[string]any) {
			delete(value, "conversationUrn")
			delete(value, "backendConversationUrn")
		}},
	} {
		for _, path := range []string{"direct", "prepared"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				var writes atomic.Int32
				c := offlineClient(func(req *http.Request) (*http.Response, error) {
					if req.Method != http.MethodPost {
						return fixtureRead(req, 1), nil
					}
					writes.Add(1)
					var payload map[string]any
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					message := payload["message"].(map[string]any)
					value := createMessageValue(message["originToken"].(string), message["body"])
					if tc.mutate != nil {
						tc.mutate(value)
					}
					body, err := json.Marshal(map[string]any{"value": value})
					if err != nil {
						t.Fatal(err)
					}
					return response(req, http.StatusOK, string(body)), nil
				})
				p := DirectMessageParams{
					RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient",
					ExpectedSenderURN: "urn:li:fsd_profile:SELF", Body: "approved",
				}
				var receipt *MessageReceipt
				var err error
				if path == "prepared" {
					op, openErr := c.OpenOutreach(context.Background(), p.ExpectedSenderURN)
					if openErr != nil {
						t.Fatal(openErr)
					}
					defer op.Close()
					h, prepareErr := op.PrepareMessage(context.Background(), p)
					if prepareErr != nil {
						t.Fatal(prepareErr)
					}
					receipt, err = h.Send(context.Background())
				} else {
					receipt, err = c.SendMessageWithReceipt(context.Background(), p)
				}
				if writes.Load() != 1 {
					t.Fatalf("POST dispatched %d times", writes.Load())
				}
				if tc.reason == "" {
					if err != nil || receipt == nil || receipt.MessageURN != createdMessageURN || receipt.ConversationURN != createdConversationURN {
						t.Fatalf("receipt=%+v err=%v", receipt, err)
					}
				} else if receipt != nil || !IsWriteUnknown(err) || WriteUnknownReason(err) != tc.reason || WriteUnknownDetail(err) != tc.detail {
					t.Fatalf("receipt=%+v reason=%q detail=%q err=%v", receipt, WriteUnknownReason(err), WriteUnknownDetail(err), err)
				}
			})
		}
	}
}

func TestOutreachSupportedEncodingReads(t *testing.T) {
	for _, public := range []bool{false, true} {
		t.Run(fmt.Sprint("public=", public), func(t *testing.T) {
			const body = `{"elements":[]}`
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.Header.Get("Accept-Encoding") != "gzip" {
					t.Fatal("read advertised unsupported encodings")
				}
				r := response(req, http.StatusOK, "")
				r.Header.Set("Content-Encoding", "gzip")
				r.Body = io.NopCloser(bytes.NewReader(gzipFixture(t, []byte(body))))
				return r, nil
			})
			var got []byte
			var err error
			if public {
				got, err = c.doPublicGet(context.Background(), baseURL+"/jobs-guest/fixture")
			} else {
				got, err = c.doRequest(context.Background(), apiBase+"/fixture")
			}
			if err != nil || string(got) != body {
				t.Fatalf("body=%q err=%v", got, err)
			}
		})
	}
}

func TestOutreachGzipWarmupRestrictionAndBound(t *testing.T) {
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/feed/" || req.Header.Get("Accept-Encoding") != "gzip" {
			t.Fatal("unexpected warm-up request")
		}
		r := response(req, http.StatusOK, "")
		r.Header.Set("Content-Encoding", "gzip")
		r.Body = io.NopCloser(bytes.NewReader(gzipFixture(t, []byte("<html>Your account is temporarily restricted</html>"))))
		return r, nil
	})
	c.warmedUp.Store(false)
	if err := c.HealthCheck(context.Background()); !errors.Is(err, ErrAccountRestricted) {
		t.Fatalf("compressed restriction missed: %v", err)
	}
	r := response(nil, http.StatusOK, "")
	r.Header.Set("Content-Encoding", "gzip")
	r.Body = io.NopCloser(bytes.NewReader(gzipFixture(t, []byte(strings.Repeat("a", 128*1024)))))
	body, err := readResponseBodyLimit(r, 64*1024)
	if err != nil || len(body) != 64*1024 {
		t.Fatalf("warm-up body bound: bytes=%d err=%v", len(body), err)
	}
}

func TestWriteUnknownReasonIsBounded(t *testing.T) {
	if got := WriteUnknownReason(fmt.Errorf("wrapped: %w", unknownReceiptReason(writeUnknownMessageReceipt))); got != "message_receipt" {
		t.Fatalf("wrapped reason=%q", got)
	}
	if got := WriteUnknownReason(&WriteError{Cause: ErrWriteUnknown, unknownReason: "provider content must stay private"}); got != "unclassified" {
		t.Fatalf("unbounded reason=%q", got)
	}
	for _, err := range []error{nil, ErrWriteRejected, &WriteError{Cause: ErrUnauthorized, unknownReason: writeUnknownResponseStatus}} {
		if got := WriteUnknownReason(err); got != "" {
			t.Fatalf("definite outcome received unknown reason=%q", got)
		}
	}
}
