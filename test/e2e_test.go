package test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/weka/weka-operator/test/e2e/fixtures"
	"github.com/weka/weka-operator/test/e2e/services"
	"github.com/weka/weka-operator/test/e2e/types"

	"github.com/thoas/go-funk"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
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

func TestHappyPath(t *testing.T) {
	e2eTest := NewE2ETest(t)
	t.Run("Test Environment", e2eTest.ValidateTestEnvironment)
	t.Run("Provision", e2eTest.Provision)
	t.Run("Install", e2eTest.Install)
	t.Run("Validate Startup Completed", e2eTest.ValidateStartupCompleted)
	t.Run("Cleanup", e2eTest.Cleanup)
}

func NewE2ETest(t *testing.T) *E2ETest {
	operatorTemplate := "small_s3"
	provisionTemplate := "aws_small_udp"
	clusterName := os.Getenv("CLUSTER_NAME")
	if clusterName == "" {
		t.Fatalf("Expected CLUSTER_NAME to be set")
	}
	names := []string{clusterName}
	wekaClusterName := "cluster-dev"
	operatorNamespace := "weka-operator-system"

	operatorVersion := os.Getenv("OPERATOR_VERSION")
	if operatorVersion == "" {
		t.Fatalf("Expected OPERATOR_VERSION to be set")
	}

	wekaImage := os.Getenv("WEKA_IMAGE")
	if wekaImage == "" {
		t.Fatalf("Expected WEKA_IMAGE to be set")
	}

	logger := zap.New(zap.UseDevMode(true))
	log.SetLogger(logger)

	ctx := context.Background()
	j := services.NewJobless(ctx)
	clusters := funk.Map(names, func(name string) *fixtures.Cluster {
		cluster := &fixtures.Cluster{
			Name:              name,
			WekaClusterName:   wekaClusterName,
			OperatorNamespace: operatorNamespace,
			OperatorTemplate:  operatorTemplate,
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
				QuayUsername:    os.Getenv("QUAY_USERNAME"),
				QuayPassword:    os.Getenv("QUAY_PASSWORD"),
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
		Jobless: services.NewJobless(ctx),

		Clusters: clusterTests,
	}
}

func (f *E2ETest) ValidateTestEnvironment(t *testing.T) {
	requiredEnvVars := []string{"QUAY_USERNAME", "QUAY_PASSWORD"}
	for _, envVar := range requiredEnvVars {
		name := fmt.Sprintf("Environment Variable: %s", envVar)
		t.Run(name, func(t *testing.T) {
			v := os.Getenv(envVar)
			if v == "" {
				t.Errorf("%s is not set", envVar)
			}
		})
	}
}

func (f *E2ETest) Provision(t *testing.T) {
	ctx := f.Ctx
	for _, test := range f.Clusters {
		cluster := test.Cluster
		// Provision the cluster
		t.Run(cluster.Name, func(t *testing.T) {
			provisionParams := *cluster.ProvisionParams
			deployment, err := f.Jobless.EnsureDeployment(ctx, provisionParams)
			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}
			if deployment.GetClusterName() != cluster.Name {
				t.Errorf("Expected cluster name to match %s, got %s", cluster.Name, deployment.GetClusterName())
			}
			cluster.Deployment = deployment
		})

		// Verify provisioning succeeded
		t.Run(cluster.Name, func(t *testing.T) {
			deployment := cluster.Deployment
			if deployment == nil {
				t.Fatalf("Expected deployment to be set, got nil")
			}
			if deployment.GetClusterName() != cluster.Name {
				t.Errorf("Expected cluster name to match %s, got %s", cluster.Name, deployment.GetClusterName())
			}
		})
	}
}

func (f *E2ETest) Install(t *testing.T) {
	ctx := f.Ctx
	for _, test := range f.Clusters {
		cluster := test.Cluster
		t.Run(cluster.Name, func(t *testing.T) {
			params := cluster.InstallParams

			var installation *services.Installation
			var err error

			// With retries because installation may not be complete when the command
			// returns
			for i := range 3 {
				installation, err = f.Jobless.Install(ctx, params)
				if err != nil {
					t.Logf("Failed to install, retrying %d of 3: %v", i+1, err)
					continue
				}
			}
			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}
			if installation == nil {
				t.Fatalf("Expected installation to be set, got nil")
			}
			if installation.KubeConfigPath == "" {
				t.Errorf("Expected kubeconfig path to be set, got empty string")
			} else {
				t.Logf("Kubeconfig path: %s", installation.KubeConfigPath)
			}

			cluster.Installation = installation

			if err := cluster.SetupK8s(ctx); err != nil {
				t.Fatalf("Expected SetupK8s to succeed, got %v", err)
			}
		})

		t.Run(cluster.Name, func(t *testing.T) {
			installation := cluster.Installation
			if installation == nil {
				t.Fatalf("Expected installation to be set, got nil")
			}
			if installation.KubeConfigPath == "" {
				t.Errorf("Expected kubeconfig path to be set, got empty string")
			}
		})
	}
}

func (f *E2ETest) Cleanup(t *testing.T) {
	ctx := f.Ctx
	for _, test := range f.Clusters {
		cluster := test.Cluster
		t.Run(cluster.Name, func(t *testing.T) {
			if err := f.Jobless.DeleteDeployment(ctx, cluster.Name); err != nil {
				t.Fatalf("Expected Cleanup to succeed, got %v", err)
			}
		})
	}
}

func (f *E2ETest) ValidateStartupCompleted(t *testing.T) {
	ctx := f.Ctx
	for _, test := range f.Clusters {
		t.Run(test.Cluster.Name, (&Cluster{ClusterTest: *test}).ValidateStartupCompleted(ctx))
	}
}
