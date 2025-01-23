package util

import (
	"math/rand"
	"sync"
	"time"
)

type ThrolltingSettings struct {
	// will update the timestamp on ThrottlingMap only if the step succeeded
	// (disabled by default)
	EnsureStepSuccess bool
}

type ThrottlingSyncMap struct {
	syncMap   *TypedSyncMap[string, time.Time]
	partition string
}

type Throttler interface {
	// Store stores the current time for the given key
	ShouldRun(key string, interval time.Duration, settings ThrolltingSettings) bool
	SetNow(key string)
}

func NewSyncMapThrottler() *ThrottlingSyncMap {
	return &ThrottlingSyncMap{
		partition: "",
		syncMap: &TypedSyncMap[string, time.Time]{
			m: sync.Map{},
		},
	}
}

func (tsm *ThrottlingSyncMap) ShouldRun(key string, interval time.Duration, settings ThrolltingSettings) bool {
	// interval defines throttling interval, i.e how often sometihng should be allowed to be done
	partKey := tsm.partition + ":" + key

	if value, ok := tsm.syncMap.Load(partKey); ok {
		if time.Since(value) > interval {
			if !settings.EnsureStepSuccess {
				tsm.SetNow(key)
			}
			return true
		}
		return false
	} else if !settings.EnsureStepSuccess {
		// select random time within interval for initial value, so first time always will be trottled to distribute many such callers
		// TODO: Have a config for this behavior??
		milliSeconds := interval.Milliseconds()
		randomPreSetInterval := time.Duration(rand.Intn(int(milliSeconds)))
		newTime := time.Now().Add(-randomPreSetInterval)
		tsm.syncMap.LoadOrStore(partKey, newTime) // even if some other time set in parallel - safe to asume it would not allow us to run
		return false
	} else {
		return true
	}
}

func (tsm *ThrottlingSyncMap) SetNow(key string) {
	key = tsm.partition + ":" + key
	// SetNow stores the current time for the given key
	tsm.syncMap.Store(key, time.Now())
}

func (tsm *ThrottlingSyncMap) WithPartition(partition string) Throttler {
	var newPartition string
	if tsm.partition != "" {
		newPartition = tsm.partition + ":" + partition
	} else {
		newPartition = partition
	}
	return &ThrottlingSyncMap{
		partition: newPartition,
		syncMap:   tsm.syncMap,
	}
}
