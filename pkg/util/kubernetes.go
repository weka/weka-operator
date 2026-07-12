package util

import (
	"context"
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
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/config"
)

type ConfigurationError struct {
	Err     error
	Message string
}

func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("configuration error: %s, %v", e.Message, e.Err)
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
