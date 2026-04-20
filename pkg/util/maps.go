package util

import (
	"cmp"
	"iter"
	"slices"
	"strings"

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

func MergeMaps(originalMap, newMap map[string]string) map[string]string {
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
func MapMissingItems(originalMap, newMap map[string]string) map[string]string {
	retMap := make(map[string]string)
	for k, v := range newMap {
		if _, ok := originalMap[k]; !ok {
			retMap[k] = v
		}
	}
	return retMap
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

func RemoveKeysStartingWithPrefix(originalMap map[string]string, prefix string) map[string]string {
	retMap := make(map[string]string)
	for k, v := range originalMap {
		if !strings.HasPrefix(k, prefix) {
			retMap[k] = v
		}
	}

	return retMap
}
