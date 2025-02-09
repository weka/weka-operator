package util

import (
	"math/rand"
)

func SliceEquals[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Shuffle shuffles any slice in place using Go generics.
func Shuffle[T any](slice []T) {
	// Seed the random number generator (consider seeding once in your app if needed)
	rand.Shuffle(len(slice), func(i, j int) {
		slice[i], slice[j] = slice[j], slice[i]
	})
}
