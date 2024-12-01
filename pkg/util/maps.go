package util

import (
	"cmp"
	"golang.org/x/exp/maps"
	"iter"
	"slices"
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
