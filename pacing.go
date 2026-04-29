package linkedin

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// HumanPacing describes a stochastic pacing policy that imitates a real
// browsing user. The defaults are calibrated for "stealthy daily prospecting":
// a request every ~6s with reading pauses and occasional long breaks, capped
// to a typical workday window.
//
// A zero value disables humanization (use the deterministic min-gap instead).
type HumanPacing struct {
	// BaseGap is the centre of the per-request delay distribution. Each gap is
	// sampled uniformly in [BaseGap*JitterLow, BaseGap*JitterHigh].
	BaseGap     time.Duration
	JitterLow   float64 // multiplier for the lower bound (e.g. 0.6)
	JitterHigh  float64 // multiplier for the upper bound (e.g. 2.5)

	// ReadingPauseEvery is the *expected* number of requests between
	// "reading pauses" — short interruptions that simulate the user reading a
	// profile. The actual interval is geometrically distributed.
	// Set to 0 to disable.
	ReadingPauseEvery int
	ReadingPauseMin   time.Duration
	ReadingPauseMax   time.Duration

	// DistractionEvery is the expected number of requests between long
	// "distraction" breaks (got coffee, took a call, etc.). Set to 0 to disable.
	DistractionEvery int
	DistractionMin   time.Duration
	DistractionMax   time.Duration

	// WorkingHours, if non-nil, restricts requests to a daily window. Outside
	// the window the pacer either sleeps until the window opens or returns
	// ErrOutsideHours, depending on SleepUntilOpen.
	WorkingHours    *WorkingHoursWindow
	SleepUntilOpen  bool

	// DailyBudget hard-caps the number of authenticated Voyager requests per
	// 24-hour rolling window. 0 disables the cap. Once exceeded, the pacer
	// returns ErrDailyBudget and the caller should checkpoint and exit.
	DailyBudget int
}

// WorkingHoursWindow defines a daily allowed window in a named timezone.
// StartHour and EndHour are 0-23, inclusive start, exclusive end (e.g.
// 8 → 21 = 8:00 AM through 8:59:59 PM).
type WorkingHoursWindow struct {
	Location  *time.Location
	StartHour int // inclusive
	EndHour   int // exclusive
	// Weekdays, if non-empty, restricts to the given days (time.Sunday=0..time.Saturday=6).
	Weekdays []time.Weekday
}

// IsOpen reports whether the given time falls inside the window.
func (w *WorkingHoursWindow) IsOpen(t time.Time) bool {
	if w == nil {
		return true
	}
	loc := w.Location
	if loc == nil {
		loc = time.Local
	}
	local := t.In(loc)
	if len(w.Weekdays) > 0 {
		ok := false
		for _, wd := range w.Weekdays {
			if local.Weekday() == wd {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	h := local.Hour()
	return h >= w.StartHour && h < w.EndHour
}

// NextOpen returns the next instant the window opens (>= t).
func (w *WorkingHoursWindow) NextOpen(t time.Time) time.Time {
	if w == nil {
		return t
	}
	loc := w.Location
	if loc == nil {
		loc = time.Local
	}
	cur := t.In(loc)
	for i := 0; i < 8; i++ {
		day := cur.AddDate(0, 0, i)
		open := time.Date(day.Year(), day.Month(), day.Day(), w.StartHour, 0, 0, 0, loc)
		if i == 0 && cur.After(open) {
			// already past today's opening
			h := cur.Hour()
			if h < w.EndHour {
				return cur
			}
			continue
		}
		// also enforce weekday filter
		if len(w.Weekdays) > 0 {
			ok := false
			for _, wd := range w.Weekdays {
				if open.Weekday() == wd {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		return open
	}
	return cur.Add(24 * time.Hour) // fallback
}

// DefaultBurnerPacing returns a maximally-conservative pacing profile suitable
// for a fresh burner account that must look like a single human prospector.
//
// Throughput: ~6–10 requests/min average, ~150–200 requests/hour, ~250
// requests/day under default budget.
func DefaultBurnerPacing(loc *time.Location) HumanPacing {
	if loc == nil {
		loc, _ = time.LoadLocation("America/Los_Angeles")
	}
	return HumanPacing{
		BaseGap:           5 * time.Second,
		JitterLow:         0.4,
		JitterHigh:        2.6,
		ReadingPauseEvery: 8,
		ReadingPauseMin:   8 * time.Second,
		ReadingPauseMax:   45 * time.Second,
		DistractionEvery:  90,
		DistractionMin:    90 * time.Second,
		DistractionMax:    8 * time.Minute,
		DailyBudget:       250,
		WorkingHours: &WorkingHoursWindow{
			Location:  loc,
			StartHour: 8,
			EndHour:   21,
			Weekdays:  []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		},
		SleepUntilOpen: true,
	}
}

// pacer manages humanized request timing and the daily request counter.
type pacer struct {
	policy HumanPacing
	rng    *rand.Rand

	mu             sync.Mutex
	lastReq        time.Time
	requestsToday  int
	dayKey         string // YYYY-MM-DD in policy.WorkingHours.Location
	reqSinceReadPause int
	reqSinceDistract  int
}

func newPacer(p HumanPacing) *pacer {
	return &pacer{
		policy: p,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// wait blocks until the next humanized slot. Returns one of:
//   - nil: ok to proceed
//   - ErrOutsideHours: outside working hours and SleepUntilOpen=false
//   - ErrDailyBudget: daily budget exhausted
//   - ctx.Err(): context cancelled
func (p *pacer) wait(ctx context.Context) error {
	if p == nil || p.policy.BaseGap <= 0 {
		return nil
	}

	p.mu.Lock()
	now := time.Now()
	loc := time.Local
	if p.policy.WorkingHours != nil && p.policy.WorkingHours.Location != nil {
		loc = p.policy.WorkingHours.Location
	}
	day := now.In(loc).Format("2006-01-02")
	if day != p.dayKey {
		p.dayKey = day
		p.requestsToday = 0
	}

	if p.policy.DailyBudget > 0 && p.requestsToday >= p.policy.DailyBudget {
		p.mu.Unlock()
		return ErrDailyBudget
	}

	if !p.policy.WorkingHours.IsOpen(now) {
		nextOpen := p.policy.WorkingHours.NextOpen(now)
		if !p.policy.SleepUntilOpen {
			p.mu.Unlock()
			return ErrOutsideHours
		}
		wait := time.Until(nextOpen)
		p.mu.Unlock()
		if wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		// Re-check on the next pass with the lock.
		return p.wait(ctx)
	}

	// Compute the gap from the last request.
	gap := p.sampleGapLocked()
	p.reqSinceReadPause++
	p.reqSinceDistract++

	if p.policy.DistractionEvery > 0 && p.reqSinceDistract >= p.policy.DistractionEvery && p.rng.Intn(p.policy.DistractionEvery) == 0 {
		gap = p.sampleDurationLocked(p.policy.DistractionMin, p.policy.DistractionMax)
		p.reqSinceDistract = 0
		p.reqSinceReadPause = 0
	} else if p.policy.ReadingPauseEvery > 0 && p.reqSinceReadPause >= p.policy.ReadingPauseEvery && p.rng.Intn(p.policy.ReadingPauseEvery) == 0 {
		gap = p.sampleDurationLocked(p.policy.ReadingPauseMin, p.policy.ReadingPauseMax)
		p.reqSinceReadPause = 0
	}

	var sleepFor time.Duration
	if !p.lastReq.IsZero() {
		elapsed := now.Sub(p.lastReq)
		if elapsed < gap {
			sleepFor = gap - elapsed
		}
	}
	// Schedule the slot deterministically to allow concurrent callers to queue.
	p.lastReq = now.Add(sleepFor)
	p.requestsToday++
	p.mu.Unlock()

	if sleepFor > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleepFor):
		}
	}
	return nil
}

func (p *pacer) sampleGapLocked() time.Duration {
	low := p.policy.JitterLow
	high := p.policy.JitterHigh
	if low <= 0 {
		low = 1.0
	}
	if high < low {
		high = low
	}
	mult := low + p.rng.Float64()*(high-low)
	return time.Duration(float64(p.policy.BaseGap) * mult)
}

func (p *pacer) sampleDurationLocked(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	span := int64(max - min)
	return min + time.Duration(p.rng.Int63n(span))
}

// requestsRemaining reports how many calls are left under the daily budget.
// 0 means budget disabled or already exceeded.
func (p *pacer) requestsRemaining() int {
	if p == nil || p.policy.DailyBudget <= 0 {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy.DailyBudget - p.requestsToday
}

// applyServerBackoff forces the next request to wait at least `wait` longer
// than the pacer would otherwise schedule it.
func (p *pacer) applyServerBackoff(wait time.Duration) {
	if p == nil || wait <= 0 {
		return
	}
	p.mu.Lock()
	earliest := time.Now().Add(wait)
	if p.lastReq.Before(earliest) {
		p.lastReq = earliest
	}
	p.mu.Unlock()
}
