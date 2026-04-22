package mcp

import (
	"context"

	linkedin "github.com/teslashibe/linkedin-go"
	"github.com/teslashibe/mcptool"
)

// ResolveInput is the shared typed input for linkedin_resolve_*.
type ResolveInput struct {
	Query string `json:"query" jsonschema:"description=human-readable search string (e.g. 'Berlin' or 'Stripe'),required"`
}

func resolveLocations(ctx context.Context, c *linkedin.Client, in ResolveInput) (any, error) {
	return c.ResolveLocations(ctx, in.Query)
}

func resolveCompanies(ctx context.Context, c *linkedin.Client, in ResolveInput) (any, error) {
	return c.ResolveCompanies(ctx, in.Query)
}

func resolveSchools(ctx context.Context, c *linkedin.Client, in ResolveInput) (any, error) {
	return c.ResolveSchools(ctx, in.Query)
}

var resolveTools = []mcptool.Tool{
	mcptool.Define[*linkedin.Client, ResolveInput](
		"linkedin_resolve_locations",
		"Resolve a location name to LinkedIn geo URNs for use in linkedin_search_people filters",
		"ResolveLocations",
		resolveLocations,
	),
	mcptool.Define[*linkedin.Client, ResolveInput](
		"linkedin_resolve_companies",
		"Resolve a company name to LinkedIn company URNs for use in search filters",
		"ResolveCompanies",
		resolveCompanies,
	),
	mcptool.Define[*linkedin.Client, ResolveInput](
		"linkedin_resolve_schools",
		"Resolve a school name to LinkedIn school URNs for use in search filters",
		"ResolveSchools",
		resolveSchools,
	),
}
