package util

import (
	"cmp"
	"iter"
	"slices"

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
