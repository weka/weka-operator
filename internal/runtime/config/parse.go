package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

// CoreSelection is either automatic discovery or an explicit, ordered list of core IDs.
type CoreSelection struct {
	Auto bool
	IDs  []int
}

// parseExactBool matches Python `env == "true"`: only the exact string "true" is true.
func parseExactBool(e Env, key string) bool {
	return e.Get(key) == "true"
}

// parseFoldBool matches Python `env.lower() == "true"`, with def used when the key is unset.
func parseFoldBool(e Env, key string, def bool) bool {
	v, ok := e.Lookup(key)
	if !ok {
		return def
	}
	return strings.EqualFold(v, "true")
}

// parseOptionalFoldBool returns nil when the key is unset, so "do not modify" stays distinct.
func parseOptionalFoldBool(e Env, key string) *bool {
	v, ok := e.Lookup(key)
	if !ok {
		return nil
	}
	b := strings.EqualFold(v, "true")
	return &b
}

// parseNotFalse matches Python `env.lower() == "false"` inverted: anything but "false" is true.
func parseNotFalse(e Env, key string) bool {
	return strings.ToLower(e.Get(key)) != "false"
}

// parseIntStrict fails on non-numeric input; an unset or empty value yields def.
func parseIntStrict(e Env, key string, def int) (int, error) {
	v, ok := e.Lookup(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

// parsePortPermissive mirrors Python parse_port: unparsable or absent input means "unresolved" (0).
func parsePortPermissive(e Env, key string) int {
	n, err := strconv.Atoi(e.Get(key))
	if err != nil {
		return 0
	}
	return n
}

// parseCSV splits on "," and trims; an unset or empty value yields nil.
func parseCSV(e Env, key string) []string {
	v := e.Get(key)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// parseCoreSelection keeps "auto" distinct from an explicit ID list.
func parseCoreSelection(e Env, key string) (CoreSelection, error) {
	v, ok := e.Lookup(key)
	if !ok || v == "" || v == "auto" {
		return CoreSelection{Auto: true}, nil
	}
	ids := make([]int, 0, strings.Count(v, ",")+1)
	for _, p := range strings.Split(v, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return CoreSelection{}, fmt.Errorf("%s: %w", key, err)
		}
		ids = append(ids, n)
	}
	return CoreSelection{IDs: ids}, nil
}

// parseSelectors decodes a JSON selector array; an unset or empty value yields nil.
func parseSelectors(e Env, key string) ([]weka.NetworkSelector, error) {
	v := e.Get(key)
	if v == "" {
		return nil, nil
	}
	var sel []weka.NetworkSelector
	if err := json.Unmarshal([]byte(v), &sel); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return sel, nil
}
