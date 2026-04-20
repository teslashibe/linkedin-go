package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// SearchGroups searches for LinkedIn groups matching the given keywords.
// Uses the same GraphQL search infrastructure as SearchPeople.
func (c *Client) SearchGroups(ctx context.Context, p GroupSearchParams) ([]Group, error) {
	count := p.Count
	if count <= 0 {
		count = 10
	}
	if count > 49 {
		count = 49
	}

	filters := []string{filterEntry("resultType", []string{"GROUPS"})}

	var queryParts []string
	if p.Keywords != "" {
		queryParts = append(queryParts, fmt.Sprintf("keywords:%s", url.QueryEscape(p.Keywords)))
	}
	queryParts = append(queryParts, "flagshipSearchIntent:SEARCH_SRP")
	queryParts = append(queryParts, fmt.Sprintf("queryParameters:List(%s)", strings.Join(filters, ",")))
	queryParts = append(queryParts, "includeFiltersInResponse:false")

	variables := fmt.Sprintf("(start:%d,count:%d,origin:GLOBAL_SEARCH_HEADER,query:(%s))",
		p.Start, count, strings.Join(queryParts, ","))

	reqURL := fmt.Sprintf("%s/graphql?variables=%s&queryId=%s",
		apiBase, variables, c.searchQueryID)

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp searchAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	entityIndex := make(map[string]*includedEntity, len(resp.Included))
	for i := range resp.Included {
		entityIndex[resp.Included[i].EntityURN] = &resp.Included[i]
	}

	var resultURNs []string
	for _, cluster := range resp.Data.Data.SearchDashClustersByAll.Elements {
		for _, item := range cluster.Items {
			if item.EntityResultURN != "" {
				resultURNs = append(resultURNs, item.EntityResultURN)
			}
		}
	}

	groups := make([]Group, 0, len(resultURNs))
	for _, urn := range resultURNs {
		ent, ok := entityIndex[urn]
		if !ok || ent.Type != typeEntityResult {
			continue
		}
		g := Group{URN: ent.TrackingURN}
		if ent.Title != nil {
			g.Name = string(*ent.Title)
		}
		if ent.PrimarySubtitle != nil {
			g.Description = string(*ent.PrimarySubtitle)
		}
		if ent.NavigationURL != "" {
			g.GroupURL = ent.NavigationURL
			g.ID = extractGroupID(ent.NavigationURL)
		}
		groups = append(groups, g)
	}

	return groups, nil
}

// GetGroup retrieves full group metadata by group ID.
func (c *Client) GetGroup(ctx context.Context, groupID string) (*Group, error) {
	if groupID == "" {
		return nil, fmt.Errorf("%w: group ID required", ErrInvalidParams)
	}
	reqURL := fmt.Sprintf("%s/groups/groups/%s", apiBase, groupID)

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	g := &Group{ID: groupID}
	if urn, ok := raw["entityUrn"].(string); ok {
		g.URN = urn
	}
	if name, ok := raw["name"].(string); ok {
		g.Name = name
	}
	if desc, ok := raw["description"].(string); ok {
		g.Description = desc
	}
	if count, ok := raw["memberCount"].(float64); ok {
		g.MemberCount = int(count)
	}
	if rules, ok := raw["rules"].(string); ok {
		g.Rules = rules
	}
	if vis, ok := raw["visibility"].(string); ok {
		g.IsPrivate = vis == "PRIVATE"
	}
	g.GroupURL = fmt.Sprintf("https://www.linkedin.com/groups/%s", groupID)

	return g, nil
}

// GetGroupPosts returns paginated posts from a group's feed.
func (c *Client) GetGroupPosts(ctx context.Context, p GroupPostParams) ([]GroupPost, error) {
	if p.GroupID == "" {
		return nil, fmt.Errorf("%w: group ID required", ErrInvalidParams)
	}
	count := p.Count
	if count <= 0 {
		count = 10
	}
	sortBy := p.SortBy
	if sortBy == "" {
		sortBy = "RECENT"
	}

	reqURL := fmt.Sprintf("%s/groups/groups/%s/posts?q=group&start=%d&count=%d&sortBy=%s",
		apiBase, p.GroupID, p.Start, count, sortBy)

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	posts := make([]GroupPost, 0, len(resp.Elements))
	for _, raw := range resp.Elements {
		var elem map[string]interface{}
		if err := json.Unmarshal(raw, &elem); err != nil {
			continue
		}
		post := GroupPost{}
		if urn, ok := elem["entityUrn"].(string); ok {
			post.URN = urn
		}
		if author, ok := elem["author"].(string); ok {
			post.AuthorURN = author
		}
		if commentary, ok := elem["commentary"].(map[string]interface{}); ok {
			if text, ok := commentary["text"].(string); ok {
				post.Text = text
			}
		}
		if text, ok := elem["text"].(string); ok && post.Text == "" {
			post.Text = text
		}
		if created, ok := elem["createdAt"].(float64); ok {
			post.CreatedAt = int64(created)
		}
		if likes, ok := elem["likeCount"].(float64); ok {
			post.LikeCount = int(likes)
		}
		if comments, ok := elem["commentCount"].(float64); ok {
			post.CommentCount = int(comments)
		}
		posts = append(posts, post)
	}

	return posts, nil
}

// GetGroupMembers returns paginated members of a group.
func (c *Client) GetGroupMembers(ctx context.Context, p GroupMemberParams) ([]GroupMember, error) {
	if p.GroupID == "" {
		return nil, fmt.Errorf("%w: group ID required", ErrInvalidParams)
	}
	count := p.Count
	if count <= 0 {
		count = 10
	}

	reqURL := fmt.Sprintf("%s/groups/groups/%s/members?q=group&start=%d&count=%d",
		apiBase, p.GroupID, p.Start, count)
	if p.Role != "" {
		reqURL += "&role=" + p.Role
	}

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	members := make([]GroupMember, 0, len(resp.Elements))
	for _, raw := range resp.Elements {
		var elem map[string]interface{}
		if err := json.Unmarshal(raw, &elem); err != nil {
			continue
		}
		m := GroupMember{}
		if urn, ok := elem["entityUrn"].(string); ok {
			m.URN = urn
		}
		if profile, ok := elem["*member"].(string); ok {
			m.ProfileURN = profile
		}
		if role, ok := elem["role"].(string); ok {
			m.Role = role
		}
		if mp, ok := elem["miniProfile"].(map[string]interface{}); ok {
			if fn, ok := mp["firstName"].(string); ok {
				m.FirstName = fn
			}
			if ln, ok := mp["lastName"].(string); ok {
				m.LastName = ln
			}
			if hl, ok := mp["occupation"].(string); ok {
				m.Headline = hl
			}
		}
		members = append(members, m)
	}

	return members, nil
}

// GetMyGroups returns all groups the authenticated user is a member of.
func (c *Client) GetMyGroups(ctx context.Context) ([]Group, error) {
	reqURL := fmt.Sprintf("%s/groups/memberships?q=member&start=0&count=50", apiBase)

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Elements []json.RawMessage `json:"elements"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	groups := make([]Group, 0, len(resp.Elements))
	for _, raw := range resp.Elements {
		var elem map[string]interface{}
		if err := json.Unmarshal(raw, &elem); err != nil {
			continue
		}
		g := Group{IsMember: true}
		if urn, ok := elem["*group"].(string); ok {
			g.URN = urn
			g.ID = extractURNIDFromGroups(urn)
		}
		if group, ok := elem["group"].(map[string]interface{}); ok {
			if name, ok := group["name"].(string); ok {
				g.Name = name
			}
			if count, ok := group["memberCount"].(float64); ok {
				g.MemberCount = int(count)
			}
		}
		if g.ID != "" {
			g.GroupURL = fmt.Sprintf("https://www.linkedin.com/groups/%s", g.ID)
		}
		groups = append(groups, g)
	}

	return groups, nil
}

// JoinGroup sends a membership request to join a group.
func (c *Client) JoinGroup(ctx context.Context, groupID string) error {
	if groupID == "" {
		return fmt.Errorf("%w: group ID required", ErrInvalidParams)
	}
	reqURL := fmt.Sprintf("%s/groups/groups/%s/members?action=join", apiBase, groupID)
	_, err := c.makePostRequest(ctx, reqURL, []byte("{}"))
	return err
}

// LeaveGroup removes the authenticated user from a group.
func (c *Client) LeaveGroup(ctx context.Context, groupID string) error {
	if groupID == "" {
		return fmt.Errorf("%w: group ID required", ErrInvalidParams)
	}
	reqURL := fmt.Sprintf("%s/groups/groups/%s/members?action=leave", apiBase, groupID)
	_, err := c.makePostRequest(ctx, reqURL, []byte("{}"))
	return err
}

// CreateGroupPost posts a plain-text message to a group. Requires membership.
func (c *Client) CreateGroupPost(ctx context.Context, p CreateGroupPostParams) error {
	if p.GroupID == "" {
		return fmt.Errorf("%w: group ID required", ErrInvalidParams)
	}
	if p.Text == "" {
		return ErrPostEmpty
	}

	reqURL := fmt.Sprintf("%s/groups/groups/%s/posts", apiBase, p.GroupID)
	payload := map[string]interface{}{
		"commentary": map[string]string{"text": p.Text},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	_, err = c.makePostRequest(ctx, reqURL, data)
	return err
}

func extractGroupID(navURL string) string {
	u, err := url.Parse(navURL)
	if err != nil {
		return ""
	}
	path := strings.TrimRight(u.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func extractURNIDFromGroups(urn string) string {
	parts := strings.Split(urn, ":")
	if len(parts) == 0 {
		return urn
	}
	return parts[len(parts)-1]
}
