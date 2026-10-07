package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const parentServiceRef = "urn:li:fs_comment:FIXTURE-SERVICE"

// Synthetic identifiers retain the shape of a bounded authenticated read:
// data's one opaque service reference selects feed.Comment; its own urn is the
// legacy native comment, with null ancestry fields and direct commentV2.text.
// Same-urn SocialDetail/other feed records were included but not referenced.
func exactParentEnvelope() map[string]any {
	return map[string]any{
		"data": map[string]any{"*elements": []any{parentServiceRef}},
		"included": []any{
			map[string]any{"$type": "com.linkedin.voyager.feed.SocialDetail", "entityUrn": "urn:li:fs_socialDetail:DECOY", "urn": "urn:li:comment:(activity:123,456)"},
			map[string]any{"$type": "com.linkedin.voyager.feed.Comment", "entityUrn": parentServiceRef, "urn": "urn:li:comment:(activity:123,456)", "threadUrn": nil, "parentCommentUrn": nil, "commentV2": map[string]any{"text": "fixture parent"}},
			map[string]any{"$type": "com.linkedin.voyager.feed.Decoy", "entityUrn": "urn:li:feed:DECOY", "urn": "urn:li:comment:(activity:123,456)"},
		},
	}
}

func exactParentFixture() string {
	body, _ := json.Marshal(exactParentEnvelope())
	return string(body)
}

func fixtureParent(root map[string]any) map[string]any {
	return root["included"].([]any)[1].(map[string]any)
}

func TestExactParentResourceBindsOwnIdentityAndAncestry(t *testing.T) {
	for _, tc := range []struct {
		name, approved string
		change         func(map[string]any)
	}{
		{"opaque service identity and unreferenced same-urn decoys", repairParentID, nil},
		{"canonical backend post alias uses verified native activity", "urn:li:comment:(urn:li:ugcPost:111,456)", nil},
		{"modern approved alias", "urn:li:fsd_comment:(456,urn:li:ugcPost:111)", nil},
		{"explicit agreeing comment alias", repairParentID, func(root map[string]any) {
			fixtureParent(root)["commentUrn"] = "urn:li:fsd_comment:(456,urn:li:ugcPost:111)"
		}},
		{"comment service alias agrees", repairParentID, func(root map[string]any) {
			ref := "urn:li:fsd_comment:(456,urn:li:ugcPost:111)"
			root["data"].(map[string]any)["*elements"] = []any{ref}
			fixtureParent(root)["entityUrn"] = ref
		}},
		{"existing nested parent linkage has another ID on this activity", repairParentID, func(root map[string]any) {
			fixtureParent(root)["parentCommentUrn"] = "urn:li:comment:(activity:123,222)"
			fixtureParent(root)["threadUrn"] = "urn:li:fsd_comment:(222,urn:li:ugcPost:111)"
		}},
		{"explicit bound post", repairParentID, func(root map[string]any) { fixtureParent(root)["objectUrn"] = repairPostID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := exactParentEnvelope()
			if tc.change != nil {
				tc.change(root)
			}
			body, _ := json.Marshal(root)
			var reads, writes int
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPost {
					writes++
					var payload map[string]any
					if json.NewDecoder(req.Body).Decode(&payload) != nil || payload["threadUrn"] != "urn:li:comment:(activity:123,456)" || valueText(payload["commentary"]) != repairCommentText || req.GetBody != nil {
						t.Fatal("nested approved text/ancestry changed or replay enabled")
					}
					return response(req, 201, `{"data":{"entityUrn":"`+repairModernID+`"}}`), nil
				}
				if req.URL.Path == "/voyager/api/feed/comments" {
					reads++
					assertSingleParentRequest(t, req)
					return response(req, 200, string(body)), nil
				}
				if strings.Contains(req.URL.Path, "/socialActions/") {
					t.Fatal("retired comments-page lookup")
				}
				return fixtureRead(req, 2), nil
			})
			created, err := c.CreateComment(context.Background(), CreateCommentParams{PostURN: repairPostID, ParentCommentURN: tc.approved, ExpectedSenderURN: repairSenderID, Text: repairCommentText})
			canonical, _ := CanonicalCommentURN(tc.approved)
			if err != nil || created == nil || created.ParentURN != canonical || created.URN != repairCommentID || reads != 1 || writes != 1 {
				t.Fatalf("reads=%d writes=%d created=%+v err=%v", reads, writes, created, err)
			}
		})
	}
}

func assertSingleParentRequest(t *testing.T, req *http.Request) {
	t.Helper()
	query := req.URL.Query()
	if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "www.linkedin.com" || query.Get("q") != "singleComment" || query.Get("commentUrn") != "urn:li:comment:(activity:123,456)" || len(query) != 2 {
		t.Fatal("parent read is not the exact source-proven resource and bound native tuple")
	}
}

func TestExactParentResourceFailuresNeverDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		change     func(map[string]any)
		want       error
	}{
		{"malformed JSON", `{`, nil, ErrParseFailed},
		{"null root", `null`, nil, ErrParseFailed},
		{"legacy page cannot substitute", `{"elements":[{"urn":"urn:li:comment:(activity:123,456)"}]}`, nil, ErrParseFailed},
		{"root error", "", func(r map[string]any) { r["code"] = "RESTRICTED" }, ErrParseFailed},
		{"data error", "", func(r map[string]any) { r["data"].(map[string]any)["status"] = 403 }, ErrParseFailed},
		{"null data", "", func(r map[string]any) { r["data"] = nil }, ErrParseFailed},
		{"null references", "", func(r map[string]any) { r["data"].(map[string]any)["*elements"] = nil }, ErrParseFailed},
		{"empty references", "", func(r map[string]any) { r["data"].(map[string]any)["*elements"] = []any{} }, ErrNotFound},
		{"multiple references", "", func(r map[string]any) {
			r["data"].(map[string]any)["*elements"] = []any{parentServiceRef, "urn:li:fs_comment:OTHER"}
		}, ErrParseFailed},
		{"duplicate references", "", func(r map[string]any) {
			r["data"].(map[string]any)["*elements"] = []any{parentServiceRef, parentServiceRef}
		}, ErrParseFailed},
		{"object reference", "", func(r map[string]any) {
			r["data"].(map[string]any)["*elements"] = []any{map[string]any{"entityUrn": parentServiceRef}}
		}, ErrParseFailed},
		{"null reference", "", func(r map[string]any) { r["data"].(map[string]any)["*elements"] = []any{nil} }, ErrParseFailed},
		{"foreign reference", "", func(r map[string]any) {
			r["data"].(map[string]any)["*elements"] = []any{"https://example.test/comment"}
		}, ErrParseFailed},
		{"unresolved reference despite matching siblings", "", func(r map[string]any) { r["data"].(map[string]any)["*elements"] = []any{"urn:li:fs_comment:MISSING"} }, ErrNotFound},
		{"null included", "", func(r map[string]any) { r["included"] = nil }, ErrParseFailed},
		{"ambiguous included binding", "", func(r map[string]any) { r["included"] = append(r["included"].([]any), fixtureParent(r)) }, ErrParseFailed},
		{"wrong type cannot borrow comment sibling", "", func(r map[string]any) { fixtureParent(r)["$type"] = "com.linkedin.voyager.feed.SocialDetail" }, ErrParseFailed},
		{"nested wrapper cannot supply selected own fields", "", func(r map[string]any) {
			p := fixtureParent(r)
			p["comment"] = map[string]any{"urn": p["urn"], "commentV2": p["commentV2"]}
			delete(p, "urn")
		}, ErrParseFailed},
		{"selected error", "", func(r map[string]any) { fixtureParent(r)["error"] = map[string]any{} }, ErrParseFailed},
		{"null own urn", "", func(r map[string]any) { fixtureParent(r)["urn"] = nil }, ErrParseFailed},
		{"wrong comment ID", "", func(r map[string]any) { fixtureParent(r)["urn"] = "urn:li:comment:(activity:123,777)" }, ErrParseFailed},
		{"wrong post", "", func(r map[string]any) { fixtureParent(r)["urn"] = "urn:li:comment:(activity:999,456)" }, ErrParseFailed},
		{"malformed own urn", "", func(r map[string]any) { fixtureParent(r)["urn"] = "urn:li:comment:(activity:123,456,777)" }, ErrParseFailed},
		{"conflicting comment alias", "", func(r map[string]any) { fixtureParent(r)["commentUrn"] = "urn:li:comment:(activity:123,777)" }, ErrParseFailed},
		{"null comment alias", "", func(r map[string]any) { fixtureParent(r)["commentUrn"] = nil }, ErrParseFailed},
		{"malformed comment service alias", "", func(r map[string]any) {
			ref := "urn:li:fsd_comment:BAD"
			r["data"].(map[string]any)["*elements"] = []any{ref}
			fixtureParent(r)["entityUrn"] = ref
		}, ErrParseFailed},
		{"conflicting comment service alias", "", func(r map[string]any) {
			ref := "urn:li:fsd_comment:(777,urn:li:activity:123)"
			r["data"].(map[string]any)["*elements"] = []any{ref}
			fixtureParent(r)["entityUrn"] = ref
		}, ErrParseFailed},
		{"foreign thread linkage", "", func(r map[string]any) { fixtureParent(r)["threadUrn"] = "urn:li:comment:(activity:999,222)" }, ErrParseFailed},
		{"foreign parent linkage", "", func(r map[string]any) { fixtureParent(r)["parentCommentUrn"] = "urn:li:comment:(activity:999,222)" }, ErrParseFailed},
		{"post cannot substitute for parent comment linkage", "", func(r map[string]any) { fixtureParent(r)["parentCommentUrn"] = repairActivityID }, ErrParseFailed},
		{"foreign explicit post", "", func(r map[string]any) { fixtureParent(r)["objectUrn"] = "urn:li:ugcPost:999" }, ErrParseFailed},
		{"backend post cannot substitute for explicit activity", "", func(r map[string]any) { fixtureParent(r)["activityUrn"] = repairPostID }, ErrParseFailed},
		{"null explicit post", "", func(r map[string]any) { fixtureParent(r)["objectUrn"] = nil }, ErrParseFailed},
		{"malformed thread linkage", "", func(r map[string]any) { fixtureParent(r)["threadUrn"] = map[string]any{} }, ErrParseFailed},
		{"null text container", "", func(r map[string]any) { fixtureParent(r)["commentV2"] = nil }, ErrParseFailed},
		{"text error", "", func(r map[string]any) { fixtureParent(r)["commentV2"].(map[string]any)["code"] = "RESTRICTED" }, ErrParseFailed},
		{"null text", "", func(r map[string]any) { fixtureParent(r)["commentV2"].(map[string]any)["text"] = nil }, ErrParseFailed},
		{"nested text cannot supply own text", "", func(r map[string]any) {
			fixtureParent(r)["commentV2"] = map[string]any{"child": map[string]any{"text": "decoy"}}
		}, ErrParseFailed},
		{"known child error", "", func(r map[string]any) { fixtureParent(r)["comment"] = map[string]any{"errors": []any{}} }, ErrParseFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == "" {
				root := exactParentEnvelope()
				tc.change(root)
				encoded, _ := json.Marshal(root)
				body = string(encoded)
			}
			var reads, writes int
			c := offlineClient(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodPost {
					writes++
					t.Fatal("invalid exact-parent response authorized a POST")
				}
				if req.URL.Path == "/voyager/api/feed/comments" {
					reads++
					assertSingleParentRequest(t, req)
					return response(req, 200, body), nil
				}
				if strings.Contains(req.URL.Path, "/socialActions/") {
					t.Fatal("retired page or alternate parent lookup")
				}
				return fixtureRead(req, 2), nil
			})
			_, err := c.CreateComment(context.Background(), CreateCommentParams{PostURN: repairPostID, ParentCommentURN: repairParentID, ExpectedSenderURN: repairSenderID, Text: repairCommentText})
			want := tc.want
			if want == nil {
				want = ErrParseFailed
			}
			if !errors.Is(err, want) || reads != 1 || writes != 0 {
				t.Fatalf("reads=%d writes=%d err=%v want=%v", reads, writes, err, want)
			}
		})
	}
}
