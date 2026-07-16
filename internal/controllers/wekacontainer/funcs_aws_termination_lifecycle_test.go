package wekacontainer

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	awslib "github.com/weka/weka-operator/internal/services/aws"
)

// fakeLifecycleClient is a test double for awslib.LifecycleClient.
type fakeLifecycleClient struct {
	asgName        string
	lifecycleState string

	describeCalls  int
	heartbeatCalls int
	completeCalls  int
	completeResult string
}

func (f *fakeLifecycleClient) DescribeInstance(ctx context.Context, instanceID string) (string, string, error) {
	f.describeCalls++
	return f.asgName, f.lifecycleState, nil
}

func (f *fakeLifecycleClient) RecordHeartbeat(ctx context.Context, hookName, asgName, instanceID string) error {
	f.heartbeatCalls++
	return nil
}

func (f *fakeLifecycleClient) CompleteAction(ctx context.Context, hookName, asgName, instanceID, result string) error {
	f.completeCalls++
	f.completeResult = result
	return nil
}

var _ = Describe("reconcileTerminationLifecycle", func() {
	var (
		scheme    *runtime.Scheme
		container *weka.WekaContainer
		node      *v1.Node
		pod       *v1.Pod
		fakeAsg   *fakeLifecycleClient
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(v1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())

		config.Config.Aws.NodeLifecycleHookName = "weka-drive-drain"

		container = &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"},
			Spec:       weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive},
		}
		node = &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node1"},
			Spec: v1.NodeSpec{
				ProviderID:    "aws:///eu-west-1a/i-0123456789abcdef0",
				Unschedulable: true,
			},
		}
		pod = &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "default"}}

		fakeAsg = &fakeLifecycleClient{asgName: "my-asg", lifecycleState: "Terminating:Wait"}

		lifecycleClientsMu.Lock()
		lifecycleClients = map[string]awslib.LifecycleClient{}
		newLifecycleClient = func(region string) awslib.LifecycleClient { return fakeAsg }
		lifecycleClientsMu.Unlock()
	})

	AfterEach(func() {
		config.Config.Aws.NodeLifecycleHookName = ""
		lifecycleClientsMu.Lock()
		lifecycleClients = map[string]awslib.LifecycleClient{}
		newLifecycleClient = awslib.NewLifecycleClient
		lifecycleClientsMu.Unlock()
	})

	newReconciler := func() *containerReconcilerLoop {
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(container).
			WithStatusSubresource(container).
			Build()
		return &containerReconcilerLoop{
			Client:    fakeClient,
			container: container,
			node:      node,
			pod:       pod,
		}
	}

	It("no-ops (no ASG action) when the instance is not in Terminating:Wait", func() {
		fakeAsg.lifecycleState = "InService"
		r := newReconciler()

		Expect(r.reconcileTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.describeCalls).To(Equal(1))
		Expect(fakeAsg.heartbeatCalls).To(Equal(0))
		Expect(fakeAsg.completeCalls).To(Equal(0))
	})

	It("sends one heartbeat and sets the timestamp when drives are not yet removed and no prior heartbeat exists", func() {
		r := newReconciler()

		Expect(r.reconcileTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.heartbeatCalls).To(Equal(1))
		Expect(fakeAsg.completeCalls).To(Equal(0))
		Expect(container.Status.Timestamps).To(HaveKey(consts.LifecycleHeartbeatTimestamp))
	})

	It("skips heartbeat when the last heartbeat was recorded less than an hour ago", func() {
		container.Status.Timestamps = map[string]metav1.Time{
			consts.LifecycleHeartbeatTimestamp: {Time: time.Now().Add(-10 * time.Minute)},
		}
		r := newReconciler()

		Expect(r.reconcileTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.heartbeatCalls).To(Equal(0))
		Expect(fakeAsg.completeCalls).To(Equal(0))
	})

	It("sends a new heartbeat when the last one was recorded more than an hour ago", func() {
		container.Status.Timestamps = map[string]metav1.Time{
			consts.LifecycleHeartbeatTimestamp: {Time: time.Now().Add(-2 * time.Hour)},
		}
		r := newReconciler()

		Expect(r.reconcileTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.heartbeatCalls).To(Equal(1))
	})

	It("completes the lifecycle action (CONTINUE) once drives are removed", func() {
		meta_SetStatusConditionTrue(&container.Status.Conditions, condition.CondContainerDrivesRemoved)
		r := newReconciler()

		Expect(r.reconcileTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.completeCalls).To(Equal(1))
		Expect(fakeAsg.completeResult).To(Equal("CONTINUE"))
		Expect(fakeAsg.heartbeatCalls).To(Equal(0))
	})
})

// meta_SetStatusConditionTrue is a tiny test helper mirroring k8s.io/apimachinery/pkg/api/meta.SetStatusCondition,
// setting condType to True so WekaContainer.DrivesRemoved() reports true.
func meta_SetStatusConditionTrue(conditions *[]metav1.Condition, condType string) {
	now := metav1.Now()
	*conditions = append(*conditions, metav1.Condition{
		Type:               condType,
		Status:             metav1.ConditionTrue,
		Reason:             "Test",
		LastTransitionTime: now,
	})
}
