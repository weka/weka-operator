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
