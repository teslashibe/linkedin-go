package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ResolveLocations searches LinkedIn's typeahead for geographic locations
// matching the query and returns their URNs. Use the URNs with SearchParams.GeoURN.
//
// The URN field carries the bare LinkedIn entity ID (e.g. "90000084" for the
// SF Bay Area), not a fully qualified urn:li:geo:… string — that's the form
// the Voyager search filter accepts and silently ignores any URN-prefixed
// value, so we strip the prefix here.
func (c *Client) ResolveLocations(ctx context.Context, query string) ([]GeoResult, error) {
	results, err := c.resolveTypeahead(ctx, query, typeaheadGeo)
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
// or SearchParams.PastCompany. The URN field carries the bare company ID.
func (c *Client) ResolveCompanies(ctx context.Context, query string) ([]CompanyResult, error) {
	results, err := c.resolveTypeahead(ctx, query, typeaheadCompany)
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
// and returns their URNs. Use the URNs with SearchParams.School. The URN field
// carries the bare school ID.
func (c *Client) ResolveSchools(ctx context.Context, query string) ([]SchoolResult, error) {
	results, err := c.resolveTypeahead(ctx, query, typeaheadSchool)
	if err != nil {
		return nil, err
	}
	out := make([]SchoolResult, len(results))
	for i, r := range results {
		out[i] = SchoolResult{URN: r.urn, Name: r.name}
	}
	return out, nil
}

const (
	typeaheadGeo     = "GEO"
	typeaheadCompany = "COMPANY"
	typeaheadSchool  = "SCHOOL"

	publicTypeaheadPath  = baseURL + "/jobs-guest/api/typeaheadHits"
	voyagerTypeaheadPath = apiBase + "/graphql"
	// voyagerTypeaheadQueryID is the GraphQL queryId for the dash typeahead.
	// LinkedIn rotates this periodically; if it returns 5xx we fall through
	// to the public endpoint instead.
	voyagerTypeaheadQueryID = "voyagerSearchDashReusableTypeahead.57a4fa600f3f5f0c5950200a105f64cf"
)

type typeaheadHit struct {
	urn  string
	name string
}

// resolveTypeahead resolves a free-text query to LinkedIn entity IDs.
//
// Strategy:
//  1. Try the public /jobs-guest/api/typeaheadHits endpoint first. It needs
//     no auth, no rotating queryId, and returns clean {id, displayName, type}.
//  2. If that returns nothing usable (e.g. SCHOOL queries get coerced to
//     COMPANY), fall back to the authenticated Voyager GraphQL typeahead.
//     If that has drifted (HTTP 5xx) the public-endpoint result wins.
func (c *Client) resolveTypeahead(ctx context.Context, query, taType string) ([]typeaheadHit, error) {
	if query == "" {
		return nil, fmt.Errorf("%w: query required", ErrInvalidParams)
	}

	publicHits, publicErr := c.publicTypeahead(ctx, query, taType)
	if publicErr == nil && len(publicHits) > 0 {
		return publicHits, nil
	}

	voyagerHits, voyagerErr := c.voyagerTypeahead(ctx, query, taType)
	if voyagerErr == nil && len(voyagerHits) > 0 {
		return voyagerHits, nil
	}

	if publicErr != nil && voyagerErr != nil {
		return nil, fmt.Errorf("typeahead: public=%v, voyager=%v", publicErr, voyagerErr)
	}
	return nil, nil
}

// publicTypeahead hits the unauthenticated jobs-guest typeahead.
// Response shape: [{"id":"90000084","type":"GEO","displayName":"…","trackingId":"…"}]
func (c *Client) publicTypeahead(ctx context.Context, query, taType string) ([]typeaheadHit, error) {
	reqURL := fmt.Sprintf("%s?typeaheadType=%s&query=%s",
		publicTypeaheadPath, taType, url.QueryEscape(query))

	body, err := c.doPublicGet(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var elems []publicTypeaheadHit
	if err := json.Unmarshal(body, &elems); err != nil {
		return nil, fmt.Errorf("%w: public typeahead: %v", ErrParseFailed, err)
	}

	out := make([]typeaheadHit, 0, len(elems))
	wantType := strings.ToUpper(taType)
	for _, e := range elems {
		if e.ID == "" {
			continue
		}
		// Public endpoint coerces SCHOOL queries to COMPANY hits; only return
		// elements whose returned type matches what the caller asked for.
		if wantType != "" && strings.ToUpper(e.Type) != wantType {
			continue
		}
		out = append(out, typeaheadHit{urn: e.ID, name: e.DisplayName})
	}
	return out, nil
}

// voyagerTypeahead is the legacy GraphQL path. Kept as fallback for SCHOOL
// (the public endpoint doesn't support it) and as a safety net.
func (c *Client) voyagerTypeahead(ctx context.Context, query, taType string) ([]typeaheadHit, error) {
	reqURL := fmt.Sprintf("%s?variables=(query:%s,types:List(%s),count:10)&queryId=%s",
		voyagerTypeaheadPath, url.QueryEscape(query), taType, voyagerTypeaheadQueryID)

	body, err := c.makeRequest(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var restResp typeaheadRestResponse
	if err := json.Unmarshal(body, &restResp); err == nil && len(restResp.Elements) > 0 {
		hits := make([]typeaheadHit, 0, len(restResp.Elements))
		for _, elem := range restResp.Elements {
			if elem.TargetURN == "" {
				continue
			}
			name := ""
			if elem.Text != nil {
				name = elem.Text.Text
			}
			hits = append(hits, typeaheadHit{urn: stripURNPrefix(elem.TargetURN), name: name})
		}
		if len(hits) > 0 {
			return hits, nil
		}
	}

	var inclResp typeaheadResponse
	if err := json.Unmarshal(body, &inclResp); err == nil && len(inclResp.Included) > 0 {
		hits := make([]typeaheadHit, 0, len(inclResp.Included))
		for _, ent := range inclResp.Included {
			urn := ent.TargetURN
			if urn == "" {
				urn = ent.EntityURN
			}
			if urn == "" {
				continue
			}
			name := ent.Name
			if name == "" && ent.Title != nil {
				name = ent.Title.Text
			}
			hits = append(hits, typeaheadHit{urn: stripURNPrefix(urn), name: name})
		}
		if len(hits) > 0 {
			return hits, nil
		}
	}

	var probe json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("%w: typeahead response: %v", ErrParseFailed, err)
	}
	return nil, nil
}

// stripURNPrefix reduces "urn:li:geo:90000084" → "90000084" so the value
// can be dropped straight into the Voyager search filter, which silently
// ignores fully qualified URNs.
func stripURNPrefix(urn string) string {
	if urn == "" {
		return ""
	}
	if i := strings.LastIndex(urn, ":"); i >= 0 && i < len(urn)-1 {
		tail := urn[i+1:]
		// Some legacy entity URNs look like "urn:li:fs_geo:(90000084)".
		tail = strings.TrimLeft(tail, "(")
		tail = strings.TrimRight(tail, ")")
		return tail
	}
	return urn
}

// publicTypeaheadHit is the response element from /jobs-guest/api/typeaheadHits.
type publicTypeaheadHit struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"displayName"`
	TrackingID  string `json:"trackingId,omitempty"`
}

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
	TargetURN string `json:"targetUrn,omitempty"`
	Title     *struct {
		Text string `json:"text"`
	} `json:"title,omitempty"`
	Name string `json:"name,omitempty"`
}
