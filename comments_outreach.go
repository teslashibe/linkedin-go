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
	// Existing provider read path is bounded. Absence never authorizes a fallback
	// top-level comment; it is a safe failure until a parent can be verified.
	items, err := c.GetPostComments(ctx, PostCommentParams{PostURN: target.PostURN, Count: 50})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		observed, err := CanonicalCommentURN(item.URN)
		if err != nil {
			continue
		}
		if sameComment(observed, canonical, target) {
			itemPost, err := canonicalPostReference(item.PostURN)
			if err != nil || (itemPost != target.PostURN && itemPost != target.ActivityURN) {
				return nil, ErrInvalidParams
			}
			target.ParentCommentURN = canonical
			return target, nil
		}
	}
	return nil, ErrNotFound
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

func commentReceipt(body []byte, restID, location string, target *CommentTarget, text, sender string) (string, error) {
	found := ""
	accept := func(candidate string) bool {
		canonical, err := CanonicalCommentURN(candidate)
		if err != nil {
			return false
		}
		post, _ := CommentPostURN(canonical)
		if post != target.PostURN && post != target.ActivityURN {
			return false
		}
		if target.ParentCommentURN != "" && sameComment(canonical, target.ParentCommentURN, target) {
			return false
		}
		if found != "" && !sameComment(found, canonical, target) {
			return false
		}
		found = canonical
		return true
	}
	if len(body) != 0 {
		value, err := createdValue(body)
		if err != nil {
			return "", err
		}
		if observed, exists := value["commentary"]; exists && valueText(observed) != text {
			return "", unknownReceipt()
		}
		if observed, exists := value["text"]; exists && valueText(observed) != text {
			return "", unknownReceipt()
		}
		for _, key := range []string{"actorUrn", "authorUrn", "*author", "*actor"} {
			if observed, exists := value[key]; exists {
				raw, ok := observed.(string)
				canonical, err := CanonicalMemberURN(raw)
				if !ok || err != nil || canonical != sender {
					return "", unknownReceipt()
				}
			}
		}
		if observed, exists := value["parentCommentUrn"]; exists {
			parent, ok := observed.(string)
			if !ok {
				return "", unknownReceipt()
			}
			if target.ParentCommentURN == "" && parent != "" {
				return "", unknownReceipt()
			}
			if target.ParentCommentURN != "" {
				canonical, err := CanonicalCommentURN(parent)
				if err != nil || !sameComment(canonical, target.ParentCommentURN, target) {
					return "", unknownReceipt()
				}
			}
		}
		for _, key := range []string{"entityUrn", "commentUrn", "urn"} {
			if observed, exists := value[key]; exists {
				candidate, ok := observed.(string)
				if !ok || !accept(candidate) {
					return "", unknownReceipt()
				}
			}
		}
	}
	for _, header := range []string{restID, location} {
		if header == "" {
			continue
		}
		for range 2 {
			decoded, err := url.PathUnescape(header)
			if err != nil {
				return "", unknownReceipt()
			}
			if decoded == header {
				break
			}
			header = decoded
		}
		if index := strings.Index(header, "urn:li:"); index >= 0 {
			header = header[index:]
		}
		if !accept(header) {
			return "", unknownReceipt()
		}
	}
	if found == "" {
		return "", unknownReceipt()
	}
	return found, nil
}
