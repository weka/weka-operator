package resources

import (
	"bytes"
	"errors"
	"html/template"

	"github.com/go-logr/logr"
	"github.com/kr/pretty"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/yaml"

	_ "embed"
)

//go:embed container.yaml.tpl
var containerTemplate string

type ContainerFactory struct {
	container *wekav1alpha1.WekaContainer
	logger    logr.Logger
}

func NewContainerFactory(container *wekav1alpha1.WekaContainer, logger logr.Logger) *ContainerFactory {
	return &ContainerFactory{
		container: container,
		logger:    logger.WithName("ContainerFactory"),
	}
}

// NewDeployment creates a new deployment for the container
//
// The deployment creates a single pod locked a specific node
func (f *ContainerFactory) NewDeployment() (*appsv1.Deployment, error) {
	if containerTemplate == "" {
		return nil, errors.New("container template is empty")
	}
	deploymentTemplate := template.New("deployment")
	deploymentTemplate, err := deploymentTemplate.Parse(containerTemplate)
	if err != nil {
		return nil, err
	}

	container := f.container.Spec
	if container.ImagePullSecretName == "" {
		container.ImagePullSecretName = "quay-cred"
	}
	if container.ManagementPort == 0 {
		container.ManagementPort = 14000
	}
	if container.InterfaceName == "" {
		container.InterfaceName = "udp"
	}

	namespace := f.container.Namespace
	var deploymentYaml bytes.Buffer
	if err := deploymentTemplate.Execute(&deploymentYaml, deployment{
		Name:                container.Name,
		Namespace:           namespace,
		NodeName:            container.NodeName,
		ImagePullSecretName: container.ImagePullSecretName,
		Image:               container.Image,
		WekaVersion:         container.WekaVersion,
		BackendIP:           container.BackendIP,
		ManagementPort:      container.ManagementPort,
		InterfaceName:       container.InterfaceName,
	}); err != nil {
		f.logger.Info("deployment yaml", "yaml", deploymentYaml.String())
		return nil, pretty.Errorf("failed to execute deployment template", err)
	}

	var deployment appsv1.Deployment
	if err := yaml.Unmarshal(deploymentYaml.Bytes(), &deployment); err != nil {
		return nil, err
	}

	if deployment.Name == "" {
		return nil, pretty.Errorf("deployment name is empty", deploymentYaml.String())
	}
	if deployment.Namespace == "" {
		return nil, errors.New("deployment namespace is empty")
	}

	return &deployment, nil
}

type deployment struct {
	Name                string
	Namespace           string
	NodeName            string
	ImagePullSecretName string
	Image               string
	WekaVersion         string
	BackendIP           string
	ManagementPort      int32
	InterfaceName       string
}
