package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sapi "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

type Kubernetes interface {
	GetClient(ctx context.Context) (client.Client, error)
	GetServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error)
	GetServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
	Setup(ctx context.Context) error
	TearDown(ctx context.Context) error
}

func NewKubernetes(clusterName string) Kubernetes {
	environment := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "pkg", "weka-k8s-api", "crds")},
		ErrorIfCRDPathMissing: true,
		UseExistingCluster:    func(b bool) *bool { return &b }(true),
	}
	return &kubernetes{
		ClusterName: clusterName,
		Environment: environment,
	}
}

type kubernetes struct {
	Environment *envtest.Environment
	ClusterName string

	Client     client.Client
	RestConfig *rest.Config
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

func (k *kubernetes) GetServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	cfg := k.RestConfig
	clientset, err := k8sapi.NewForConfig(cfg)
	if err != nil {
		return nil, nil, &KubernetesError{
			Message: "failed to create clientset",
			Err:     err,
		}
	}

	groups, _, err := clientset.Discovery().ServerGroupsAndResources()
	if err != nil {
		return nil, nil, &KubernetesError{
			Message: "failed to get server resources",
			Err:     err,
		}
	}

	return groups, nil, nil
}

func (k *kubernetes) GetServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	cfg := k.RestConfig
	clientset, err := k8sapi.NewForConfig(cfg)
	if err != nil {
		return nil, &KubernetesError{
			Message: "failed to create clientset",
			Err:     err,
		}
	}

	resourceList, err := clientset.Discovery().ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		return nil, &KubernetesError{
			Message: "failed to get server resources for group version",
			Err:     err,
		}
	}

	return resourceList, nil
}

func (k *kubernetes) StartEnvTest(ctx context.Context) (*rest.Config, error) {
	if k.RestConfig == nil {
		if os.Getenv("KUBECONFIG") == "" {
			return nil, &KubernetesError{
				Message: "StartEnvTest failed: KUBECONFIG is not set",
			}
		}

		cfg, err := k.Environment.Start()
		if err != nil {
			return nil, &KubernetesError{
				Message: "StartEnvTest > Start failed",
				Err:     err,
			}
		}
		k.RestConfig = cfg
	}
	return k.RestConfig, nil
}
