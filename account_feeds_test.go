package linkedin

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func feedFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestRecentConnectionsStrictPage(t *testing.T) {
	body := feedFixture(t, "recent_connections")
	var calls int
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		calls++
		q := req.URL.Query()
		if req.Method != http.MethodGet || req.URL.Path != "/voyager/api/relationships/dash/connections" || q.Get("sortType") != "RECENTLY_ADDED" || q.Get("start") != "0" || q.Get("count") != "2" || q.Get("decorationId") != "com.linkedin.voyager.dash.deco.web.mynetwork.ConnectionListWithProfile-16" {
			t.Fatal("unexpected connection request")
		}
		return response(req, 200, body), nil
	})
	c.verifiedMember.Store("urn:li:fsd_profile:SELF")
	page, err := c.ListRecentConnections(context.Background(), "urn:li:fsd_profile:SELF", RecentConnectionsParams{Count: 2})
	if err != nil || calls != 1 || len(page.Connections) != 2 || page.Complete || !page.TotalKnown || page.Total != 3 || page.NextStart != 2 || page.Connections[0].RecipientURN != "urn:li:fsd_profile:ALPHA" || page.Connections[0].ConnectedAt != 1760000200000 {
		t.Fatalf("page=%+v calls=%d err=%v", page, calls, err)
	}
}

func TestRecentConnectionsCannotManufacturePartialCoverage(t *testing.T) {
	fixture := feedFixture(t, "recent_connections")
	for name, body := range map[string]string{
		"missing reference":       strings.Replace(fixture, `"entityUrn": "urn:li:fsd_connection:ALPHA"`, `"entityUrn": "urn:li:fsd_connection:OTHER"`, 1),
		"wrong associated member": strings.Replace(fixture, `"*connectedMemberResolutionResult": "urn:li:fsd_profile:ALPHA"`, `"*connectedMemberResolutionResult": "urn:li:fsd_profile:OTHER"`, 1),
		"wrong tuple sender":      strings.ReplaceAll(fixture, "(SELF,BETA)", "(OTHER,BETA)"),
		"wrong returned offset":   strings.Replace(fixture, `"start": 0`, `"start": 5`, 1),
		"fractional timestamp":    strings.Replace(fixture, "1760000200000", "1760000200000.5", 1),
		"nested error":            strings.Replace(fixture, `"createdAt": 1760000200000`, `"createdAt": 1760000200000, "errors": [{"message":"unavailable"}]`, 1),
		"duplicate":               strings.Replace(fixture, `"urn:li:fsd_connection:(SELF,BETA)"`, `"urn:li:fsd_connection:ALPHA"`, 1),
		"missing collection":      `{"data":{"paging":{"total":0}},"included":[]}`,
		"missing paging":          `{"data":{"*elements":[]},"included":[]}`,
		"missing offset":          `{"data":{"*elements":[],"paging":{"count":2,"total":0}},"included":[]}`,
		"empty before total":      `{"data":{"*elements":[],"paging":{"start":0,"count":2,"total":3}},"included":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			page, err := parseRecentConnections([]byte(body), "urn:li:fsd_profile:SELF", RecentConnectionsParams{Count: 2})
			if err == nil || page != nil {
				t.Fatalf("partial page=%+v err=%v", page, err)
			}
		})
	}
}

func TestRecentConnectionsMovingHeadKeepsOverlapAndUnknownDate(t *testing.T) {
	// The same connection may move to the next offset after a head insertion.
	// SDK pagination must retain it; host identity overlap owns deduplication.
	body := `{"data":{"*elements":["urn:li:fsd_connection:ALPHA"],"paging":{"start":2,"count":2,"total":3}},"included":[{"entityUrn":"urn:li:fsd_connection:ALPHA","*connectedMember":"urn:li:fsd_profile:ALPHA"}]}`
	page, err := parseRecentConnections([]byte(body), "urn:li:fsd_profile:SELF", RecentConnectionsParams{Start: 2, Count: 2})
	if err != nil || !page.Complete || page.NextStart != 3 || len(page.Connections) != 1 || page.Connections[0].ConnectedAt != 0 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestRecentConnectionsProviderWindowDoesNotImplyExhaustion(t *testing.T) {
	body := `{"data":{"*elements":["urn:li:fsd_connection:ALPHA"],"paging":{"start":0,"count":1}},"included":[{"entityUrn":"urn:li:fsd_connection:ALPHA","*connectedMember":"urn:li:fsd_profile:ALPHA"}]}`
	page, err := parseRecentConnections([]byte(body), "urn:li:fsd_profile:SELF", RecentConnectionsParams{Count: 10})
	if err != nil || page.Complete || page.TotalKnown || page.Count != 1 || page.NextStart != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestActivityCategoryPageIsReadOnlyMetadata(t *testing.T) {
	body := feedFixture(t, "activity_category")
	var calls int
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		calls++
		q := req.URL.Query()
		want := "(query:(predicateUnions:List((conversationCategoryPredicate:(category:INBOX)))),count:20,mailboxUrn:urn:li:fsd_profile:SELF,nextCursor:older:page=(1)&x)"
		if req.Method != http.MethodGet || req.URL.Path != "/voyager/api/voyagerMessagingGraphQL/graphql" || q.Get("queryId") != "reviewedCategory.hash" || q.Get("variables") != want {
			t.Fatalf("unexpected category request: %q", q.Get("variables"))
		}
		return response(req, 200, body), nil
	})
	c.activityQueries.ConversationsByCategory = "reviewedCategory.hash"
	c.verifiedMember.Store("urn:li:fsd_profile:SELF")
	page, err := c.ListActivityConversations(context.Background(), "urn:li:fsd_profile:SELF", ActivityConversationListParams{NextCursor: "older:page=(1)&x"})
	if err != nil || calls != 1 || page.Complete || page.NextCursor != "next:page=(2)&x" || len(page.Conversations) != 1 {
		t.Fatalf("page=%+v calls=%d err=%v", page, calls, err)
	}
	conv := page.Conversations[0]
	if len(conv.Participants) != 2 || conv.Participants[0] != "urn:li:fsd_profile:SELF" || conv.LastActivityAt != 1760000300000 || len(conv.Messages) != 0 || conv.MessagesComplete {
		t.Fatalf("metadata=%+v", conv)
	}
}

func TestActivityCategoryContinuationAndIdentityFailures(t *testing.T) {
	body := feedFixture(t, "activity_category")
	for _, cursor := range []string{`null`, `""`} {
		terminal := strings.Replace(body, `"next:page=(2)&x"`, cursor, 1)
		page, err := activityFixtureClient(t, terminal).ListActivityConversations(context.Background(), "urn:li:fsd_profile:SELF", ActivityConversationListParams{})
		if err != nil || !page.Complete || page.NextCursor != "" {
			t.Fatalf("terminal=%+v err=%v", page, err)
		}
	}
	for name, changed := range map[string]string{
		"unresolved":       strings.Replace(body, `"entityUrn": "urn:li:msg_messagingParticipant:urn:li:fsd_profile:ALPHA"`, `"entityUrn": "urn:li:msg_messagingParticipant:urn:li:fsd_profile:OTHER"`, 1),
		"wrong mailbox":    strings.ReplaceAll(body, "fsd_profile:SELF", "fsd_profile:OTHER"),
		"bad cursor":       strings.Replace(body, `"next:page=(2)&x"`, `{}`, 1),
		"loop":             body,
		"missing metadata": strings.Replace(body, `"metadata": {"nextCursor": "next:page=(2)&x"}`, `"unsupported": {}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			p := ActivityConversationListParams{}
			if name == "loop" {
				p.NextCursor = "next:page=(2)&x"
			}
			page, err := activityFixtureClient(t, changed).ListActivityConversations(context.Background(), "urn:li:fsd_profile:SELF", p)
			if err == nil || page != nil {
				t.Fatalf("page=%+v err=%v", page, err)
			}
		})
	}
}

func TestActivityCategoryIgnoresUnrequestedMessageDecorations(t *testing.T) {
	body := feedFixture(t, "activity_category")
	body = strings.Replace(body, `"lastMessage": {"entityUrn": "urn:li:msg_message:message", "body": {"text": "Synthetic fixture text"}}`, `"*lastMessage": "urn:li:msg_message:unresolved"`, 1)
	page, err := activityFixtureClient(t, body).ListActivityConversations(context.Background(), "urn:li:fsd_profile:SELF", ActivityConversationListParams{})
	if err != nil || len(page.Conversations) != 1 || len(page.Conversations[0].Messages) != 0 {
		t.Fatalf("metadata=%+v err=%v", page, err)
	}
}

func TestActivityCategoryCanonicalParticipantAliasesAgree(t *testing.T) {
	body := feedFixture(t, "activity_category")
	body = strings.Replace(body, `"entityUrn": "urn:li:msg_messagingParticipant:urn:li:fsd_profile:ALPHA", "participantType"`, `"entityUrn": "urn:li:msg_messagingParticipant:urn:li:fsd_profile:ALPHA", "hostIdentityUrn": "urn:li:fs_miniProfile:ALPHA", "participantType"`, 1)
	page, err := activityFixtureClient(t, body).ListActivityConversations(context.Background(), "urn:li:fsd_profile:SELF", ActivityConversationListParams{})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].Participants[1] != "urn:li:fsd_profile:ALPHA" {
		t.Fatalf("metadata=%+v err=%v", page, err)
	}
}

func TestAccountFeedIdentityAndLimitsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	c := offlineClient(func(req *http.Request) (*http.Response, error) { calls.Add(1); return response(req, 200, `{}`), nil })
	if _, err := c.ListRecentConnections(context.Background(), "urn:li:fsd_profile:SELF", RecentConnectionsParams{}); !errors.Is(err, ErrSenderIdentityMismatch) {
		t.Fatal(err)
	}
	if _, err := c.ListActivityConversations(context.Background(), "urn:li:fsd_profile:SELF", ActivityConversationListParams{}); !errors.Is(err, ErrSenderIdentityMismatch) {
		t.Fatal(err)
	}
	c.verifiedMember.Store("urn:li:fsd_profile:SELF")
	for _, p := range []RecentConnectionsParams{{Start: -1}, {Count: 50}, {Count: -1}} {
		if _, err := c.ListRecentConnections(context.Background(), "urn:li:fsd_profile:SELF", p); !errors.Is(err, ErrInvalidParams) {
			t.Fatal(err)
		}
	}
	for _, p := range []ActivityConversationListParams{{Count: 26}, {Count: -1}, {NextCursor: strings.Repeat("x", 4097)}} {
		if _, err := c.ListActivityConversations(context.Background(), "urn:li:fsd_profile:SELF", p); !errors.Is(err, ErrInvalidParams) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unverified feed reached provider")
	}
}

// A read-only production observation of an accepted invitation thread returned
// one sender/actor mismatch alongside ordinary human messages. It supplied no
// invitation identifier or subtype, so that message must remain unknown. The
// fixture uses synthetic IDs, text and timestamps while retaining that shape.
func TestAcceptedThreadDoesNotInventInvitationProvenance(t *testing.T) {
	c := activityFixtureClient(t, feedFixture(t, "activity_ambiguous_note"))
	c.verifiedMember.Store("urn:li:fsd_profile:proofSender")
	page, err := c.GetActivityMessages(context.Background(), "urn:li:fsd_profile:proofSender", "urn:li:msg_conversation:THREAD", "", 20)
	if err != nil || !page.Complete || len(page.Messages) != 6 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	human, unknown := 0, 0
	for _, m := range page.Messages {
		switch m.Kind {
		case "human":
			human++
		case "unknown":
			unknown++
			if m.SenderURN != "urn:li:fsd_profile:proofSender" {
				t.Fatal("unexpected ambiguous message sender")
			}
		}
	}
	if human != 5 || unknown != 1 {
		t.Fatalf("human=%d unknown=%d", human, unknown)
	}
}
