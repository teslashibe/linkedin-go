package linkedin

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// newRNG returns a fresh, time-seeded math/rand source.
func newRNG() *rand.Rand {
	return rand.New(rand.NewSource(time.Now().UnixNano()))
}

// BrowserProfile captures the per-fingerprint values that LinkedIn's bot
// detection uses to decide if a session is browser-driven. Build one with
// MacChromeProfile / WindowsChromeProfile or construct manually for full
// control.
type BrowserProfile struct {
	UserAgent       string // full UA string
	SecChUA         string // e.g. `"Chromium";v="136", "Google Chrome";v="136", "Not.A/Brand";v="99"`
	SecChUAMobile   string // "?0" or "?1"
	SecChUAPlatform string // e.g. `"macOS"`
	AcceptLanguage  string
	ClientVersion   string // e.g. "1.13.36120"; matches /web-app/ deployed bundle
	TimezoneName    string // IANA, e.g. "America/Los_Angeles"
	TimezoneOffset  int    // minutes from UTC, e.g. -420 for PDT
	DisplayWidth    int
	DisplayHeight   int
	DisplayDensity  int
}

// MacChromePT returns a believable Mac Chrome 136 profile pinned to Pacific Time
// with a 14" MacBook Pro display. Use this for a single-operator burner.
func MacChromePT() BrowserProfile {
	return BrowserProfile{
		UserAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
		SecChUA:         `"Chromium";v="136", "Google Chrome";v="136", "Not.A/Brand";v="99"`,
		SecChUAMobile:   "?0",
		SecChUAPlatform: `"macOS"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		ClientVersion:   "1.13.36120",
		TimezoneName:    "America/Los_Angeles",
		TimezoneOffset:  -420, // PDT in April; PST is -480
		DisplayWidth:    1728,
		DisplayHeight:   1117,
		DisplayDensity:  2,
	}
}

// WindowsChromeET returns a believable Windows 11 Chrome 136 profile on Eastern
// Time. Useful for rotating fingerprints across burners.
func WindowsChromeET() BrowserProfile {
	return BrowserProfile{
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36",
		SecChUA:         `"Chromium";v="136", "Google Chrome";v="136", "Not.A/Brand";v="99"`,
		SecChUAMobile:   "?0",
		SecChUAPlatform: `"Windows"`,
		AcceptLanguage:  "en-US,en;q=0.9",
		ClientVersion:   "1.13.36120",
		TimezoneName:    "America/New_York",
		TimezoneOffset:  -240,
		DisplayWidth:    1920,
		DisplayHeight:   1080,
		DisplayDensity:  1,
	}
}

// xLiTrack renders the JSON value for the x-li-track header.
func (b BrowserProfile) xLiTrack() string {
	cv := b.ClientVersion
	if cv == "" {
		cv = "1.13.36120"
	}
	tz := b.TimezoneName
	if tz == "" {
		tz = "America/Los_Angeles"
	}
	return fmt.Sprintf(
		`{"clientVersion":"%s","mpVersion":"%s","osName":"web","timezoneOffset":%d,"timezone":"%s","deviceFormFactor":"DESKTOP","mpName":"voyager-web","displayDensity":%d,"displayWidth":%d,"displayHeight":%d}`,
		cv, cv, b.TimezoneOffset, tz, b.DisplayDensity, b.DisplayWidth, b.DisplayHeight,
	)
}

// pageInstance returns the appropriate x-li-page-instance for the given URL,
// matching what a real Voyager-driven browser would send.
func pageInstance(reqURL string) string {
	switch {
	case strings.Contains(reqURL, "voyagerSearchDashClusters"),
		strings.Contains(reqURL, "voyagerSearchDashReusable"),
		strings.Contains(reqURL, "/search/"):
		return "urn:li:page:d_flagship3_search_srp_people;" + browseTrackingID()
	case strings.Contains(reqURL, "voyagerIdentityDashProfiles"),
		strings.Contains(reqURL, "/profile/"),
		strings.Contains(reqURL, "/in/"):
		return "urn:li:page:d_flagship3_profile_view_base;" + browseTrackingID()
	case strings.Contains(reqURL, "voyagerMessagingDash"),
		strings.Contains(reqURL, "/messaging/"):
		return "urn:li:page:d_flagship3_messaging;" + browseTrackingID()
	case strings.Contains(reqURL, "voyagerGroupsDash"),
		strings.Contains(reqURL, "/groups/"):
		return "urn:li:page:d_flagship3_groups;" + browseTrackingID()
	case strings.Contains(reqURL, "/feed/"):
		return "urn:li:page:d_flagship3_feed;" + browseTrackingID()
	default:
		return "urn:li:page:d_flagship3_feed;" + browseTrackingID()
	}
}

// browseTrackingID returns a per-process tracking-id-like value that's stable
// for the duration of the run (Voyager's value is a request-scoped UUID, but
// any stable hex blob avoids the obvious "always 0" tell).
func browseTrackingID() string {
	return processBrowseID
}

// referrerFor picks a believable Referer header for the given request URL.
// For Voyager API calls, the Referer would typically be the page the user is
// currently viewing (search results, profile, etc.).
func referrerFor(reqURL string) string {
	switch {
	case strings.Contains(reqURL, "voyagerIdentityDashProfiles"),
		strings.Contains(reqURL, "/profile/"):
		// previous page when fetching a profile is usually the SRP
		return "https://www.linkedin.com/search/results/people/"
	case strings.Contains(reqURL, "voyagerSearchDashClusters"),
		strings.Contains(reqURL, "voyagerSearchDashReusable"),
		strings.Contains(reqURL, "/search/"):
		return "https://www.linkedin.com/feed/"
	case strings.Contains(reqURL, "/jobs-guest/"):
		return "https://www.linkedin.com/jobs/"
	case strings.Contains(reqURL, "/messaging/"):
		return "https://www.linkedin.com/messaging/"
	default:
		return "https://www.linkedin.com/feed/"
	}
}

// applyVoyagerHeaders sets the full Chrome-like header set on a request.
func (c *Client) applyVoyagerHeaders(headers map[string]string, reqURL string, post bool) {
	bp := c.browser
	headers["User-Agent"] = bp.UserAgent
	headers["Accept"] = "application/vnd.linkedin.normalized+json+2.1"
	headers["Accept-Language"] = bp.AcceptLanguage
	headers["Accept-Encoding"] = "gzip, deflate, br, zstd"
	headers["csrf-token"] = c.auth.CSRF
	headers["x-li-lang"] = "en_US"
	headers["x-restli-protocol-version"] = "2.0.0"
	headers["x-li-track"] = bp.xLiTrack()
	headers["x-li-page-instance"] = pageInstance(reqURL)
	headers["sec-ch-ua"] = bp.SecChUA
	headers["sec-ch-ua-mobile"] = bp.SecChUAMobile
	headers["sec-ch-ua-platform"] = bp.SecChUAPlatform
	headers["sec-fetch-dest"] = "empty"
	headers["sec-fetch-mode"] = "cors"
	headers["sec-fetch-site"] = "same-origin"
	headers["Referer"] = referrerFor(reqURL)
	headers["Origin"] = "https://www.linkedin.com"
	if post {
		headers["Content-Type"] = "application/json; charset=UTF-8"
		// Voyager's web bundle sends an empty PEM header on writes — match it.
		headers["x-li-pem-metadata"] = "Voyager - Messaging=write"
	} else {
		headers["x-li-pem-metadata"] = "Voyager - People SRP=search-results"
	}
}

// processBrowseID is set once at package init to a stable hex value used
// across all x-li-page-instance headers in this process. Real browsers use
// per-page-load UUIDs but any stable random-looking value is preferable to
// the literal "0" we used previously.
var processBrowseID = newProcessBrowseID()

func newProcessBrowseID() string {
	rng := newRNG()
	hex := func(n int) string {
		const alphabet = "0123456789abcdef"
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[rng.Intn(16)]
		}
		return string(b)
	}
	return hex(8) + "-" + hex(4) + "-" + hex(4) + "-" + hex(4) + "-" + hex(12)
}
