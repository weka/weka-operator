package test

import (
	"context"
	"fmt"
	"os"

	"github.com/weka/weka-operator/internal/pkg/instrumentation"
	"github.com/weka/weka-operator/test/e2e/fixtures"
	"github.com/weka/weka-operator/test/e2e/services"
	"github.com/weka/weka-operator/test/e2e/types"

	"github.com/thoas/go-funk"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func antonVPC() *types.AwsParams {
	return &types.AwsParams{
		Region:         "eu-west-1",
		VpcId:          "vpc-015c3fc68c903683b",
		SubnetId:       "subnet-0f150b5aaadcd8505",
		SecurityGroups: []string{"sg-03b539095aedf3ab9"},
		AmiId:          "ami-027429546b1b42b5a",
		KeyPairName:    "dev-key",
	}
}

func BlessIPv6VPC() *types.AwsParams {
	return &types.AwsParams{
		Region:         "eu-west-1",
		VpcId:          "vpc-07af87826b3029e5a",
		SubnetId:       "subnet-0ca396539bf9a7792",
		SecurityGroups: []string{"sg-0a425e9600ad3cdd8"},
		AmiId:          "ami-027429546b1b42b5a",
		KeyPairName:    "dev-key",
	}
}

func NewE2ETest(ctx context.Context) *E2ETest {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "NewE2ETest")
	defer done()

	provisionTemplate := "aws_small"
	clusterName := *ClusterName
	names := []string{clusterName}
	wekaClusterName := "cluster-dev"
	operatorNamespace := "weka-operator-system"

	operatorVersion := *OperatorVersion

	wekaImage := *WekaImage

	log.SetLogger(logger.Logger)

	quayUsername := os.Getenv("QUAY_USERNAME")
	if quayUsername == "" {
		quayUsername = *QuayUsername
	}

	quayPassword := os.Getenv("QUAY_PASSWORD")
	if quayPassword == "" {
		quayPassword = *QuayPassword
	}

	j := services.NewJobless(ctx, *BlissVersion)
	clusters := funk.Map(names, func(name string) *fixtures.Cluster {
		cluster := &fixtures.Cluster{
			Name:              name,
			WekaClusterName:   wekaClusterName,
			OperatorNamespace: operatorNamespace,
			ProvisionTemplate: provisionTemplate,

			Jobless: j,

			ProvisionParams: &types.ProvisionParams{
				ClusterName: name,
				Template:    provisionTemplate,
				// AwsParams:   *BlessIPv6VPC(),
				AwsParams: *antonVPC(),
			},

			InstallParams: &types.InstallParams{
				ClusterName:     name,
				NoCsi:           true,
				QuayUsername:    quayUsername,
				QuayPassword:    quayPassword,
				WekaImage:       wekaImage,
				OperatorVersion: operatorVersion,
			},
		}

		return cluster
	}).([]*fixtures.Cluster)

	clusterTests := funk.Map(clusters, func(cluster *fixtures.Cluster) *ClusterTest {
		return &ClusterTest{
			Ctx:     ctx,
			Jobless: j,
			Cluster: cluster,
		}
	}).([]*ClusterTest)
	return &E2ETest{
		Ctx:     ctx,
		Jobless: services.NewJobless(ctx, *BlissVersion),

		Clusters: clusterTests,
	}
}

func ValidateTestEnvironment(ctx context.Context) error {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "ValidateTestEnvironment")
	defer done()

	requiredEnvVars := []string{"QUAY_USERNAME", "QUAY_PASSWORD"}
	for _, envVar := range requiredEnvVars {
		logger.Info("Validating environment variable", "variable", envVar)
		v := os.Getenv(envVar)
		if v == "" {
			return fmt.Errorf("%s is not set", envVar)
		}
	}
	return nil
}

func (f *E2ETest) Provision(ctx context.Context) error {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "E2ETest.Provision")
	defer done()

	for _, test := range f.Clusters {
		cluster := test.Cluster

		logger.Info("Provisioning", "cluster", cluster.Name)

		// Provision the cluster
		provisionParams := *cluster.ProvisionParams
		deployment, err := f.Jobless.EnsureDeployment(ctx, provisionParams)
		if err != nil {
			return fmt.Errorf("Expected no error, got %v", err)
		}
		if deployment.GetClusterName() != cluster.Name {
			return fmt.Errorf("Expected cluster name to match %s, got %s", cluster.Name, deployment.GetClusterName())
		}
	}
	return nil
}
func (f *E2ETest) Install(ctx context.Context) error {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "E2ETest.Install")
	defer done()

	for _, test := range f.Clusters {
		cluster := test.Cluster
		params := cluster.InstallParams

		logger.Info("Installing", "cluster", cluster.Name)

		var installation *services.Installation
		var err error

		// With retries because installation may not be complete when the command
		// returns
		for i := range 3 {
			installation, err = f.Jobless.Install(ctx, params)
			if err != nil {
				logger.V(1).Info("Install failed, retrying", "error", err, "try", i)
				continue
			}
		}
		if err != nil {
			return fmt.Errorf("Expected no error, got %v", err)
		}
		if installation == nil {
			return fmt.Errorf("Expected installation to be set, got nil")
		}
		if installation.KubeConfigPath == "" {
			return fmt.Errorf("Expected kubeconfig path to be set, got empty string")
		}

		cluster.Installation = installation

		if err := cluster.SetupK8s(ctx); err != nil {
			return fmt.Errorf("Expected SetupK8s to succeed, got %v", err)
		}

		if installation == nil {
			return fmt.Errorf("Expected installation to be set, got nil")
		}
		if installation.KubeConfigPath == "" {
			return fmt.Errorf("Expected kubeconfig path to be set, got empty string")
		}
	}
	return nil
}

func (f *E2ETest) Cleanup(ctx context.Context) error {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "E2ETest.Cleanup")
	defer done()

	if !*Cleanup {
		logger.Info("Cleanup disabled")
		return nil
	}
	for _, test := range f.Clusters {
		cluster := test.Cluster
		logger.Info("Deleting deployment", "cluster", cluster.Name)
		if err := f.Jobless.DeleteDeployment(ctx, cluster.Name); err != nil {
			return fmt.Errorf("Expected Cleanup to succeed, got %v", err)
		}
	}
	return nil
}
