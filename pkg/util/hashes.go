package util

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

type hashMapEntry struct {
	Key   string
	Value string
}

// HashableMap is a struct that can replace a map in order to be hashed
// Create a new HashableMap instance with NewHashableMap from a map[string]string and use it in place of a map
type HashableMap struct {
	Entries []hashMapEntry
}

// Allows to compare two HashableMap instances
func (hm *HashableMap) Equals(other *HashableMap) bool {
	if len(hm.Entries) != len(other.Entries) {
		return false
	}
	for i, entry := range hm.Entries {
		if entry.Key != other.Entries[i].Key {
			return false
		}
		if entry.Value != other.Entries[i].Value {
			return false
		}
	}
	return true
}

func NewHashableMap(m map[string]string) *HashableMap {
	hm := &HashableMap{
		Entries: make([]hashMapEntry, 0, len(m)),
	}
	for k, v := range m {
		e := hashMapEntry{
			Key:   k,
			Value: v,
		}
		hm.Entries = append(hm.Entries, e)
	}

	// sort the keys alphabetically to ensure the order is deterministic
	sort.Slice(hm.Entries, func(i, j int) bool {
		return hm.Entries[i].Key < hm.Entries[j].Key
	})
	return hm
}

// checkForMaps recursively checks if a value contains any maps in its fields
// Maps are not supported because:
// 1. Go's map iteration order is not deterministic - the same map can produce different hash values
// 2. gob encoding of maps may vary between runs due to non-deterministic key ordering
// 3. This would break the requirement for consistent, reproducible hashes across multiple runs
func checkForMaps(v reflect.Value) error {
	switch v.Kind() {
	case reflect.Map:
		// Maps have non-deterministic iteration order in Go, which would cause
		// the same logical map to produce different hashes on different runs.
		// This violates the fundamental requirement of hash consistency.
		return errors.New("HashStruct does not support structs containing maps. Use HashableMap instead")
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Field(i)
			if field.CanInterface() {
				if err := checkForMaps(field); err != nil {
					return err
				}
			}
		}
	case reflect.Ptr:
		if !v.IsNil() {
			if err := checkForMaps(v.Elem()); err != nil {
				return err
			}
		}
	case reflect.Interface:
		if !v.IsNil() {
			if err := checkForMaps(v.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if err := checkForMaps(v.Index(i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// HashStruct generates a hash of a struct
// NOTE: This function returns an error if the struct contains maps. For structs with maps, use HashableMap instead.
func HashStruct(s any) (string, error) {
	// Check for maps in the struct before proceeding
	v := reflect.ValueOf(s)
	if err := checkForMaps(v); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(s)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(buf.Bytes())
	return fmt.Sprintf("%x", hash), nil
}

// Generates SHA-256 hash and takes the first n characters
func GetHash(s string, n int) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])[:n]
}
