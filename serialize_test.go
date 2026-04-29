package linkedin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRequestSlotSerializes asserts that 10 parallel callers do not all hit
// the upstream simultaneously — the per-client mutex must hold each through
// its full request lifecycle so LinkedIn never sees a burst.
//
// This is the test that catches the original "Claude Desktop fired 10 tool
// calls in parallel and the pacer queued the *gap* but not the *roundtrip*"
// regression.
func TestRequestSlotSerializes(t *testing.T) {
	const callers = 8
	const upstreamHandler = 50 * time.Millisecond

	var inFlight int32
	var maxInFlight int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip the warm-up GET /feed/ — it's not a Voyager call we care
		// about for this test, and the warmUp path doesn't go through
		// makeRequest's mutex (it has its own once-per-client gate).
		if strings.Contains(r.URL.Path, "/feed/") {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html><body>feed</body></html>"))
			return
		}
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			peak := atomic.LoadInt32(&maxInFlight)
			if cur <= peak || atomic.CompareAndSwapInt32(&maxInFlight, peak, cur) {
				break
			}
		}
		time.Sleep(upstreamHandler)
		atomic.AddInt32(&inFlight, -1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	// Build a client that points at the test server, with no pacing so
	// timing only reflects the per-client serialization.
	hc := &http.Client{Timeout: 5 * time.Second}
	c := New(Auth{LiAt: "x", CSRF: "y"}, WithHTTPClient(hc), WithRetry(1, time.Millisecond))
	// Force the warm-up "done" so warmUp doesn't re-enter the mutex via a
	// recursive call path during the test fan-out.
	c.warmedUp.Store(true)

	ctx := context.Background()
	target := srv.URL + "/voyager/api/test"

	var wg sync.WaitGroup
	wg.Add(callers)
	start := time.Now()
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			_, err := c.makeRequest(ctx, target)
			if err != nil {
				t.Errorf("makeRequest: %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if peak := atomic.LoadInt32(&maxInFlight); peak > 1 {
		t.Errorf("max concurrent in-flight = %d, want 1 — per-client serialization is broken", peak)
	}
	wantMin := time.Duration(callers) * upstreamHandler
	if elapsed < wantMin {
		t.Errorf("total elapsed = %s; with serialization want >= %s (%d × %s)", elapsed, wantMin, callers, upstreamHandler)
	}
}

// TestPendingRequestsObservable confirms PendingRequests reports the queue
// depth — used by host applications to surface "queued behind N" state.
func TestPendingRequestsObservable(t *testing.T) {
	c := New(Auth{LiAt: "x", CSRF: "y"})
	if got := c.PendingRequests(); got != 0 {
		t.Fatalf("PendingRequests at idle = %d, want 0", got)
	}

	// Acquire one slot; observe the depth from another goroutine.
	ctx := context.Background()
	release := c.acquireRequestSlot(ctx)
	if release == nil {
		t.Fatal("acquireRequestSlot returned nil at idle")
	}
	t.Cleanup(release)

	if got := c.PendingRequests(); got != 1 {
		t.Errorf("PendingRequests with one slot held = %d, want 1", got)
	}

	// Spin up 3 waiters; PendingRequests should reach 4 (1 holder + 3 waiters).
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel := c.acquireRequestSlot(ctx)
			if rel == nil {
				t.Errorf("waiter got nil release")
				return
			}
			rel()
		}()
	}

	// Give waiters a moment to register on the queue.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c.PendingRequests() == 4 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := c.PendingRequests(); got != 4 {
		t.Errorf("PendingRequests with 3 waiters = %d, want 4", got)
	}
}
