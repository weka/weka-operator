package util

import (
	"math/rand"
	"sync"
	"time"
)

type ThrottlingSyncMap struct {
	syncMap   *TypedSyncMap[string, time.Time]
	partition string
}

type Throttler interface {
	// Store stores the current time for the given key
	ShouldRun(key string, interval time.Duration) bool
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

func (tsm *ThrottlingSyncMap) ShouldRun(key string, interval time.Duration) bool {
	// interval defines throttling interval, i.e how often sometihng should be allowed to be done
	key = tsm.partition + ":" + key

	if value, ok := tsm.syncMap.Load(key); ok {
		if time.Since(value) > interval {
			tsm.SetNow(key)
			return true
		}
		return false
	} else {
		// select random time within interval for initial value, so first time always will be trottled to distribute many such callers
		// TODO: Have a config for this behavior??
		milliSeconds := interval.Milliseconds()
		randomPreSetInterval := time.Duration(rand.Intn(int(milliSeconds)))
		newTime := time.Now().Add(-randomPreSetInterval)
		tsm.syncMap.LoadOrStore(key, newTime) // even if some other time set in parallel - safe to asume it would not allow us to run
		return false
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
