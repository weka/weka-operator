package wekacontainer

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// A gap longer than this between failures ends the streak.
const channelStreakGap = 2 * time.Minute

// channelFailures holds, per container UID, the current streak of exec channel failures.
var channelFailures = &failureTracker{}

type failureTracker struct{ streaks sync.Map }

type failureStreak struct{ first, last time.Time }

// failure records a failure at now and returns the streak start; a stale streak restarts.
func (t *failureTracker) failure(uid types.UID, now time.Time) time.Time {
	s := failureStreak{first: now, last: now}
	if v, ok := t.streaks.Load(uid); ok {
		if prev, ok := v.(failureStreak); ok && now.Sub(prev.last) <= channelStreakGap {
			s.first = prev.first
		}
	}
	t.streaks.Store(uid, s)
	return s.first
}

func (t *failureTracker) clear(uid types.UID) { t.streaks.Delete(uid) }
