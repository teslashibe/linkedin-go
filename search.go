package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Network represents a LinkedIn network distance filter.
type Network string

// Spotlight represents a LinkedIn spotlight filter.
type Spotlight string

const (
	NetworkFirst  Network = "F"
	NetworkSecond Network = "S"
	NetworkThird  Network = "O"

	SpotlightOpenToWork Spotlight = "OPEN_TO_WORK"
	SpotlightHiring     Spotlight = "HIRING"
)

// SearchParams configures a people search. All filter fields are optional.
type SearchParams struct {
	Keywords string
	Start    int
	Count    int // default 10, max 49

	Network         []Network
	CurrentCompany  []string // company URNs (use ResolveCompanies)
	PastCompany     []string
	GeoURN          []string // location URNs (use ResolveLocations)
	Industry        []string // industry codes
	School          []string // school URNs (use ResolveSchools)
	Title           string
	ProfileLanguage []string    // e.g. "en", "fr"
	ConnectionOf    string      // profile URN — friend-of-friend
	Spotlight       []Spotlight // OPEN_TO_WORK, HIRING
}

// SearchPeople searches for people on LinkedIn and returns matching profiles.
// Scans the included array directly for EntityResultViewModel entries, which
// is more resilient than walking the cluster tree.
func (c *Client) SearchPeople(ctx context.Context, p SearchParams) ([]Profile, error) {
	body, err := c.makeRequest(ctx, c.buildSearchURL(p))
	if err != nil {
		return nil, err
	}

	var resp searchAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	profiles := make([]Profile, 0)
	for i := range resp.Included {
		ent := &resp.Included[i]
		if ent.Type != typeEntityResult {
			continue
		}
		if ent.Title == nil || ent.PrimarySubtitle == nil || ent.SecondarySubtitle == nil {
			continue
		}
		prof := Profile{URN: ent.TrackingURN}
		parts := strings.SplitN(string(*ent.Title), " ", 2)
		if len(parts) >= 1 {
			prof.FirstName = parts[0]
		}
		if len(parts) == 2 {
			prof.LastName = parts[1]
		}
		prof.Headline = string(*ent.PrimarySubtitle)
		prof.Location = Location{City: string(*ent.SecondarySubtitle)}
		if ent.NavigationURL != "" {
			prof.ProfileURL = ent.NavigationURL
			prof.PublicID = extractVanityName(ent.NavigationURL)
		}
		profiles = append(profiles, prof)
	}

	return profiles, nil
}

func (c *Client) buildSearchURL(p SearchParams) string {
	count := p.Count
	if count <= 0 {
		count = 10
	}
	if count > 49 {
		count = 49
	}

	filters := buildFilterParams(p)

	var queryParts []string
	if p.Keywords != "" {
		queryParts = append(queryParts, fmt.Sprintf("keywords:%s", url.QueryEscape(p.Keywords)))
	}
	queryParts = append(queryParts, "flagshipSearchIntent:SEARCH_SRP")
	if len(filters) > 0 {
		queryParts = append(queryParts, fmt.Sprintf("queryParameters:List(%s)", strings.Join(filters, ",")))
	}
	queryParts = append(queryParts, "includeFiltersInResponse:false")

	variables := fmt.Sprintf("(start:%d,count:%d,origin:FACETED_SEARCH,query:(%s))",
		p.Start, count, strings.Join(queryParts, ","))

	return fmt.Sprintf("%s/graphql?queryId=%s&includeWebMetadata=true&variables=%s",
		apiBase, c.searchQueryID, variables)
}

func buildFilterParams(p SearchParams) []string {
	var filters []string

	if len(p.Network) > 0 {
		vals := make([]string, len(p.Network))
		for i, n := range p.Network {
			vals[i] = string(n)
		}
		filters = append(filters, filterEntry("network", vals))
	}
	if len(p.CurrentCompany) > 0 {
		filters = append(filters, filterEntry("currentCompany", p.CurrentCompany))
	}
	if len(p.PastCompany) > 0 {
		filters = append(filters, filterEntry("pastCompany", p.PastCompany))
	}
	if len(p.GeoURN) > 0 {
		filters = append(filters, filterEntry("geoUrn", p.GeoURN))
	}
	if len(p.Industry) > 0 {
		filters = append(filters, filterEntry("industry", p.Industry))
	}
	if len(p.School) > 0 {
		filters = append(filters, filterEntry("school", p.School))
	}
	if p.Title != "" {
		filters = append(filters, filterEntry("title", []string{p.Title}))
	}
	if len(p.ProfileLanguage) > 0 {
		filters = append(filters, filterEntry("profileLanguage", p.ProfileLanguage))
	}
	if p.ConnectionOf != "" {
		filters = append(filters, filterEntry("connectionOf", []string{p.ConnectionOf}))
	}
	if len(p.Spotlight) > 0 {
		vals := make([]string, len(p.Spotlight))
		for i, s := range p.Spotlight {
			vals[i] = string(s)
		}
		filters = append(filters, filterEntry("spotlight", vals))
	}

	filters = append(filters, filterEntry("resultType", []string{"PEOPLE"}))
	return filters
}

func filterEntry(key string, values []string) string {
	return fmt.Sprintf("(key:%s,value:List(%s))", key, strings.Join(values, ","))
}

func extractVanityName(navURL string) string {
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
