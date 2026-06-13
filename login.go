package linkedin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Credential login for LinkedIn (#267).
//
// Reverse-engineered from the live www.linkedin.com/login form: the page sets a
// JSESSIONID cookie and embeds a set of hidden inputs (loginCsrfParam,
// csrfToken, pageInstance, etc.). We GET the login page, scrape every hidden
// field, then POST them verbatim plus session_key (email) + session_password to
// /checkpoint/lg/login-submit. On success LinkedIn sets the li_at cookie (and
// updates JSESSIONID); those feed straight into New(Auth{...}).
//
// LinkedIn frequently interposes a checkpoint challenge (email PIN / captcha)
// from unfamiliar IPs — Login surfaces ErrChallengeRequired so the caller can
// route the user through it (e.g. fetch the email code) rather than failing
// opaquely. A residential egress (ProxyURL) greatly reduces challenge rate.

const (
	loginPageURL   = "https://www.linkedin.com/login"
	loginSubmitURL = "https://www.linkedin.com/checkpoint/lg/login-submit"
	loginUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)

var reHiddenInput = regexp.MustCompile(`<input[^>]*\btype="hidden"[^>]*>`)
var reAnyInput = regexp.MustCompile(`<input[^>]*>`)
var reInputName = regexp.MustCompile(`\bname="([^"]*)"`)
var reInputValue = regexp.MustCompile(`\bvalue="([^"]*)"`)
var reFormAction = regexp.MustCompile(`<form[^>]*\baction="([^"]*)"`)

// LoginParams holds LinkedIn credentials.
type LoginParams struct {
	Email    string
	Password string
	// UserAgent overrides the browser UA used for the login + minted session.
	UserAgent string
	// ProxyURL routes the login through an HTTP/S proxy (residential egress).
	ProxyURL string
	// VerificationCode is an email/SMS PIN for LinkedIn's checkpoint challenge.
	// When LinkedIn interposes a challenge, Login completes it with this code.
	VerificationCode string
	// VerificationProvider fetches the PIN on demand (e.g. polls Gmail for the
	// "Here's your verification code" email) when a challenge appears and no
	// static VerificationCode was supplied. Enables unattended login.
	VerificationProvider func(ctx context.Context) (string, error)
}

// LoginResult is the minted session, ready for New.
type LoginResult struct {
	Auth      Auth
	UserAgent string
}

// Login authenticates with an email + password and returns minted session
// cookies (li_at, JSESSIONID/CSRF). It does not require pre-existing cookies.
func Login(ctx context.Context, p LoginParams) (*LoginResult, error) {
	if strings.TrimSpace(p.Email) == "" || p.Password == "" {
		return nil, fmt.Errorf("linkedin: email and password are required")
	}
	ua := p.UserAgent
	if ua == "" {
		ua = loginUserAgent
	}
	debug := strings.TrimSpace(os.Getenv("LINKEDIN_LOGIN_DEBUG")) != ""

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	if p.ProxyURL != "" {
		if parsed, err := url.Parse(p.ProxyURL); err == nil {
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.Proxy = http.ProxyURL(parsed)
			hc.Transport = tr
		}
	}

	// 1. Load the login page: seeds JSESSIONID/bcookie and carries the hidden
	//    form fields (loginCsrfParam, csrfToken, …) the submit requires.
	hidden, err := fetchLoginForm(ctx, hc, ua)
	if err != nil {
		return nil, fmt.Errorf("linkedin: load login page: %w", err)
	}

	// 2. POST the full hidden-field set + credentials.
	form := url.Values{}
	for k, v := range hidden {
		form.Set(k, v)
	}
	form.Set("session_key", p.Email)
	form.Set("session_password", p.Password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginSubmitURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Origin", "https://www.linkedin.com")
	req.Header.Set("Referer", loginPageURL)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linkedin: login request: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	if debug {
		fmt.Fprintf(os.Stderr, "[linkedin-login-debug] submit status=%d finalURL=%s\n", resp.StatusCode, resp.Request.URL.String())
	}

	auth := collectAuth(jar)

	// li_at present => authenticated.
	if auth.LiAt != "" {
		return finishAuth(jar, ua), nil
	}

	finalURL := ""
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	low := strings.ToLower(string(body))

	// Checkpoint challenge (email/SMS PIN): complete it with the code when we
	// have a way to obtain one, then re-check for li_at.
	isChallenge := strings.Contains(finalURL, "checkpoint/challenge") ||
		strings.Contains(finalURL, "/checkpoint/lg/login-challenge") ||
		strings.Contains(low, "checkpoint/challenge")
	if isChallenge {
		code := strings.TrimSpace(p.VerificationCode)
		if code == "" && p.VerificationProvider != nil {
			c, perr := p.VerificationProvider(ctx)
			if perr != nil {
				return nil, fmt.Errorf("linkedin: fetch verification code: %w", perr)
			}
			code = strings.TrimSpace(c)
		}
		if code == "" {
			return nil, ErrChallengeRequired
		}
		if cerr := completeChallenge(ctx, hc, ua, finalURL, string(body), code, debug); cerr != nil {
			return nil, cerr
		}
		if a := collectAuth(jar); a.LiAt != "" {
			return finishAuth(jar, ua), nil
		}
		return nil, fmt.Errorf("linkedin: challenge submitted but no session established (wrong/expired code?)")
	}

	switch {
	case strings.Contains(low, "wrong email") || strings.Contains(low, "couldn’t find") || strings.Contains(low, "couldn't find") || strings.Contains(low, "please enter a valid"):
		return nil, fmt.Errorf("linkedin: wrong email or password")
	case strings.Contains(low, "captcha") || strings.Contains(low, "are you a robot") || strings.Contains(low, "security verification"):
		return nil, ErrChallengeRequired
	default:
		return nil, fmt.Errorf("linkedin: login did not establish a session (status %d)", resp.StatusCode)
	}
}

func finishAuth(jar http.CookieJar, ua string) *LoginResult {
	auth := collectAuth(jar)
	auth.CSRF = csrfFromJar(jar)
	auth.JSESSIONID = auth.CSRF
	return &LoginResult{Auth: auth, UserAgent: ua}
}

// completeChallenge submits the PIN to LinkedIn's checkpoint challenge form.
// It parses the challenge page's form action + hidden fields, fills the PIN
// field, and posts. challengePage is the body already fetched at challengeURL.
func completeChallenge(ctx context.Context, hc *http.Client, ua, challengeURL, challengePage, code string, debug bool) error {
	action := reFormAction.FindStringSubmatch(challengePage)
	submitURL := "https://www.linkedin.com/checkpoint/challenge/verify"
	if action != nil && action[1] != "" {
		submitURL = absoluteURL(htmlUnescape(action[1]))
	}

	form := url.Values{}
	for _, tag := range reAnyInput.FindAllString(challengePage, -1) {
		nameM := reInputName.FindStringSubmatch(tag)
		if nameM == nil || nameM[1] == "" {
			continue
		}
		val := ""
		if valM := reInputValue.FindStringSubmatch(tag); valM != nil {
			val = htmlUnescape(valM[1])
		}
		form.Set(nameM[1], val)
	}
	// Fill the PIN field. LinkedIn names it "pin" (occasionally "0-pin").
	switch {
	case form.Has("pin"):
		form.Set("pin", code)
	case form.Has("0-pin"):
		form.Set("0-pin", code)
	default:
		form.Set("pin", code)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Origin", "https://www.linkedin.com")
	req.Header.Set("Referer", challengeURL)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("linkedin: challenge submit: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if debug {
		fin := ""
		if resp.Request != nil && resp.Request.URL != nil {
			fin = resp.Request.URL.String()
		}
		fmt.Fprintf(os.Stderr, "[linkedin-login-debug] challenge submit status=%d finalURL=%s bodyLen=%d\n", resp.StatusCode, fin, len(body))
	}
	return nil
}

func absoluteURL(href string) string {
	if strings.HasPrefix(href, "http") {
		return href
	}
	if strings.HasPrefix(href, "/") {
		return "https://www.linkedin.com" + href
	}
	return "https://www.linkedin.com/" + href
}

func fetchLoginForm(ctx context.Context, hc *http.Client, ua string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginPageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()

	hidden := map[string]string{}
	for _, tag := range reHiddenInput.FindAllString(string(body), -1) {
		nameM := reInputName.FindStringSubmatch(tag)
		if nameM == nil || nameM[1] == "" {
			continue
		}
		val := ""
		if valM := reInputValue.FindStringSubmatch(tag); valM != nil {
			val = htmlUnescape(valM[1])
		}
		// Later occurrences win (the active form is last in the document).
		hidden[nameM[1]] = val
	}
	if hidden["loginCsrfParam"] == "" {
		return nil, fmt.Errorf("loginCsrfParam not found on login page")
	}
	return hidden, nil
}

func collectAuth(jar http.CookieJar) Auth {
	var a Auth
	for _, ck := range jar.Cookies(linkedinBaseURL) {
		switch ck.Name {
		case "li_at":
			a.LiAt = ck.Value
		}
	}
	return a
}

func csrfFromJar(jar http.CookieJar) string {
	for _, ck := range jar.Cookies(linkedinBaseURL) {
		if ck.Name == "JSESSIONID" {
			return strings.Trim(ck.Value, `"`)
		}
	}
	return ""
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">")
	return r.Replace(s)
}
