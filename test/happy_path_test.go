package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
)

func TestHappyPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(pkgCtx, 30*time.Minute)
	defer cancel()
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "TestHappyPath")
	defer done()

	operatorNamespace := "weka-operator-system"
	cluster := testingCluster(*WekaImage, operatorNamespace)
	e2eTest, err := NewE2ETest(ctx, cluster)
	if err != nil {
		t.Fatalf("NewE2ETest: %v", err)
	}

	k8sClient, err := e2eTest.Clusters[0].k8sClient(ctx)
	if err != nil {
		t.Fatalf("k8sClient: %v", err)
	}
	// Create signing and discovery tasks
	logger.Info("Creating sign drives operation")
	signDrivesOp := signDrivesOp(*WekaImage)
	if err := k8sClient.Create(ctx, &signDrivesOp); err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	defer cancel()
	waitFor(ctx, func(ctx context.Context) bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: signDrivesOp.Name, Namespace: operatorNamespace}, &signDrivesOp)
		if err != nil {
			return false
		}

		return signDrivesOp.Status.Status == "Done"
	})

	logger.Info("Creating discover drives operation")
	discoverDrivesOp := discoverDrivesOp(*ClusterName, *WekaImage)
	if err := k8sClient.Create(ctx, &discoverDrivesOp); err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	waitFor(ctx, func(ctx context.Context) bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: discoverDrivesOp.Name, Namespace: operatorNamespace}, &discoverDrivesOp)
		if err != nil {
			return false
		}
		return discoverDrivesOp.Status.Status == "Done"
	})

	// Deploy the driver builder
	driverBuilder := driverBuilder(*WekaImage, operatorNamespace)
	logger.Info("Deploying driver builder", "driverBuilder", driverBuilder.Name)
	if err := k8sClient.Create(ctx, &driverBuilder); err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	waitFor(ctx, func(ctx context.Context) bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: driverBuilder.Name, Namespace: operatorNamespace}, &driverBuilder)
		return err == nil
	})

	driverBuilderService := driverBuilderService(driverBuilder.Name, operatorNamespace)
	logger.Info("Deploying driver builder service", "driverBuilderService", driverBuilderService.Name)
	if err := k8sClient.Create(ctx, &driverBuilderService); err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	waitFor(ctx, func(ctx context.Context) bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: driverBuilderService.Name, Namespace: operatorNamespace}, &driverBuilderService)
		return err == nil
	})

	// Creaet the cluster
	logger.Info("Creating cluster", "cluster", cluster.Name)
	if err := k8sClient.Create(ctx, cluster); err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	waitFor(ctx, func(ctx context.Context) bool {
		err := k8sClient.Get(ctx, client.ObjectKey{Name: cluster.Name, Namespace: operatorNamespace}, cluster)
		return err == nil
	})

	subject := Cluster{ClusterTest: *e2eTest.Clusters[0]}
	test := subject.ValidateStartupCompleted(ctx)
	name := fmt.Sprintf("Cluster %s", cluster.Name)
	t.Run(name, test)
}

func testingCluster(wekaImage string, operatorNamespace string) *wekav1alpha1.WekaCluster {
	driversDistService := fmt.Sprintf("https://weka-driver-builder.%s.svc.cluster.local:60002", operatorNamespace)
	numComputeContainers := 6
	numDriveContainers := 6
	cluster := &wekav1alpha1.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-dev",
			Namespace: "weka-operator-system",
		},
		Spec: wekav1alpha1.WekaClusterSpec{
			Template: "dynamic",
			Dynamic: &wekav1alpha1.WekaConfig{
				ComputeContainers: &numComputeContainers,
				DriveContainers:   &numDriveContainers,
			},
			Image:              wekaImage,
			ImagePullSecret:    "quay-io-robot-secret",
			DriversDistService: driversDistService,
			GracefulDestroyDuration: metav1.Duration{
				Duration: 10 * time.Second,
			},
			NodeSelector: map[string]string{
				"weka.io/supports-backends": "true",
			},
		},
	}
	return cluster
}

func driverBuilder(wekaImage string, operatorNamespace string) wekav1alpha1.WekaContainer {
	return wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "weka-driver-builder",
			Namespace: operatorNamespace,
			Labels: map[string]string{
				"app": "weka-driver-builder",
			},
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			AgentPort:         60001,
			Image:             wekaImage,
			ImagePullSecret:   "quay-io-robot-secret",
			Mode:              "dist",
			WekaContainerName: "dist",
			NumCores:          1,
			Port:              60002,
		},
	}
}

func driverBuilderService(driverBuilderName string, operatorNamespace string) v1.Service {
	return v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      driverBuilderName,
			Namespace: operatorNamespace,
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app": "weka-driver-builder",
			},
			Ports: []v1.ServicePort{
				{
					Name:       "weka-driver-builder",
					Port:       60002,
					TargetPort: intstr.FromInt(60002),
				},
			},
		},
	}
}

func signDrivesOp(wekaImage string) wekav1alpha1.WekaManualOperation {
	return wekav1alpha1.WekaManualOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sign-aws-drives",
			Namespace: "weka-operator-system",
		},
		Spec: wekav1alpha1.WekaManualOperationSpec{
			Action:          "sign-drives",
			Image:           wekaImage,
			ImagePullSecret: "quay-io-robot-secret",
			Payload: wekav1alpha1.ManualOperatorPayload{
				SignDrives: &wekav1alpha1.SignDrivesPayload{
					Type:         "aws-all",
					NodeSelector: map[string]string{},
					DevicePaths:  []string{},
				},
			},
		},
	}
}

func discoverDrivesOp(clusterName string, wekaImage string) wekav1alpha1.WekaManualOperation {
	return wekav1alpha1.WekaManualOperation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "discover-drives",
			Namespace: "weka-operator-system",
		},
		Spec: wekav1alpha1.WekaManualOperationSpec{
			Action:          "discover-drives",
			Image:           wekaImage,
			ImagePullSecret: "quay-io-robot-secret",
			Payload: wekav1alpha1.ManualOperatorPayload{
				DiscoverDrives: &wekav1alpha1.DiscoverDrivesPayload{
					NodeSelector: map[string]string{},
				},
			},
		},
	}
}
