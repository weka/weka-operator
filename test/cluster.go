package test

import (
	"context"
	"testing"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"

	"github.com/kr/pretty"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Cluster struct {
	ClusterTest
}

func (c *Cluster) ValidateStartupCompleted(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {

		t.Run("Verify Namespace", c.VerifyNamespace(ctx))
		t.Run("Verify Weka Containers", c.VerifyWekaContainers(ctx))
		t.Run("Verify Weka Cluster", c.VerifyWekaCluster(ctx))
	}
}

func (c *Cluster) VerifyNamespace(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, _, done := instrumentation.GetLogSpan(ctx, "VerifyNamespace")
		defer done()

		ns := &v1.Namespace{}
		if err := c.Get(ctx, client.ObjectKey{Name: c.Cluster.OperatorNamespace}, ns); err != nil {
			t.Fatalf("failed to get namespace: %v", err)
		}
	}
}

func (c *Cluster) VerifyWekaContainers(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, _, done := instrumentation.GetLogSpan(ctx, "VerifyWekaContainers")
		defer done()

		driveContainers := &wekav1alpha1.WekaContainerList{}
		t.Run("List Drive Containers", func(t *testing.T) {

			waitFor(ctx, func(ctx context.Context) bool {
				labels := map[string]string{
					"weka.io/mode": "drive",
				}
				err := c.List(ctx, driveContainers, client.MatchingLabels(labels), client.InNamespace(c.Cluster.OperatorNamespace))
				return err == nil && len(driveContainers.Items) >= 5
			})
			if len(driveContainers.Items) < 5 {
				t.Fatalf("expected 5 drive containers, got %d - namespace %s", len(driveContainers.Items), c.Cluster.OperatorNamespace)
			}
		})

		computeContainers := &wekav1alpha1.WekaContainerList{}
		t.Run("List Compute Containers", func(t *testing.T) {

			waitFor(ctx, func(ctx context.Context) bool {
				labels := map[string]string{
					"weka.io/mode": "compute",
				}
				err := c.List(ctx, computeContainers, client.MatchingLabels(labels), client.InNamespace(c.Cluster.OperatorNamespace))
				return err == nil && len(computeContainers.Items) >= 5
			})
			if len(computeContainers.Items) < 5 {
				t.Fatalf("expected 5 compute containers, got %d", len(computeContainers.Items))
			}
		})

		containers := &wekav1alpha1.WekaContainerList{}
		containers.Items = append(containers.Items, driveContainers.Items...)
		containers.Items = append(containers.Items, computeContainers.Items...)

		t.Run("Drivers Ensured Condition", c.DriversEnsuredCondition(ctx, containers))
		t.Run("Joined Cluster Condition", c.JoinedClusterCondition(ctx, containers))
		t.Run("Drives Added Condition", c.ContainerDrivesAddedCondition(ctx, driveContainers))
	}
}

func (c *Cluster) VerifyWekaCluster(ctx context.Context) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, logger, done := instrumentation.GetLogSpan(ctx, "VerifyWekaCluster")
		defer done()

		cluster := &wekav1alpha1.WekaCluster{}
		key := types.NamespacedName{
			Namespace: c.Cluster.OperatorNamespace,
			Name:      c.Cluster.WekaClusterName,
		}
		if err := c.Get(ctx, key, cluster); err != nil {
			t.Fatalf("failed to get weka cluster: %v", err)
		}

		logger.SetValues("cluster", cluster.Name)

		t.Run("Pods Ready Condition", c.PodsReadyCondition(ctx, cluster))
		t.Run("Cluster Created Condition", c.ClusterCreatedCondition(ctx, cluster))
		t.Run("Drives Added Condition", c.DrivesAddedCondition(ctx, cluster))
		t.Run("IO Started Condition", c.IOStartedCondition(ctx, cluster))
	}
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

func (c *Cluster) PodsReadyCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		if err := c.waitForCondition(ctx, cluster, condition.CondPodsReady); err != nil {
			t.Fatalf("failed to wait for Pods Ready condition: %v", err)
		}
	}
}

func (c *Cluster) SecretsAppliedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		if err := c.waitForCondition(ctx, cluster, condition.CondClusterSecretsApplied); err != nil {
			t.Fatalf("failed to wait for Secrets Applied condition: %v", err)
		}
	}
}

func (c *Cluster) ClusterCreatedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		if err := c.waitForCondition(ctx, cluster, condition.CondClusterCreated); err != nil {
			t.Fatalf("failed to wait for Cluster Created condition: %v", err)
		}
	}
}

func (c *Cluster) DrivesAddedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		if err := c.waitForCondition(ctx, cluster, condition.CondDrivesAdded); err != nil {
			t.Fatalf("failed to wait for Drives Added condition: %v", err)
		}
	}
}

func (c *Cluster) IOStartedCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster) func(t *testing.T) {
	return func(t *testing.T) {
		if err := c.waitForCondition(ctx, cluster, condition.CondIoStarted); err != nil {
			t.Fatalf("failed to wait for IO Started condition: %v", err)
		}
	}
}

func (c *Cluster) waitForCondition(ctx context.Context, cluster *wekav1alpha1.WekaCluster, cond string) error {
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
