package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// ResolveLocations searches LinkedIn's typeahead for geographic locations
// matching the query and returns their URNs. Use the URNs with SearchParams.GeoURN.
func (c *Client) ResolveLocations(ctx context.Context, query string) ([]GeoResult, error) {
	results, err := c.resolveTypeahead(ctx, query, "GEO")
	if err != nil {
		return nil, err
	}
	out := make([]GeoResult, len(results))
	for i, r := range results {
		out[i] = GeoResult{URN: r.urn, Name: r.name}
	}
	return out, nil
}

// ResolveCompanies searches LinkedIn's typeahead for companies matching the
// query and returns their URNs. Use the URNs with SearchParams.CurrentCompany
// or SearchParams.PastCompany.
func (c *Client) ResolveCompanies(ctx context.Context, query string) ([]CompanyResult, error) {
	results, err := c.resolveTypeahead(ctx, query, "COMPANY")
	if err != nil {
		return nil, err
	}
	out := make([]CompanyResult, len(results))
	for i, r := range results {
		out[i] = CompanyResult{URN: r.urn, Name: r.name}
	}
	return out, nil
}

// ResolveSchools searches LinkedIn's typeahead for schools matching the query
// and returns their URNs. Use the URNs with SearchParams.School.
func (c *Client) ResolveSchools(ctx context.Context, query string) ([]SchoolResult, error) {
	results, err := c.resolveTypeahead(ctx, query, "SCHOOL")
	if err != nil {
		return nil, err
	}
	out := make([]SchoolResult, len(results))
	for i, r := range results {
		out[i] = SchoolResult{URN: r.urn, Name: r.name}
	}
	return out, nil
}

type typeaheadHit struct {
	urn  string
	name string
}

func (c *Client) resolveTypeahead(ctx context.Context, query, taType string) ([]typeaheadHit, error) {
	if query == "" {
		return nil, fmt.Errorf("%w: query required", ErrInvalidParams)
	}

	reqURL := fmt.Sprintf("%s/typeahead/hitsV2?keywords=%s&origin=GLOBAL_SEARCH_HEADER&q=type&type=%s",
		apiBase, url.QueryEscape(query), taType)

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	// REST API format (elements-based)
	var restResp typeaheadRestResponse
	if err := json.Unmarshal(body, &restResp); err == nil && len(restResp.Elements) > 0 {
		hits := make([]typeaheadHit, 0, len(restResp.Elements))
		for _, elem := range restResp.Elements {
			urn := elem.TargetURN
			if urn == "" {
				continue
			}
			name := ""
			if elem.Text != nil {
				name = elem.Text.Text
			}
			hits = append(hits, typeaheadHit{urn: urn, name: name})
		}
		if len(hits) > 0 {
			return hits, nil
		}
	}

	// GraphQL format (included-based)
	var inclResp typeaheadResponse
	if err := json.Unmarshal(body, &inclResp); err == nil && len(inclResp.Included) > 0 {
		hits := make([]typeaheadHit, 0, len(inclResp.Included))
		for _, ent := range inclResp.Included {
			if ent.EntityURN == "" {
				continue
			}
			name := ent.Name
			if name == "" && ent.Title != nil {
				name = ent.Title.Text
			}
			hits = append(hits, typeaheadHit{urn: ent.EntityURN, name: name})
		}
		if len(hits) > 0 {
			return hits, nil
		}
	}

	// Both parse attempts yielded no usable hits — check if the body
	// was valid JSON at all before reporting "no results".
	var probe json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("%w: typeahead response: %v", ErrParseFailed, err)
	}
	return nil, nil
}

// Typeahead response types (unexported).

type typeaheadRestResponse struct {
	Elements []typeaheadRestElement `json:"elements"`
}

type typeaheadRestElement struct {
	TargetURN string `json:"targetUrn,omitempty"`
	Text      *struct {
		Text string `json:"text"`
	} `json:"text,omitempty"`
}

type typeaheadResponse struct {
	Included []typeaheadEntity `json:"included"`
}

type typeaheadEntity struct {
	Type      string `json:"$type"`
	EntityURN string `json:"entityUrn,omitempty"`
	Title     *struct {
		Text string `json:"text"`
	} `json:"title,omitempty"`
	Name string `json:"name,omitempty"`
}
