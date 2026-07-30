package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GetPost returns a normalized post for a post/activity URN or LinkedIn post URL.
func (c *Client) GetPost(ctx context.Context, identifier string) (*Post, error) {
	urn, err := normalizePostURN(identifier)
	if err != nil {
		return nil, err
	}
	body, err := c.makeRequest(ctx, apiBase+"/feed/updates/"+url.PathEscape(urn))
	if err != nil {
		return nil, err
	}
	posts, err := parsePosts(body, urn)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, ErrNotFound
	}
	return &posts[0], nil
}

// GetPostComments returns one bounded offset page of comments for a post.
func (c *Client) GetPostComments(ctx context.Context, p PostCommentParams) ([]PostComment, error) {
	urn, err := normalizePostURN(p.PostURN)
	if err != nil {
		return nil, err
	}
	if p.Start < 0 {
		return nil, fmt.Errorf("%w: start must be at least 0", ErrInvalidParams)
	}
	count := p.Count
	if count <= 0 {
		count = 20
	}
	if count > 50 {
		return nil, fmt.Errorf("%w: count must be at most 50", ErrInvalidParams)
	}
	reqURL := fmt.Sprintf("%s/socialActions/%s/comments?start=%d&count=%d", apiBase, url.PathEscape(urn), p.Start, count)
	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	return parseComments(body, urn)
}

// GetUserPosts returns recent public posts for a member/profile URN or vanity name.
func (c *Client) GetUserPosts(ctx context.Context, p UserPostParams) ([]Post, error) {
	member := strings.TrimSpace(p.Member)
	if member == "" {
		return nil, fmt.Errorf("%w: member URN or vanity name required", ErrInvalidParams)
	}
	if p.Start < 0 {
		return nil, fmt.Errorf("%w: start must be at least 0", ErrInvalidParams)
	}
	count := p.Count
	if count <= 0 {
		count = 5
	}
	if count > 20 {
		return nil, fmt.Errorf("%w: count must be at most 20", ErrInvalidParams)
	}

	hops := 1
	if !strings.HasPrefix(member, "urn:li:") {
		vanity := normalizeVanityName(member)
		if vanity == "" {
			return nil, fmt.Errorf("%w: member URN or vanity name required", ErrInvalidParams)
		}
		if _, ok := c.vanityURN.Load(strings.ToLower(vanity)); !ok {
			hops = 2
		}
	}
	if err := c.requireDeadlineBudget(ctx, hops); err != nil {
		return nil, err
	}

	resolved, err := c.resolveMemberURN(ctx, member)
	if err != nil {
		return nil, err
	}
	reqURL := fmt.Sprintf("%s/identity/profileUpdatesV2?profileUrn=%s&q=memberShareFeed&start=%d&count=%d", apiBase, url.QueryEscape(resolved), p.Start, count)
	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}
	return parsePosts(body, "")
}

// resolveMemberURN returns a member/profile URN, using a session vanity cache
// when the caller passes a vanity name or profile URL.
func (c *Client) resolveMemberURN(ctx context.Context, member string) (string, error) {
	member = strings.TrimSpace(member)
	if strings.HasPrefix(member, "urn:li:") {
		return member, nil
	}
	vanity := normalizeVanityName(member)
	if vanity == "" {
		return "", fmt.Errorf("%w: member URN or vanity name required", ErrInvalidParams)
	}
	key := strings.ToLower(vanity)
	if cached, ok := c.vanityURN.Load(key); ok {
		if urn, _ := cached.(string); urn != "" {
			return urn, nil
		}
	}
	profile, err := c.GetProfile(ctx, vanity)
	if err != nil {
		return "", err
	}
	if profile == nil || strings.TrimSpace(profile.URN) == "" {
		return "", ErrNotFound
	}
	c.vanityURN.Store(key, profile.URN)
	return profile.URN, nil
}

func normalizeVanityName(member string) string {
	vanity := extractVanityName(member)
	if vanity == "" {
		vanity = strings.Trim(member, "/")
	}
	return strings.TrimSpace(vanity)
}

const defaultHTTPBudget = 8 * time.Second

// requireDeadlineBudget fails fast with ErrTimeout when the remaining context
// deadline cannot cover warm-up + paced Voyager hops. This avoids burning the
// caller's budget on multi-second human pacing before the real fetch starts.
func (c *Client) requireDeadlineBudget(ctx context.Context, voyagerHops int) error {
	if voyagerHops < 1 {
		voyagerHops = 1
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("%w: context deadline already exceeded", ErrTimeout)
	}
	hops := voyagerHops
	if !c.warmedUp.Load() {
		hops++
	}
	gap := c.pacingGapCeiling()
	need := time.Duration(hops) * (gap + defaultHTTPBudget)
	if remaining < need {
		return fmt.Errorf("%w: insufficient deadline budget (%s remaining, need %s for %d paced hops)",
			ErrTimeout, remaining.Round(time.Millisecond), need.Round(time.Millisecond), hops)
	}
	return nil
}

func (c *Client) pacingGapCeiling() time.Duration {
	if c.pacer != nil && c.pacer.policy.BaseGap > 0 {
		high := c.pacer.policy.JitterHigh
		if high < 1 {
			high = 1
		}
		return time.Duration(float64(c.pacer.policy.BaseGap) * high)
	}
	if c.minGap > 0 {
		return c.minGap
	}
	return defaultMinGap
}

func normalizePostURN(identifier string) (string, error) {
	s := strings.TrimSpace(identifier)
	if s == "" {
		return "", fmt.Errorf("%w: post URN or URL required", ErrInvalidParams)
	}
	if strings.HasPrefix(s, "urn:li:") {
		return strings.TrimRight(s, "/"), nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || !strings.HasSuffix(strings.ToLower(u.Hostname()), "linkedin.com") {
		return "", fmt.Errorf("%w: expected a LinkedIn post URN or URL", ErrInvalidParams)
	}
	decoded, _ := url.PathUnescape(u.Path)
	if i := strings.Index(decoded, "urn:li:"); i >= 0 {
		return strings.TrimRight(decoded[i:], "/"), nil
	}
	// Public /posts/...-activity-1234567890-* URLs carry only the activity ID.
	if i := strings.LastIndex(decoded, "-activity-"); i >= 0 {
		tail := decoded[i+len("-activity-"):]
		if j := strings.IndexByte(tail, '-'); j >= 0 {
			tail = tail[:j]
		}
		tail = strings.Trim(tail, "/")
		if _, err := strconv.ParseUint(tail, 10, 64); err == nil {
			return "urn:li:activity:" + tail, nil
		}
	}
	return "", fmt.Errorf("%w: URL does not contain a post activity identifier", ErrInvalidParams)
}

func parsePosts(body []byte, wantedURN string) ([]Post, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("%w: post response: %v", ErrParseFailed, err)
	}
	entities := collectObjects(root)
	byURN := indexObjectsByURN(entities)
	posts := make([]Post, 0)
	seen := map[string]bool{}
	for _, obj := range entities {
		urn := firstString(obj, "entityUrn", "urn", "updateUrn", "activityUrn")
		if !looksLikePost(obj, urn) {
			continue
		}
		activityURN := firstStringDeep(obj, "activityUrn", "updateUrn")
		if embedded := embeddedActivityURN(urn); embedded != "" {
			activityURN = embedded
		}
		if activityURN == "" && strings.Contains(urn, ":activity:") {
			activityURN = urn
		}
		if wantedURN != "" && urn != wantedURN && activityURN != wantedURN && !samePostID(urn, wantedURN) && !samePostID(activityURN, wantedURN) {
			continue
		}
		if urn == "" {
			urn = activityURN
		}
		if urn == "" || seen[urn] {
			continue
		}
		seen[urn] = true
		raw, _ := json.Marshal(obj)
		post := Post{
			URN: urn, ActivityURN: activityURN,
			Text:      firstTextDeep(obj, "commentary", "text", "comment", "description"),
			CreatedAt: firstInt64Deep(obj, "createdAt", "publishedAt", "timestamp", "lastModifiedAt"),
			URL:       firstStringDeep(obj, "permalink", "url", "navigationUrl"), Raw: raw,
		}
		post.Author = parseAuthor(obj, byURN)
		post.Engagement = parseEngagement(obj)
		if post.URL == "" && post.ActivityURN != "" {
			post.URL = "https://www.linkedin.com/feed/update/" + post.ActivityURN
		}
		posts = append(posts, post)
	}
	return posts, nil
}

func parseComments(body []byte, postURN string) ([]PostComment, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("%w: comments response: %v", ErrParseFailed, err)
	}
	entities := collectObjects(root)
	byURN := indexObjectsByURN(entities)
	comments := make([]PostComment, 0)
	seen := map[string]bool{}
	for _, obj := range entities {
		urn := firstString(obj, "entityUrn", "urn", "commentUrn")
		typ := strings.ToLower(firstString(obj, "$type"))
		if !strings.Contains(typ, "comment") && !strings.Contains(strings.ToLower(urn), "comment") {
			continue
		}
		if urn == "" || seen[urn] {
			continue
		}
		text := firstTextDeep(obj, "comment", "message", "text", "commentary")
		if text == "" {
			continue
		}
		seen[urn] = true
		comments = append(comments, PostComment{
			URN: urn, PostURN: postURN,
			ParentURN: firstStringDeep(obj, "parentCommentUrn", "parentUrn", "threadUrn"),
			Text:      text, Author: parseAuthor(obj, byURN),
			CreatedAt: firstInt64Deep(obj, "createdAt", "publishedAt", "timestamp"),
			LikeCount: firstIntDeep(obj, "likeCount", "numLikes", "totalLikes"),
		})
	}
	return comments, nil
}

func collectObjects(v any) []map[string]any {
	out := make([]map[string]any, 0)
	var walk func(any)
	walk = func(cur any) {
		switch x := cur.(type) {
		case map[string]any:
			out = append(out, x)
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(v)
	return out
}

func indexObjectsByURN(objects []map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any)
	for _, obj := range objects {
		if urn := firstString(obj, "entityUrn", "urn"); urn != "" {
			out[urn] = obj
		}
	}
	return out
}

func looksLikePost(obj map[string]any, urn string) bool {
	typ := strings.ToLower(firstString(obj, "$type"))
	u := strings.ToLower(urn)
	if strings.Contains(typ, "comment") || strings.Contains(u, "comment") {
		return false
	}
	return strings.Contains(typ, "update") || strings.Contains(typ, "share") || strings.Contains(typ, "ugcpost") ||
		strings.Contains(u, ":activity:") || strings.Contains(u, ":share:") || strings.Contains(u, ":ugcpost:")
}

func samePostID(a, b string) bool {
	aID, bID := postNumericID(a), postNumericID(b)
	return aID != "" && aID == bID
}

func postNumericID(s string) string {
	if i := strings.Index(s, ":activity:"); i >= 0 {
		s = s[i+len(":activity:"):]
	}
	start := -1
	for i, r := range s {
		if r >= '0' && r <= '9' {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			return s[start:i]
		}
	}
	if start >= 0 {
		return s[start:]
	}
	return ""
}

func embeddedActivityURN(s string) string {
	const marker = "urn:li:activity:"
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	tail := s[i+len(marker):]
	end := 0
	for end < len(tail) && tail[end] >= '0' && tail[end] <= '9' {
		end++
	}
	if end == 0 {
		return ""
	}
	return marker + tail[:end]
}

func parseAuthor(obj map[string]any, byURN map[string]map[string]any) PostAuthor {
	authorURN := firstString(obj, "actorUrn", "authorUrn", "author", "actor", "*actor", "*author")
	authorObj := obj
	for _, key := range []string{"actor", "author", "miniProfile", "member", "company"} {
		if nested, ok := obj[key].(map[string]any); ok {
			authorObj = nested
			if authorURN == "" {
				authorURN = firstString(nested, "entityUrn", "urn", "profileUrn")
			}
			break
		}
	}
	if linked := byURN[authorURN]; linked != nil {
		authorObj = linked
	}
	name := firstString(authorObj, "name", "formattedName")
	first, last := firstString(authorObj, "firstName"), firstString(authorObj, "lastName")
	if name == "" {
		name = strings.TrimSpace(first + " " + last)
	}
	publicID := firstString(authorObj, "publicIdentifier", "publicId", "vanityName")
	profileURL := firstString(authorObj, "profileUrl", "navigationUrl")
	if profileURL == "" && publicID != "" {
		profileURL = "https://www.linkedin.com/in/" + publicID
	}
	return PostAuthor{URN: authorURN, PublicID: publicID, Name: name, Headline: firstString(authorObj, "headline", "occupation"), ProfileURL: profileURL}
}

func parseEngagement(obj map[string]any) PostEngagement {
	return PostEngagement{
		LikeCount:    firstIntDeep(obj, "likeCount", "numLikes", "reactionCount", "totalReactionCount"),
		CommentCount: firstIntDeep(obj, "commentCount", "numComments", "totalFirstLevelComments"),
		ShareCount:   firstIntDeep(obj, "shareCount", "numShares", "repostCount"),
		ViewCount:    firstIntDeep(obj, "viewCount", "impressionCount"),
	}
}

func firstTextDeep(obj map[string]any, keys ...string) string {
	if s := firstText(obj, keys...); s != "" {
		return s
	}
	for _, value := range obj {
		if nested, ok := value.(map[string]any); ok {
			if s := firstTextDeep(nested, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

func firstStringDeep(obj map[string]any, keys ...string) string {
	if s := firstString(obj, keys...); s != "" {
		return s
	}
	for _, value := range obj {
		if nested, ok := value.(map[string]any); ok {
			if s := firstStringDeep(nested, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

func firstText(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := obj[key]; ok {
			switch x := v.(type) {
			case string:
				if x != "" {
					return x
				}
			case map[string]any:
				if s := firstString(x, "text", "body", "value"); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func firstString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := obj[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func firstInt(obj map[string]any, keys ...string) int { return int(firstInt64(obj, keys...)) }

func firstIntDeep(obj map[string]any, keys ...string) int { return int(firstInt64Deep(obj, keys...)) }

func firstInt64Deep(obj map[string]any, keys ...string) int64 {
	if n := firstInt64(obj, keys...); n != 0 {
		return n
	}
	for _, value := range obj {
		if nested, ok := value.(map[string]any); ok {
			if n := firstInt64Deep(nested, keys...); n != 0 {
				return n
			}
		}
	}
	return 0
}

func firstInt64(obj map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch v := obj[key].(type) {
		case float64:
			return int64(v)
		case json.Number:
			n, _ := v.Int64()
			return n
		case string:
			n, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				return n
			}
		case map[string]any:
			if n := firstInt64(v, "count", "total", "value"); n != 0 {
				return n
			}
		}
	}
	return 0
}
