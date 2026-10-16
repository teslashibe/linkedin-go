package linkedin

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// RecentConnectionsParams selects one page of the authenticated member's
// connections, newest first. Start is an offset, not a snapshot cursor.
type RecentConnectionsParams struct {
	Start int
	Count int // default 10, maximum 49
}

type RecentConnection struct {
	ConnectionURN string `json:"connectionUrn"`
	RecipientURN  string `json:"recipientUrn"`
	// ConnectedAt is the provider's createdAt in Unix milliseconds, or zero
	// when absent. It is not the time an invitation was sent or observed.
	ConnectedAt int64 `json:"connectedAt,omitempty"`
}

type RecentConnectionsPage struct {
	Connections []RecentConnection `json:"connections"`
	Start       int                `json:"start"`
	Count       int                `json:"count"`
	Total       int                `json:"total,omitempty"`
	TotalKnown  bool               `json:"totalKnown"`
	NextStart   int                `json:"nextStart"`
	// Complete describes this offset traversal only. Additions and removals
	// can shift pages; hosts must overlap stable connection identities and
	// revisit the head before committing coverage. Absence is not removal.
	Complete bool `json:"complete"`
}

// ListRecentConnections returns a single strict, read-only connection page.
// The host owns authorization, allowance, scheduling and durable overlap. No
// profile requests or per-recipient relationship requests are made here.
func (c *Client) ListRecentConnections(ctx context.Context, expectedSender string, p RecentConnectionsParams) (*RecentConnectionsPage, error) {
	if err := c.activitySender(expectedSender); err != nil {
		return nil, err
	}
	if p.Count == 0 {
		p.Count = 10
	}
	if p.Start < 0 || p.Count < 1 || p.Count > 49 || p.Start > 1_000_000 {
		return nil, ErrInvalidParams
	}
	q := url.Values{
		"decorationId": {"com.linkedin.voyager.dash.deco.web.mynetwork.ConnectionListWithProfile-16"},
		"q":            {"search"}, "sortType": {"RECENTLY_ADDED"},
		"start": {strconv.Itoa(p.Start)}, "count": {strconv.Itoa(p.Count)},
	}
	body, err := c.makeRequest(ctx, apiBase+"/relationships/dash/connections?"+q.Encode())
	if err != nil {
		return nil, err
	}
	return parseRecentConnections(body, expectedSender, p)
}

func parseRecentConnections(body []byte, sender string, p RecentConnectionsParams) (*RecentConnectionsPage, error) {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || activityError(root) || hasIdentityErrorEnvelope(root) {
		return nil, ErrParseFailed
	}
	data, ok := root["data"].(map[string]any)
	if !ok || activityError(data) || hasIdentityErrorEnvelope(data) {
		return nil, ErrParseFailed
	}
	var response struct {
		Data struct {
			Elements *[]string `json:"*elements"`
			Paging   *struct {
				Start *int `json:"start"`
				Count *int `json:"count"`
				Total *int `json:"total"`
			} `json:"paging"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || response.Data.Elements == nil || len(*response.Data.Elements) > p.Count {
		return nil, ErrParseFailed
	}
	page := &RecentConnectionsPage{Connections: []RecentConnection{}, Start: p.Start, Count: p.Count}
	if paging := response.Data.Paging; paging != nil && paging.Start != nil && paging.Count != nil {
		if *paging.Start != p.Start || *paging.Count < 1 || *paging.Count < len(*response.Data.Elements) || *paging.Count > p.Count {
			return nil, ErrParseFailed
		}
		page.Count = *paging.Count
		if paging.Total != nil {
			if *paging.Total < 0 {
				return nil, ErrParseFailed
			}
			page.Total, page.TotalKnown = *paging.Total, true
		}
	} else {
		return nil, ErrParseFailed
	}
	refs := *response.Data.Elements
	included, ok := root["included"].([]any)
	if !ok && len(refs) != 0 || len(included) > 10000 {
		return nil, ErrParseFailed
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref] || !validConnectionURN(ref) {
			return nil, ErrParseFailed
		}
		seen[ref] = true
		var selected map[string]any
		count := 0
		for _, entry := range included {
			obj, _ := entry.(map[string]any)
			if firstString(obj, "entityUrn") == ref {
				selected, count = obj, count+1
			}
		}
		if count != 1 || activityError(selected) || hasIdentityErrorEnvelope(selected) {
			return nil, ErrParseFailed
		}
		recipient, err := connectedMemberURN(selected)
		if err != nil || recipient == sender || !connectionBinds(ref, sender, recipient) {
			return nil, ErrIdentityUnavailable
		}
		var timestamps struct {
			CreatedAt *int64 `json:"createdAt"`
		}
		raw, _ := json.Marshal(selected)
		if json.Unmarshal(raw, &timestamps) != nil || timestamps.CreatedAt != nil && *timestamps.CreatedAt < 0 {
			return nil, ErrParseFailed
		}
		connection := RecentConnection{ConnectionURN: ref, RecipientURN: recipient}
		if timestamps.CreatedAt != nil {
			connection.ConnectedAt = *timestamps.CreatedAt
		}
		page.Connections = append(page.Connections, connection)
	}
	page.NextStart = p.Start + len(refs)
	if page.TotalKnown {
		// A moving head can change total between requests. An empty page before
		// the reported end has not established coverage and cannot advance.
		if len(refs) == 0 && p.Start < page.Total {
			return nil, ErrParseFailed
		}
		if len(refs) != 0 && page.NextStart > page.Total {
			return nil, ErrParseFailed
		}
		page.Complete = page.NextStart >= page.Total
	} else {
		page.Complete = len(refs) < page.Count
	}
	return page, nil
}

func connectionBinds(connection, sender, recipient string) bool {
	suffix := strings.TrimPrefix(connection, "urn:li:fsd_connection:")
	senderID := strings.TrimPrefix(sender, "urn:li:fsd_profile:")
	recipientID := strings.TrimPrefix(recipient, "urn:li:fsd_profile:")
	return suffix == recipientID || suffix == "("+senderID+","+recipientID+")" || suffix == "("+recipientID+","+senderID+")"
}
