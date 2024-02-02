package clock

import "time"

// PassiveClock is a clock that can read the current time only.
type PassiveClock interface {
	// Now returns the current local time.
	Now() time.Time
	// Since returns the time elapsed since t.
	Since(time.Time) time.Duration
}

// SystemClock implements the clock with the system's actual time.
type SystemClock struct{}

// Now implements PassiveClock.
func (s SystemClock) Now() time.Time {
	return time.Now()
}

// Since implements PassiveClock.
func (s SystemClock) Since(t time.Time) time.Duration {
	return time.Since(t)
}

// FakeClock implements the clock with a faked static time.
type FakeClock struct {
	Time time.Time
}

// Now implements PassiveClock.
func (f FakeClock) Now() time.Time {
	return f.Time
}

// Since implements PassiveClock.
func (f FakeClock) Since(t time.Time) time.Duration {
	return f.Time.Sub(t)
}
