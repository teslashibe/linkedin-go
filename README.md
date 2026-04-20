# linkedin-go

A lean, zero-dependency Go client for LinkedIn people search and profile scraping.

```go
import "github.com/teslashibe/linkedin-go"
```

## Install

```bash
go get github.com/teslashibe/linkedin-go
```

## Auth

LinkedIn session credentials obtained from your browser dev tools:

| Env var | Cookie / header | Where to find |
|---|---|---|
| `LI_AT` | `li_at` cookie | DevTools > Application > Cookies |
| `CSRF_TOKEN` | `JSESSIONID` cookie (strip quotes) | DevTools > Application > Cookies |
| `JSESSIONID` | `JSESSIONID` cookie (with quotes) | DevTools > Application > Cookies |

## Quick start

```go
client := linkedin.New(linkedin.Auth{
    LiAt: os.Getenv("LI_AT"),
    CSRF: os.Getenv("CSRF_TOKEN"),
})

// Search with full UI-parity filters
profiles, err := client.SearchPeople(ctx, linkedin.SearchParams{
    Keywords: "engineering manager",
    GeoURN:   []string{"103644278"}, // US
    Network:  []linkedin.Network{linkedin.NetworkSecond},
})

// Full profile scrape
profile, err := client.GetProfile(ctx, "satyanadella")
fmt.Println(profile.FullName(), profile.Headline)
```

## Search filters

All filters available in the LinkedIn UI are supported:

| Field | Type | Description |
|---|---|---|
| `Keywords` | `string` | Search query |
| `Network` | `[]Network` | `F` (1st), `S` (2nd), `O` (3rd+) |
| `CurrentCompany` | `[]string` | Company URNs |
| `PastCompany` | `[]string` | Company URNs |
| `GeoURN` | `[]string` | Location URNs |
| `Industry` | `[]string` | Industry codes |
| `School` | `[]string` | School URNs |
| `Title` | `string` | Job title filter |
| `ProfileLanguage` | `[]string` | `"en"`, `"fr"`, etc. |
| `ConnectionOf` | `string` | Profile URN (friend-of-friend) |
| `Spotlight` | `[]Spotlight` | `OPEN_TO_WORK`, `HIRING` |
| `Start` | `int` | Pagination offset |
| `Count` | `int` | Results per page (default 10, max 49) |

## Human-readable resolvers

Don't know the URN for a location, company, or school? Resolve it:

```go
geos, _ := client.ResolveLocations(ctx, "San Francisco")
// [{URN: "urn:li:fsd_geo:102277331", Name: "San Francisco, California, US"}, ...]

companies, _ := client.ResolveCompanies(ctx, "Google")
schools, _ := client.ResolveSchools(ctx, "Stanford")
```

## Options

```go
client := linkedin.New(auth,
    linkedin.WithRetry(5, time.Second),             // 5 attempts, 1s base backoff
    linkedin.WithQueryIDs("newSearchID", ""),        // override Voyager query IDs
    linkedin.WithUserAgent("custom-agent/1.0"),
    linkedin.WithHTTPClient(&http.Client{Timeout: 60*time.Second}),
)

// Disable retry entirely
client := linkedin.New(auth, linkedin.WithRetry(0, 0))
```

## License

MIT
