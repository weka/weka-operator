package admission

import "strings"

// Mode is the effective per-request outcome for one policy: Warn surfaces
// the violation as an admission Warning and admits; Error rejects.
type Mode int

const (
	Warn Mode = iota
	Error
)

// PolicyDefaults is the (strict, relaxed) Mode pair baked into the operator
// for a single policy.
type PolicyDefaults struct {
	Strict  Mode
	Relaxed Mode
}

// modeFor resolves the effective Mode. Override "warn"|"error" wins;
// otherwise picks defaults.Strict or defaults.Relaxed by mode.
// Comparisons are case-insensitive.
func modeFor(mode, override string, defaults PolicyDefaults) Mode {
	switch strings.ToLower(override) {
	case "warn":
		return Warn
	case "error":
		return Error
	}
	if strings.EqualFold(mode, "strict") {
		return defaults.Strict
	}
	return defaults.Relaxed
}
