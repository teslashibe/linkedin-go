package linkedin

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

func testAccount(id, proxy string) Account {
	return Account{
		ID:       id,
		Auth:     Auth{LiAt: "li-" + id, CSRF: "csrf-" + id},
		ProxyURL: proxy,
		Pacing: &HumanPacing{
			BaseGap:     time.Millisecond,
			JitterLow:   1,
			JitterHigh:  1,
			DailyBudget: 100,
		},
	}
}

func TestNewPoolRequiresProxyWhenMulti(t *testing.T) {
	_, err := NewPool([]Account{
		testAccount("a", ""),
		testAccount("b", "http://proxy.example:8000"),
	})
	if err == nil {
		t.Fatal("expected error for missing proxy on multi-account pool")
	}
}

func TestNewPoolSingleAccountAllowsEmptyProxy(t *testing.T) {
	p, err := NewPool([]Account{testAccount("solo", "")})
	if err != nil {
		t.Fatal(err)
	}
	if p.Len() != 1 {
		t.Fatalf("len=%d", p.Len())
	}
}

func TestPoolAcquireRotatesAndReleases(t *testing.T) {
	p, err := NewPool([]Account{
		testAccount("a", "http://a.proxy:1"),
		testAccount("b", "http://b.proxy:2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l1, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if l1.ID == l2.ID {
		t.Fatalf("expected different accounts, got %q twice", l1.ID)
	}
	if l1.Client.proxyURL == nil || l2.Client.proxyURL == nil {
		t.Fatal("expected sticky proxies on both clients")
	}
	if l1.Client.proxyURL.Host == l2.Client.proxyURL.Host {
		t.Fatalf("proxies should differ: %s", l1.Client.proxyURL.Host)
	}
	l1.Release()
	l2.Release()

	// Both free again — acquire should succeed.
	l3, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	l3.Release()
}

func TestPoolSkipsBudgetExhausted(t *testing.T) {
	p, err := NewPool([]Account{
		testAccount("a", "http://a.proxy:1"),
		testAccount("b", "http://b.proxy:2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Exhaust account a by zeroing remaining budget.
	p.members[0].client.pacer.policy.DailyBudget = 1
	p.members[0].client.pacer.requestsToday = 1

	ctx := context.Background()
	l, err := p.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "b" {
		t.Fatalf("got %q, want b (a exhausted)", l.ID)
	}
	l.Release()

	// Exhaust b too → none available.
	p.members[1].client.pacer.policy.DailyBudget = 1
	p.members[1].client.pacer.requestsToday = 1
	if _, err := p.tryAcquire(); !errors.Is(err, ErrNoAccountAvailable) {
		t.Fatalf("err=%v, want ErrNoAccountAvailable", err)
	}
}

func TestLeaseReportErrorSkipsBudget(t *testing.T) {
	p, err := NewPool([]Account{testAccount("a", "http://a.proxy:1")})
	if err != nil {
		t.Fatal(err)
	}
	l, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l.ReportError(ErrDailyBudget)
	l.Release()
	if _, err := p.tryAcquire(); !errors.Is(err, ErrNoAccountAvailable) {
		t.Fatalf("err=%v after budget report", err)
	}
	st := p.Status()
	if len(st) != 1 || st[0].Available {
		t.Fatalf("status=%+v", st)
	}
}

func TestWithProxyURL(t *testing.T) {
	c := New(Auth{LiAt: "x", CSRF: "y"}, WithProxyURL("http://user:pass@sticky.example:9000"))
	if c.proxyURL == nil || c.proxyURL.Host != "sticky.example:9000" {
		t.Fatalf("proxy=%v", c.proxyURL)
	}
	u, _ := url.Parse("http://other:1")
	c2 := New(Auth{LiAt: "x", CSRF: "y"}, WithProxy(u))
	if c2.proxyURL.Host != "other:1" {
		t.Fatalf("proxy=%v", c2.proxyURL)
	}
}
