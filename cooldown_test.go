package linkedin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCooldownBlocksAllAuthenticatedRequests asserts that a cooldown'd
// client refuses requests with ErrInCooldown before any HTTP work happens.
// Reproduces the post-restriction safety case: even if the agent retries,
// nothing reaches LinkedIn until the operator-set deadline passes.
func TestCooldownBlocksAllAuthenticatedRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("upstream was hit despite cooldown")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Auth{LiAt: "x", CSRF: "y"},
		WithHTTPClient(&http.Client{Timeout: 2 * time.Second}),
		WithCooldownUntil(time.Now().Add(1*time.Hour)),
	)

	if !c.InCooldown() {
		t.Fatal("InCooldown() = false, want true")
	}

	_, err := c.makeRequest(context.Background(), srv.URL+"/voyager/api/test")
	if !errors.Is(err, ErrInCooldown) {
		t.Errorf("makeRequest err = %v, want ErrInCooldown", err)
	}
	if !strings.Contains(err.Error(), "until ") {
		t.Errorf("error message %q should embed the cooldown deadline", err)
	}

	_, err = c.makePostRequest(context.Background(), srv.URL+"/voyager/api/test", []byte(`{}`))
	if !errors.Is(err, ErrInCooldown) {
		t.Errorf("makePostRequest err = %v, want ErrInCooldown", err)
	}
}

// TestCooldownLapsesAtDeadline confirms requests resume once the deadline
// passes — no operator action required.
func TestCooldownLapsesAtDeadline(t *testing.T) {
	deadline := time.Now().Add(50 * time.Millisecond)
	c := New(Auth{LiAt: "x", CSRF: "y"}, WithCooldownUntil(deadline))

	if !c.InCooldown() {
		t.Fatal("InCooldown() = false at start, want true")
	}

	time.Sleep(80 * time.Millisecond)

	if c.InCooldown() {
		t.Errorf("InCooldown() = true after deadline, want false")
	}
	if err := c.checkCooldown(); err != nil {
		t.Errorf("checkCooldown after deadline = %v, want nil", err)
	}
}
