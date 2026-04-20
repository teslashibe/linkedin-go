package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// GetProfile retrieves a fully populated profile by LinkedIn vanity name
// (public identifier). All sub-entities (experience, education, skills,
// certifications) are scoped by the profile URN to prevent cross-entity bleed.
func (c *Client) GetProfile(ctx context.Context, vanityName string) (*Profile, error) {
	if vanityName == "" {
		return nil, fmt.Errorf("%w: vanity name required", ErrInvalidParams)
	}

	body, err := c.makeRequest(ctx, c.buildProfileURL(vanityName))
	if err != nil {
		return nil, err
	}

	var resp profileAPIResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	return parseProfileResponse(&resp, vanityName)
}

func (c *Client) buildProfileURL(vanityName string) string {
	variables := fmt.Sprintf("(vanityName:%s)", url.QueryEscape(vanityName))
	return fmt.Sprintf("%s/graphql?variables=%s&queryId=%s",
		apiBase, variables, c.profileQueryID)
}

func parseProfileResponse(resp *profileAPIResponse, vanityName string) (*Profile, error) {
	var profileEntity *includedEntity
	// Prefer exact match by public identifier to avoid cross-entity bleed
	// when multiple profiles appear in the included array.
	for i := range resp.Included {
		if resp.Included[i].Type == typeProfile && strings.EqualFold(resp.Included[i].PublicIdentifier, vanityName) {
			profileEntity = &resp.Included[i]
			break
		}
	}
	if profileEntity == nil {
		for i := range resp.Included {
			if resp.Included[i].Type == typeProfile {
				profileEntity = &resp.Included[i]
				break
			}
		}
	}
	if profileEntity == nil {
		return nil, ErrNotFound
	}

	memberID := extractMemberID(profileEntity.EntityURN)

	p := &Profile{
		URN:       profileEntity.EntityURN,
		PublicID:  profileEntity.PublicIdentifier,
		FirstName: profileEntity.FirstName,
		LastName:  profileEntity.LastName,
		Headline:  profileEntity.Headline,
		Summary:   flexTextString(profileEntity.Summary),
	}

	if profileEntity.PublicIdentifier != "" {
		p.ProfileURL = "https://www.linkedin.com/in/" + profileEntity.PublicIdentifier
	}

	if profileEntity.GeoLocation != nil && profileEntity.GeoLocation.Geo != nil {
		geo := profileEntity.GeoLocation.Geo
		p.Location = Location{
			City:    geo.DefaultLocalizedNameWithoutCountryName,
			Country: extractCountryCode(geo.CountryURN),
		}
		if geo.DefaultLocalizedName != "" && geo.DefaultLocalizedNameWithoutCountryName != "" {
			if rest := strings.TrimPrefix(geo.DefaultLocalizedName, geo.DefaultLocalizedNameWithoutCountryName); rest != "" {
				p.Location.Region = strings.TrimLeft(rest, ", ")
			}
		}
	}

	if profileEntity.ProfilePicture != nil {
		p.PictureURL = extractPictureURL(profileEntity.ProfilePicture)
	}

	if profileEntity.FollowingState != nil {
		p.Followers = profileEntity.FollowingState.FollowerCount
	}

	if profileEntity.Connections != nil && profileEntity.Connections.Paging != nil {
		p.Connections = profileEntity.Connections.Paging.Total
	}

	for i := range resp.Included {
		ent := &resp.Included[i]
		if !belongsToMember(ent.EntityURN, memberID) {
			continue
		}

		switch ent.Type {
		case typePosition:
			exp := Experience{
				Company:     ent.CompanyName,
				Description: ent.Description,
				Location:    ent.LocationName,
			}
			if ent.Title != nil {
				exp.Title = string(*ent.Title)
			}
			if ent.DateRange != nil {
				if ent.DateRange.Start != nil {
					exp.StartDate = Date{Year: ent.DateRange.Start.Year, Month: ent.DateRange.Start.Month}
				}
				if ent.DateRange.End != nil {
					exp.EndDate = Date{Year: ent.DateRange.End.Year, Month: ent.DateRange.End.Month}
				}
			}
			p.Experience = append(p.Experience, exp)

		case typeEducation:
			edu := Education{
				School: ent.SchoolName,
				Degree: ent.DegreeName,
				Field:  ent.FieldOfStudy,
			}
			if ent.DateRange != nil {
				if ent.DateRange.Start != nil {
					edu.StartDate = Date{Year: ent.DateRange.Start.Year, Month: ent.DateRange.Start.Month}
				}
				if ent.DateRange.End != nil {
					edu.EndDate = Date{Year: ent.DateRange.End.Year, Month: ent.DateRange.End.Month}
				}
			}
			p.Education = append(p.Education, edu)

		case typeSkill:
			if ent.Name != "" {
				p.Skills = append(p.Skills, ent.Name)
			}

		case typeCertification:
			cert := Certification{
				Name:   ent.Name,
				Issuer: ent.Authority,
				URL:    ent.URL,
			}
			if ent.DateRange != nil {
				if ent.DateRange.Start != nil {
					cert.Issued = Date{Year: ent.DateRange.Start.Year, Month: ent.DateRange.Start.Month}
				}
				if ent.DateRange.End != nil {
					cert.Expires = Date{Year: ent.DateRange.End.Year, Month: ent.DateRange.End.Month}
				}
			}
			p.Certifications = append(p.Certifications, cert)
		}
	}

	return p, nil
}

// extractMemberID pulls the member ID from a profile URN like
// "urn:li:fsd_profile:ACoAABxxxxx".
func extractMemberID(urn string) string {
	parts := strings.Split(urn, ":")
	if len(parts) >= 4 {
		return parts[len(parts)-1]
	}
	return urn
}

// belongsToMember checks if an entity URN belongs to the given member.
// Sub-entity URNs follow the pattern "urn:li:fsd_profilePosition:(MEMBER_ID,SEQ)".
// Also handles single-value URNs like "urn:li:fsd_profileSkill:(MEMBER_ID)".
func belongsToMember(entityURN, memberID string) bool {
	if memberID == "" {
		return false
	}
	return strings.Contains(entityURN, "("+memberID+",") ||
		strings.Contains(entityURN, "("+memberID+")")
}

func extractCountryCode(countryURN string) string {
	parts := strings.Split(countryURN, ":")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func extractPictureURL(pic *profilePictureResponse) string {
	if pic.DisplayImageReference == nil {
		return ""
	}
	ref := pic.DisplayImageReference
	if ref.RootURL == "" || len(ref.Artifacts) == 0 {
		return ""
	}
	best := ref.Artifacts[0]
	for _, a := range ref.Artifacts[1:] {
		if a.Width > best.Width {
			best = a
		}
	}
	return ref.RootURL + best.FileIdentifyingUrlPathSegment
}
