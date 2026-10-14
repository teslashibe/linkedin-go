package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const conversationFixture = `{"data":{"messengerConversationsBySyncToken":{"metadata":{"newSyncToken":"opaque"},"elements":[{"entityUrn":"urn:li:msg_conversation:thread","lastActivityAt":1700000000000,"groupChat":false,"conversationParticipants":[{"hostIdentityUrn":"urn:li:fsd_profile:SELF"},{"hostIdentityUrn":"urn:li:fsd_profile:OTHER"}],"messages":{"elements":[{"entityUrn":"urn:li:msg_message:event","deliveredAt":1700000000000,"sender":{"hostIdentityUrn":"urn:li:fsd_profile:OTHER"},"body":{"text":"Hello"},"renderContent":[]}],"metadata":{"prevCursor":"older"}}}]}}}`

func activityFixtureClient(t *testing.T, body string) *Client {
	t.Helper()
	c := offlineClient(func(r *http.Request) (*http.Response, error) { return response(r, 200, body), nil })
	c.verifiedMember.Store("urn:li:fsd_profile:SELF")
	return c
}
func TestActivityConversationSnapshot(t *testing.T) {
	c := activityFixtureClient(t, conversationFixture)
	p, e := c.GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF")
	if e != nil || len(p.Conversations) != 1 || p.Complete || p.SyncToken != "opaque" {
		t.Fatalf("page=%+v err=%v", p, e)
	}
	v := p.Conversations[0]
	if v.Group || len(v.Participants) != 2 || v.Messages[0].Kind != "human" || v.Messages[0].Body != "Hello" {
		t.Fatalf("conversation=%+v", v)
	}
}
func TestActivityPagesFailClosed(t *testing.T) {
	for _, body := range []string{`{}`, `<html>login</html>`, `{"errors":[{"message":"denied"}],"data":{}}`, `{"data":{"messengerConversationsBySyncToken":{"elements":null}}}`, strings.Replace(conversationFixture, "urn:li:msg_message:event", "invalid", 1), strings.Replace(conversationFixture, `"groupChat":false`, `"groupChat":false,"incompleteRetriableData":true`, 1)} {
		if _, e := activityFixtureClient(t, body).GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF"); !errors.Is(e, ErrParseFailed) {
			t.Fatalf("accepted malformed response, err=%v", e)
		}
	}
}
func TestActivityMessagesCursorsAndClassification(t *testing.T) {
	for _, tc := range []struct {
		cursor         string
		complete, fail bool
	}{{`null`, true, false}, {`"older"`, false, false}, {`"again"`, false, true}, {`""`, false, true}, {`{}`, false, true}} {
		body := `{"data":{"messengerMessagesByConversation":{"metadata":{"prevCursor":` + tc.cursor + `},"elements":[{"entityUrn":"urn:li:msg_message:e","deliveredAt":1700000000000,"sender":{"hostIdentityUrn":"urn:li:fsd_profile:OTHER"},"actor":{},"body":{"text":"joined"}}]}}}`
		p, e := activityFixtureClient(t, body).GetActivityMessages(context.Background(), "urn:li:fsd_profile:SELF", "urn:li:msg_conversation:t", "again", 20)
		if (e != nil) != tc.fail {
			t.Fatalf("cursor=%s page=%+v err=%v", tc.cursor, p, e)
		}
		if e == nil && (p.Complete != tc.complete || p.Messages[0].Kind != "system") {
			t.Fatalf("page=%+v", p)
		}
	}
}
func TestActivityIdentityAndInputBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	c := offlineClient(func(r *http.Request) (*http.Response, error) { calls.Add(1); return response(r, 200, `{}`), nil })
	_, e := c.GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF")
	if !errors.Is(e, ErrSenderIdentityMismatch) || calls.Load() != 0 {
		t.Fatalf("err=%v calls=%d", e, calls.Load())
	}
	c.verifiedMember.Store("urn:li:fsd_profile:SELF")
	for _, n := range []int{0, 101} {
		if _, e := c.GetActivityMessages(context.Background(), "urn:li:fsd_profile:SELF", "urn:li:msg_conversation:t", "", n); !errors.Is(e, ErrInvalidParams) {
			t.Fatal(e)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid parameters called provider")
	}
}
func TestRequestGateCountsWarmupAndRetry(t *testing.T) {
	var reserved, dispatched atomic.Int32
	denied := errors.New("capacity denied")
	c := New(Auth{LiAt: "fixture", CSRF: "fixture"}, WithMinRequestGap(0), WithRetry(3, 0), WithHTTPClient(&http.Client{Transport: outreachTransport(func(r *http.Request) (*http.Response, error) {
		dispatched.Add(1)
		if r.URL.Path == "/feed/" {
			return response(r, 200, "<html>feed</html>"), nil
		}
		return response(r, 503, `{}`), nil
	})}), WithRequestGate(func(context.Context) error {
		if reserved.Add(1) > 2 {
			return denied
		}
		return nil
	}))
	_, e := c.GetMe(context.Background())
	if !errors.Is(e, denied) || dispatched.Load() != 2 || reserved.Load() != 3 {
		t.Fatalf("err=%v dispatched=%d reserved=%d", e, dispatched.Load(), reserved.Load())
	}
}
func TestActivityRelationshipOnlyOneRequest(t *testing.T) {
	var calls atomic.Int32
	c := offlineClient(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if !strings.Contains(r.URL.Path, "voyagerRelationshipsDashMemberRelationships") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return response(r, 200, `{"data":{"entityUrn":"urn:li:fsd_memberRelationship:OTHER","memberRelationshipUnion":{"*connection":"urn:li:fsd_connection:(SELF,OTHER)"}},"included":[{"entityUrn":"urn:li:fsd_connection:(SELF,OTHER)","*connectedMemberResolutionResult":"urn:li:fsd_profile:OTHER"}]}`), nil
	})
	c.verifiedMember.Store("urn:li:fsd_profile:SELF")
	m, e := c.GetMemberRelationship(context.Background(), "urn:li:fsd_profile:OTHER")
	if e != nil || m.ConnectionDegree != 1 || calls.Load() != 1 {
		t.Fatalf("member=%+v err=%v calls=%d", m, e, calls.Load())
	}
}

func TestActivityNormalizedEnvelope(t *testing.T) {
	body := `{"data":{"data":{"messengerConversationsBySyncToken":{"*elements":["urn:li:msg_conversation:thread"],"metadata":{"newSyncToken":"s"}}}},"included":[{"entityUrn":"urn:li:msg_conversation:thread","lastActivityAt":1700000000000,"conversationParticipants":[{"hostIdentityUrn":"urn:li:fsd_profile:SELF"},{"hostIdentityUrn":"urn:li:fsd_profile:OTHER"}],"messages":{"elements":[],"metadata":{"prevCursor":null}}}]}`
	p, e := activityFixtureClient(t, body).GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF")
	if e != nil || len(p.Conversations) != 1 || !p.Conversations[0].MessagesComplete {
		t.Fatalf("page=%+v err=%v", p, e)
	}
	missing := strings.Replace(body, "urn:li:msg_conversation:thread\"],", "urn:li:msg_conversation:missing\"],", 1)
	if _, e := activityFixtureClient(t, missing).GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF"); !errors.Is(e, ErrParseFailed) {
		t.Fatal("missing reference accepted", e)
	}
}

func TestActivityEmptyErrorsIsSuccess(t *testing.T) {
	body := strings.Replace(conversationFixture, `{"data":`, `{"errors":[],"data":`, 1)
	if _, e := activityFixtureClient(t, body).GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF"); e != nil {
		t.Fatal(e)
	}
}

func TestRequestGateRefusalPreservesHostCause(t *testing.T) {
	for _, phase := range []string{"warmup", "read", "write"} {
		t.Run(phase, func(t *testing.T) {
			cause := errors.New("private host context")
			var calls atomic.Int32
			c := New(Auth{LiAt: "fixture", CSRF: "fixture"}, WithMinRequestGap(0), WithRetry(3, 0), WithRequestGate(func(context.Context) error { return cause }), WithHTTPClient(&http.Client{Transport: outreachTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return response(r, 200, `{}`), nil })}))
			var err error
			if phase != "warmup" {
				c.warmedUp.Store(true)
			}
			if phase == "write" {
				_, _, err = c.makeWriteRequest(context.Background(), apiBase+"/test", []byte(`{}`))
			} else {
				_, err = c.makeRequest(context.Background(), apiBase+"/test")
			}
			var denied *RequestGateError
			if !errors.As(err, &denied) || !errors.Is(err, cause) || IsWriteUnknown(err) || calls.Load() != 0 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
			if strings.Contains(err.Error(), cause.Error()) {
				t.Fatal("private host error leaked")
			}
		})
	}
}

func TestActivityGeneratedAndUnrecognizedCardsAreNotReplies(t *testing.T) {
	for _, tc := range []struct {
		content, body, want string
		attachment          bool
	}{
		{`{"awayMessage":{"text":"automatic reply"}}`, "Thanks", "system", false},
		{`{"messageAdRenderContent":{}}`, "Sponsored", "system", false},
		{`{"callMessageContent":{}}`, "Missed call", "system", false},
		{`{"newProviderCard":{}}`, "Unknown card", "unknown", false},
		{`{"file":{}}`, "", "human", true},
		{`{"vectorImage":{}}`, "", "human", true},
		{`{}`, "", "unknown", false},
	} {
		var raw rawActivityMessage
		body := `{"entityUrn":"urn:li:msg_message:m","deliveredAt":1700000000000,"sender":{"hostIdentityUrn":"urn:li:fsd_profile:OTHER"},"body":{"text":"` + tc.body + `"},"renderContent":[` + tc.content + `]}`
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatal(err)
		}
		msg, err := raw.parse("urn:li:msg_conversation:t")
		if err != nil || msg.Kind != tc.want || msg.HasAttachment != tc.attachment {
			t.Fatalf("card=%s message=%+v err=%v", tc.content, msg, err)
		}
	}
}

func TestActivityNormalizedNullReference(t *testing.T) {
	body := strings.Replace(conversationFixture, `"renderContent":[]`, `"renderContent":[],"*actor":null`, 1)
	page, err := activityFixtureClient(t, body).GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF")
	if err != nil || page.Conversations[0].Messages[0].Kind != "human" {
		t.Fatalf("nullable reference rejected: %v", err)
	}
}
