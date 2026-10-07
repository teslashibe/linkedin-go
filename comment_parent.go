package linkedin

import (
	"encoding/json"
	"strings"
)

// verifyExactParentComment accepts only the normalized singleComment resource.
// Its service entityUrn binds the resource reference; the selected comment's own
// urn supplies the comment identity. Unreferenced siblings supply neither.
func verifyExactParentComment(body []byte, parent string, target *CommentTarget) error {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || root == nil || hasErrorEnvelope(root) {
		return ErrParseFailed
	}
	data, ok := root["data"].(map[string]any)
	if !ok || hasErrorEnvelope(data) {
		return ErrParseFailed
	}
	refs, ok := data["*elements"].([]any)
	if !ok {
		return ErrParseFailed
	}
	if len(refs) == 0 {
		return ErrNotFound
	}
	if len(refs) != 1 {
		return ErrParseFailed
	}
	ref, ok := refs[0].(string)
	if !ok || !strings.HasPrefix(ref, "urn:li:") || len(ref) <= len("urn:li:") || strings.ContainsAny(ref, " \t\r\n") {
		return ErrParseFailed
	}
	included, ok := root["included"].([]any)
	if !ok {
		return ErrParseFailed
	}
	var selected map[string]any
	for _, value := range included {
		obj, ok := value.(map[string]any)
		if !ok || obj["entityUrn"] != ref {
			continue
		}
		if selected != nil {
			return ErrParseFailed
		}
		selected = obj
	}
	if selected == nil {
		return ErrNotFound
	}
	if selected["$type"] != "com.linkedin.voyager.feed.Comment" || hasErrorEnvelope(selected) {
		return ErrParseFailed
	}
	urn, ok := selected["urn"].(string)
	canonical, err := CanonicalCommentURN(urn)
	if !ok || err != nil || !sameComment(canonical, parent, target) {
		return ErrParseFailed
	}
	// The service identity may be opaque. If it is itself a comment alias, it
	// must be well formed and agree; explicit identity aliases are never ignored.
	for _, key := range []string{"entityUrn", "commentUrn", "*comment"} {
		value, exists := selected[key]
		if !exists {
			continue
		}
		raw, ok := value.(string)
		if !ok {
			return ErrParseFailed
		}
		if key == "entityUrn" && !commentIdentityFamily(raw) {
			continue
		}
		alias, err := CanonicalCommentURN(raw)
		if err != nil || !sameComment(alias, canonical, target) {
			return ErrParseFailed
		}
	}
	// Null thread/parent fields occur on the normalized legacy comment record.
	// Its own comment urn already proves ancestry. Explicit associations still
	// have to identify this freshly resolved post or its activity.
	for _, key := range []string{"threadUrn", "parentCommentUrn", "parentUrn", "objectUrn", "postUrn", "activityUrn", "updateUrn", "shareUrn", "backendUrn"} {
		value, exists := selected[key]
		if !exists || (value == nil && (key == "threadUrn" || key == "parentCommentUrn")) {
			continue
		}
		raw, ok := value.(string)
		if !ok {
			return ErrParseFailed
		}
		post, err := canonicalPostReference(raw)
		if key == "parentCommentUrn" {
			post, err = CommentPostURN(raw)
		} else if key == "parentUrn" || key == "threadUrn" {
			if commentPost, commentErr := CommentPostURN(raw); commentErr == nil {
				post, err = commentPost, nil
			}
		}
		if err != nil || (post != target.PostURN && post != target.ActivityURN) {
			return ErrParseFailed
		}
		if (key == "activityUrn" || key == "updateUrn") && post != target.ActivityURN {
			return ErrParseFailed
		}
	}
	text, ok := selected["commentV2"].(map[string]any)
	if !ok || hasErrorEnvelope(text) {
		return ErrParseFailed
	}
	if value, ok := text["text"].(string); !ok || value == "" {
		return ErrParseFailed
	}
	// Inspect known immediate child error envelopes without borrowing their
	// identity or text as proof of the selected parent.
	for _, key := range []string{"comment", "commentary", "metadata", "socialDetail", "commenter", "author"} {
		if child, ok := selected[key].(map[string]any); ok && hasErrorEnvelope(child) {
			return ErrParseFailed
		}
	}
	return nil
}

func commentIdentityFamily(raw string) bool {
	return strings.HasPrefix(raw, "urn:li:comment:") || strings.HasPrefix(raw, "urn:li:fsd_comment:") || strings.HasPrefix(raw, "urn:li:fsd_normComment:")
}
