package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kr/pretty"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	"github.com/weka/weka-operator/internal/app/manager/domain"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	"github.com/weka/weka-operator/internal/pkg/instrumentation"
)

type Cluster struct {
	SystemTest
}

func (c *Cluster) CreateCluster(t *testing.T) {
	t.Run("Validate Weka Cluster", c.ValidateWekaCluster)
	t.Run("Deploy Weka Cluster", c.DeployWekaCluster)
}

func (c *Cluster) ValidateStartupCompleted(t *testing.T) {
	t.Run("Verify Weka Containers", c.VerifyWekaContainers)
	t.Run("Verify Weka Cluster", c.VerifyWekaCluster)
}

func (c *Cluster) ValidateWekaCluster(t *testing.T) {
	cluster := c.testingCluster()
	if cluster.UID != "" {
		t.Fatalf("UID should be empty")
	}

	template := cluster.Spec.Template
	_, ok := domain.WekaClusterTemplates[template]
	if !ok {
		t.Fatalf("template %q not found in WekaClusterTemplates", template)
	}

	topology := cluster.Spec.Topology
	_, ok = domain.Topologies[topology]
	if !ok {
		t.Fatalf("topology %q not found", topology)
	}
}

func (c *Cluster) testingCluster() *wekav1alpha1.WekaCluster {
	driversDistService := fmt.Sprintf("http://weka-driver-builder.%s.svc.cluster.local:60002", c.Namespace)
	cluster := &wekav1alpha1.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.ClusterName,
			Namespace: c.Namespace,
		},
		Spec: wekav1alpha1.WekaClusterSpec{
			Size:               1,
			Template:           "small",
			Topology:           "discover_oci",
			Image:              c.Image,
			ImagePullSecret:    "quay-cred",
			DriversDistService: driversDistService,
			NodeSelector: map[string]string{
				"weka.io/role": "backend",
			},
		},
	}
	return cluster
}

func (c *Cluster) DeployWekaCluster(t *testing.T) {
	ctx, logger, done := instrumentation.GetLogSpan(c.Ctx, "DeployWekaCluster")
	defer done()

	cluster := c.testingCluster()

	if err := c.Create(ctx, cluster); err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.Info("cluster already exists")
		} else {
			logger.Error(err, "FormCluster cluster")
			t.Fatalf("failed to create weka cluster: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	t.Run("FormCluster Weka Cluster", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		waitFor(ctx, func(ctx context.Context) bool {
			err := c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)
			return err == nil
		})
		if cluster.UID == "" {
			t.Fatalf("UID should not be empty")
		}
	})

	t.Run("Secrets Created Condition", c.SecretsCreatedCondition(ctx, cluster))
	t.Run("Pods Created Condition", c.PodsCreatedCondition(ctx, cluster))
}

func (c *Cluster) SecretsCreatedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
		defer cancel()
		if err := waitForCondition(ctx, c, cluster, condition.CondClusterSecretsCreated); err != nil {
			t.Fatalf("failed to wait for Secrets Created condition: %v", err)
		}
	}
}

func (c *Cluster) PodsCreatedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if err := waitForCondition(ctx, c, cluster, condition.CondPodsCreated); err != nil {
			t.Fatalf("failed to wait for Pods Created condition: %v", err)
		}
	}
}

func (c *Cluster) VerifyWekaContainers(t *testing.T) {
	ctx, _, done := instrumentation.GetLogSpan(c.Ctx, "VerifyWekaContainers")
	defer done()

	driveContainers := &wekav1alpha1.WekaContainerList{}
	waitFor(ctx, func(ctx context.Context) bool {
		labels := map[string]string{
			"weka.io/mode": "drive",
		}
		err := c.List(ctx, driveContainers, client.MatchingLabels(labels))
		return err == nil && len(driveContainers.Items) == 5
	})

	computeContainers := &wekav1alpha1.WekaContainerList{}
	waitFor(ctx, func(ctx context.Context) bool {
		labels := map[string]string{
			"weka.io/mode": "compute",
		}
		err := c.List(ctx, computeContainers, client.MatchingLabels(labels))
		return err == nil && len(computeContainers.Items) == 5
	})

	containers := &wekav1alpha1.WekaContainerList{}
	containers.Items = append(containers.Items, driveContainers.Items...)
	containers.Items = append(containers.Items, computeContainers.Items...)

	t.Run("Drivers Ensured Condition", c.DriversEnsuredCondition(ctx, containers))
	t.Run("Joined Cluster Condition", c.JoinedClusterCondition(ctx, containers))
	t.Run("Drives Added Condition", c.ContainerDrivesAddedCondition(ctx, driveContainers))
}

func (c *Cluster) ContainerDrivesAddedCondition(ctx context.Context, containers *wekav1alpha1.WekaContainerList) func(t *testing.T) {
	return func(t *testing.T) {
		for _, container := range containers.Items {
			t.Run(container.Name, func(t *testing.T) {
				var driveCondition *metav1.Condition
				waitFor(ctx, func(ctx context.Context) bool {
					err := c.Get(ctx, client.ObjectKeyFromObject(&container), &container)
					if err != nil {
						return false
					}
					driveCondition = meta.FindStatusCondition(container.Status.Conditions, condition.CondDrivesAdded)
					return driveCondition != nil && driveCondition.Status == metav1.ConditionTrue
				})

				if driveCondition == nil {
					t.Fatalf("condition %q not found", condition.CondDrivesAdded)
				}
				if driveCondition.Status != metav1.ConditionTrue {
					t.Fatalf("condition %q is not true", condition.CondDrivesAdded)
				}
			})
		}
	}
}

func (c *Cluster) DriversEnsuredCondition(ctx context.Context, container *wekav1alpha1.WekaContainerList) func(t *testing.T) {
	return func(t *testing.T) {
		for _, container := range container.Items {
			t.Run(container.Name, func(t *testing.T) {
				var driverCondition *metav1.Condition
				waitFor(ctx, func(ctx context.Context) bool {
					err := c.Get(ctx, client.ObjectKeyFromObject(&container), &container)
					if err != nil {
						return false
					}
					driverCondition = meta.FindStatusCondition(container.Status.Conditions, condition.CondEnsureDrivers)
					return driverCondition != nil && driverCondition.Status == metav1.ConditionTrue
				})

				if driverCondition == nil {
					t.Fatalf("condition %q not found", condition.CondEnsureDrivers)
				}
				if driverCondition.Status != metav1.ConditionTrue {
					t.Fatalf("condition %q is not true", condition.CondEnsureDrivers)
				}
			})
		}
	}
}

func (c *Cluster) JoinedClusterCondition(ctx context.Context, container *wekav1alpha1.WekaContainerList) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, logger, done := instrumentation.GetLogSpan(ctx, "JoinedClusterCondition")
		defer done()

		for _, container := range container.Items {
			logger.SetValues("container", container.Name)
			t.Run(container.Name, func(t *testing.T) {
				var joinedCondition *metav1.Condition
				waitFor(ctx, func(ctx context.Context) bool {
					err := c.Get(ctx, client.ObjectKeyFromObject(&container), &container)
					if err != nil {
						return false
					}
					joinedCondition = meta.FindStatusCondition(container.Status.Conditions, condition.CondJoinedCluster)
					return joinedCondition != nil && joinedCondition.Status == metav1.ConditionTrue
				})
				if joinedCondition == nil {
					logger.Info("CondJoindCluster was nil")
					t.Fatalf("condition %q not found", condition.CondJoinedCluster)
				}
				if joinedCondition.Status != metav1.ConditionTrue {
					logger.Info("CondJoindCluster was not true", "condition", joinedCondition)
					t.Fatalf("condition %q is not true", condition.CondJoinedCluster)
				}
			})
		}
	}
}

func (c *Cluster) VerifyWekaCluster(t *testing.T) {
	ctx, logger, done := instrumentation.GetLogSpan(c.Ctx, "VerifyWekaCluster")
	defer done()

	cluster := &wekav1alpha1.WekaCluster{}
	key := types.NamespacedName{Namespace: c.Namespace, Name: c.ClusterName}
	if err := c.Get(ctx, key, cluster); err != nil {
		logger.Error(err, "Get cluster")
		t.Fatalf("failed to get weka cluster: %v", err)
	}

	logger.SetValues("cluster", cluster.Name)

	t.Run("Pods Ready Condition", c.PodsReadyCondition(ctx, cluster))
	t.Run("Secrets Applied Condition", c.SecretsAppliedCondition(ctx, cluster))
	t.Run("Cluster Created Condition", c.ClusterCreatedCondition(ctx, cluster))
	t.Run("Drives Added Condition", c.DrivesAddedCondition(ctx, cluster))
	t.Run("IO Started Condition", c.IOStartedCondition(ctx, cluster))
}

func (c *Cluster) PodsReadyCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if err := waitForCondition(ctx, c, cluster, condition.CondPodsReady); err != nil {
			t.Fatalf("failed to wait for Pods Ready condition: %v", err)
		}
	}
}

func (c *Cluster) SecretsAppliedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
		defer cancel()
		if err := waitForCondition(ctx, c, cluster, condition.CondClusterSecretsApplied); err != nil {
			t.Fatalf("failed to wait for Secrets Applied condition: %v", err)
		}
	}
}

func (c *Cluster) ClusterCreatedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
		defer cancel()
		if err := waitForCondition(ctx, c, cluster, condition.CondClusterCreated); err != nil {
			t.Fatalf("failed to wait for Cluster Created condition: %v", err)
		}
	}
}

func (c *Cluster) DrivesAddedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
		defer cancel()
		if err := waitForCondition(ctx, c, cluster, condition.CondDrivesAdded); err != nil {
			t.Fatalf("failed to wait for Drives Added condition: %v", err)
		}
	}
}

func (c *Cluster) IOStartedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 1*time.Minute)
		defer cancel()
		if err := waitForCondition(ctx, c, cluster, condition.CondIoStarted); err != nil {
			t.Fatalf("failed to wait for IO Started condition: %v", err)
		}
	}
}

func waitForCondition(ctx context.Context, c client.Client, cluster *wekav1alpha1.WekaCluster, cond string) error {
	ctx, logger, done := instrumentation.GetLogSpan(ctx, "waitForCondition")
	defer done()

	var actualCondition *metav1.Condition
	waitFor(ctx, func(ctx context.Context) bool {
		err := c.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)
		if err != nil {
			return false
		}
		actualCondition = meta.FindStatusCondition(cluster.Status.Conditions, cond)
		return actualCondition != nil && actualCondition.Status == metav1.ConditionTrue
	})
	if actualCondition == nil {
		return pretty.Errorf("condition %q not found", cond)
	}
	if actualCondition.Status != metav1.ConditionTrue {
		logger.Info("Condition status", "condition", actualCondition)
		return pretty.Errorf("condition %q is not true", cond)
	}
	return nil
}
