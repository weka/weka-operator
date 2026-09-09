package util

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/reference"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
)

// maxEventNoteBytes matches the events.k8s.io v1 Event.Note field cap; an oversized note is rejected
// by the apiserver and never retried, so it must be capped before the recorder ever sends it.
const maxEventNoteBytes = 1024

type ConfigurationError struct {
	Err     error
	Message string
}

func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("configuration error: %s, %v", e.Message, e.Err)
}

// eventRecorder rewrites the dedup key and caps the note on every Eventf, so the fix cannot be
// bypassed by callers that hold the recorder directly.
type eventRecorder struct {
	events.EventRecorder
	scheme *runtime.Scheme
}

// WrapEventRecorder returns rec with stable dedup keys and note truncation applied to every event.
// scheme must resolve every kind events are emitted for (use mgr.GetScheme()).
func WrapEventRecorder(rec events.EventRecorder, scheme *runtime.Scheme) events.EventRecorder {
	return &eventRecorder{EventRecorder: rec, scheme: scheme}
}

func (r *eventRecorder) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
	full := fmt.Sprintf(note, args...)
	msg := full
	if len(msg) > maxEventNoteBytes {
		msg = strings.ToValidUTF8(msg[:maxEventNoteBytes-3], "") + "..."
	}
	// The digest covers the untruncated note: two long notes that differ only past the cap would
	// otherwise share a dedup key and the second would be dropped.
	r.EventRecorder.Eventf(stableEventRef(r.scheme, regarding, full), related, eventtype, reason, action, "%s", msg)
}

// RecordEvent emits a fixed-message event; a nil recorder is a no-op so unit-tested operations
// need no guards. "%s" keeps '%' in messages (e.g. err.Error()) from being read as a directive.
// Dedup-key rewriting and note truncation happen in the recorder, see WrapEventRecorder.
func RecordEvent(rec events.EventRecorder, obj runtime.Object, eventtype, reason, action, message string) {
	if rec == nil {
		return
	}
	rec.Eventf(obj, nil, eventtype, reason, action, "%s", message)
}

// stableEventRef builds an ObjectReference to obj for the events.k8s.io dedup key, with two changes
// from the raw reference: ResourceVersion is cleared, because status we heartbeat bumps it on every
// poll and would otherwise start a fresh dedup series each time; and FieldPath carries a digest of
// message, because the note itself isn't part of the key and a second distinct note under the same
// (type, action, reason, regarding) is otherwise silently dropped instead of opening a new event.
//
// A GetReference failure means obj's kind is not in the scheme; obj is passed through untouched so
// the wrapped recorder, which runs the same lookup, reports and drops the event itself.
func stableEventRef(scheme *runtime.Scheme, obj runtime.Object, message string) runtime.Object {
	ref, err := reference.GetReference(scheme, obj)
	if err != nil {
		return obj
	}
	ref = ref.DeepCopy()
	ref.ResourceVersion = ""
	digest := sha256.Sum256([]byte(message))
	ref.FieldPath = fmt.Sprintf("note-%x", digest[:8])
	return ref
}

func GetOperatorDeployment(ctx context.Context, k8sClient crclient.Client) (*appsv1.Deployment, error) {
	if config.Config.OperatorDeploymentName == "" {
		return nil, &ConfigurationError{Message: "Operator deployment name is not set"}
	}

	namespace, err := GetPodNamespace()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator namespace")
	}

	var deployment appsv1.Deployment
	err = k8sClient.Get(ctx, types.NamespacedName{
		Name:      config.Config.OperatorDeploymentName,
		Namespace: namespace,
	}, &deployment)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get operator deployment")
	}

	return &deployment, nil
}

func GetPodNamespace() (string, error) {
	if config.Config.OperatorPodNamespace != "" {
		return config.Config.OperatorPodNamespace, nil
	}
	namespace, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		if os.IsNotExist(err) && config.Config.DevMode {
			return config.Consts.DevModeNamespace, nil
		}
		return "", err
	}
	return string(namespace), nil
}

func IsEqualConfigMapData(cm1, cm2 *v1.ConfigMap) bool {
	return reflect.DeepEqual(cm1.Data, cm2.Data)
}

// GetKubeField retrieves a field from an unstructured object given a dot-separated path.

func GetKubeField(obj *unstructured.Unstructured, fieldPath string) (interface{}, error) {
	fields := strings.Split(strings.TrimPrefix(fieldPath, "."), ".")
	value, found, err := unstructured.NestedFieldCopy(obj.Object, fields...)
	if err != nil {
		return nil, fmt.Errorf("error retrieving field %s: %w", fieldPath, err)
	}
	if !found {
		return nil, fmt.Errorf("field %s not found", fieldPath)
	}
	return value, nil
}

// GetKubeFieldValue converts the retrieved field to any specified type.
func GetKubeFieldValue[T any](obj *unstructured.Unstructured, fieldPath string) (T, error) {
	value, err := GetKubeField(obj, fieldPath)
	if err != nil {
		var zero T
		return zero, err
	}
	result, ok := value.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("field %s is not of expected type", fieldPath)
	}
	return result, nil
}

// ConvertToUnstructured converts any typed object (e.g. corev1.Node, corev1.Pod) to an unstructured.Unstructured.
func ConvertToUnstructured[T runtime.Object](obj T) (*unstructured.Unstructured, error) {
	unstrMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("error converting object: %w", err)
	}
	return &unstructured.Unstructured{Object: unstrMap}, nil
}

// GetKubeObjectFieldValue combines conversion and field extraction.
// It accepts any runtime.Object (like corev1.Node or corev1.Pod) and returns the field value of the specified type.
func GetKubeObjectFieldValue[T any, K runtime.Object](obj K, fieldPath string) (T, error) {
	unstr, err := ConvertToUnstructured(obj)
	if err != nil {
		var zero T
		return zero, err
	}
	return GetKubeFieldValue[T](unstr, fieldPath)
}

func GetKubernetesVersion(restConfig *rest.Config) (string, error) {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return "", errors.Wrap(err, "failed to create kubernetes clientset")
	}

	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return "", errors.Wrap(err, "failed to get server version")
	}

	return version.String(), nil
}

// SanitizeK8sName replaces characters not allowed in DNS-1035 labels (dots) with hyphens.
func SanitizeK8sName(name string) string {
	return strings.ReplaceAll(name, ".", "-")
}
