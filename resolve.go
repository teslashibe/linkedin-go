package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const typeaheadBaseURL = "https://www.linkedin.com"

// ResolveLocations searches LinkedIn's typeahead for locations matching
// the query and returns their URNs and human-readable names.
func (c *Client) ResolveLocations(ctx context.Context, query string) ([]GeoResult, error) {
	if query == "" {
		return nil, fmt.Errorf("%w: query is required", ErrInvalidParams)
	}

	rawURL := buildTypeaheadURL(typeaheadBaseURL, "GEO", query)
	body, err := c.doGet(ctx, rawURL, typeaheadHeaders())
	if err != nil {
		return nil, err
	}

	entities, err := parseTypeahead(body)
	if err != nil {
		return nil, err
	}

	var results []GeoResult
	for _, e := range entities {
		if e.URN == "" {
			continue
		}
		results = append(results, GeoResult{URN: e.URN, Name: e.Name})
	}
	return results, nil
}

// ResolveCompanies searches LinkedIn's typeahead for companies matching
// the query and returns their URNs and human-readable names.
func (c *Client) ResolveCompanies(ctx context.Context, query string) ([]CompanyResult, error) {
	if query == "" {
		return nil, fmt.Errorf("%w: query is required", ErrInvalidParams)
	}

	rawURL := buildTypeaheadURL(typeaheadBaseURL, "COMPANY", query)
	body, err := c.doGet(ctx, rawURL, typeaheadHeaders())
	if err != nil {
		return nil, err
	}

	entities, err := parseTypeahead(body)
	if err != nil {
		return nil, err
	}

	var results []CompanyResult
	for _, e := range entities {
		if e.URN == "" {
			continue
		}
		results = append(results, CompanyResult{URN: e.URN, Name: e.Name})
	}
	return results, nil
}

// ResolveSchools searches LinkedIn's typeahead for schools matching
// the query and returns their URNs and human-readable names.
func (c *Client) ResolveSchools(ctx context.Context, query string) ([]SchoolResult, error) {
	if query == "" {
		return nil, fmt.Errorf("%w: query is required", ErrInvalidParams)
	}

	rawURL := buildTypeaheadURL(typeaheadBaseURL, "SCHOOL", query)
	body, err := c.doGet(ctx, rawURL, typeaheadHeaders())
	if err != nil {
		return nil, err
	}

	entities, err := parseTypeahead(body)
	if err != nil {
		return nil, err
	}

	var results []SchoolResult
	for _, e := range entities {
		if e.URN == "" {
			continue
		}
		results = append(results, SchoolResult{URN: e.URN, Name: e.Name})
	}
	return results, nil
}

type typeaheadResult struct {
	URN  string
	Name string
}

func parseTypeahead(body []byte) ([]typeaheadResult, error) {
	// Try parsing as the standard included-array format first
	var resp struct {
		Included []json.RawMessage `json:"included"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: typeahead: %v", ErrParseFailed, err)
	}

	var results []typeaheadResult
	for _, raw := range resp.Included {
		var entity struct {
			EntityURN string `json:"entityUrn"`
			Title     *struct {
				Text string `json:"text"`
			} `json:"title"`
			Name string `json:"name"`
			Type string `json:"$type"`
		}
		if err := json.Unmarshal(raw, &entity); err != nil {
			continue
		}

		// Skip non-result types (like search metadata)
		if entity.EntityURN == "" {
			continue
		}
		// Only include typeahead result entities
		if !strings.Contains(entity.Type, "TypeaheadEntityResult") &&
			!strings.Contains(entity.Type, "Geo") &&
			!strings.Contains(entity.Type, "Company") &&
			!strings.Contains(entity.Type, "School") &&
			entity.Title == nil && entity.Name == "" {
			continue
		}

		name := entity.Name
		if name == "" && entity.Title != nil {
			name = entity.Title.Text
		}
		if name == "" {
			continue
		}

		// Extract clean URN — typeahead URNs often have the form
		// urn:li:fsd_geo:103644278 or urn:li:fsd_company:1234
		urn := entity.EntityURN

		results = append(results, typeaheadResult{URN: urn, Name: name})
	}

	return results, nil
}
