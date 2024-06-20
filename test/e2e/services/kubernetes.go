package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"

	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type Kubernetes interface {
	GetClient(ctx context.Context) (client.Client, error)
}

func NewKubernetes(jobless Jobless, clusterName string, kubeConfig string) Kubernetes {
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "charts", "weka-operator", "crds")},
		ErrorIfCRDPathMissing: true,
		UseExistingCluster:    func(b bool) *bool { return &b }(true),
	}
	return &kubernetes{
		Jobless:     jobless,
		ClusterName: clusterName,
		KubeConfig:  kubeConfig,
		Environment: environment,
	}
}

type kubernetes struct {
	Environment *envtest.Environment
	Jobless     Jobless
	ClusterName string

	Client     client.Client
	RestConfig *rest.Config
	KubeConfig string
}

type EnvironmentError struct {
	Variable string
	Value    string
	Message  string
}

func (e *EnvironmentError) Error() string {
	return fmt.Sprintf("%s: %s=%s", e.Message, e.Variable, e.Value)
}

type KubernetesError struct {
	Message string
	Err     error
}

func (e *KubernetesError) Error() string {
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (k *kubernetes) Setup(ctx context.Context) error {
	kubebuilderRelease := "1.26.0"
	kubebuilderOs := runtime.GOOS
	kubebuilderArch := runtime.GOARCH
	kubebuilderVersion := fmt.Sprintf("%s-%s-%s", kubebuilderRelease, kubebuilderOs, kubebuilderArch)
	os.Setenv("KUBEBUILDER_ASSETS", filepath.Join("..", "..", "..", "..", "bin", "k8s", kubebuilderVersion))

	os.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc.cluster.local")
	os.Setenv("KUBERNETES_SERVICE_PORT", "443")
	os.Setenv("UNIT_TEST", "true")

	if os.Getenv("KUBECONFIG") == "" {
		return &EnvironmentError{
			Variable: "KUBECONFIG",
			Value:    "",
			Message:  "KUBECONFIG is not set",
		}
	}

	_, err := k.GetClient(ctx)
	if err != nil {
		return &KubernetesError{
			Message: "failed to create client",
			Err:     err,
		}
	}

	return nil
}

func (k *kubernetes) TearDown(ctx context.Context) error {
	return k.Environment.Stop()
}

func (k *kubernetes) GetClient(ctx context.Context) (client.Client, error) {
	if k.Client != nil {
		return k.Client, nil
	}

	cfg, err := k.StartEnvTest(ctx)
	if err != nil {
		return nil, &KubernetesError{
			Message: "GetClient failed to start test environment",
			Err:     err,
		}
	}

	if err := wekav1alpha1.AddToScheme(scheme.Scheme); err != nil {
		return nil, &KubernetesError{
			Message: "failed to add scheme",
			Err:     err,
		}
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, &KubernetesError{
			Message: "GetClient failed to create client",
			Err:     err,
		}
	}
	k.Client = c
	return k.Client, nil
}

func (k *kubernetes) StartEnvTest(ctx context.Context) (*rest.Config, error) {
	if k.RestConfig == nil {
		if err := k.ExportKubeConfig(ctx); err != nil {
			return nil, &KubernetesError{
				Message: "failed to start test environment",
				Err:     err,
			}
		}

		cfg, err := k.Environment.Start()
		if err != nil {
			return nil, &KubernetesError{
				Message: "failed to start test environment",
				Err:     err,
			}
		}
		k.RestConfig = cfg
	}
	return k.RestConfig, nil
}

func (k *kubernetes) ExportKubeConfig(ctx context.Context) error {
	path, err := k.GetKubeConfigPath(ctx)
	if err != nil {
		return &KubernetesError{
			Message: "ExportKubeConfig failed while getting kubeconfig path",
			Err:     err,
		}
	}
	if path == "" {
		return &KubernetesError{
			Message: "ExportKubeConfig failed",
			Err:     fmt.Errorf("kubeconfig path is empty"),
		}
	}

	os.Setenv("KUBECONFIG", path)
	return nil
}

func (k *kubernetes) GetKubeConfigPath(ctx context.Context) (string, error) {
	if k.KubeConfig == "" {

		if k.Jobless == nil {
			return "", errors.New("GetKubeConfigPath failed: Jobless is nil")
		}

		clusterName := k.ClusterName
		path, err := k.Jobless.GetKubeConfig(clusterName)
		if err != nil {
			return "", &KubernetesError{
				Message: "GetKubeConfigPath failed",
				Err:     err,
			}
		}
		k.KubeConfig = path
	}
	return k.KubeConfig, nil
}
