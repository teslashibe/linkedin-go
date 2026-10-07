package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
)

const (
	repairCommentText = "  exact receipt fixture 😀\n "
	repairActivityID  = "urn:li:activity:123"
	repairPostID      = "urn:li:ugcPost:111"
	repairSenderID    = "urn:li:fsd_profile:SELF"
	repairParentID    = "urn:li:comment:(urn:li:activity:123,456)"
	repairCommentID   = "urn:li:comment:(urn:li:activity:123,789)"
	repairCommentPost = "urn:li:comment:(urn:li:ugcPost:111,789)"
	repairModernID    = "urn:li:fsd_comment:(789,urn:li:activity:123)"
)

// These synthetic acknowledgements exercise the public SDK operation through
// an intercepting transport. They neither use provider credentials nor prove
// live delivery. The socialActions Location shape comes from the tracked old
// S'more TestLinkedInCreateCommentRecoversCreatedURN fixture.
func repairCommentAttempt(t *testing.T, body, restID, location, parent string) (*PostComment, error, int32) {
	t.Helper()
	var writes atomic.Int32
	client := offlineClient(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			return fixtureRead(req, 2), nil
		}
		writes.Add(1)
		if req.URL.Path != "/voyager/api/voyagerSocialDashNormComments" || req.URL.Query().Get("decorationId") != "com.linkedin.voyager.dash.deco.social.NormComment-43" || req.GetBody != nil {
			t.Fatal("unexpected or replayable comment request")
		}
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		thread := repairActivityID
		if parent != "" {
			thread = "urn:li:comment:(activity:123,456)"
		}
		if payload["threadUrn"] != thread || valueText(payload["commentary"]) != repairCommentText {
			t.Fatal("approved comment text or ancestry changed")
		}
		if _, supplied := payload["parentCommentUrn"]; supplied {
			t.Fatal("parent was substituted with a top-level request field")
		}
		resp := response(req, http.StatusCreated, body)
		if restID != "" {
			resp.Header.Set("X-RestLi-Id", restID)
		}
		if location != "" {
			resp.Header.Set("Location", location)
		}
		return resp, nil
	})
	comment, err := client.CreateComment(context.Background(), CreateCommentParams{
		PostURN: repairPostID, ActivityURN: repairActivityID, ExpectedSenderURN: repairSenderID,
		ParentCommentURN: parent, Text: repairCommentText,
	})
	return comment, err, writes.Load()
}

func repairLocation(post, comment string) string {
	return "https://www.linkedin.com/voyager/api/socialActions/" + url.PathEscape(post) + "/comments/" + comment
}

func TestCommentReceiptRepairSparseAcknowledgments(t *testing.T) {
	for _, tc := range []struct {
		name, body, restID, location, parent, want string
	}{
		{"empty body full RestLi ID", "", repairCommentID, "", "", repairCommentID},
		{"empty root full RestLi ID", `{}`, repairModernID, "", "", repairCommentID},
		{"empty data encoded RestLi ID", `{"data":{}}`, url.PathEscape(repairModernID), "", "", repairCommentID},
		{"empty root legacy Location", `{}`, "", repairLocation(repairPostID, "789"), "", repairCommentPost},
		{"empty data full URN Location", `{"data":{}}`, "", repairModernID, "", repairCommentID},
		{"Location query is separate from identity", `{}`, "", repairLocation(repairPostID, "789") + "?tracking=urn%3Ali%3Aactivity%3A999", "", repairCommentPost},
		{"encoded query delimiters remain query data", `{}`, "", repairLocation(repairPostID, "789") + "?tracking=urn%3Ali%3Aactivity%3A999%23metadata&opaque=a%26b%3Fc", "", repairCommentPost},
		{"body and both header aliases agree", `{"data":{"entityUrn":"` + repairModernID + `"}}`, repairCommentPost, repairLocation(repairActivityID, "789"), "", repairCommentID},
		{"sparse nested reply is a new comment", `{}`, repairModernID, "", repairParentID, repairCommentID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comment, err, writes := repairCommentAttempt(t, tc.body, tc.restID, tc.location, tc.parent)
			if err != nil || comment == nil || writes != 1 || comment.URN != tc.want || comment.PostURN != repairPostID || comment.ParentURN != tc.parent || comment.Text != repairCommentText || WriteUnknownReason(err) != "" {
				t.Fatalf("receipt=%+v writes=%d reason=%q err=%v", comment, writes, WriteUnknownReason(err), err)
			}
		})
	}
}

func TestCommentReceiptRepairRejectsUncertainAcknowledgmentsOnce(t *testing.T) {
	for _, tc := range []struct {
		name, body, restID, location, parent, reason string
	}{
		{"malformed JSON cannot use a header", `{"data":`, repairModernID, "", "", "creation_value"},
		{"error cannot use a header", `{"errors":[{"message":"fixture failure"}]}`, repairModernID, "", "", "creation_value"},
		{"empty errors is not an approved sparse shape", `{"data":{},"errors":[]}`, repairModernID, "", "", "creation_value"},
		{"extra root data is not an approved sparse shape", `{"data":{},"included":[]}`, repairModernID, "", "", "creation_value"},
		{"null data is not sparse", `{"data":null}`, repairModernID, "", "", "creation_value"},
		{"unknown wrapper is not sparse", `{"result":{"entityUrn":"` + repairModernID + `"}}`, repairModernID, "", "", "creation_value"},
		{"unresolved explicit reference", `{"data":{"*value":"` + repairModernID + `"},"included":[{"entityUrn":"urn:li:fsd_comment:(888,urn:li:activity:123)"}]}`, repairModernID, "", "", "creation_value"},
		{"unrelated included is not a creation", `{"included":[{"entityUrn":"` + repairModernID + `"}]}`, repairModernID, "", "", "creation_value"},
		{"sparse body still requires an ID", `{}`, "", "", "", "comment_receipt"},
		{"explicit empty value cannot use a header", `{"data":{"value":{}}}`, repairModernID, "", "", "comment_receipt"},
		{"malformed body ID cannot use a header", `{"value":{"entityUrn":17}}`, repairModernID, "", "", "comment_receipt"},
		{"body ID has another post", `{"data":{"entityUrn":"urn:li:fsd_comment:(789,urn:li:activity:999)"}}`, "", "", "", "comment_receipt"},
		{"body ID aliases conflict", `{"data":{"entityUrn":"` + repairModernID + `","commentUrn":"urn:li:fsd_comment:(888,urn:li:activity:123)"}}`, "", "", "", "comment_receipt"},
		{"body and RestLi ID conflict", `{"data":{"entityUrn":"` + repairModernID + `"}}`, "urn:li:fsd_comment:(888,urn:li:activity:123)", "", "", "comment_header"},
		{"body and Location conflict", `{"data":{"entityUrn":"` + repairModernID + `"}}`, "", repairLocation(repairPostID, "888"), "", "comment_header"},
		{"header IDs conflict", `{}`, repairModernID, repairLocation(repairPostID, "888"), "", "comment_header"},
		{"bare RestLi ID is insufficient", `{}`, "789", "", "", "comment_header"},
		{"malformed RestLi encoding", `{}`, "%GG", "", "", "comment_header"},
		{"Location post differs", `{}`, "", repairLocation("urn:li:ugcPost:999", "789"), "", "comment_header"},
		{"Location comment is not numeric", `{}`, "", repairLocation(repairPostID, "opaque"), "", "comment_header"},
		{"Location extra path", `{}`, "", repairLocation(repairPostID, "789") + "/extra", "", "comment_header"},
		{"Location fragment", `{}`, "", repairLocation(repairPostID, "789") + "#receipt", "", "comment_header"},
		{"foreign Location cannot borrow a URN", `{}`, "", "https://evil.example/" + repairCommentID, "", "comment_header"},
		{"foreign Location otherwise has the exact route", `{}`, "", "https://evil.example/voyager/api/socialActions/urn:li:ugcPost:111/comments/789", "", "comment_header"},
		{"HTTP Location", `{}`, "", "http://www.linkedin.com/voyager/api/socialActions/urn:li:ugcPost:111/comments/789", "", "comment_header"},
		{"Location username", `{}`, "", "https://user@www.linkedin.com/voyager/api/socialActions/urn:li:ugcPost:111/comments/789", "", "comment_header"},
		{"Location explicit port", `{}`, "", "https://www.linkedin.com:443/voyager/api/socialActions/urn:li:ugcPost:111/comments/789", "", "comment_header"},
		{"unsupported Location invalidates a body receipt", `{"data":{"entityUrn":"` + repairModernID + `"}}`, "", "https://www.linkedin.com/unrelated", "", "comment_header"},
		{"a query URN cannot supply missing path identity", `{}`, "", "https://www.linkedin.com/unrelated?receipt=" + url.QueryEscape(repairCommentID), "", "comment_header"},
		{"different author", `{"data":{"entityUrn":"` + repairModernID + `","author":{"profileUrn":"urn:li:fsd_profile:OTHER"}}}`, "", "", "", "identity_mismatch"},
		{"unresolved commenter", `{"data":{"entityUrn":"` + repairModernID + `","commenter":{"title":{"text":"fixture name"}}}}`, "", "", "", "identity_mismatch"},
		{"changed text", `{"data":{"entityUrn":"` + repairModernID + `","commentary":{"text":"changed"}}}`, "", "", "", "request_mismatch"},
		{"unexpected parent", `{"data":{"entityUrn":"` + repairModernID + `","parentCommentUrn":"` + repairParentID + `"}}`, "", "", "", "request_mismatch"},
		{"nested parent contradicts request", `{"data":{"entityUrn":"` + repairModernID + `","parentCommentUrn":"urn:li:comment:(activity:123,555)"}}`, "", "", repairParentID, "request_mismatch"},
		{"body receipt is the existing parent", `{"data":{"entityUrn":"urn:li:fsd_comment:(456,urn:li:activity:123)"}}`, "", "", repairParentID, "comment_receipt"},
		{"header receipt is the existing parent", `{}`, repairParentID, "", repairParentID, "comment_header"},
		{"Location receipt is the existing parent through a post alias", `{}`, "", repairLocation(repairPostID, "456"), repairParentID, "comment_header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comment, err, writes := repairCommentAttempt(t, tc.body, tc.restID, tc.location, tc.parent)
			if comment != nil || writes != 1 || !IsWriteUnknown(err) || WriteUnknownReason(err) != tc.reason {
				t.Fatalf("receipt=%+v writes=%d reason=%q want=%q err=%v", comment, writes, WriteUnknownReason(err), tc.reason, err)
			}
		})
	}
}

func TestCommentReceiptRepairMissingParentNeverDispatches(t *testing.T) {
	comment, err, writes := repairCommentAttempt(t, `{}`, repairModernID, "", "urn:li:comment:(activity:123,999)")
	if comment != nil || writes != 0 || !errors.Is(err, ErrNotFound) || WriteUnknownReason(err) != "" {
		t.Fatalf("receipt=%+v writes=%d reason=%q err=%v", comment, writes, WriteUnknownReason(err), err)
	}
}

func TestInvitationReceiptRepairBoundedOutcomesOnce(t *testing.T) {
	const note = " exact invitation note 😀 "
	for _, tc := range []struct {
		name, body, reason string
		status             int
		definite           error
	}{
		{"confirmed exact note", `{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:7000000000000000000","recipientUrn":"urn:li:fsd_profile:RECIPIENT","customMessage":" exact invitation note 😀 "}}}`, "", 201, nil},
		{"malformed JSON", `{`, "creation_value", 201, nil},
		{"provider error envelope", `{"errors":[{"message":"fixture failure"}],"value":{"invitationUrn":"urn:li:fsd_invitation:7000000000000000000"}}`, "creation_value", 201, nil},
		{"missing invitation ID", `{"data":{"value":{}}}`, "invitation_receipt", 201, nil},
		{"null invitation ID", `{"data":{"value":{"invitationUrn":null}}}`, "invitation_receipt", 201, nil},
		{"conflicting invitation IDs", `{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:7000000000000000000","*invitation":"urn:li:fsd_invitation:7000000000000000001"}}}`, "invitation_receipt", 201, nil},
		{"different recipient", `{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:7000000000000000000","invitee":{"inviteeUnion":{"memberProfile":"urn:li:fsd_profile:OTHER"}}}}}`, "identity_mismatch", 201, nil},
		{"unresolved recipient", `{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:7000000000000000000","recipientUrn":null}}}`, "identity_mismatch", 201, nil},
		{"changed required note", `{"data":{"value":{"invitationUrn":"urn:li:fsd_invitation:7000000000000000000","customMessage":""}}}`, "request_mismatch", 201, nil},
		{"email required", `{"data":{"code":"EMAIL_REQUIRED"}}`, "", 422, ErrEmailRequired},
		{"note allowance exhausted", `{"data":{"code":"PERSONALIZED_INVITATION_LIMIT_REACHED"}}`, "", 422, ErrNoteUnavailable},
		{"provider reports pending", `{"code":"INVITATION_PENDING"}`, "", 409, ErrInvitationPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			client := offlineClient(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost {
					return fixtureRead(req, 2), nil
				}
				writes.Add(1)
				if req.URL.Path != "/voyager/api/voyagerRelationshipsDashMemberRelationships" || req.URL.Query().Get("action") != "verifyQuotaAndCreateV2" || req.GetBody != nil {
					t.Fatal("unexpected or replayable invitation request")
				}
				var payload map[string]any
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				invitee, _ := payload["invitee"].(map[string]any)
				union, _ := invitee["inviteeUnion"].(map[string]any)
				if payload["customMessage"] != note || union["memberProfile"] != "urn:li:fsd_profile:RECIPIENT" {
					t.Fatal("approved recipient or required note changed")
				}
				return response(req, tc.status, tc.body), nil
			})
			receipt, err := client.SendConnectionInvitation(context.Background(), InvitationParams{
				RecipientURN: "urn:li:fsd_profile:RECIPIENT", RecipientProfileURL: "https://www.linkedin.com/in/recipient", ExpectedSenderURN: repairSenderID, Note: note,
			})
			if writes.Load() != 1 {
				t.Fatalf("POST dispatched %d times", writes.Load())
			}
			switch {
			case tc.definite != nil:
				if receipt != nil || !errors.Is(err, tc.definite) || IsWriteUnknown(err) || WriteUnknownReason(err) != "" {
					t.Fatalf("receipt=%+v reason=%q err=%v", receipt, WriteUnknownReason(err), err)
				}
			case tc.reason != "":
				if receipt != nil || !IsWriteUnknown(err) || WriteUnknownReason(err) != tc.reason {
					t.Fatalf("receipt=%+v reason=%q want=%q err=%v", receipt, WriteUnknownReason(err), tc.reason, err)
				}
			default:
				if err != nil || receipt == nil || receipt.InvitationURN != "urn:li:fsd_invitation:7000000000000000000" || receipt.RecipientURN != "urn:li:fsd_profile:RECIPIENT" {
					t.Fatalf("receipt=%+v err=%v", receipt, err)
				}
			}
		})
	}
}
