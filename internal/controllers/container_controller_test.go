package controllers

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	wekav1alpha1 "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/internal/services/exec"
	"github.com/weka/weka-operator/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconcile_ContainerController(t *testing.T) {
	os.Setenv("KUBERNETES_SERVICE_HOST", "localhost")
	os.Setenv("KUBERNETES_SERVICE_PORT", "6443")
	os.Setenv("OPERATOR_DEV_MODE", "true")
	os.Setenv("UNIT_TEST", "true")
	os.Setenv("WEKA_OPERATOR_MAINTENANCE_SA_NAME", "weka-operator-maintenance")

	ctx, _, done := instrumentation.GetLogSpan(pkgCtx, "TestReconcile_ContainerController")
	defer done()

	exec.NewExecService = exec.NullExecService
	util.NewExecInPod = util.NullExecInPod
	manager, err := testingManager()
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	k8sClient := manager.GetClient()
	name := "test-container"
	key := client.ObjectKey{
		Namespace: "default",
		Name:      name,
	}

	container := &wekav1alpha1.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: key.Namespace,
			Name:      name,
		},
		Spec: wekav1alpha1.WekaContainerSpec{
			Mode:              "drive",
			CpuPolicy:         wekav1alpha1.CpuPolicyDedicated,
			Image:             "wekaproject/weka-drivers-loader:latest",
			WekaContainerName: "test-container",
		},
	}
	err = k8sClient.Create(ctx, container)
	if err != nil {
		t.Fatalf("failed to create container: %v", err)
	}

	reconciler := NewContainerController(manager)
	req := ctrl.Request{NamespacedName: key}
	t.Run("FromBeginning", func(t *testing.T) {
		_, err = reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("failed to reconcile container: %v", err)
		}

		container = &wekav1alpha1.WekaContainer{}
		err = k8sClient.Get(ctx, key, container)
		if err != nil {
			t.Errorf("failed to get container: %v", err)
		}

		for _, condition := range container.Status.Conditions {
			if condition.Status != metav1.ConditionFalse {
				t.Errorf("container condition should not be ready: %v", condition)
			}
		}
	})

	t.Run("DiscoveryNodeInfo", func(t *testing.T) {
		containerList := &wekav1alpha1.WekaContainerList{}
		err := k8sClient.List(ctx, containerList, client.MatchingLabels{"weka.io/mode": "discovery"})
		if err != nil {
			t.Fatalf("failed to list containers: %v", err)
		}
		if len(containerList.Items) == 0 {
			t.Fatalf("no discover containers found")
		}
		container := &containerList.Items[0]
		discoveryNodeInfo := &discovery.DiscoveryNodeInfo{}

		jsonData, err := json.Marshal(discoveryNodeInfo)
		if err != nil {
			t.Fatalf("failed to marshal discoveryNodeInfo: %v", err)
		}
		discoveryString := string(jsonData)
		container.Status.ExecutionResult = &discoveryString

		err = k8sClient.Status().Update(ctx, container)
		if err != nil {
			t.Fatalf("failed to update container status: %v", err)
		}

		result, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result != (ctrl.Result{RequeueAfter: 3 * time.Second}) {
			t.Fatalf("expected requeue result, got: %v", result)
		}
	})

	// TODO: Assign pod to node
	t.Run("AssignPodToNode", func(t *testing.T) {
		container := &wekav1alpha1.WekaContainer{}
		err := k8sClient.Get(ctx, key, container)
		if err != nil {
			t.Fatalf("failed to get container: %v", err)
		}

		pod := &corev1.Pod{}
		err = k8sClient.Get(ctx, client.ObjectKey{
			Namespace: container.Namespace,
			Name:      container.Name,
		}, pod)
		if err != nil {
			t.Fatalf("failed to get pod: %v", err)
		}

		pod.Spec.NodeName = "node-1"
		pod.Status.Phase = corev1.PodRunning
		err = k8sClient.Update(ctx, pod)
		if err != nil {
			t.Fatalf("failed to update pod: %v", err)
		}

		container.Status.Allocations = &wekav1alpha1.ContainerAllocations{}
		err = k8sClient.Status().Update(ctx, container)
		if err != nil {
			t.Fatalf("failed to update container status: %v", err)
		}

		result, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result != (ctrl.Result{RequeueAfter: 3 * time.Second}) {
			t.Fatalf("expected no requeue result, got: %v", result)
		}

		if err := k8sClient.Get(ctx, key, container); err != nil {
			t.Fatalf("failed to get container: %v", err)
		}

		completedConditions := []string{
			condition.CondContainerAffinitySet,
			condition.CondContainerResourcesWritten,
		}
		for _, condition := range completedConditions {
			c := meta.FindStatusCondition(container.Status.Conditions, condition)
			if c == nil {
				t.Errorf("container condition not found: %v", condition)
			}
		}
	})

	t.Run("EnsureDrivers", func(t *testing.T) {
		driversLoader := &wekav1alpha1.WekaContainer{}
		driversLoaderKey := client.ObjectKey{
			Namespace: "weka-operator-system",
			Name:      "weka-drivers-loader-",
		}
		err := k8sClient.Get(ctx, driversLoaderKey, driversLoader)
		if err != nil {
			t.Fatalf("failed to get drivers loader: %v", err)
		}

		driveLoadResults := &operations.DriveLoadResults{
			Loaded: true,
		}
		jsonData, err := json.Marshal(driveLoadResults)
		if err != nil {
			t.Fatalf("failed to marshal driveLoadResults: %v", err)
		}
		driveLoadString := string(jsonData)
		driversLoader.Status.ExecutionResult = &driveLoadString

		err = k8sClient.Status().Update(ctx, driversLoader)
		if err != nil {
			t.Fatalf("failed to update drivers loader status: %v", err)
		}

		result, err := reconciler.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		node := &corev1.Node{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: "node-1"}, node); err != nil {
			t.Fatalf("failed to get node: %v", err)
		}

		driversVersionExpected := fmt.Sprintf("%s:%s", container.Spec.Image, node.Status.NodeInfo.BootID)
		if node.Annotations["weka.io/drivers-loaded"] != driversVersionExpected {
			t.Errorf("expected drivers loaded annotation to be set to %v, got: %v", driversVersionExpected, node.Annotations["weka.io/drivers-loaded"])
		}

		if !result.Requeue {
			t.Errorf("expected requeue result, got: %v", result)
		}
		if result.RequeueAfter != 30*time.Second {
			t.Errorf("expected requeue after 30 seconds, got: %v", result.RequeueAfter)
		}

		if err := k8sClient.Get(ctx, key, container); err != nil {
			t.Fatalf("failed to get container: %v", err)
		}

		completedConditions := []string{
			condition.CondEnsureDrivers,
			condition.CondJoinedCluster,
		}
		for _, condition := range completedConditions {
			c := meta.FindStatusCondition(container.Status.Conditions, condition)
			if c == nil {
				t.Errorf("container condition not found: %v", condition)
				continue
			}
			if c.Status != metav1.ConditionTrue {
				t.Errorf("container condition not ready: %+v", c)
			}
		}
	})

	t.Run("Validate Final Conditions", func(t *testing.T) {
		conditions := container.Status.Conditions
		for _, condition := range conditions {
			t.Logf("condition: %v", condition)
			if condition.Status != metav1.ConditionTrue {
				t.Errorf("container condition not ready: %v", condition)
			}
		}
	})

	if err := k8sClient.Delete(ctx, container); err != nil {
		t.Fatalf("failed to delete container: %v", err)
	}
}

func TestNewContainerController(t *testing.T) {
	manager, err := testingManager()
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	subject := NewContainerController(manager)
	if subject == nil {
		t.Errorf("NewContainerController() returned nil")
		return
	}

	if subject.Client == nil {
		t.Errorf("NewContainerController() returned controller with nil client")
	}

	if subject.Scheme == nil {
		t.Errorf("NewContainerController() returned controller with nil scheme")
	}
}
