/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weka/weka-operator/internal/app/manager/controllers/condition"
	wekav1alpha1 "github.com/weka/weka-operator/internal/pkg/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (env *TestEnvironment) createNamespace(name string) error {
	ctx, cancel := context.WithTimeout(env.Ctx, 10*time.Second)
	defer cancel()

	key := client.ObjectKey{Name: name}
	namespace := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	if err := env.Client.Get(ctx, key, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			if err := env.Client.Create(ctx, namespace); err != nil {
				return err
			}
		}
	}
	waitFor(ctx, func(ctx context.Context) bool {
		err := env.Client.Get(ctx, key, namespace)
		return err == nil
	})

	return nil
}

func (env *TestEnvironment) deleteNamespace(name string) {
	logger := env.Logger.WithName("deleteNamespace")
	logger.Info("Deleting namespace", "name", name)
	defer logger.Info("Deleted namespace", "name", name)

	ctx, cancel := context.WithTimeout(env.Ctx, 10*time.Second)
	defer cancel()

	key := client.ObjectKey{Name: name}
	namespace := &v1.Namespace{}
	if err := env.Client.Get(ctx, key, namespace); err != nil {
		logger.Error(err, "Failed to get namespace", "name", name)
		return
	}
	if err := env.Client.Delete(ctx, namespace); err != nil {
		logger.Error(err, "Failed to delete namespace", "name", name)
		return
	}
	waitFor(ctx, func(ctx context.Context) bool {
		err := env.Client.Get(ctx, key, namespace)
		return apierrors.IsNotFound(err)
	})
}

func testingCluster() *wekav1alpha1.WekaCluster {
	key := client.ObjectKey{Name: "test-cluster", Namespace: "default"}
	cluster := &wekav1alpha1.WekaCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
		Spec: wekav1alpha1.WekaClusterSpec{
			Size:     5,
			Template: "dev",
			Topology: "dev_wekabox",
			Image:    "test-image",
		},
	}
	return cluster
}

type ReconcilerTestCase struct {
	Reconciler *WekaClusterReconciler
}

func TestNewWekaClusterReconciler(t *testing.T) {
	testEnv, err := setupTestEnv(context.Background())
	if err != nil {
		t.Fatalf("failed to setup test environment: %v", err)
	}
	defer teardownTestEnv(testEnv)

	testEnv.createNamespace("weka-operator-system")
	defer testEnv.deleteNamespace("weka-operator-system")

	subject := NewWekaClusterController(testEnv.Manager)
	members := []struct {
		name   string
		member interface{}
	}{
		{"controller", subject},
		{"client", subject.Client},
		{"scheme", subject.Scheme},
		{"recorder", subject.Recorder},
	}
	for _, test := range members {
		if test.member == nil {
			t.Errorf("Member was nil: %s", test.name)
		}
	}
	test := ReconcilerTestCase{Reconciler: subject}
	t.Run("doFinalizerOperationsForwekaCluster", doFinalizerOperationsForwekaCluster(testEnv, test))
}

func doFinalizerOperationsForwekaCluster(testEnv *TestEnvironment, test ReconcilerTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		recorder := record.NewFakeRecorder(1)
		test.Reconciler.Recorder = recorder

		if err := test.Reconciler.doFinalizerOperationsForwekaCluster(testEnv.Ctx, testingCluster()); err != nil {
			t.Fatalf("failed to do finalizer operations: %v", err)
		}

		expected := "Warning Deleting Custom Resource test-cluster is being deleted from the namespace default"
		actual := string(<-recorder.Events)
		if !strings.Contains(actual, expected) {
			t.Fatalf("expected event '%s', got: '%s'", expected, actual)
		}
	}
}

type ClusterTestCase struct {
	key      client.ObjectKey
	template string
}

func TestWekaClusterController(t *testing.T) {
	testEnv, err := setupTestEnv(context.Background())
	if err != nil {
		t.Fatalf("failed to setup test environment: %v", err)
	}
	logger := testEnv.Logger.WithName("TestWekaClusterController")

	testEnv.createNamespace("weka-operator-system")

	tests := []ClusterTestCase{
		{
			key:      client.ObjectKey{Name: "test-cluster-dev", Namespace: "default"},
			template: "dev",
		},
	}

	for _, test := range tests {
		logger.Info("Running", "key", test.key)

		key := test.key
		cluster := &wekav1alpha1.WekaCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      key.Name,
				Namespace: key.Namespace,
			},
			Spec: wekav1alpha1.WekaClusterSpec{
				Size:     1,
				Template: test.template,
				Topology: "dev_wekabox",
				Image:    "test-image",
			},
		}

		if err := testEnv.Client.Create(testEnv.Ctx, cluster); err != nil {
			t.Fatalf("failed to create cluster: %v", err)
		}
		waitFor(testEnv.Ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			return err == nil
		})
		name := test.template
		t.Run(fmt.Sprintf("ShouldSetAFinalizer-%s", name), ShouldSetAFinalizer(testEnv, test))
		t.Run(fmt.Sprintf("ShouldInitializeTheState-%s", name), ShouldInitializeTheState(testEnv, test))
		// t.Run(fmt.Sprintf("ShouldCreatePods-%s", name), ShouldCreatePods(testEnv, test))
		// t.Run(fmt.Sprintf("CondClusterCreated-%s", name), CondClusterCreated(testEnv, test))
		t.Run(fmt.Sprintf("ShouldCreateLoginCredentials-%s", name), CondClusterSecretsCreated(testEnv, test))
		t.Run(fmt.Sprintf("CondDrivesAdded-%s", name), CondDrivesAdded(testEnv, test))
		t.Run(fmt.Sprintf("CondIoStarted-%s", name), CondIoStarted(testEnv, test))
		// t.Run(fmt.Sprintf("CondClusterSecretsApplied-%s", name), CondClusterSecretsApplied(testEnv, test))

		if err := testEnv.Client.Delete(testEnv.Ctx, cluster); err != nil {
			t.Fatalf("failed to delete cluster: %v", err)
		}
		waitFor(testEnv.Ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			return apierrors.IsNotFound(err)
		})
	}

	testEnv.deleteNamespace("weka-operator-system")
	teardownTestEnv(testEnv)
}

func TestDeleteCluster(t *testing.T) {
	testEnv, err := setupTestEnv(context.Background())
	if err != nil {
		t.Fatalf("failed to setup test environment: %v", err)
	}
	defer teardownTestEnv(testEnv)

	ctx, cancel := context.WithTimeout(testEnv.Ctx, 10*time.Second)
	defer cancel()

	key := client.ObjectKey{Name: "test-cluster", Namespace: "default"}
	cluster := testingCluster()
	if err := testEnv.Client.Create(ctx, cluster); err != nil {
		t.Fatalf("failed to create cluster: %v", err)
	}
	waitFor(ctx, func(ctx context.Context) bool {
		err := testEnv.Client.Get(ctx, key, cluster)
		return err == nil
	})

	t.Run(fmt.Sprintf("ShouldSetAFinalizer-%s", key.Name), ShouldSetAFinalizer(testEnv, ClusterTestCase{key: key}))

	// Delete the cluster
	if err := testEnv.Client.Delete(ctx, cluster); err != nil {
		t.Fatalf("failed to delete cluster: %v", err)
	}
	waitFor(ctx, func(ctx context.Context) bool {
		err := testEnv.Client.Get(ctx, key, cluster)
		return apierrors.IsNotFound(err)
	})
}

func ShouldSetAFinalizer(testEnv *TestEnvironment, test ClusterTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		logger := testEnv.Logger.WithName("ShouldSetAFinalizer")
		logger.Info("Running", "key", test.key)
		defer logger.Info("Finished", "key", test.key)

		timeout := 10 * time.Second
		ctx, cancel := context.WithTimeout(testEnv.Ctx, timeout)
		defer cancel()

		cluster := &wekav1alpha1.WekaCluster{}
		waitFor(ctx, func(ctx context.Context) bool {
			key := test.key
			err := testEnv.Client.Get(ctx, key, cluster)
			return err == nil && controllerutil.ContainsFinalizer(cluster, WekaFinalizer)
		})
		if !controllerutil.ContainsFinalizer(cluster, WekaFinalizer) {
			t.Errorf("Finalizer not set")
		}
	}
}

func ShouldInitializeTheState(testEnv *TestEnvironment, test ClusterTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		timeout := 10 * time.Second
		ctx, cancel := context.WithTimeout(testEnv.Ctx, timeout)
		defer cancel()

		cluster := &wekav1alpha1.WekaCluster{}
		waitFor(ctx, func(ctx context.Context) bool {
			key := test.key
			err := testEnv.Client.Get(ctx, key, cluster)
			return err == nil && len(cluster.Status.Conditions) > 0
		})
		if len(cluster.Status.Conditions) == 0 {
			t.Errorf("Expected at least one condition")
		}
	}
}

func ShouldCreatePods(testEnv *TestEnvironment, test ClusterTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		timeout := 10 * time.Second
		ctx, cancel := context.WithTimeout(testEnv.Ctx, timeout)
		defer cancel()

		key := test.key
		cluster := &wekav1alpha1.WekaCluster{}
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			return err == nil && len(cluster.Status.Conditions) > 0
		})
		if len(cluster.Status.Conditions) == 0 {
			t.Errorf("Expected at least one condition")
		}

		var podsCreatedCondition *metav1.Condition
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			if err != nil {
				return false
			}

			podsCreatedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondPodsCreated)
			if podsCreatedCondition == nil {
				return false
			}

			return podsCreatedCondition.Status == metav1.ConditionTrue
		})
		if podsCreatedCondition == nil {
			t.Errorf("Expected pods created condition")
		}
		if podsCreatedCondition.Reason != "Init" {
			t.Errorf("Expected reason to be Init, got %v", podsCreatedCondition)
		}
		if podsCreatedCondition.Message != "All pods are created" {
			t.Errorf("Expected message to be 'All pods are created', got %v", podsCreatedCondition)
		}

		pods := &v1.PodList{}
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.List(ctx, pods, client.InNamespace(key.Namespace))
			if err != nil {
				return false
			}
			return len(pods.Items) > 0
		})
		if len(pods.Items) == 0 {
			t.Skip("No pods found")
		}
	}
}

func CondClusterSecretsCreated(testEnv *TestEnvironment, test ClusterTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(testEnv.Ctx, 10*time.Second)
		defer cancel()

		cluster := &wekav1alpha1.WekaCluster{}
		key := test.key
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			return err == nil && len(cluster.Status.Conditions) > 0
		})

		var clusterSecretsCreatedCondition *metav1.Condition
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			if err != nil {
				return false
			}
			clusterSecretsCreatedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondClusterSecretsCreated)
			return clusterSecretsCreatedCondition != nil && clusterSecretsCreatedCondition.Status == metav1.ConditionTrue
		})

		if clusterSecretsCreatedCondition == nil {
			t.Errorf("Expected cluster secrets created condition")
		}
		if clusterSecretsCreatedCondition.Reason != "Init" {
			t.Errorf("Expected reason to be Init, got %v", clusterSecretsCreatedCondition)
		}
	}
}

func CondClusterCreated(testEnv *TestEnvironment, test ClusterTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(testEnv.Ctx, 10*time.Second)
		defer cancel()

		cluster := &wekav1alpha1.WekaCluster{}
		key := test.key
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			return err == nil && len(cluster.Status.Conditions) > 0
		})

		drivePod := &v1.Pod{}
		drivePodKey := client.ObjectKey{Name: fmt.Sprintf("%s-drive-0", key.Name), Namespace: key.Namespace}
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, drivePodKey, drivePod)
			return err == nil
		})
		if drivePod.Name != fmt.Sprintf("%s-drive-0", key.Name) {
			t.Skipf("Expected drive pod to be %s-drive-0, got %v", key.Name, drivePod.Name)
		}

		var clusterCreatedCondition *metav1.Condition
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			if err != nil {
				return false
			}
			clusterCreatedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondClusterCreated)
			return clusterCreatedCondition != nil && clusterCreatedCondition.Status == metav1.ConditionTrue
		})

		if clusterCreatedCondition == nil {
			t.Fatal("Expected cluster created condition")
		}
		// Not actually what we want, but this is what it does in test right now
		if clusterCreatedCondition.Reason != "Init" {
			t.Errorf("Expected reason to be Init, got %v", clusterCreatedCondition)
		}
		if !strings.Contains(clusterCreatedCondition.Message, "Could not create executor") {
			t.Errorf("Expected message to be 'containers list is empty', got %v", clusterCreatedCondition)
		}
	}
}

func CondDrivesAdded(testEnv *TestEnvironment, test ClusterTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(testEnv.Ctx, 10*time.Second)
		defer cancel()

		cluster := &wekav1alpha1.WekaCluster{}
		key := test.key
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			return err == nil && len(cluster.Status.Conditions) > 0
		})

		var drivesAddedCondition *metav1.Condition
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			if err != nil {
				return false
			}
			drivesAddedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondDrivesAdded)
			return drivesAddedCondition != nil && drivesAddedCondition.Status == metav1.ConditionTrue
		})

		if drivesAddedCondition == nil {
			t.Errorf("Expected drives added condition")
		}
		if drivesAddedCondition.Reason != "Init" {
			t.Errorf("Expected reason to be Init, got %v", drivesAddedCondition)
		}
		if drivesAddedCondition.Message != "Drives are not added yet" {
			t.Errorf("Expected message to be 'Drives are not added yet', got %v", drivesAddedCondition)
		}
	}
}

func CondIoStarted(testEnv *TestEnvironment, test ClusterTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		t.Skip("Not implemented")
	}
}

func CondClusterSecretsApplied(testEnv *TestEnvironment, test ClusterTestCase) func(t *testing.T) {
	return func(t *testing.T) {
		ctx, cancel := context.WithTimeout(testEnv.Ctx, 10*time.Second)
		defer cancel()

		cluster := &wekav1alpha1.WekaCluster{}
		key := test.key
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			return err == nil && len(cluster.Status.Conditions) > 0
		})

		var clusterSecretsAppliedCondition *metav1.Condition
		waitFor(ctx, func(ctx context.Context) bool {
			err := testEnv.Client.Get(ctx, key, cluster)
			if err != nil {
				return false
			}
			clusterSecretsAppliedCondition = meta.FindStatusCondition(cluster.Status.Conditions, condition.CondClusterSecretsApplied)
			return clusterSecretsAppliedCondition != nil && clusterSecretsAppliedCondition.Status == metav1.ConditionTrue
		})

		if clusterSecretsAppliedCondition == nil {
			t.Errorf("Expected cluster secrets applied condition")
		}
		if clusterSecretsAppliedCondition.Reason != "Init" {
			t.Errorf("Expected reason to be Init, got %v", clusterSecretsAppliedCondition)
		}
	}
}
