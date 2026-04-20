package linkedin_test

import (
	"context"
	"os"
	"testing"
	"time"

	linkedin "github.com/teslashibe/linkedin-go"
)

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("required env var %s is not set", key)
	}
	return v
}

func newClient(t *testing.T) *linkedin.Client {
	t.Helper()
	return linkedin.New(linkedin.Auth{
		LiAt:       mustEnv(t, "LI_AT"),
		CSRF:       mustEnv(t, "CSRF_TOKEN"),
		JSESSIONID: mustEnv(t, "JSESSIONID"),
	})
}

func TestIntegration_NewClient(t *testing.T) {
	c := newClient(t)
	if c == nil {
		t.Fatal("New() returned nil")
	}
	t.Log("PASS: New(auth) returns usable *Client")
}

func TestIntegration_NewClient_WithRetryDisabled(t *testing.T) {
	c := linkedin.New(linkedin.Auth{
		LiAt:       mustEnv(t, "LI_AT"),
		CSRF:       mustEnv(t, "CSRF_TOKEN"),
		JSESSIONID: mustEnv(t, "JSESSIONID"),
	}, linkedin.WithRetry(0, 0))
	if c == nil {
		t.Fatal("New() with WithRetry(0,0) returned nil")
	}
	t.Log("PASS: WithRetry(0, 0) disables retry without panic")
}

func TestIntegration_NewClient_NilHTTPClient(t *testing.T) {
	c := linkedin.New(linkedin.Auth{
		LiAt:       mustEnv(t, "LI_AT"),
		CSRF:       mustEnv(t, "CSRF_TOKEN"),
		JSESSIONID: mustEnv(t, "JSESSIONID"),
	}, linkedin.WithHTTPClient(nil))
	if c == nil {
		t.Fatal("New() with nil HTTPClient returned nil")
	}
	t.Log("PASS: WithHTTPClient(nil) is safe")
}

func TestIntegration_SearchPeople(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	profiles, err := c.SearchPeople(ctx, linkedin.SearchParams{
		Keywords: "software engineer",
		Count:    5,
		Network:  []linkedin.Network{linkedin.NetworkSecond},
	})
	if err != nil {
		t.Fatalf("SearchPeople failed: %v", err)
	}

	t.Logf("SearchPeople returned %d profiles", len(profiles))
	if len(profiles) == 0 {
		t.Fatal("SearchPeople returned 0 profiles — expected at least 1")
	}

	p := profiles[0]
	t.Logf("  [0] Name=%q Headline=%q PublicID=%q", p.FirstName+" "+p.LastName, p.Headline, p.PublicID)

	if p.PublicID == "" && p.ProfileURL == "" {
		t.Error("first result has no PublicID or ProfileURL")
	}
	t.Log("PASS: SearchPeople returns results with populated fields")
}

func TestIntegration_GetProfile(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	profile, err := c.GetProfile(ctx, "satyanadella")
	if err != nil {
		t.Fatalf("GetProfile failed: %v", err)
	}

	t.Logf("Profile: %s (%s)", profile.FullName(), profile.Headline)
	t.Logf("  URN:         %s", profile.URN)
	t.Logf("  Location:    %s", profile.Location)
	t.Logf("  PictureURL:  %s", truncate(profile.PictureURL, 80))
	t.Logf("  Connections: %d", profile.Connections)
	t.Logf("  Followers:   %d", profile.Followers)
	t.Logf("  Experience:  %d entries", len(profile.Experience))
	t.Logf("  Education:   %d entries", len(profile.Education))
	t.Logf("  Skills:      %d entries", len(profile.Skills))
	t.Logf("  Certs:       %d entries", len(profile.Certifications))

	if profile.FirstName == "" {
		t.Error("FirstName is empty")
	}
	if profile.LastName == "" {
		t.Error("LastName is empty")
	}
	if profile.URN == "" {
		t.Error("URN is empty")
	}
	if profile.Headline == "" {
		t.Error("Headline is empty")
	}
	if profile.PublicID == "" {
		t.Error("PublicID is empty")
	}
	if profile.ProfileURL == "" {
		t.Error("ProfileURL is empty")
	}

	t.Log("PASS: GetProfile returns fully populated Profile")
}

func TestIntegration_GetProfile_EmptyVanity(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	_, err := c.GetProfile(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty vanity name")
	}
	t.Logf("GetProfile(\"\") correctly returned: %v", err)
	t.Log("PASS: GetProfile rejects empty vanity name")
}

func TestIntegration_ResolveLocations(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := c.ResolveLocations(ctx, "San Francisco")
	if err != nil {
		t.Fatalf("ResolveLocations failed: %v", err)
	}

	t.Logf("ResolveLocations returned %d results", len(results))
	for i, r := range results {
		t.Logf("  [%d] URN=%s Name=%q", i, r.URN, r.Name)
	}

	if len(results) == 0 {
		t.Fatal("ResolveLocations returned 0 results")
	}
	if results[0].URN == "" {
		t.Error("first result has empty URN")
	}
	if results[0].Name == "" {
		t.Error("first result has empty Name")
	}
	t.Log("PASS: ResolveLocations returns results with URN and Name")
}

func TestIntegration_ResolveCompanies(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := c.ResolveCompanies(ctx, "Google")
	if err != nil {
		t.Fatalf("ResolveCompanies failed: %v", err)
	}

	t.Logf("ResolveCompanies returned %d results", len(results))
	for i, r := range results {
		t.Logf("  [%d] URN=%s Name=%q", i, r.URN, r.Name)
	}

	if len(results) == 0 {
		t.Fatal("ResolveCompanies returned 0 results")
	}
	t.Log("PASS: ResolveCompanies returns results")
}

func TestIntegration_ResolveSchools(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := c.ResolveSchools(ctx, "Stanford")
	if err != nil {
		t.Fatalf("ResolveSchools failed: %v", err)
	}

	t.Logf("ResolveSchools returned %d results", len(results))
	for i, r := range results {
		t.Logf("  [%d] URN=%s Name=%q", i, r.URN, r.Name)
	}

	if len(results) == 0 {
		t.Fatal("ResolveSchools returned 0 results")
	}
	t.Log("PASS: ResolveSchools returns results")
}

func TestIntegration_SearchWithResolvedFilters(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	geos, err := c.ResolveLocations(ctx, "San Francisco")
	if err != nil || len(geos) == 0 {
		t.Skipf("skipping: could not resolve location: %v", err)
	}

	profiles, err := c.SearchPeople(ctx, linkedin.SearchParams{
		Keywords: "engineer",
		GeoURN:   []string{geos[0].URN},
		Count:    3,
	})
	if err != nil {
		t.Fatalf("SearchPeople with resolved GeoURN failed: %v", err)
	}

	t.Logf("SearchPeople (geo-filtered) returned %d profiles", len(profiles))
	t.Log("PASS: SearchPeople works with resolver-provided URNs")
}

func TestIntegration_WithQueryIDs(t *testing.T) {
	c := linkedin.New(linkedin.Auth{
		LiAt:       mustEnv(t, "LI_AT"),
		CSRF:       mustEnv(t, "CSRF_TOKEN"),
		JSESSIONID: mustEnv(t, "JSESSIONID"),
	}, linkedin.WithQueryIDs("customSearch.123", "customProfile.456"))

	if c == nil {
		t.Fatal("New() with WithQueryIDs returned nil")
	}
	t.Log("PASS: WithQueryIDs accepts custom IDs without panic")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
