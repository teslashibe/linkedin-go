package linkedin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetUserPosts_URNSkipsProfileHop(t *testing.T) {
	var profileHits, postsHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/feed/"):
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>ok</html>"))
		case strings.Contains(r.URL.RawQuery, "vanityName:") || strings.Contains(r.URL.Path, "graphql"):
			profileHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"included":[{"$type":"com.linkedin.voyager.dash.identity.profile.Profile","entityUrn":"urn:li:fsd_profile:ABC","publicIdentifier":"alice","firstName":"A","lastName":"B"}]}`))
		case strings.Contains(r.URL.Path, "profileUpdatesV2"):
			postsHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"included":[{"entityUrn":"urn:li:activity:1","$type":"com.linkedin.voyager.feed.render.UpdateV2","commentary":{"text":{"text":"hi"}}}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := testClientAgainst(srv)
	c.warmedUp.Store(true)

	_, err := c.GetUserPosts(context.Background(), UserPostParams{
		Member: "urn:li:fsd_profile:ABC",
		Count:  1,
	})
	if err != nil {
		t.Fatalf("GetUserPosts: %v", err)
	}
	if profileHits.Load() != 0 {
		t.Fatalf("profile hops = %d, want 0 when member URN provided", profileHits.Load())
	}
	if postsHits.Load() != 1 {
		t.Fatalf("posts hops = %d, want 1", postsHits.Load())
	}
}

func TestGetUserPosts_VanityCacheHit(t *testing.T) {
	var profileHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/feed/"):
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>ok</html>"))
		case strings.Contains(r.URL.RawQuery, "vanityName:") || strings.Contains(r.URL.Path, "graphql"):
			profileHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"included":[{"$type":"com.linkedin.voyager.dash.identity.profile.Profile","entityUrn":"urn:li:fsd_profile:ABC","publicIdentifier":"alice","firstName":"A","lastName":"B"}]}`))
		case strings.Contains(r.URL.Path, "profileUpdatesV2"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"included":[{"entityUrn":"urn:li:activity:1","$type":"com.linkedin.voyager.feed.render.UpdateV2","commentary":{"text":{"text":"hi"}}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := testClientAgainst(srv)
	c.warmedUp.Store(true)

	for i := 0; i < 2; i++ {
		if _, err := c.GetUserPosts(context.Background(), UserPostParams{Member: "alice", Count: 1}); err != nil {
			t.Fatalf("GetUserPosts[%d]: %v", i, err)
		}
	}
	if profileHits.Load() != 1 {
		t.Fatalf("profile hops = %d, want 1 (second call uses session cache)", profileHits.Load())
	}
}

func TestGetUserPosts_InsufficientDeadlineBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("upstream should not be hit when deadline budget is insufficient")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := testClientAgainst(srv)
	c.warmedUp.Store(false)
	c.pacer = newPacer(HumanPacing{
		BaseGap:    5 * time.Second,
		JitterLow:  1,
		JitterHigh: 2.6,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.GetUserPosts(ctx, UserPostParams{Member: "alice", Count: 1})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if !strings.Contains(err.Error(), "insufficient deadline budget") {
		t.Fatalf("err = %q, want insufficient deadline budget message", err)
	}
}

func TestWrapWarmUpTransportErr_DeadlineAndEOF(t *testing.T) {
	cases := []error{
		context.DeadlineExceeded,
		io.ErrUnexpectedEOF,
		errors.New("Get \"https://www.linkedin.com/feed/\": unexpected EOF"),
	}
	for _, in := range cases {
		err := wrapWarmUpTransportErr(in)
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("wrapWarmUpTransportErr(%v) = %v, want ErrTimeout", in, err)
		}
	}
}

type hostRewriteTransport struct {
	host string
	base http.RoundTripper
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = "http"
	cloned.URL.Host = t.host
	cloned.Host = t.host
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(cloned)
}

func testClientAgainst(srv *httptest.Server) *Client {
	host := strings.TrimPrefix(srv.URL, "http://")
	hc := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &hostRewriteTransport{host: host},
	}
	return New(Auth{LiAt: "x", CSRF: "y"}, WithHTTPClient(hc), WithRetry(1, time.Millisecond))
}
