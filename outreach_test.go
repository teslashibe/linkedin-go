package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type outreachTransport func(*http.Request) (*http.Response, error)

func (f outreachTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func response(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func offlineClient(handler outreachTransport) *Client {
	c := New(Auth{LiAt: "fixture", CSRF: "fixture"}, WithHTTPClient(&http.Client{Transport: handler}), WithMinRequestGap(0), WithRetry(8, time.Nanosecond))
	c.warmedUp.Store(true)
	return c
}

// Self fixtures recover the old S'more miniProfile reference contract, adding
// mismatched and duplicate included entities absent from the old fallback.
func TestOutreachAuthenticatedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		good       bool
	}{
		{"referenced", `{"data":{"*miniProfile":"urn:li:fs_miniProfile:SELF"},"included":[{"entityUrn":"urn:li:fs_miniProfile:OTHER"},{"entityUrn":"urn:li:fs_miniProfile:SELF","publicIdentifier":"sender"}]}`, true},
		{"direct", `{"miniProfile":{"entityUrn":"urn:li:fs_miniProfile:SELF"}}`, true},
		{"missing reference", `{"data":{"*miniProfile":"urn:li:fs_miniProfile:SELF"},"included":[{"entityUrn":"urn:li:fs_miniProfile:OTHER"}]}`, false},
		{"included only", `{"included":[{"entityUrn":"urn:li:fs_miniProfile:SELF"}]}`, false},
		{"duplicate", `{"data":{"*miniProfile":"urn:li:fs_miniProfile:SELF"},"included":[{"entityUrn":"urn:li:fs_miniProfile:SELF"},{"entityUrn":"urn:li:fs_miniProfile:SELF"}]}`, false},
		{"conflicting direct identity", `{"miniProfile":{"entityUrn":"urn:li:fs_miniProfile:OTHER"},"data":{"*miniProfile":"urn:li:fs_miniProfile:SELF"},"included":[{"entityUrn":"urn:li:fs_miniProfile:SELF"}]}`, false},
		{"conflicting direct envelopes", `{"miniProfile":{"entityUrn":"urn:li:fs_miniProfile:SELF"},"data":{"miniProfile":{"entityUrn":"urn:li:fs_miniProfile:OTHER"}}}`, false},
		{"error envelope", `{"code":"AUTH_REQUIRED","miniProfile":{"entityUrn":"urn:li:fs_miniProfile:SELF"}}`, false},
		{"selected entity error", `{"data":{"*miniProfile":"urn:li:fs_miniProfile:SELF"},"included":[{"entityUrn":"urn:li:fs_miniProfile:SELF","code":"RESTRICTED"}]}`, false},
		{"vanity only", `{"miniProfile":{"publicIdentifier":"sender"}}`, false},
		{"HTML", `<html>login</html>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, err := parseAuthenticatedProfile([]byte(tc.body))
			if tc.good {
				if err != nil || profile.URN != "urn:li:fsd_profile:SELF" {
					t.Fatalf("profile=%+v err=%v", profile, err)
				}
			} else if !errors.Is(err, ErrIdentityUnavailable) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestOutreachProfileIdentifiersFailClosed(t *testing.T) {
	for _, input := range []string{"https://evillinkedin.com/in/recipient", "http://linkedin.com/in/recipient", "https://www.linkedin.com:443/in/recipient", "https://user@www.linkedin.com/in/recipient", "https://www.linkedin.com/in/recipient/extra", "https://www.linkedin.com/in/recipient?other=1", "urn:li:fsd_profile:RECIPIENT"} {
		if _, err := profileSlug(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

// Relationship fixtures use the current author-owned crouton-labs/capture
// response union schema; unknown, missing and contradictory references block.
func TestOutreachRelationshipEvidence(t *testing.T) {
	const prefix = `{"data":{"entityUrn":"urn:li:fsd_memberRelationship:RECIPIENT","memberRelationshipUnion":`
	for _, tc := range []struct {
		name, body string
		degree     int
		state      string
		invalid    bool
	}{
		{"connected", prefix + `{"*connection":"urn:li:fsd_connection:CONNECTION"}},"included":[{"entityUrn":"urn:li:fsd_connection:CONNECTION","connectedMember":"urn:li:fsd_profile:RECIPIENT"}]}`, 1, "connected", false},
		{"connected tuple", prefix + `{"*connection":"urn:li:fsd_connection:(SELF,RECIPIENT)"}},"included":[{"entityUrn":"urn:li:fsd_connection:(SELF,RECIPIENT)","*connectedMemberResolutionResult":"urn:li:fsd_profile:RECIPIENT"}]}`, 1, "connected", false},
		{"tuple wrong recipient", prefix + `{"*connection":"urn:li:fsd_connection:(SELF,OTHER)"}},"included":[{"entityUrn":"urn:li:fsd_connection:(SELF,OTHER)","*connectedMemberResolutionResult":"urn:li:fsd_profile:RECIPIENT"}]}`, 0, "unknown", true},
		{"tuple wrong sender", prefix + `{"*connection":"urn:li:fsd_connection:(OTHER,RECIPIENT)"}},"included":[{"entityUrn":"urn:li:fsd_connection:(OTHER,RECIPIENT)","*connectedMemberResolutionResult":"urn:li:fsd_profile:RECIPIENT"}]}`, 0, "unknown", true},
		{"referenced connection error", prefix + `{"*connection":"urn:li:fsd_connection:CONNECTION"}},"included":[{"entityUrn":"urn:li:fsd_connection:CONNECTION","connectedMember":"urn:li:fsd_profile:RECIPIENT","code":"RESTRICTED"}]}`, 0, "unknown", true},
		{"unresolved connection", prefix + `{"*connection":"urn:li:fsd_connection:CONNECTION"}}}`, 0, "unknown", false},
		{"connection has no recipient", prefix + `{"*connection":"urn:li:fsd_connection:CONNECTION"}},"included":[{"entityUrn":"urn:li:fsd_connection:CONNECTION"}]}`, 0, "unknown", false},
		{"connection wrong recipient", prefix + `{"*connection":"urn:li:fsd_connection:CONNECTION"}},"included":[{"entityUrn":"urn:li:fsd_connection:CONNECTION","connectedMember":"urn:li:fsd_profile:OTHER"}]}`, 0, "unknown", true},
		{"available", prefix + `{"noConnection":{"memberDistance":"DISTANCE_2","invitationUnion":{"noInvitation":{}}}}}}`, 2, "available", false},
		{"available without distance", prefix + `{"noConnection":{"invitationUnion":{"noInvitation":{}}}}}}`, 0, "available", false},
		{"available unknown distance", prefix + `{"noConnection":{"memberDistance":"DISTANCE_OUT_OF_NETWORK","invitationUnion":{"noInvitation":{}}}}}}`, 0, "available", false},
		{"noConnection contradicts first degree", prefix + `{"noConnection":{"memberDistance":"DISTANCE_1","invitationUnion":{"noInvitation":{}}}}}}`, 0, "unknown", true},
		{"noInvitation error", prefix + `{"noConnection":{"invitationUnion":{"noInvitation":{"code":"RESTRICTED"}}}}}}`, 0, "unknown", false},
		{"invitation union error", prefix + `{"noConnection":{"invitationUnion":{"code":"RESTRICTED","noInvitation":{}}}}}}`, 0, "unknown", true},
		{"noInvitation and invitation reference conflict", prefix + `{"noConnection":{"invitationUnion":{"noInvitation":{},"*invitation":"urn:li:fsd_invitation:INVITE"}}}}}`, 0, "unknown", true},
		{"unbound noInvitation", `{"data":{"memberRelationshipUnion":{"noConnection":{"invitationUnion":{"noInvitation":{}}}}}}`, 0, "unknown", false},
		{"wrong noInvitation identity", `{"data":{"entityUrn":"urn:li:fsd_memberRelationship:OTHER","memberRelationshipUnion":{"noConnection":{"invitationUnion":{"noInvitation":{}}}}}}`, 0, "unknown", true},
		{"null noInvitation", prefix + `{"noConnection":{"memberDistance":"DISTANCE_2","invitationUnion":{"noInvitation":null}}}}}`, 2, "unknown", false},
		{"false noInvitation", prefix + `{"noConnection":{"memberDistance":"DISTANCE_2","invitationUnion":{"noInvitation":false}}}}}`, 2, "unknown", false},
		{"errored noConnection", prefix + `{"noConnection":{"code":"RESTRICTED","memberDistance":"DISTANCE_2","invitationUnion":{"noInvitation":{}}}}}}`, 0, "unknown", true},
		{"pending", prefix + `{"noConnection":{"memberDistance":"DISTANCE_2","invitationUnion":{"*invitation":"urn:li:fsd_invitation:INVITE"}}}},"included":[{"entityUrn":"urn:li:fsd_invitation:INVITE","invitationState":"PENDING","invitationType":"SENT"}]}`, 2, "pending", false},
		{"unknown empty union", prefix + `{}}}`, 0, "unknown", false},
		{"no invitation unknown", prefix + `{"noConnection":{"memberDistance":"DISTANCE_2"}}}}`, 2, "unknown", false},
		{"contradictory", prefix + `{"*connection":"urn:li:fsd_connection:CONNECTION","noConnection":{}}}}`, 0, "unknown", true},
		{"wrong identity", `{"data":{"entityUrn":"urn:li:fsd_memberRelationship:OTHER","memberRelationshipUnion":{}}}`, 0, "unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			member := &Member{URN: "urn:li:fsd_profile:RECIPIENT", InvitationState: "unknown"}
			err := parseRelationship([]byte(tc.body), "urn:li:fsd_memberRelationship:RECIPIENT", member, "urn:li:fsd_profile:SELF")
			if tc.invalid {
				if err == nil {
					t.Fatal("accepted invalid evidence")
				}
				return
			}
			if err != nil || member.ConnectionDegree != tc.degree || member.InvitationState != tc.state {
				t.Fatalf("member=%+v err=%v", member, err)
			}
		})
	}
}

// An authenticated read of the approved controlled target returned the exact
// relationship identity and both object unions, but no recognized distance.
// Synthetic IDs and text retain that structural contract without private data.
func TestOutreachNoInvitationWithoutDistanceIsNotDMEvidence(t *testing.T) {
	for _, action := range []string{"connect", "dm"} {
		t.Run(action, func(t *testing.T) {
			var writes atomic.Int32
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPost {
					writes.Add(1)
					if action != "connect" {
						t.Fatal("unconnected recipient received a DM dispatch")
					}
					var payload struct {
						CustomMessage string `json:"customMessage"`
						Invitee       struct {
							Union struct {
								Profile string `json:"memberProfile"`
							} `json:"inviteeUnion"`
						} `json:"invitee"`
					}
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if payload.CustomMessage != " exact note 😀\n " || payload.Invitee.Union.Profile != "urn:li:fsd_profile:RECIPIENT" {
						t.Fatal("invitation destination or approved note changed")
					}
					return response(req, 201, `{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:NEW"}}}`), nil
				}
				if strings.Contains(req.URL.Path, "MemberRelationships/") {
					return response(req, 200, `{"data":{"entityUrn":"urn:li:fsd_memberRelationship:RECIPIENT","memberRelationshipUnion":{"noConnection":{"invitationUnion":{"noInvitation":{}}}}}}`), nil
				}
				return fixtureRead(req, 2), nil
			})
			if action == "dm" {
				_, err := c.SendMessageWithReceipt(context.Background(), DirectMessageParams{RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Body: "approved"})
				if !errors.Is(err, ErrRecipientNotMessageable) || writes.Load() != 0 {
					t.Fatalf("writes=%d err=%v", writes.Load(), err)
				}
				return
			}
			receipt, err := c.SendConnectionInvitation(context.Background(), InvitationParams{RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Note: " exact note 😀\n "})
			if err != nil || receipt.InvitationURN != "urn:li:fsd_invitation:NEW" || writes.Load() != 1 {
				t.Fatalf("receipt=%+v writes=%d err=%v", receipt, writes.Load(), err)
			}
		})
	}
}

func TestOutreachCommentIdentities(t *testing.T) {
	want := "urn:li:comment:(urn:li:activity:123,456)"
	for _, raw := range []string{"urn:li:comment:(activity:123,456)", want, "urn:li:fsd_comment:(456,urn:li:activity:123)", "urn:li:fsd_normComment:(456,urn:li:activity:123)"} {
		if got, err := CanonicalCommentURN(raw); err != nil || got != want {
			t.Fatalf("raw=%q got=%q err=%v", raw, got, err)
		}
	}
	for _, raw := range []string{"urn:li:person:123", "urn:li:comment:(activity:123,)", "urn:li:comment:(activity:123,456)/evil", "urn:li:comment:(activity:123,456,789)", "urn:li:fsd_comment:(urn:li:activity:123,456)"} {
		if _, err := CanonicalCommentURN(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestOutreachSingleDispatchDespiteRetryConfiguration(t *testing.T) {
	for _, status := range []int{307, 308, 429, 500, 502} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.GetBody != nil || req.Header.Get("Idempotency-Key") != "" {
					t.Fatal("replay enabled")
				}
				r := response(req, status, `{}`)
				r.Header.Set("Location", "https://www.linkedin.com/replayed")
				r.Header.Set("Retry-After", "60")
				return r, nil
			})
			_, _, err := c.makeWriteRequest(context.Background(), apiBase+"/fixture", []byte(`{}`))
			if calls.Load() != 1 {
				t.Fatalf("provider writes=%d", calls.Load())
			}
			if status == 429 {
				var typed *WriteError
				if !errors.As(err, &typed) || typed.RetryAfter != time.Minute || !errors.Is(err, ErrRateLimited) {
					t.Fatalf("err=%v", err)
				}
			} else if !errors.Is(err, ErrWriteUnknown) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

type failedRead struct{}

func (failedRead) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failedRead) Close() error             { return nil }

func TestOutreachLostAcknowledgmentNeverRepeats(t *testing.T) {
	for _, transportFailure := range []bool{true, false} {
		var calls atomic.Int32
		c := offlineClient(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			if transportFailure {
				return nil, io.ErrUnexpectedEOF
			}
			r := response(req, 201, `{}`)
			r.Body = failedRead{}
			return r, nil
		})
		_, _, err := c.makeWriteRequest(context.Background(), apiBase+"/fixture", []byte(`{}`))
		if !errors.Is(err, ErrWriteUnknown) || calls.Load() != 1 {
			t.Fatalf("calls=%d err=%v", calls.Load(), err)
		}
	}
}

func TestOutreachCreationReferenceIsRequired(t *testing.T) {
	for _, body := range []string{
		`{"data":{"*value":"urn:li:msg_message:MISSING"},"included":[{"entityUrn":"urn:li:msg_message:OTHER"}]}`,
		`{"included":[{"entityUrn":"urn:li:msg_message:OLD"}]}`,
		`{"code":"CANT_RESEND_YET","value":{"invitationUrn":"urn:li:fsd_invitation:OLD"}}`,
		`{"errors":[{"message":"failed"}],"data":{"entityUrn":"urn:li:fsd_comment:(456,urn:li:activity:123)"}}`,
		`{"value":{"status":504,"invitationUrn":"urn:li:fsd_invitation:OLD"}}`,
		`{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:NEW"}},"value":{"invitationUrn":"urn:li:fsd_invitation:OLD"}}`,
		`not JSON`,
	} {
		if _, err := createdValue([]byte(body)); !errors.Is(err, ErrWriteUnknown) {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
}

func TestOutreachPostBindingDoesNotPairSiblings(t *testing.T) {
	for _, body := range []string{
		`{"data":{"activityUrn":"urn:li:activity:123"},"metadata":{"shareUrn":"urn:li:ugcPost:999"}}`,
		`{"elements":[{"entityUrn":"urn:li:activity:123"},{"entityUrn":"urn:li:activity:999","metadata":{"shareUrn":"urn:li:ugcPost:999"}}]}`,
		`{"elements":[{"entityUrn":"urn:li:activity:123","activityUrn":"urn:li:activity:999","metadata":{"shareUrn":"urn:li:ugcPost:111"}}]}`,
		`{"elements":[{"entityUrn":"urn:li:activity:123","metadata":{"shareUrn":"urn:li:ugcPost:111"},"threadUrn":"urn:li:ugcPost:999"}]}`,
	} {
		if target, err := resolvedCommentTarget([]byte(body), "urn:li:activity:123"); err == nil {
			t.Fatalf("accepted cross-bound target %+v", target)
		}
	}
	for _, input := range []string{"https://evillinkedin.com/feed/update/urn:li:activity:123", "http://www.linkedin.com/feed/update/urn:li:activity:123", "https://user@www.linkedin.com/feed/update/urn:li:activity:123", "https://www.linkedin.com:443/feed/update/urn:li:activity:123", "https://www.linkedin.com/unrelated/urn:li:activity:123", "urn:li:activity:123/"} {
		if _, err := outreachPostReference(input); err == nil {
			t.Fatalf("accepted invalid post URL %q", input)
		}
	}
}

func TestOutreachCommentAcknowledgmentAssociation(t *testing.T) {
	target := &CommentTarget{PostURN: "urn:li:ugcPost:111", ActivityURN: "urn:li:activity:123", ParentCommentURN: "urn:li:comment:(urn:li:activity:123,456)"}
	for _, body := range []string{
		`{"data":{"entityUrn":"urn:li:fsd_comment:(456,urn:li:activity:123)"}}`,
		`{"included":[{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)"}]}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:999)"}}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","commentary":{"text":"changed"}}}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","authorUrn":"urn:li:fsd_profile:OTHER"}}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","parentCommentUrn":""}}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","commentUrn":"urn:li:fsd_comment:(789,urn:li:activity:999)"}}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","author":{"profileUrn":"urn:li:fsd_profile:OTHER"}}}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","*commenter":"urn:li:fsd_profile:OTHER"}}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","commenter":{"displayName":"unresolved"}}}`,
		`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","commenter":{"actorUnion":{"companyUrn":"urn:li:fsd_company:123"}}}}`,
	} {
		if _, err := commentReceipt([]byte(body), "", "", target, " exact ", "urn:li:fsd_profile:SELF"); !errors.Is(err, ErrWriteUnknown) {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
	if got, err := commentReceipt([]byte(`{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)","commentary":{"text":" exact "}}}`), "", "", target, " exact ", "urn:li:fsd_profile:SELF"); err != nil || got != "urn:li:comment:(urn:li:activity:123,789)" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestOutreachInvitationReceiptConflictsAreUnknown(t *testing.T) {
	for _, body := range []string{
		`{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:NEW","*invitation":"urn:li:fsd_invitation:OLD"}}}`,
		`{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:NEW","memberProfile":"urn:li:fsd_profile:RECIPIENT","recipientUrn":"urn:li:fsd_profile:OTHER"}}}`,
		`{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:NEW","invitee":{"inviteeUnion":{"memberProfile":null}}}}}`,
		`{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:NEW","customMessage":""}}}`,
	} {
		var writes atomic.Int32
		c := offlineClient(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPost {
				writes.Add(1)
				return response(req, 201, body), nil
			}
			return fixtureRead(req, 2), nil
		})
		_, err := c.SendConnectionInvitation(context.Background(), InvitationParams{RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Note: "approved"})
		if !IsWriteUnknown(err) || writes.Load() != 1 {
			t.Fatalf("writes=%d err=%v", writes.Load(), err)
		}
	}
}

func TestOutreachConservativeUTF16Limits(t *testing.T) {
	if UTF16Length("a😀") != 3 {
		t.Fatal("UTF16 accounting")
	}
	if err := validateOutreachText(strings.Repeat("😀", 500), MessageLimit); err != nil {
		t.Fatal(err)
	}
	if err := validateOutreachText(strings.Repeat("😀", 501), MessageLimit); err == nil {
		t.Fatal("overlimit accepted")
	}
	if err := validateOutreachText(" \n\t ", MessageLimit); err == nil {
		t.Fatal("empty accepted")
	}
}

func fixtureRead(req *http.Request, degree int) *http.Response {
	if strings.HasSuffix(req.URL.Path, "/me") {
		return response(req, 200, `{"miniProfile":{"entityUrn":"urn:li:fs_miniProfile:SELF","publicIdentifier":"sender"}}`)
	}
	if strings.HasSuffix(req.URL.Path, "/graphql") {
		return response(req, 200, `{"included":[{"$type":"com.linkedin.voyager.dash.identity.profile.Profile","entityUrn":"urn:li:fsd_profile:RECIPIENT","publicIdentifier":"recipient"}]}`)
	}
	if strings.Contains(req.URL.Path, "MemberRelationships/") {
		if degree == 1 {
			return response(req, 200, `{"data":{"memberRelationshipUnion":{"*connection":"urn:li:fsd_connection:CONNECTION"}},"included":[{"entityUrn":"urn:li:fsd_connection:CONNECTION","connectedMember":"urn:li:fsd_profile:RECIPIENT"}]}`)
		}
		return response(req, 200, `{"data":{"entityUrn":"urn:li:fsd_memberRelationship:RECIPIENT","memberRelationshipUnion":{"noConnection":{"memberDistance":"DISTANCE_2","invitationUnion":{"noInvitation":{}}}}}}`)
	}
	if req.URL.Path == "/voyager/api/feed/comments" && req.URL.Query().Get("q") == "singleComment" {
		if req.URL.Query().Get("commentUrn") != "urn:li:comment:(activity:123,456)" {
			return response(req, 200, `{"data":{"*elements":[]},"included":[]}`)
		}
		return response(req, 200, exactParentFixture())
	}
	if strings.Contains(req.URL.Path, "/feed/") {
		return response(req, 200, `{"elements":[{"entityUrn":"urn:li:activity:123","activityUrn":"urn:li:activity:123","metadata":{"shareUrn":"urn:li:ugcPost:111"}}]}`)
	}
	return response(req, 404, `{}`)
}

// Request/receipt shapes are pinned to primary author sources in docs/outreach.md.
// These stubs exercise protocol handling; they do not establish live delivery.
func TestOutreachExactTextAndSingleWrite(t *testing.T) {
	for _, action := range []string{"dm", "reply", "reply_top_level", "connect"} {
		t.Run(action, func(t *testing.T) {
			text := "  exact 😀\n "
			var writes atomic.Int32
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					degree := 2
					if action == "dm" {
						degree = 1
					}
					return fixtureRead(req, degree), nil
				}
				writes.Add(1)
				var payload map[string]any
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				switch action {
				case "dm":
					if req.URL.Path != "/voyager/api/voyagerMessagingDashMessengerMessages" || payload["mailboxUrn"] != "urn:li:fsd_profile:SELF" {
						t.Fatalf("wrong DM: %s %+v", req.URL.Path, payload)
					}
					message := payload["message"].(map[string]any)
					if message["body"].(map[string]any)["text"] != text {
						t.Fatal("changed text")
					}
					recipients := payload["hostRecipientUrns"].([]any)
					if len(recipients) != 1 || recipients[0] != "urn:li:fsd_profile:RECIPIENT" {
						t.Fatal("wrong recipients")
					}
					return response(req, 201, `{"data":{"*value":"urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)"},"included":[{"entityUrn":"urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)","backendUrn":"urn:li:messagingMessage:2-message==","conversationUrn":"urn:li:msg_conversation:(urn:li:fsd_profile:SELF,2-conversation==)","backendConversationUrn":"urn:li:messagingThread:2-conversation=="}]}`), nil
				case "reply", "reply_top_level":
					thread := "urn:li:activity:123"
					if action == "reply" {
						thread = "urn:li:comment:(activity:123,456)"
					}
					if payload["threadUrn"] != thread || payload["commentary"].(map[string]any)["text"] != text || req.URL.Query().Get("decorationId") != "com.linkedin.voyager.dash.deco.social.NormComment-43" {
						t.Fatalf("wrong nested payload=%+v", payload)
					}
					if _, exists := payload["parentCommentUrn"]; exists {
						t.Fatal("silent top-level parent field")
					}
					return response(req, 201, `{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:123)"}}`), nil
				default:
					if payload["customMessage"] != text || req.URL.Query().Get("action") != "verifyQuotaAndCreateV2" {
						t.Fatal("changed note or wrong endpoint")
					}
					return response(req, 201, `{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:INVITATION"}}}`), nil
				}
			})
			var err error
			switch action {
			case "dm":
				_, err = c.SendMessageWithReceipt(context.Background(), DirectMessageParams{RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Body: text})
			case "reply", "reply_top_level":
				parent := ""
				if action == "reply" {
					parent = "urn:li:comment:(activity:123,456)"
				}
				_, err = c.CreateComment(context.Background(), CreateCommentParams{PostURN: "urn:li:activity:123", ParentCommentURN: parent, ExpectedSenderURN: "urn:li:fsd_profile:SELF", Text: text})
			default:
				_, err = c.SendConnectionInvitation(context.Background(), InvitationParams{RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Note: text})
			}
			if err != nil || writes.Load() != 1 {
				t.Fatalf("writes=%d err=%v", writes.Load(), err)
			}
		})
	}
}

func TestOutreachMessagingReceiptAliases(t *testing.T) {
	for _, tc := range []struct {
		value map[string]any
		good  bool
	}{
		{map[string]any{"entityUrn": "urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)", "backendUrn": "urn:li:messagingMessage:2-message=="}, true},
		{map[string]any{"entityUrn": "urn:li:msg_message:(urn:li:fsd_profile:OTHER,2-message==)", "backendUrn": "urn:li:messagingMessage:2-message=="}, false},
		{map[string]any{"entityUrn": "urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)", "backendUrn": "urn:li:messagingMessage:DIFFERENT"}, false},
		{map[string]any{"entityUrn": "urn:li:msg_message:()"}, false},
		{map[string]any{"entityUrn": "urn:li:msg_message:(urn:li:fsd_profile:SELF,)"}, false},
	} {
		_, good := selectMessagingReceipt(tc.value, []string{"backendUrn", "entityUrn"}, "urn:li:fsd_profile:SELF", "urn:li:msg_message:", "urn:li:messagingMessage:")
		if good != tc.good {
			t.Fatalf("value=%+v good=%v", tc.value, good)
		}
	}
}

func TestOutreachMissingOrConflictingAcknowledgmentIsUnknown(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"data":{"entityUrn":"urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)","conversationUrn":"urn:li:msg_conversation:(urn:li:fsd_profile:SELF,2-conversation==)","body":{"text":""}}}`,
		`{"data":{"entityUrn":"urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)","conversationUrn":"urn:li:msg_conversation:(urn:li:fsd_profile:SELF,2-conversation==)","senderUrn":"urn:li:fsd_profile:OTHER"}}`,
		`{"data":{"entityUrn":"urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)","conversationUrn":"urn:li:msg_conversation:(urn:li:fsd_profile:OTHER,2-conversation==)"}}`,
		`{"data":{"entityUrn":"urn:li:msg_message:(urn:li:fsd_profile:SELF,2-message==)","*conversation":"urn:li:msg_conversation:(urn:li:fsd_profile:SELF,2-conversation==)","*sender":"urn:li:msg_messagingParticipant:urn:li:fsd_profile:OTHER"}}`,
	} {
		var writes atomic.Int32
		c := offlineClient(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPost {
				writes.Add(1)
				return response(req, 201, body), nil
			}
			return fixtureRead(req, 1), nil
		})
		_, err := c.SendMessageWithReceipt(context.Background(), DirectMessageParams{RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Body: "approved"})
		if !IsWriteUnknown(err) || writes.Load() != 1 {
			t.Fatalf("writes=%d err=%v", writes.Load(), err)
		}
	}
}

func TestOutreachUnverifiedInvitationAndParentNeverDispatch(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  error
	}{
		{"connected", ErrAlreadyConnected},
		{"pending", ErrInvitationPending},
		{"unknown", ErrEligibilityUnknown},
	} {
		var writes atomic.Int32
		c := offlineClient(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPost {
				writes.Add(1)
			}
			if strings.Contains(req.URL.Path, "MemberRelationships/") {
				if tc.state == "connected" {
					return fixtureRead(req, 1), nil
				}
				if tc.state == "pending" {
					return response(req, 200, `{"data":{"memberRelationshipUnion":{"noConnection":{"memberDistance":"DISTANCE_2","invitationUnion":{"*invitation":"urn:li:fsd_invitation:INVITATION"}}}},"included":[{"entityUrn":"urn:li:fsd_invitation:INVITATION","invitationState":"PENDING"}]}`), nil
				}
				return response(req, 200, `{"data":{"memberRelationshipUnion":{}}}`), nil
			}
			return fixtureRead(req, 2), nil
		})
		_, err := c.SendConnectionInvitation(context.Background(), InvitationParams{RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Note: "approved"})
		if !errors.Is(err, tc.want) || writes.Load() != 0 {
			t.Fatalf("writes=%d err=%v", writes.Load(), err)
		}
	}
	var writes atomic.Int32
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost {
			writes.Add(1)
		}
		return fixtureRead(req, 2), nil
	})
	_, err := c.CreateComment(context.Background(), CreateCommentParams{PostURN: "urn:li:activity:123", ParentCommentURN: "urn:li:comment:(activity:999,456)", ExpectedSenderURN: "urn:li:fsd_profile:SELF", Text: "approved"})
	if !errors.Is(err, ErrInvalidParams) || writes.Load() != 0 {
		t.Fatalf("writes=%d err=%v", writes.Load(), err)
	}
}

func TestOutreachCookieSnapshotAndRotatedCSRF(t *testing.T) {
	var requests atomic.Int32
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		if requests.Add(1) > 1 && req.Header.Get("Csrf-Token") != "rotated" {
			t.Fatal("stale CSRF")
		}
		r := fixtureRead(req, 2)
		if requests.Load() == 1 {
			r.Header.Add("Set-Cookie", `JSESSIONID="rotated"; Path=/; Secure`)
		}
		return r, nil
	})
	if _, err := c.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.AuthSnapshot()["JSESSIONID"] != "rotated" {
		t.Fatal("rotated cookie absent from snapshot")
	}
	if c.VerifiedMemberURN() != "urn:li:fsd_profile:SELF" {
		t.Fatal("successful self proof absent")
	}
	if _, err := c.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOutreachReadFallbackAndFingerprintFailClosed(t *testing.T) {
	c := offlineClient(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "MemberRelationships/") {
			return response(req, 404, `{}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/networkinfo") {
			return response(req, 200, `{"code":"AUTH_REQUIRED","data":{"distance":{"value":"DISTANCE_1"}}}`), nil
		}
		return fixtureRead(req, 1), nil
	})
	if member, err := c.ResolveMember(context.Background(), "recipient"); err == nil {
		t.Fatalf("accepted errored fallback %+v", member)
	}
	if !errors.Is(writeRejection([]byte(`{"data":{"code":"RECIPIENT_NOT_FIRST_DEGREE_CONNECTION"}}`)), ErrRecipientNotMessageable) {
		t.Fatal("nested recipient rejection not classified")
	}
	const ua = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"
	profile, err := BrowserProfileFromUserAgent(ua)
	if err != nil || profile.UserAgent != ua || profile.SecChUA != `"Chromium";v="155"` || profile.SecChUAPlatform != `"Linux"` || profile.TimezoneName != "" {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	c.browser = profile
	headers := map[string]string{}
	c.applyVoyagerHeaders(headers, apiBase+"/me", false)
	if _, exists := headers["x-li-track"]; exists {
		t.Fatal("unobserved timezone/display emitted")
	}
	if _, err := BrowserProfileFromUserAgent("unknown"); err == nil {
		t.Fatal("unknown browser accepted")
	}
}

func TestOutreachPreflightNeverDispatchesWrongSenderOrRecipient(t *testing.T) {
	for _, tc := range []struct {
		sender, recipient string
		degree            int
		want              error
	}{
		{"urn:li:fsd_profile:OTHER", "urn:li:fsd_profile:RECIPIENT", 1, ErrSenderIdentityMismatch},
		{"urn:li:fsd_profile:SELF", "urn:li:fsd_profile:OTHER", 1, ErrRecipientIdentityMismatch},
		{"urn:li:fsd_profile:SELF", "urn:li:fsd_profile:RECIPIENT", 2, ErrRecipientNotMessageable},
	} {
		var writes atomic.Int32
		c := offlineClient(func(req *http.Request) (*http.Response, error) {
			if req.Method == http.MethodPost {
				writes.Add(1)
			}
			return fixtureRead(req, tc.degree), nil
		})
		_, err := c.SendMessageWithReceipt(context.Background(), DirectMessageParams{RecipientURN: tc.recipient, RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: tc.sender, Body: "approved"})
		if !errors.Is(err, tc.want) || writes.Load() != 0 {
			t.Fatalf("writes=%d err=%v want=%v", writes.Load(), err, tc.want)
		}
	}
}
