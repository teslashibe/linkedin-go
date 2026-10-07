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

func TestOutreachGzipAcknowledgmentSingleDispatch(t *testing.T) {
	const text = "  exact 😀\n "
	const messageURN = "urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)"
	const conversationURN = "urn:li:msg_conversation:(urn:li:fsd_profile:SELF,2-conversation==)"
	for _, tc := range []struct {
		name, reason string
		mutate       func(map[string]any)
	}{
		{name: "confirmed"},
		{name: "corrupt_gzip", reason: "response_body"},
		{name: "truncated_gzip", reason: "response_body"},
		{name: "unsupported_encoding", reason: "response_body"},
		{name: "missing_creation_value", reason: "creation_value"},
		{name: "missing_message", reason: "message_receipt", mutate: func(value map[string]any) { delete(value, "entityUrn") }},
		{name: "missing_conversation", reason: "conversation_receipt", mutate: func(value map[string]any) { delete(value, "conversationUrn") }},
		{name: "wrong_sender", reason: "identity_mismatch", mutate: func(value map[string]any) { value["senderUrn"] = "urn:li:fsd_profile:OTHER" }},
		{name: "wrong_recipient", reason: "identity_mismatch", mutate: func(value map[string]any) { value["recipientUrn"] = "urn:li:fsd_profile:OTHER" }},
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
				value := map[string]any{
					"entityUrn": messageURN, "conversationUrn": conversationURN,
					"senderUrn": "urn:li:fsd_profile:SELF", "recipientUrn": "urn:li:fsd_profile:RECIPIENT",
					"originToken": origin, "body": message["body"], "hostRecipientUrns": recipients,
				}
				if tc.mutate != nil {
					tc.mutate(value)
				}
				body, err := json.Marshal(map[string]any{"data": map[string]any{"value": value}})
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
				r := response(req, http.StatusCreated, "")
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
				if err != nil || receipt == nil || receipt.MessageURN != messageURN || receipt.ConversationURN != conversationURN || WriteUnknownReason(err) != "" {
					t.Fatalf("receipt=%+v err=%v", receipt, err)
				}
			} else if receipt != nil || !IsWriteUnknown(err) || WriteUnknownReason(err) != tc.reason {
				t.Fatalf("receipt=%+v reason=%q err=%v", receipt, WriteUnknownReason(err), err)
			}
		})
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
