package util

import (
	"cmp"
	"iter"
	"slices"
	"sync"

	"golang.org/x/exp/maps"
)

func MapOrdered[K cmp.Ordered, V any](m map[K]V) iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		var keys = maps.Keys(m)

		slices.Sort(keys)

		for _, k := range keys {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}

func MergeMaps(originalMap map[string]string, newMap map[string]string) map[string]string {
	retMap := make(map[string]string)
	for k, v := range originalMap {
		retMap[k] = v
	}
	for k, v := range newMap {
		retMap[k] = v
	}
	return retMap
}

// MapMissingItems returns a map containing the items that are in the newMap but not in the originalMap
func MapMissingItems(originalMap map[string]string, newMap map[string]string) map[string]string {
	retMap := make(map[string]string)
	for k, v := range newMap {
		if _, ok := originalMap[k]; !ok {
			retMap[k] = v
		}
	}
	return retMap
}

type TypedSyncMap[K comparable, V any] struct {
	m sync.Map
}

func (tsm *TypedSyncMap[K, V]) Store(key K, value V) {
	tsm.m.Store(key, value)
}

func (tsm *TypedSyncMap[K, V]) Load(key K) (V, bool) {
	value, ok := tsm.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return value.(V), true
}

func (tsm *TypedSyncMap[K, V]) LoadOrStore(key K, value V) (V, bool) {
	actual, loaded := tsm.m.LoadOrStore(key, value)
	if loaded {
		return actual.(V), true
	}
	return value, false
}

func AreMapsEqual[K comparable, V comparable](a, b map[K]V) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if v2, ok := b[k]; !ok || v != v2 {
			return false
		}
	}

	return true
}
