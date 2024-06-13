package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/weka/jobless/pkg/jobless"
	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/test/e2e/fixtures"
	"github.com/weka/weka-operator/test/e2e/services"

	"github.com/thoas/go-funk"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type E2ETest struct {
	Ctx     context.Context
	Jobless services.Jobless

	Clusters []*ClusterTest
}

type ClusterTest struct {
	Ctx     context.Context
	Jobless services.Jobless

	Cluster *fixtures.Cluster
}

func antonVPC() *jobless.AwsParams {
	return &jobless.AwsParams{
		Region:         "eu-west-1",
		VpcId:          "vpc-015c3fc68c903683b",
		SubnetId:       "subnet-0f150b5aaadcd8505",
		SecurityGroups: []string{"sg-03b539095aedf3ab9"},
		AmiId:          "ami-027429546b1b42b5a",
		KeyPairName:    "mpfefferle",
	}
}

func BlessIPv6VPC() *jobless.AwsParams {
	return &jobless.AwsParams{
		Region:         "eu-west-1",
		VpcId:          "vpc-07af87826b3029e5a",
		SubnetId:       "subnet-0ca396539bf9a7792",
		SecurityGroups: []string{"sg-0a425e9600ad3cdd8"},
		AmiId:          "ami-027429546b1b42b5a",
		KeyPairName:    "mpfefferle",
	}
}

func TestHappyPath(t *testing.T) {
	operatorTemplate := "small_s3"
	provisionTemplate := "aws_small"
	names := []string{"mbp-operator-e2e-1", "mbp-operator-e2e-2"}
	wekaClusterName := "cluster-dev"
	operatorNamespace := "weka-operator-system"

	ctx := context.Background()
	j := services.NewJobless(ctx)
	clusters := funk.Map(names, func(name string) *fixtures.Cluster {
		return &fixtures.Cluster{
			Name:              name,
			WekaClusterName:   wekaClusterName,
			OperatorNamespace: operatorNamespace,
			OperatorTemplate:  operatorTemplate,
			ProvisionTemplate: provisionTemplate,

			Jobless: j,

			ProvisionParams: &jobless.ProvisionParams{
				ClusterName: name,
				Template:    provisionTemplate,
				// AwsParams:   *BlessIPv6VPC(),
				AwsParams: *antonVPC(),
			},

			InstallParams: &jobless.InstallParams{
				ClusterName:     name,
				NoCsi:           true,
				QuayUsername:    os.Getenv("QUAY_USERNAME"),
				QuayPassword:    os.Getenv("QUAY_PASSWORD"),
				WekaImage:       "quay.io/weka.io/weka-in-container:4.2.7.64-s3multitenancy.7",
				OperatorVersion: "v1.0.0-beta.8",
			},
		}
	}).([]*fixtures.Cluster)

	clusterTests := funk.Map(clusters, func(cluster *fixtures.Cluster) *ClusterTest {
		return &ClusterTest{
			Ctx:     ctx,
			Jobless: j,
			Cluster: cluster,
		}
	}).([]*ClusterTest)

	fixture := &E2ETest{
		Ctx:     ctx,
		Jobless: j,

		Clusters: clusterTests,
	}
	t.Run("Test Environment", fixture.ValidateTestEnvironment)
	t.Run("Provision", fixture.Provision)
	t.Run("Install", fixture.Install)
	t.Run("Validate Startup Completed", fixture.ValidateStartupCompleted)
	t.Run("Cleanup", fixture.Cleanup)
}

func TestIPv6VPC(t *testing.T) {
	t.Skip("Skipping IPv6 VPC test")
	operatorTemplate := "small_s3"
	provisionTemplate := "aws_small"
	names := []string{"mbp-operator-e2e-1", "mbp-operator-e2e-2"}
	wekaClusterName := "cluster-dev"
	operatorNamespace := "weka-operator-system"

	ctx := context.Background()
	j := services.NewJobless(ctx)
	clusters := funk.Map(names, func(name string) *fixtures.Cluster {
		return &fixtures.Cluster{
			Name:              name,
			WekaClusterName:   wekaClusterName,
			OperatorNamespace: operatorNamespace,
			OperatorTemplate:  operatorTemplate,
			ProvisionTemplate: provisionTemplate,

			Jobless: j,

			ProvisionParams: &jobless.ProvisionParams{
				ClusterName: name,
				Template:    provisionTemplate,
				AwsParams:   *BlessIPv6VPC(),
			},

			InstallParams: &jobless.InstallParams{
				ClusterName:     name,
				NoCsi:           true,
				QuayUsername:    os.Getenv("QUAY_USERNAME"),
				QuayPassword:    os.Getenv("QUAY_PASSWORD"),
				WekaImage:       "quay.io/weka.io/weka-in-container:4.2.7.64-s3multitenancy.7",
				OperatorVersion: "v1.0.0-beta.8",
			},
		}
	}).([]*fixtures.Cluster)

	clusterTests := funk.Map(clusters, func(cluster *fixtures.Cluster) *ClusterTest {
		return &ClusterTest{
			Ctx:     ctx,
			Jobless: j,
			Cluster: cluster,
		}
	}).([]*ClusterTest)

	fixture := &E2ETest{
		Ctx:     ctx,
		Jobless: j,

		Clusters: clusterTests,
	}
	t.Run("Test Environment", fixture.ValidateTestEnvironment)
	t.Run("Provision", fixture.Provision)
	t.Run("Install", fixture.Install)
	t.Run("Validate Startup Completed", fixture.ValidateStartupCompleted)
	t.Run("Cleanup", fixture.Cleanup)
}

func (f *E2ETest) ValidateTestEnvironment(t *testing.T) {
	os.Setenv("AWS_PROFILE", "devkube")

	requiredEnvVars := []string{"QUAY_USERNAME", "QUAY_PASSWORD", "AWS_PROFILE"}
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

			installation, err := f.Jobless.Install(ctx, params)
			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}
			if installation == nil {
				t.Fatalf("Expected installation to be set, got nil")
			}
			if installation.KubeConfigPath == "" {
				t.Errorf("Expected kubeconfig path to be set, got empty string")
			}

			cluster.Installation = installation
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
	conditions := []string{
		condition.CondClusterSecretsCreated,
		condition.CondPodsCreated,
		condition.CondPodsReady,
		condition.CondClusterSecretsApplied,
		condition.CondClusterCreated,
		condition.CondDrivesAdded,
		condition.CondIoStarted,
	}
	for _, test := range f.Clusters {
		cluster := test.Cluster
		if err := cluster.SetupK8s(ctx); err != nil {
			t.Fatalf("Expected SetupK8s to succeed, got %v", err)
		}
		defer cluster.TeardownK8s(ctx)

		if cluster.Kubernetes == nil {
			t.Fatalf("Expected k8s to be set, got nil")
		}
		client, err := cluster.Kubernetes.GetClient(ctx)
		if err != nil {
			t.Fatalf("Failed to GetClient: %v", err)
		}
		if client == nil {
			t.Fatalf("Expected client to be set, got nil")
		}

		name := fmt.Sprintf("Validate %s", cluster.Name)
		t.Run(name, func(t *testing.T) {
			for _, cond := range conditions {
				name := fmt.Sprintf("Condition: %s", cond)
				t.Run(name, test.Condition(ctx, cond))
			}
		})
	}
}

func (c *ClusterTest) Condition(ctx context.Context, conditionName string) func(t *testing.T) {
	return func(t *testing.T) {
		cluster := &wekav1alpha1.WekaCluster{}
		k8s := c.Cluster.Kubernetes
		if k8s == nil {
			t.Fatalf("Expected k8s to be set, got nil")
		}
		ctx := c.Ctx
		client, err := k8s.GetClient(ctx)
		if err != nil {
			t.Fatalf("Failed to GetClient: %v", err)
		}
		if client == nil {
			t.Fatalf("Expected k8s client to be set, got nil")
		}
		if err := client.Get(ctx, c.Cluster.NamespacedName(), cluster); err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}

		cond := meta.FindStatusCondition(cluster.Status.Conditions, conditionName)
		if cond == nil {
			t.Fatalf("Expected condition %s to be set, got nil", conditionName)
		}
		if cond.Status != metav1.ConditionTrue {
			t.Errorf("Expected condition %s to be true, got %s", conditionName, cond.Status)
		}
	}
}
