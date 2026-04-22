package mcp

import (
	"context"

	linkedin "github.com/teslashibe/linkedin-go"
	"github.com/teslashibe/mcptool"
)

// GetProfileInput is the typed input for linkedin_get_profile.
type GetProfileInput struct {
	VanityName string `json:"vanity_name" jsonschema:"description=LinkedIn vanity name (the URL slug after /in/),required"`
}

func getProfile(ctx context.Context, c *linkedin.Client, in GetProfileInput) (any, error) {
	return c.GetProfile(ctx, in.VanityName)
}

var profileTools = []mcptool.Tool{
	mcptool.Define[*linkedin.Client, GetProfileInput](
		"linkedin_get_profile",
		"Fetch a LinkedIn profile by vanity name (the slug after /in/)",
		"GetProfile",
		getProfile,
	),
}
