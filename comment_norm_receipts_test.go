package linkedin

import (
	"errors"
	"fmt"
	"testing"
)

const repairNormPrefix = "urn:li:fsd_normComment:"

// First-party display code constructs this service identity by prefixing a
// comment's dashEntityUrn. These synthetic envelopes exercise that identity at
// the existing creation boundary; they are not a captured live acknowledgement.
func TestCommentReceiptNormServiceIdentitySingleDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"legacy underlying comment", `{"data":{"entityUrn":"` + repairNormPrefix + `urn:li:comment:(activity:123,789)"}}`},
		{"modern underlying comment", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `"}}`},
		{"existing normalized tuple underlying comment", `{"data":{"entityUrn":"` + repairNormPrefix + `urn:li:fsd_normComment:(789,urn:li:activity:123)"}}`},
		{"all own aliases agree", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","urn":"` + repairCommentPost + `","commentUrn":"` + repairCommentID + `","text":"  exact receipt fixture 😀\n ","authorUrn":"` + repairSenderID + `","parentCommentUrn":"` + repairParentID + `"}}`},
		{"explicit unique creation reference", `{"data":{"*value":"` + repairNormPrefix + repairModernID + `"},"included":[{"entityUrn":"` + repairNormPrefix + repairModernID + `"},{"entityUrn":"urn:li:fsd_comment:(888,urn:li:activity:999)"}]}`},
		{"unrelated included child evidence is ignored", `{"data":{"*value":"` + repairNormPrefix + repairModernID + `"},"included":[{"entityUrn":"` + repairNormPrefix + repairModernID + `"},{"entityUrn":"urn:li:fsd_comment:(888,urn:li:activity:999)","singleComment":{"elements":[]},"*singleComment":"urn:li:collection:UNRELATED"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comment, err, writes := repairCommentAttempt(t, tc.body, repairCommentPost, "", repairParentID)
			if err != nil || comment == nil || writes != 1 || comment.URN != repairCommentPost || comment.ParentURN != repairParentID || comment.Text != repairCommentText || WriteUnknownReason(err) != "" || WriteUnknownDetail(err) != "" {
				t.Fatalf("receipt=%+v writes=%d reason=%q detail=%q err=%v", comment, writes, WriteUnknownReason(err), WriteUnknownDetail(err), err)
			}
		})
	}
	// Receipt service identities never become accepted public destinations.
	if _, err := CanonicalCommentURN(repairNormPrefix + repairModernID); !errors.Is(err, ErrInvalidParams) {
		t.Fatal("public target grammar broadened")
	}
}

func TestCommentReceiptNormServiceIdentityFailuresStayUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason, detail string
	}{
		{"repeated service wrapper", `{"data":{"entityUrn":"` + repairNormPrefix + repairNormPrefix + repairModernID + `"}}`, "comment_receipt", "id_invalid"},
		{"unrelated service prefix", `{"data":{"entityUrn":"urn:li:fs_normComment:` + repairModernID + `"}}`, "comment_receipt", "id_invalid"},
		{"bare numeric service ID", `{"data":{"entityUrn":"` + repairNormPrefix + `789"}}`, "comment_receipt", "id_invalid"},
		{"unrelated underlying identity", `{"data":{"entityUrn":"` + repairNormPrefix + repairSenderID + `"}}`, "comment_receipt", "id_invalid"},
		{"unsupported nested tuple", `{"data":{"entityUrn":"` + repairNormPrefix + `urn:li:comment:(comment:(activity:123,456),789)"}}`, "comment_receipt", "id_invalid"},
		{"underlying URI suffix", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `?tracking=1"}}`, "comment_receipt", "id_invalid"},
		{"encoded body identity is not normalized", `{"data":{"entityUrn":"` + repairNormPrefix + `urn%3Ali%3Acomment%3A(activity%3A123%2C789)"}}`, "comment_receipt", "id_invalid"},
		{"foreign post inside wrapper", `{"data":{"entityUrn":"` + repairNormPrefix + `urn:li:fsd_comment:(789,urn:li:activity:999)"}}`, "comment_receipt", "post_mismatch"},
		{"wrapped existing parent", `{"data":{"entityUrn":"` + repairNormPrefix + `urn:li:comment:(activity:123,456)"}}`, "comment_receipt", "existing_parent"},
		{"wrapped aliases conflict", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","urn":"urn:li:comment:(activity:123,888)"}}`, "comment_receipt", "alias_mismatch"},
		{"wrapped exact text mismatch", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","text":"changed"}}`, "request_mismatch", ""},
		{"wrapped exact author mismatch", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","authorUrn":"urn:li:fsd_profile:OTHER"}}`, "identity_mismatch", ""},
		{"wrapped explicit parent mismatch", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","parentCommentUrn":"urn:li:comment:(activity:123,222)"}}`, "request_mismatch", ""},
		{"wrapped error envelope", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","code":"RESTRICTED"}}`, "creation_value", ""},
		{"wrapped unresolved creation reference", `{"data":{"*value":"` + repairNormPrefix + repairModernID + `"},"included":[]}`, "creation_value", ""},
		{"wire singleComment object remains unsupported", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","singleComment":{"elements":[{"urn":"` + repairCommentID + `"}]}}}`, "comment_receipt", "detail_unsupported"},
		{"wire singleComment reference remains unsupported", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","*singleComment":"urn:li:collection:UNKNOWN"}}`, "comment_receipt", "detail_unsupported"},
		{"explicit null singleComment remains unsupported", `{"data":{"entityUrn":"` + repairNormPrefix + repairModernID + `","singleComment":null}}`, "comment_receipt", "detail_unsupported"},
		{"data child evidence survives direct value selection", `{"data":{"value":{"entityUrn":"` + repairNormPrefix + repairModernID + `"},"singleComment":{"elements":[]}}}`, "comment_receipt", "detail_unsupported"},
		{"root child reference survives direct data value selection", `{"*singleComment":"urn:li:collection:UNKNOWN","data":{"value":{"entityUrn":"` + repairNormPrefix + repairModernID + `"}}}`, "comment_receipt", "detail_unsupported"},
		{"root child evidence survives root value selection", `{"value":{"entityUrn":"` + repairNormPrefix + repairModernID + `"},"singleComment":null}`, "comment_receipt", "detail_unsupported"},
		{"data child reference survives included value selection", `{"data":{"*value":"` + repairNormPrefix + repairModernID + `","*singleComment":"urn:li:collection:UNKNOWN"},"included":[{"entityUrn":"` + repairNormPrefix + repairModernID + `"}]}`, "comment_receipt", "detail_unsupported"},
		{"root child evidence survives included value selection", `{"singleComment":{"elements":[]},"data":{"*value":"` + repairNormPrefix + repairModernID + `"},"included":[{"entityUrn":"` + repairNormPrefix + repairModernID + `"}]}`, "comment_receipt", "detail_unsupported"},
		{"data value child reference survives createdComment selection", `{"data":{"value":{"createdComment":{"entityUrn":"` + repairNormPrefix + repairModernID + `"},"*singleComment":"urn:li:collection:UNKNOWN"}}}`, "comment_receipt", "detail_unsupported"},
		{"root value child evidence survives createdComment selection", `{"value":{"createdComment":{"entityUrn":"` + repairNormPrefix + repairModernID + `"},"singleComment":null}}`, "comment_receipt", "detail_unsupported"},
		{"data value child evidence survives equal included creation reference", `{"data":{"*value":"` + repairNormPrefix + repairModernID + `","value":{"createdComment":{"entityUrn":"` + repairNormPrefix + repairModernID + `"},"singleComment":{"elements":[]}}},"included":[{"entityUrn":"` + repairNormPrefix + repairModernID + `"}]}`, "comment_receipt", "detail_unsupported"},
		{"root value child reference survives equal included creation reference", `{"*value":"` + repairNormPrefix + repairModernID + `","value":{"createdComment":{"entityUrn":"` + repairNormPrefix + repairModernID + `"},"*singleComment":"urn:li:collection:UNKNOWN"},"included":[{"entityUrn":"` + repairNormPrefix + repairModernID + `"}]}`, "comment_receipt", "detail_unsupported"},
		{"non-string body ID", `{"value":{"entityUrn":17}}`, "comment_receipt", "id_type"},
		{"missing body ID", `{"data":{"value":{}}}`, "comment_receipt", "id_missing"},
		{"sparse body and absent headers", `{}`, "comment_receipt", "id_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A header never bypasses an unsupported or contradictory body.
			restID := repairModernID
			if tc.name == "sparse body and absent headers" {
				restID = ""
			}
			comment, err, writes := repairCommentAttempt(t, tc.body, restID, "", repairParentID)
			if comment != nil || writes != 1 || !IsWriteUnknown(err) || WriteUnknownReason(err) != tc.reason || WriteUnknownDetail(err) != tc.detail {
				t.Fatalf("receipt=%+v writes=%d reason=%q detail=%q err=%v", comment, writes, WriteUnknownReason(err), WriteUnknownDetail(err), err)
			}
		})
	}
}

func TestCommentReceiptUnknownDetailIsBounded(t *testing.T) {
	for _, detail := range []string{commentReceiptIDType, commentReceiptIDInvalid, commentReceiptPostMismatch, commentReceiptExistingParent, commentReceiptAliasMismatch, commentReceiptIDMissing, commentReceiptDetailUnsupported} {
		err := fmt.Errorf("host wrapper: %w", unknownCommentReceipt(detail))
		if WriteUnknownReason(err) != "comment_receipt" || WriteUnknownDetail(err) != detail {
			t.Fatal("safe wrapped receipt detail lost")
		}
	}
	if WriteUnknownDetail(unknownCommentReceipt("unbounded provider content")) != "unclassified" {
		t.Fatal("unbounded detail escaped")
	}
	for _, err := range []error{nil, ErrWriteRejected, unknownReceiptReason(writeUnknownCommentHeader), &WriteError{Cause: ErrUnauthorized, unknownReason: writeUnknownCommentReceipt, unknownDetail: commentReceiptIDInvalid}} {
		if WriteUnknownDetail(err) != "" {
			t.Fatal("detail exposed for a different or definite outcome")
		}
	}
}
