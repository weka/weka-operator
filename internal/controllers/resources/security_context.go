package resources

import (
	"context"
	"encoding/json"
	"os"
	"reflect"

	"github.com/weka/go-weka-observability/instrumentation"
	corev1 "k8s.io/api/core/v1"
)

// GetSecurityProfile parses WEKA_POD_SECURITY_CONTEXT and returns the pod-level
// securityContext to apply to produced pods. Called on every pod creation —
// no caching. Returns nil when the env var is unset / empty / "{}" / "null" /
// all-zero or contains malformed JSON. Each call returns a freshly allocated
// struct, safe for the caller to mutate.
//
// Malformed JSON is logged as an error and returns nil. The log fires per
// pod-creation call by design — repeated errors make a misconfigured
// values.yaml impossible to miss.
func GetSecurityProfile() *corev1.PodSecurityContext {
	raw := os.Getenv("WEKA_POD_SECURITY_CONTEXT")
	if raw == "" {
		return nil
	}
	var sc corev1.PodSecurityContext
	if err := json.Unmarshal([]byte(raw), &sc); err != nil {
		instrumentation.CurrentSpanLogger(context.Background()).Error(err, "WEKA_POD_SECURITY_CONTEXT contains invalid JSON; security context injection disabled")
		return nil
	}
	if reflect.DeepEqual(sc, corev1.PodSecurityContext{}) {
		return nil
	}
	return &sc
}
