package linkedin

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

func canonicalPostReference(raw string) (string, error) {
	for _, kind := range []string{"activity", "ugcPost", "share"} {
		prefix := "urn:li:" + kind + ":"
		if id, ok := strings.CutPrefix(raw, prefix); ok && numericID(id) {
			return raw, nil
		}
	}
	return "", ErrInvalidParams
}

func numericID(id string) bool {
	if id == "" || len(id) > 30 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// CanonicalCommentURN accepts strict provider comment tuples and makes the inner
// post reference explicit. It never treats an unrelated URN as a comment target.
func CanonicalCommentURN(raw string) (string, error) {
	inner := ""
	for _, prefix := range []string{"urn:li:comment:(", "urn:li:fsd_comment:(", "urn:li:fsd_normComment:("} {
		if value, ok := strings.CutPrefix(raw, prefix); ok && strings.HasSuffix(value, ")") {
			inner = strings.TrimSuffix(value, ")")
			break
		}
	}
	parts := strings.Split(inner, ",")
	if len(parts) == 2 && (strings.HasPrefix(raw, "urn:li:fsd_comment:") || strings.HasPrefix(raw, "urn:li:fsd_normComment:")) {
		parts[0], parts[1] = parts[1], parts[0]
	}
	if len(parts) != 2 || !numericID(parts[1]) {
		return "", ErrInvalidParams
	}
	post := parts[0]
	if !strings.HasPrefix(post, "urn:li:") {
		post = "urn:li:" + post
	}
	if _, err := canonicalPostReference(post); err != nil {
		return "", err
	}
	return "urn:li:comment:(" + post + "," + parts[1] + ")", nil
}

func CommentPostURN(raw string) (string, error) {
	comment, err := CanonicalCommentURN(raw)
	if err != nil {
		return "", err
	}
	return strings.Split(strings.TrimPrefix(comment, "urn:li:comment:("), ",")[0], nil
}

func outreachPostReference(identifier string) (string, error) {
	if strings.HasPrefix(identifier, "urn:li:") {
		return canonicalPostReference(identifier)
	}
	u, err := url.Parse(identifier)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		(u.Hostname() != "linkedin.com" && u.Hostname() != "www.linkedin.com") {
		return "", ErrInvalidParams
	}
	parts := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	if len(parts) == 4 && parts[0] == "" && parts[1] == "feed" && parts[2] == "update" {
		return canonicalPostReference(parts[3])
	}
	if len(parts) == 3 && parts[0] == "" && parts[1] == "posts" {
		if index := strings.LastIndex(parts[2], "-activity-"); index >= 0 {
			id := strings.SplitN(parts[2][index+len("-activity-"):], "-", 2)[0]
			if numericID(id) {
				return "urn:li:activity:" + id, nil
			}
		}
	}
	return "", ErrInvalidParams
}

// ResolveCommentTarget binds an activity to the observed share/ugcPost thread.
// Parent comments must be observed on that thread before a nested write.
func (c *Client) ResolveCommentTarget(ctx context.Context, identifier, parent string) (*CommentTarget, error) {
	wanted, err := outreachPostReference(identifier)
	if err != nil {
		return nil, err
	}
	if _, err := canonicalPostReference(wanted); err != nil {
		return nil, err
	}
	var target *CommentTarget
	var lastErr error
	for _, endpoint := range []string{
		apiBase + "/feed/updatesV2?q=backendUrnOrNss&urnOrNss=" + url.QueryEscape(wanted),
		apiBase + "/feed/updates/" + url.PathEscape(wanted),
	} {
		body, err := c.makeRequest(ctx, endpoint)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrChallengeRequired) || errors.Is(err, ErrAccountRestricted) {
				return nil, err
			}
			lastErr = err
			continue
		}
		target, err = resolvedCommentTarget(body, wanted)
		if err == nil {
			break
		}
		lastErr = err
	}
	if target == nil {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrNotFound
	}
	if parent == "" {
		return target, nil
	}
	canonical, err := CanonicalCommentURN(parent)
	if err != nil {
		return nil, err
	}
	post, _ := CommentPostURN(canonical)
	if post != target.PostURN && post != target.ActivityURN {
		return nil, ErrInvalidParams
	}
	// The current singleComment resource returns an explicit reference to its
	// comment entity. Use the same verified activity tuple as the nested writer;
	// a page of other comments cannot establish this exact parent's identity.
	parentID := strings.TrimSuffix(strings.Split(canonical, ",")[1], ")")
	nativeParent := "urn:li:comment:(" + strings.TrimPrefix(target.ActivityURN, "urn:li:") + "," + parentID + ")"
	body, err := c.makeRequest(ctx, apiBase+"/feed/comments?q=singleComment&commentUrn="+url.QueryEscape(nativeParent))
	if err != nil {
		return nil, err
	}
	if err := verifyExactParentComment(body, canonical, target); err != nil {
		return nil, err
	}
	target.ParentCommentURN = canonical
	return target, nil
}

func sameComment(a, b string, target *CommentTarget) bool {
	if a == b {
		return true
	}
	postA, errA := CommentPostURN(a)
	postB, errB := CommentPostURN(b)
	if errA != nil || errB != nil || (postA != target.PostURN && postA != target.ActivityURN) || (postB != target.PostURN && postB != target.ActivityURN) {
		return false
	}
	return strings.Split(a, ",")[1] == strings.Split(b, ",")[1]
}

func resolvedCommentTarget(body []byte, wanted string) (*CommentTarget, error) {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return nil, ErrParseFailed
	}
	if wrapper, ok := root.(map[string]any); ok {
		data, _ := wrapper["data"].(map[string]any)
		if hasErrorEnvelope(wrapper) || hasErrorEnvelope(data) {
			return nil, ErrParseFailed
		}
	}
	var found *CommentTarget
	for _, obj := range collectObjects(root) {
		entity := firstString(obj, "entityUrn", "urn")
		// Only an identified update/post can bind the activity and backend
		// thread. Recursive scans of a response wrapper can pair sibling posts.
		entityActivity := embeddedActivityURN(entity)
		entityPost, entityErr := canonicalPostReference(entity)
		if entityActivity == "" && entityErr != nil {
			continue
		}
		activity := firstString(obj, "activityUrn", "updateUrn")
		if embedded := embeddedActivityURN(entity); embedded != "" {
			if activity != "" && activity != embedded {
				return nil, ErrParseFailed
			}
			activity = embedded
		}
		if strings.HasPrefix(entity, "urn:li:activity:") {
			if activity != "" && activity != entity {
				return nil, ErrParseFailed
			}
			activity = entity
		}
		thread := ""
		nodes := []map[string]any{obj}
		for _, key := range []string{"metadata", "socialDetail"} {
			if child, ok := obj[key].(map[string]any); ok {
				nodes = append(nodes, child)
			}
		}
		for _, node := range nodes {
			for _, key := range []string{"shareUrn", "threadUrn", "backendUrn", "*socialDetail", "socialDetailUrn"} {
				candidate := firstString(node, key)
				if index := strings.Index(candidate, "urn:li:ugcPost:"); index >= 0 {
					candidate = candidate[index:]
				}
				if index := strings.Index(candidate, "urn:li:share:"); index >= 0 {
					candidate = candidate[index:]
				}
				if value, err := canonicalPostReference(candidate); err == nil && !strings.HasPrefix(value, "urn:li:activity:") {
					if thread != "" && thread != value {
						return nil, ErrParseFailed
					}
					thread = value
				}
			}
		}
		if entityErr == nil && !strings.HasPrefix(entityPost, "urn:li:activity:") {
			if thread != "" && thread != entityPost {
				return nil, ErrParseFailed
			}
			thread = entityPost
		}
		if wanted != entity && wanted != activity && wanted != thread {
			continue
		}
		for _, node := range nodes {
			if hasErrorEnvelope(node) {
				return nil, ErrParseFailed
			}
		}
		if _, err := canonicalPostReference(activity); err != nil || !strings.HasPrefix(activity, "urn:li:activity:") || thread == "" {
			continue
		}
		if found != nil && (found.PostURN != thread || found.ActivityURN != activity) {
			return nil, ErrParseFailed
		}
		found = &CommentTarget{PostURN: thread, ActivityURN: activity}
	}
	if found == nil {
		return nil, ErrNotFound
	}
	return found, nil
}

// CreateComment uses the captured NormComments protocol. Top-level and nested
// destinations differ in threadUrn; no alternate POST path is attempted.
func (c *Client) CreateComment(ctx context.Context, p CreateCommentParams) (*PostComment, error) {
	if err := validateOutreachText(p.Text, CommentLimit); err != nil {
		return nil, err
	}
	me, err := c.requireSender(ctx, p.ExpectedSenderURN)
	if err != nil {
		return nil, err
	}
	target, err := c.ResolveCommentTarget(ctx, p.PostURN, p.ParentCommentURN)
	if err != nil {
		return nil, err
	}
	if p.ActivityURN != "" && p.ActivityURN != target.ActivityURN {
		return nil, ErrInvalidParams
	}
	thread := target.ActivityURN
	if target.ParentCommentURN != "" {
		parentID := strings.TrimSuffix(strings.Split(target.ParentCommentURN, ",")[1], ")")
		thread = "urn:li:comment:(" + strings.TrimPrefix(target.ActivityURN, "urn:li:") + "," + parentID + ")"
	}
	payload, _ := json.Marshal(map[string]any{
		"commentary": map[string]any{"text": p.Text, "attributesV2": []any{}, "$type": "com.linkedin.voyager.dash.common.text.TextViewModel"},
		"threadUrn":  thread,
	})
	body, headers, err := c.makeWriteRequest(ctx, apiBase+"/voyagerSocialDashNormComments?decorationId=com.linkedin.voyager.dash.deco.social.NormComment-43", payload)
	if err != nil {
		return nil, err
	}
	urn, err := commentReceipt(body, headers.Get("X-RestLi-Id"), headers.Get("Location"), target, p.Text, me.URN)
	if err != nil {
		return nil, err
	}
	return &PostComment{URN: urn, PostURN: target.PostURN, ParentURN: target.ParentCommentURN, Text: p.Text}, nil
}

// NormComment's service identity prefixes the underlying dash comment URN in
// first-party display code. Only one such prefix is accepted at the receipt
// boundary; public target parsing and arbitrary service IDs remain unchanged.
func commentCreationURN(raw string) (string, error) {
	if inner, ok := strings.CutPrefix(raw, "urn:li:fsd_normComment:"); ok && strings.HasPrefix(inner, "urn:li:") {
		return CanonicalCommentURN(inner)
	}
	return CanonicalCommentURN(raw)
}

func unknownCommentReceipt(detail string) error {
	return &WriteError{Cause: ErrWriteUnknown, unknownReason: writeUnknownCommentReceipt, unknownDetail: detail}
}

func hasUnsupportedCommentDetail(body []byte, selected map[string]any) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return false // The creation-envelope check handles malformed JSON.
	}
	data, _ := root["data"].(map[string]any)
	rootValue, _ := root["value"].(map[string]any)
	dataValue, _ := data["value"].(map[string]any)
	for _, boundary := range []map[string]any{root, data, rootValue, dataValue, selected} {
		for _, key := range []string{"singleComment", "*singleComment"} {
			if _, exists := boundary[key]; exists {
				return true
			}
		}
	}
	return false
}

func commentReceipt(body []byte, restID, location string, target *CommentTarget, text, sender string) (string, error) {
	found := ""
	accept := func(candidate string) string {
		canonical, err := commentCreationURN(candidate)
		if err != nil {
			return commentReceiptIDInvalid
		}
		post, _ := CommentPostURN(canonical)
		if post != target.PostURN && post != target.ActivityURN {
			return commentReceiptPostMismatch
		}
		if target.ParentCommentURN != "" && sameComment(canonical, target.ParentCommentURN, target) {
			return commentReceiptExistingParent
		}
		if found != "" && !sameComment(found, canonical, target) {
			return commentReceiptAliasMismatch
		}
		found = canonical
		return ""
	}
	if len(body) != 0 {
		value, err := createdValue(body)
		sparse := sparseCommentAcknowledgment(body)
		if err != nil && !sparse {
			return "", unknownReceiptReason(writeUnknownCreationValue)
		}
		if observed, exists := value["commentary"]; exists && valueText(observed) != text {
			return "", unknownReceiptReason(writeUnknownRequestMismatch)
		}
		if observed, exists := value["text"]; exists && valueText(observed) != text {
			return "", unknownReceiptReason(writeUnknownRequestMismatch)
		}
		if !commentAuthorMatches(value, sender) {
			return "", unknownReceiptReason(writeUnknownIdentityMismatch)
		}
		if observed, exists := value["parentCommentUrn"]; exists {
			parent, ok := observed.(string)
			if !ok {
				return "", unknownReceiptReason(writeUnknownRequestMismatch)
			}
			if target.ParentCommentURN == "" && parent != "" {
				return "", unknownReceiptReason(writeUnknownRequestMismatch)
			}
			if target.ParentCommentURN != "" {
				canonical, err := CanonicalCommentURN(parent)
				if err != nil || !sameComment(canonical, target.ParentCommentURN, target) {
					return "", unknownReceiptReason(writeUnknownRequestMismatch)
				}
			}
		}
		// First-party JS consumes a store-normalized singleComment collection,
		// but its wire reference/collection shape has not been captured. Do not
		// ignore explicit child evidence or invent a parser for that envelope.
		if hasUnsupportedCommentDetail(body, value) {
			return "", unknownCommentReceipt(commentReceiptDetailUnsupported)
		}
		for _, key := range []string{"entityUrn", "commentUrn", "urn"} {
			if observed, exists := value[key]; exists {
				candidate, ok := observed.(string)
				if !ok {
					return "", unknownCommentReceipt(commentReceiptIDType)
				}
				if detail := accept(candidate); detail != "" {
					return "", unknownCommentReceipt(detail)
				}
			}
		}
		if !sparse && found == "" {
			return "", unknownCommentReceipt(commentReceiptIDMissing)
		}
	}
	if restID != "" {
		candidate, err := commentURNHeader(restID)
		if err != nil || accept(candidate) != "" {
			return "", unknownReceiptReason(writeUnknownCommentHeader)
		}
	}
	if location != "" {
		candidate, err := commentLocation(location, target)
		if err != nil || accept(candidate) != "" {
			return "", unknownReceiptReason(writeUnknownCommentHeader)
		}
	}
	if found == "" {
		return "", unknownCommentReceipt(commentReceiptIDMissing)
	}
	return found, nil
}

// Only these empty success envelopes may defer entirely to creation headers.
// Unsupported wrappers, included-only records, errors and explicit references
// remain failures rather than being ignored when a header is also present.
func sparseCommentAcknowledgment(body []byte) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || root == nil {
		return false
	}
	if len(root) == 0 {
		return true
	}
	data, ok := root["data"].(map[string]any)
	return len(root) == 1 && ok && len(data) == 0
}

func commentURNHeader(raw string) (string, error) {
	for range 2 {
		decoded, err := url.PathUnescape(raw)
		if err != nil {
			return "", ErrInvalidParams
		}
		if decoded == raw {
			break
		}
		raw = decoded
	}
	return CanonicalCommentURN(raw)
}

// Location is either a full comment identity or the exact resource route from
// the tracked S'more fixture. Query is URI metadata, never part of the URN.
func commentLocation(raw string, target *CommentTarget) (string, error) {
	if candidate, err := commentURNHeader(raw); err == nil {
		return candidate, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "www.linkedin.com" ||
		u.User != nil || u.Port() != "" || u.Fragment != "" {
		return "", ErrInvalidParams
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 7 || parts[0] != "" || parts[1] != "voyager" || parts[2] != "api" ||
		parts[3] != "socialActions" || parts[5] != "comments" || !numericID(parts[6]) {
		return "", ErrInvalidParams
	}
	post, err := canonicalPostReference(parts[4])
	if err != nil || (post != target.PostURN && post != target.ActivityURN) {
		return "", ErrInvalidParams
	}
	return CanonicalCommentURN("urn:li:comment:(" + post + "," + parts[6] + ")")
}

func commentAuthorMatches(value map[string]any, sender string) bool {
	for _, key := range []string{"actorUrn", "authorUrn", "*author", "*actor", "*commenter"} {
		if observed, exists := value[key]; exists {
			raw, ok := observed.(string)
			canonical, err := CanonicalMemberURN(raw)
			if !ok || err != nil || canonical != sender {
				return false
			}
		}
	}
	for _, key := range []string{"author", "actor", "commenter"} {
		observed, exists := value[key]
		if !exists {
			continue
		}
		if raw, ok := observed.(string); ok {
			canonical, err := CanonicalMemberURN(raw)
			if err != nil || canonical != sender {
				return false
			}
			continue
		}
		obj, ok := observed.(map[string]any)
		if !ok || hasErrorEnvelope(obj) {
			return false
		}
		nodes := []map[string]any{obj}
		for _, childKey := range []string{"actor", "actorUnion"} {
			if child, exists := obj[childKey]; exists {
				nested, ok := child.(map[string]any)
				if !ok || hasErrorEnvelope(nested) {
					return false
				}
				nodes = append(nodes, nested)
			}
		}
		proven := false
		for _, node := range nodes {
			if _, company := node["companyUrn"]; company {
				return false
			}
			for _, memberKey := range []string{"entityUrn", "urn", "profileUrn", "*profileUrn", "*miniProfile", "commenterProfileId"} {
				if observed, exists := node[memberKey]; exists {
					raw, ok := observed.(string)
					if memberKey == "commenterProfileId" {
						raw = "urn:li:fsd_profile:" + raw
					}
					canonical, err := CanonicalMemberURN(raw)
					if !ok || err != nil || canonical != sender {
						return false
					}
					proven = true
				}
			}
		}
		if !proven {
			return false
		}
	}
	return true
}
