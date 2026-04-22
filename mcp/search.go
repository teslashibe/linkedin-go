package mcp

import (
	"context"

	linkedin "github.com/teslashibe/linkedin-go"
	"github.com/teslashibe/mcptool"
)

// SearchPeopleInput is the typed input for linkedin_search_people.
type SearchPeopleInput struct {
	Query           string   `json:"query,omitempty" jsonschema:"description=keywords or name to search; at least one filter is recommended"`
	Title           string   `json:"title,omitempty" jsonschema:"description=current job title filter (e.g. 'software engineer')"`
	GeoURNs         []string `json:"geo_urns,omitempty" jsonschema:"description=location URNs from linkedin_resolve_locations"`
	CurrentCompany  []string `json:"current_company,omitempty" jsonschema:"description=current company URNs from linkedin_resolve_companies"`
	PastCompany     []string `json:"past_company,omitempty" jsonschema:"description=past company URNs"`
	Schools         []string `json:"schools,omitempty" jsonschema:"description=school URNs from linkedin_resolve_schools"`
	Industry        []string `json:"industry,omitempty" jsonschema:"description=industry codes"`
	Network         []string `json:"network,omitempty" jsonschema:"description=connection-degree filter; allowed values: F,S,O (1st, 2nd, 3rd+)"`
	Spotlight       []string `json:"spotlight,omitempty" jsonschema:"description=spotlight filter; allowed values: OPEN_TO_WORK,HIRING"`
	ProfileLanguage []string `json:"profile_language,omitempty" jsonschema:"description=ISO language codes, e.g. en,fr"`
	Start           int      `json:"start,omitempty" jsonschema:"description=pagination offset,minimum=0,default=0"`
	Count           int      `json:"count,omitempty" jsonschema:"description=results per page,minimum=1,maximum=49,default=10"`
}

func searchPeople(ctx context.Context, c *linkedin.Client, in SearchPeopleInput) (any, error) {
	p := linkedin.SearchParams{
		Keywords:        in.Query,
		Title:           in.Title,
		GeoURN:          in.GeoURNs,
		CurrentCompany:  in.CurrentCompany,
		PastCompany:     in.PastCompany,
		School:          in.Schools,
		Industry:        in.Industry,
		ProfileLanguage: in.ProfileLanguage,
		Start:           in.Start,
		Count:           in.Count,
	}
	for _, n := range in.Network {
		p.Network = append(p.Network, linkedin.Network(n))
	}
	for _, s := range in.Spotlight {
		p.Spotlight = append(p.Spotlight, linkedin.Spotlight(s))
	}
	res, err := c.SearchPeople(ctx, p)
	if err != nil {
		return nil, err
	}
	limit := in.Count
	if limit <= 0 {
		limit = 10
	}
	return mcptool.PageOf(res, "", limit), nil
}

var searchTools = []mcptool.Tool{
	mcptool.Define[*linkedin.Client, SearchPeopleInput](
		"linkedin_search_people",
		"Search LinkedIn people by keyword, title, location, company, school, network, or industry",
		"SearchPeople",
		searchPeople,
	),
}
