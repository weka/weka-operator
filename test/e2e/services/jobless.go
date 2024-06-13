package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/weka/jobless/pkg/jobless"
)

type Jobless interface {
	EnsureDeployment(ctx context.Context, params jobless.ProvisionParams) (jobless.Deployment, error)
	GetDeployment(ctx context.Context, clusterName string) (jobless.Deployment, error)

	Install(ctx context.Context, installParams *jobless.InstallParams) (*jobless.Installation, error)
	GetInstallation(ctx context.Context, installParams *jobless.InstallParams) *jobless.Installation
	GetKubeConfig(ctx context.Context, clusterName string) (string, error)

	Provision(ctx context.Context, provisionParams jobless.ProvisionParams) error
	DeleteDeployment(ctx context.Context, clusterName string) error
}

func NewJobless(ctx context.Context) Jobless {
	return &joblessImpl{
		Jobless: jobless.NewJobless(ctx),
	}
}

type joblessImpl struct {
	Jobless       jobless.Jobless
	InstallParams *jobless.InstallParams
}

func (j *joblessImpl) EnsureDeployment(ctx context.Context, params jobless.ProvisionParams) (jobless.Deployment, error) {
	clusterName := params.ClusterName
	deployment, err := j.GetDeployment(ctx, clusterName)
	var notFound *jobless.DeploymentNotFoundError
	if errors.As(err, &notFound) {
		if err := j.Provision(ctx, params); err != nil {
			return nil, err
		}
		return j.GetDeployment(ctx, clusterName)
	}
	if err != nil {
		return nil, err
	}

	return deployment, err
}

func (j *joblessImpl) Install(ctx context.Context, installParams *jobless.InstallParams) (*jobless.Installation, error) {
	return j.Jobless.Install(ctx, *installParams)
}

func (j *joblessImpl) GetInstallation(ctx context.Context, installParams *jobless.InstallParams) *jobless.Installation {
	j.InstallParams = installParams
	return j.Jobless.Info().GetInstallation(ctx, *installParams)
}

func (j *joblessImpl) GetKubeConfig(ctx context.Context, clusterName string) (string, error) {
	if j.InstallParams == nil {
		deployment, err := j.GetDeployment(ctx, clusterName)
		if err != nil {
			return "", fmt.Errorf("get kubeconfig - failed to get deployment: %w", err)
		}
		awsDeployment, ok := deployment.(*jobless.AwsDeployment)
		if !ok {
			return "", fmt.Errorf("get kubeconfig - deployment is not an AWS deployment")
		}
		region := awsDeployment.Region

		installParams, err := j.Jobless.Info().GetInstallParams(ctx, clusterName, region)
		if err != nil {
			return "", fmt.Errorf("get kubeconfig - failed to get install params: %w", err)
		}
		j.InstallParams = installParams
	}
	installation := j.GetInstallation(ctx, j.InstallParams)
	return installation.KubeConfigPath, nil
}

func (j *joblessImpl) GetDeployment(ctx context.Context, clusterName string) (jobless.Deployment, error) {
	return j.Jobless.Info().GetDeployment(ctx, clusterName)
}

func (j *joblessImpl) Provision(ctx context.Context, provisionParams jobless.ProvisionParams) error {
	return j.Jobless.Provision(ctx, provisionParams)
}

func (j *joblessImpl) DeleteDeployment(ctx context.Context, clusterName string) error {
	return j.Jobless.Delete(ctx, clusterName)
}
