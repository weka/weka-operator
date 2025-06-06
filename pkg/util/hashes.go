package util

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// HashStruct generates a hash of a struct
// NOTE: This function is not deterministic for the cases when the struct contains maps and unset strings.
// For deterministic hashing of structs with maps, use HashableMap instead.
func HashStruct(s any) (string, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(s)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(buf.Bytes())
	return fmt.Sprintf("%x", hash), nil
}

func DeterministicHashStruct(s any) (string, error) {
	jsonData, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(jsonData)
	return fmt.Sprintf("%x", hash), nil
}

// Generates SHA-256 hash and takes the first n characters
func GetHash(s string, n int) string {
	hash := sha256.Sum256([]byte(s))
	return hex.EncodeToString(hash[:])[:n]
}
