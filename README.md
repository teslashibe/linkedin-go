# linkedin-go

A lean, zero-dependency Go client for LinkedIn people search and profile scraping.

```go
import "github.com/teslashibe/linkedin-go"
```

## Status

Under active development. See [open issues](https://github.com/teslashibe/linkedin-go/issues) for the roadmap.

## Auth

LinkedIn session credentials obtained from your browser dev tools:

| Env var | Cookie / header | Where to find |
|---|---|---|
| `LI_AT` | `li_at` cookie | DevTools → Application → Cookies |
| `CSRF_TOKEN` | `JSESSIONID` cookie (strip quotes) | DevTools → Application → Cookies |
| `JSESSIONID` | `JSESSIONID` cookie (with quotes) | DevTools → Application → Cookies |

```go
client := linkedin.New(linkedin.Auth{
    LiAt:       os.Getenv("LI_AT"),
    CSRF:       os.Getenv("CSRF_TOKEN"),
    JSESSIONID: os.Getenv("JSESSIONID"),
})
```

## License

MIT
