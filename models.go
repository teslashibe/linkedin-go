package linkedin

import (
	"encoding/json"
	"fmt"
)

// Profile is the clean domain model returned by SearchPeople and GetProfile.
type Profile struct {
	PublicID       string         `json:"publicId"`
	URN            string         `json:"urn,omitempty"`
	FirstName      string         `json:"firstName"`
	LastName       string         `json:"lastName"`
	Headline       string         `json:"headline,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	Location       Location       `json:"location,omitempty"`
	ProfileURL     string         `json:"profileUrl,omitempty"`
	PictureURL     string         `json:"pictureUrl,omitempty"`
	Connections    int            `json:"connections,omitempty"`
	Followers      int            `json:"followers,omitempty"`
	Experience     []Experience   `json:"experience,omitempty"`
	Education      []Education    `json:"education,omitempty"`
	Skills         []string       `json:"skills,omitempty"`
	Certifications []Certification `json:"certifications,omitempty"`
}

// FullName returns "FirstName LastName".
func (p Profile) FullName() string {
	if p.FirstName == "" && p.LastName == "" {
		return ""
	}
	if p.LastName == "" {
		return p.FirstName
	}
	if p.FirstName == "" {
		return p.LastName
	}
	return p.FirstName + " " + p.LastName
}

type Location struct {
	City    string `json:"city,omitempty"`
	Region  string `json:"region,omitempty"`
	Country string `json:"country,omitempty"`
}

// String returns "City, Region, Country" omitting empty parts.
func (l Location) String() string {
	parts := make([]string, 0, 3)
	for _, s := range []string{l.City, l.Region, l.Country} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

type Experience struct {
	Title       string `json:"title"`
	Company     string `json:"company"`
	Location    string `json:"location,omitempty"`
	StartDate   Date   `json:"startDate,omitempty"`
	EndDate     Date   `json:"endDate,omitempty"`
	Description string `json:"description,omitempty"`
}

type Education struct {
	School    string `json:"school"`
	Degree    string `json:"degree,omitempty"`
	Field     string `json:"field,omitempty"`
	StartDate Date   `json:"startDate,omitempty"`
	EndDate   Date   `json:"endDate,omitempty"`
}

type Certification struct {
	Name    string `json:"name"`
	Issuer  string `json:"issuer,omitempty"`
	URL     string `json:"url,omitempty"`
	Issued  Date   `json:"issued,omitempty"`
	Expires Date   `json:"expires,omitempty"`
}

type Date struct {
	Year  int `json:"year,omitempty"`
	Month int `json:"month,omitempty"`
}

// IsZero reports whether the date has no data.
func (d Date) IsZero() bool { return d.Year == 0 && d.Month == 0 }

// --- Resolver result types ---

type GeoResult struct {
	URN  string `json:"urn"`
	Name string `json:"name"`
}

type CompanyResult struct {
	URN  string `json:"urn"`
	Name string `json:"name"`
}

type SchoolResult struct {
	URN  string `json:"urn"`
	Name string `json:"name"`
}

// --- Internal API response types (unexported) ---

// flexText handles LinkedIn fields that can be a string or {"text":"..."}.
type flexText string

func (ft *flexText) UnmarshalJSON(data []byte) error {
	var obj struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &obj); err == nil && obj.Text != "" {
		*ft = flexText(obj.Text)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*ft = flexText(s)
		return nil
	}
	if string(data) == "null" {
		*ft = ""
		return nil
	}
	return fmt.Errorf("cannot unmarshal %s into flexText", string(data))
}

// searchAPIResponse is the top-level search response.
type searchAPIResponse struct {
	Data     searchRootData   `json:"data"`
	Included []includedEntity `json:"included"`
}

type searchRootData struct {
	Data searchInnerData `json:"data"`
}

type searchInnerData struct {
	SearchDashClustersByAll searchClusters `json:"searchDashClustersByAll"`
}

type searchClusters struct {
	Paging   apiPaging        `json:"paging"`
	Elements []clusterElement `json:"elements"`
}

type apiPaging struct {
	Start int `json:"start"`
	Count int `json:"count"`
	Total int `json:"total"`
}

type clusterElement struct {
	Items []clusterItem `json:"items"`
}

type clusterItem struct {
	EntityResultURN string `json:"*entityResult"`
}

// profileAPIResponse is the top-level profile response.
type profileAPIResponse struct {
	Data     profileRootData  `json:"data"`
	Included []includedEntity `json:"included"`
}

type profileRootData struct {
	Data profileInnerData `json:"data"`
}

type profileInnerData struct {
	IdentityDashProfilesByMemberIdentity profileCollection `json:"identityDashProfilesByMemberIdentity"`
}

type profileCollection struct {
	Elements []string `json:"*elements,omitempty"`
}

// includedEntity is a union struct that captures all entity types from the
// "included" array. The $type field determines which fields are populated.
type includedEntity struct {
	Type      string `json:"$type"`
	EntityURN string `json:"entityUrn,omitempty"`

	// EntityResultViewModel fields (search results)
	TrackingURN       string    `json:"trackingUrn,omitempty"`
	Title             *flexText `json:"title,omitempty"`
	PrimarySubtitle   *flexText `json:"primarySubtitle,omitempty"`
	SecondarySubtitle *flexText `json:"secondarySubtitle,omitempty"`
	NavigationURL     string    `json:"navigationUrl,omitempty"`

	// Profile fields
	PublicIdentifier string `json:"publicIdentifier,omitempty"`
	FirstName        string `json:"firstName,omitempty"`
	LastName         string `json:"lastName,omitempty"`
	Headline         string `json:"headline,omitempty"`
	Summary          string `json:"summary,omitempty"`

	// Location
	GeoLocation *geoLocationResponse `json:"geoLocation,omitempty"`

	// Profile picture
	ProfilePicture *profilePictureResponse `json:"profilePicture,omitempty"`

	// Following state (follower count)
	FollowingState *followingStateResponse `json:"followingState,omitempty"`

	// Position fields
	CompanyName  string             `json:"companyName,omitempty"`
	CompanyURN   string             `json:"*company,omitempty"`
	Description  string             `json:"description,omitempty"`
	DateRange    *dateRangeResponse `json:"dateRange,omitempty"`
	LocationName string             `json:"locationName,omitempty"`

	// Education fields
	SchoolName   string `json:"schoolName,omitempty"`
	SchoolURN    string `json:"*school,omitempty"`
	DegreeName   string `json:"degreeName,omitempty"`
	FieldOfStudy string `json:"fieldOfStudy,omitempty"`

	// Skill fields
	Name string `json:"name,omitempty"`

	// Certification fields
	Authority     string `json:"authority,omitempty"`
	LicenseNumber string `json:"licenseNumber,omitempty"`
	URL           string `json:"url,omitempty"`

	// Network distance (connections)
	Connections *connectionsResponse `json:"connections,omitempty"`
}

type geoLocationResponse struct {
	Geo *geoResponse `json:"geo,omitempty"`
}

type geoResponse struct {
	DefaultLocalizedName         string `json:"defaultLocalizedName,omitempty"`
	DefaultLocalizedNameWithoutCountryName string `json:"defaultLocalizedNameWithoutCountryName,omitempty"`
	CountryURN                   string `json:"countryUrn,omitempty"`
}

type profilePictureResponse struct {
	DisplayImageReference *vectorImageResponse `json:"displayImageReference,omitempty"`
	A11yText              string               `json:"a11yText,omitempty"`
}

type vectorImageResponse struct {
	RootURL   string             `json:"rootUrl,omitempty"`
	Artifacts []vectorArtifact   `json:"artifacts,omitempty"`
}

type vectorArtifact struct {
	Width                         int    `json:"width,omitempty"`
	Height                        int    `json:"height,omitempty"`
	FileIdentifyingUrlPathSegment string `json:"fileIdentifyingUrlPathSegment,omitempty"`
}

type followingStateResponse struct {
	FollowerCount int `json:"followerCount,omitempty"`
}

type connectionsResponse struct {
	Paging *apiPaging `json:"paging,omitempty"`
}

type dateRangeResponse struct {
	Start *dateResponse `json:"start,omitempty"`
	End   *dateResponse `json:"end,omitempty"`
}

type dateResponse struct {
	Year  int `json:"year,omitempty"`
	Month int `json:"month,omitempty"`
}

// Entity type constants
const (
	typeProfile      = "com.linkedin.voyager.dash.identity.profile.Profile"
	typePosition     = "com.linkedin.voyager.dash.identity.profile.Position"
	typeEducation    = "com.linkedin.voyager.dash.identity.profile.Education"
	typeSkill        = "com.linkedin.voyager.dash.identity.profile.treasury.EndorsedSkill"
	typeCertification = "com.linkedin.voyager.dash.identity.profile.Certification"
	typeEntityResult = "com.linkedin.voyager.dash.search.EntityResultViewModel"
)
