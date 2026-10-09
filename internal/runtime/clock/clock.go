// Package clock abstracts time so timing-sensitive workflows (daemon restart cadence, polling)
// can be tested deterministically instead of against the wall clock.
package clock

import (
	"context"
	"time"
)

// Clock supplies the current time and a delay primitive. Implementations must be safe for
// concurrent use.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// System is the real, wall-clock backed Clock.
var System Clock = systemClock{}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Sleep waits for d or until ctx is cancelled, whichever comes first.
func Sleep(ctx context.Context, c Clock, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.After(d):
		return nil
	}
}

// Poll calls fn every interval until it returns (true, nil), fn returns an error, or ctx is
// cancelled.
func Poll(ctx context.Context, c Clock, interval time.Duration, fn func() (bool, error)) error {
	for {
		ok, err := fn()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if err := Sleep(ctx, c, interval); err != nil {
			return err
		}
	}
}
