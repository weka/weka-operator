package util

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"fmt"
)

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

func HashStructShortUnsafe(s any) (string, error) {
	h, err := HashStruct(s)
	if err != nil {
		return "", err
	}
	return h[:16], nil
}
