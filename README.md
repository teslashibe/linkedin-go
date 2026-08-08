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

// Recent posts + comments authored by the member
posts, err := client.GetUserPosts(ctx, linkedin.UserPostParams{Member: "satyanadella", Count: 10})
comments, err := client.GetUserComments(ctx, linkedin.UserCommentParams{Member: "satyanadella", Count: 10})
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
    linkedin.WithProxyURL("http://user:pass@host:port"), // sticky egress per client
    linkedin.WithHTTPClient(&http.Client{Timeout: 60*time.Second}),
)

// Disable retry entirely
client := linkedin.New(auth, linkedin.WithRetry(0, 0))
```

## Multi-account pool (sticky proxy per account)

Rotate N burner sessions without sharing one IP. Each `Account` owns its own
cookies, daily budget, fingerprint, and sticky proxy. Acquire a `Lease` for the
full multi-call unit of work (profile + posts + comments) so affinity stays on
one identity + IP.

When the pool has more than one account, `ProxyURL` is required on every member.

```go
pool, err := linkedin.NewPool([]linkedin.Account{
    {
        ID: "burner-1",
        Auth: linkedin.Auth{LiAt: os.Getenv("LI_AT_1"), CSRF: os.Getenv("LI_CSRF_1")},
        ProxyURL: os.Getenv("LI_PROXY_1"), // sticky residential/mobile
    },
    {
        ID: "burner-2",
        Auth: linkedin.Auth{LiAt: os.Getenv("LI_AT_2"), CSRF: os.Getenv("LI_CSRF_2")},
        ProxyURL: os.Getenv("LI_PROXY_2"),
    },
})
if err != nil {
    log.Fatal(err)
}

lease, err := pool.Acquire(ctx)
if err != nil {
    // ErrNoAccountAvailable — all budgets/cooldowns exhausted
    return err
}
defer lease.Release()

profile, err := lease.Client.GetProfile(ctx, "satyanadella")
if err != nil {
    lease.ReportError(err) // parks budget/challenge/restricted accounts
    return err
}
```

Round-robin skips members that are in use, over daily budget, in cooldown, or
soft-skipped after auth/challenge errors. `pool.Status()` / `pool.Available()`
are for operator dashboards.

## MCP support

This package ships an [MCP](https://modelcontextprotocol.io/) tool surface in `./mcp` for use with [`teslashibe/mcptool`](https://github.com/teslashibe/mcptool)-compatible hosts (e.g. [`teslashibe/agent-setup`](https://github.com/teslashibe/agent-setup)). 19 tools cover the full client API: people search, profile fetch, group search/fetch/posts/members/membership/post, messaging (list/read/send), and typeahead resolvers (locations, companies, schools).

```go
import (
    "github.com/teslashibe/mcptool"
    linkedin "github.com/teslashibe/linkedin-go"
    linkmcp "github.com/teslashibe/linkedin-go/mcp"
)

client := linkedin.New(linkedin.Auth{...})
provider := linkmcp.Provider{}
for _, tool := range provider.Tools() {
    // register tool with your MCP server, passing client as the
    // opaque client argument when invoking
}
```

A coverage test in `mcp/mcp_test.go` fails if a new exported method is added to `*Client` without either being wrapped by an MCP tool or being added to `mcp.Excluded` with a reason — keeping the MCP surface in lockstep with the package API is enforced by CI rather than convention.

## License

MIT
