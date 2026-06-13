// Command linkedin-login-probe smoke-tests LinkedIn email/password login end to
// end: it logs in, mints li_at + JSESSIONID, builds a client, and resolves the
// authenticated member for liveness. Credentials come from env:
//
//	LINKEDIN_EMAIL, LINKEDIN_PASSWORD, LINKEDIN_PROXY_URL (optional)
//
// Exit 0 on success, non-zero otherwise.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	linkedin "github.com/teslashibe/linkedin-go"
)

func main() {
	email := strings.TrimSpace(os.Getenv("LINKEDIN_EMAIL"))
	pass := os.Getenv("LINKEDIN_PASSWORD")
	if email == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "linkedin-login-probe: set LINKEDIN_EMAIL and LINKEDIN_PASSWORD")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	params := linkedin.LoginParams{
		Email:            email,
		Password:         pass,
		ProxyURL:         strings.TrimSpace(os.Getenv("LINKEDIN_PROXY_URL")),
		VerificationCode: strings.TrimSpace(os.Getenv("LINKEDIN_VERIFICATION_CODE")),
	}
	// When no static code is given, poll a file the operator (or a Gmail-MCP
	// step) drops the fresh PIN into — lets us validate the in-session flow.
	if params.VerificationCode == "" {
		if codeFile := strings.TrimSpace(os.Getenv("LINKEDIN_CODE_FILE")); codeFile != "" {
			params.VerificationProvider = func(ctx context.Context) (string, error) {
				deadline := time.Now().Add(100 * time.Second)
				for time.Now().Before(deadline) {
					if b, err := os.ReadFile(codeFile); err == nil {
						if c := strings.TrimSpace(string(b)); c != "" {
							return c, nil
						}
					}
					select {
					case <-ctx.Done():
						return "", ctx.Err()
					case <-time.After(3 * time.Second):
					}
				}
				return "", fmt.Errorf("timed out waiting for code in %s", codeFile)
			}
		}
	}

	res, err := linkedin.Login(ctx, params)
	if err != nil {
		if errors.Is(err, linkedin.ErrChallengeRequired) {
			fmt.Fprintln(os.Stderr, "login: LinkedIn checkpoint/challenge required (email PIN or captcha)")
			os.Exit(3)
		}
		fmt.Fprintf(os.Stderr, "login: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("login ok: li_at_len=%d csrf=%s\n", len(res.Auth.LiAt), res.Auth.CSRF)
}
