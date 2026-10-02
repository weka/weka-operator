package config

import (
	"os"
	"strings"
)

// Env is an immutable snapshot of process environment variables. Tests construct it directly.
type Env map[string]string

// CaptureEnv snapshots os.Environ() once, at process entry.
func CaptureEnv() Env {
	e := make(Env)
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		e[k] = v
	}
	return e
}

// Get returns the value, or "" when unset.
func (e Env) Get(key string) string {
	return e[key]
}

// Lookup distinguishes an unset variable from one explicitly set to the empty string.
func (e Env) Lookup(key string) (string, bool) {
	v, ok := e[key]
	return v, ok
}
