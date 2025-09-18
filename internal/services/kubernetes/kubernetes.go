package kubernetes

import (
	"context"

	"github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type K8sOwnerRef struct {
	Scheme *runtime.Scheme
	Obj    metav1.Object
}

type KubeService interface {
	GetNode(ctx context.Context, nodeName types.NodeName) (*v1.Node, error)
	GetNodes(ctx context.Context, nodeSelector map[string]string) ([]v1.Node, error)
	GetPodsSimple(ctx context.Context, namespace string, labels map[string]string) ([]v1.Pod, error)
	GetWekaContainersSimple(ctx context.Context, namespace string, node string, labels map[string]string) ([]v1alpha1.WekaContainer, error)
	GetWekaContainers(ctx context.Context, options GetPodsOptions) ([]v1alpha1.WekaContainer, error)
	GetSecret(ctx context.Context, secretName, namespace string) (*v1.Secret, error)
	EnsureSecret(ctx context.Context, secret *v1.Secret, owner *K8sOwnerRef) error
}

func NewKubeService(client client.Client) KubeService {
	return &ApiKubeService{
		Client: client,
	}
}

type ApiKubeService struct {
	Client client.Client
}

type GetPodsOptions struct {
	Namespace string
	Node      string
	Labels    map[string]string
	LabelsIn  map[string][]string
}

func (s *ApiKubeService) GetWekaContainers(ctx context.Context, options GetPodsOptions) ([]v1alpha1.WekaContainer, error) {
	wekaContainers := &v1alpha1.WekaContainerList{}

	listOptions := []client.ListOption{}

	// Create a list of options to pass to the List method
	if options.Namespace != "" {
		listOptions = []client.ListOption{
			client.InNamespace(options.Namespace),
		}
	}

	if len(options.Labels) != 0 {
		listOptions = append(listOptions, client.MatchingLabels(options.Labels))
	}

	// Add node name filtering if the node parameter is not empty
	if options.Node != "" {
		listOptions = append(listOptions, client.MatchingFields{"status.nodeAffinity": options.Node})
	}

	if len(options.LabelsIn) != 0 {
		selector := labels.NewSelector()
		for k, v := range options.LabelsIn {
			req, err := labels.NewRequirement(k, selection.In, v)
			if err != nil {
				return nil, err
			}
			selector = selector.Add(*req)
		}
		listOptions = append(listOptions, client.MatchingLabelsSelector{Selector: selector})
	}

	// List the pods with the given options
	err := s.Client.List(ctx, wekaContainers, listOptions...)
	if err != nil {
		return []v1alpha1.WekaContainer{}, err
	}
	return wekaContainers.Items, nil
}

func (s *ApiKubeService) GetPods(ctx context.Context, options GetPodsOptions) ([]v1.Pod, error) {
	pods := &v1.PodList{}

	listOptions := []client.ListOption{}

	// Create a list of options to pass to the List method
	if options.Namespace != "" {
		listOptions = []client.ListOption{
			client.InNamespace(options.Namespace),
		}
	}

	if len(options.Labels) != 0 {
		listOptions = append(listOptions, client.MatchingLabels(options.Labels))
	}

	// Add node name filtering if the node parameter is not empty
	if options.Node != "" {
		listOptions = append(listOptions, client.MatchingFields{"spec.nodeName": options.Node})
	}

	if len(options.LabelsIn) != 0 {
		selector := labels.NewSelector()
		for k, v := range options.LabelsIn {
			req, err := labels.NewRequirement(k, selection.In, v)
			if err != nil {
				return nil, err
			}
			selector = selector.Add(*req)
		}
		listOptions = append(listOptions, client.MatchingLabelsSelector{Selector: selector})
	}

	// List the pods with the given options
	err := s.Client.List(ctx, pods, listOptions...)
	if err != nil {
		return []v1.Pod{}, err
	}
	return pods.Items, nil
}

func (s *ApiKubeService) GetPodsSimple(ctx context.Context, namespace string, labels map[string]string) ([]v1.Pod, error) {
	pods := &v1.PodList{}

	// Create a list of options to pass to the List method
	listOptions := []client.ListOption{
		client.InNamespace(namespace),
	}

	if len(labels) != 0 {
		listOptions = append(listOptions, client.MatchingLabels(labels))
	}

	// List the pods with the given options
	err := s.Client.List(ctx, pods, listOptions...)
	if err != nil {
		return []v1.Pod{}, err
	}
	return pods.Items, nil
}

func (s *ApiKubeService) GetWekaContainersSimple(ctx context.Context, namespace string, node string, labels map[string]string) ([]v1alpha1.WekaContainer, error) {
	wekaContainers := &v1alpha1.WekaContainerList{}

	// Create a list of options to pass to the List method
	listOptions := []client.ListOption{
		client.InNamespace(namespace),
	}

	if len(labels) != 0 {
		listOptions = append(listOptions, client.MatchingLabels(labels))
	}

	// Add node name filtering if the node parameter is not empty
	if node != "" {
		listOptions = append(listOptions, client.MatchingFields{"status.nodeAffinity": node})
	}

	// List the pods with the given options
	err := s.Client.List(ctx, wekaContainers, listOptions...)
	if err != nil {
		return []v1alpha1.WekaContainer{}, err
	}
	return wekaContainers.Items, nil
}

func (s *ApiKubeService) GetNodes(ctx context.Context, nodeSelector map[string]string) ([]v1.Node, error) {
	nodes := &v1.NodeList{}
	err := s.Client.List(ctx, nodes, client.MatchingLabels(nodeSelector))
	if err != nil {
		return []v1.Node{}, err
	}
	return nodes.Items, nil
}

func (s *ApiKubeService) GetNode(ctx context.Context, nodeName types.NodeName) (*v1.Node, error) {
	node := &v1.Node{}
	err := s.Client.Get(ctx, client.ObjectKey{
		Name: string(nodeName),
	}, node)
	if err != nil {
		return nil, err
	}
	return node, nil
}

func (s *ApiKubeService) GetSecret(ctx context.Context, secretName, namespace string) (*v1.Secret, error) {
	secret := &v1.Secret{}
	err := s.Client.Get(ctx, client.ObjectKey{
		Name:      secretName,
		Namespace: namespace,
	}, secret)
	if err != nil {
		return nil, err
	}
	return secret, nil
}

func (s *ApiKubeService) EnsureSecret(ctx context.Context, secret *v1.Secret, owner *K8sOwnerRef) error {
	existing := &v1.Secret{}
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: secret.ObjectMeta.Namespace, Name: secret.ObjectMeta.Name}, existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		if owner != nil {
			err := ctrl.SetControllerReference(owner.Obj, secret, owner.Scheme)
			if err != nil {
				return err
			}
		}

		err = s.Client.Create(ctx, secret)
		if err != nil {
			return err
		}
	}

	return nil
}
