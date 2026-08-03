package wekacontainer

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/config"
	awslib "github.com/weka/weka-operator/internal/services/aws"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

// fakeLifecycleClient is a test double for awslib.LifecycleClient.
type fakeLifecycleClient struct {
	asgName        string
	lifecycleState string

	describeCalls  int
	heartbeatCalls int
	completeCalls  int
	completeResult string

	describeInstanceErr error

	putErr           error
	putCalls         int
	lastPutAsgName   string
	lastPutHookName  string
	lastPutHeartbeat int32
}

func (f *fakeLifecycleClient) DescribeInstance(ctx context.Context, instanceID string) (string, string, error) {
	f.describeCalls++
	if f.describeInstanceErr != nil {
		return "", "", f.describeInstanceErr
	}
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

func (f *fakeLifecycleClient) PutTerminationHook(ctx context.Context, asgName, hookName string, heartbeatTimeout int32) error {
	f.putCalls++
	f.lastPutAsgName = asgName
	f.lastPutHookName = hookName
	f.lastPutHeartbeat = heartbeatTimeout
	return f.putErr
}

// fakeKubeService is a KubeService test double: only GetPods is exercised here (by
// allBackendPodsOnNodeExited), returning the preset backend pods / error. The other interface methods
// are inherited from the embedded (nil) interface and would panic if called — none are, in these tests.
type fakeKubeService struct {
	kubernetes.KubeService
	pods []v1.Pod
	err  error
}

func (f *fakeKubeService) GetPods(ctx context.Context, options kubernetes.GetPodsOptions) ([]v1.Pod, error) {
	return f.pods, f.err
}

func podInPhase(name string, phase v1.PodPhase) v1.Pod {
	return v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status:     v1.PodStatus{Phase: phase},
	}
}

var _ = Describe("reconcileAwsTerminationLifecycle", func() {
	var (
		scheme      *runtime.Scheme
		container   *weka.WekaContainer
		node        *v1.Node
		pod         *v1.Pod
		fakeAsg     *fakeLifecycleClient
		backendPods []v1.Pod
		kubeErr     error
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(v1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())

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

		// Default: one backend pod on the node still Running => not all exited => HOLD.
		backendPods = []v1.Pod{podInPhase("p1", v1.PodRunning)}
		kubeErr = nil

		fakeAsg = &fakeLifecycleClient{asgName: "my-asg", lifecycleState: "Terminating:Wait"}

		lifecycleClientsMu.Lock()
		lifecycleClients = map[string]awslib.LifecycleClient{}
		newLifecycleClient = func(region string) awslib.LifecycleClient { return fakeAsg }
		lifecycleClientsMu.Unlock()

		releasedInstances.Reset()
	})

	AfterEach(func() {
		lifecycleClientsMu.Lock()
		lifecycleClients = map[string]awslib.LifecycleClient{}
		newLifecycleClient = awslib.NewLifecycleClient
		lifecycleClientsMu.Unlock()

		releasedInstances.Reset()
	})

	newReconciler := func() *containerReconcilerLoop {
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(container).
			WithStatusSubresource(container).
			Build()
		return &containerReconcilerLoop{
			Client:      fakeClient,
			KubeService: &fakeKubeService{pods: backendPods, err: kubeErr},
			container:   container,
			node:        node,
			pod:         pod,
		}
	}

	It("no-ops (no ASG action) when the instance is not in Terminating:Wait", func() {
		fakeAsg.lifecycleState = "InService"
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.describeCalls).To(Equal(1))
		Expect(fakeAsg.heartbeatCalls).To(Equal(0))
		Expect(fakeAsg.completeCalls).To(Equal(0))
	})

	It("sends one heartbeat and sets the timestamp when a backend pod has not exited and no prior heartbeat exists", func() {
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.heartbeatCalls).To(Equal(1))
		Expect(fakeAsg.completeCalls).To(Equal(0))
		Expect(container.Status.Timestamps).To(HaveKey(lifecycleHeartbeatTimestamp))
	})

	It("skips heartbeat when the last heartbeat was recorded less than an hour ago", func() {
		container.Status.Timestamps = map[string]metav1.Time{
			lifecycleHeartbeatTimestamp: {Time: time.Now().Add(-10 * time.Minute)},
		}
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.heartbeatCalls).To(Equal(0))
		Expect(fakeAsg.completeCalls).To(Equal(0))
	})

	It("sends a new heartbeat when the last one was recorded more than an hour ago", func() {
		container.Status.Timestamps = map[string]metav1.Time{
			lifecycleHeartbeatTimestamp: {Time: time.Now().Add(-2 * time.Hour)},
		}
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.heartbeatCalls).To(Equal(1))
	})

	It("keeps holding (heartbeats, no release) while some backend pod on the node is still running", func() {
		backendPods = []v1.Pod{podInPhase("drive", v1.PodSucceeded), podInPhase("compute", v1.PodRunning)}
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.heartbeatCalls).To(Equal(1))
		Expect(fakeAsg.completeCalls).To(Equal(0))
	})

	It("completes the lifecycle action (CONTINUE) once all backend pods on the node have exited (Succeeded)", func() {
		backendPods = []v1.Pod{podInPhase("drive", v1.PodSucceeded), podInPhase("compute", v1.PodSucceeded)}
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.completeCalls).To(Equal(1))
		Expect(fakeAsg.completeResult).To(Equal("CONTINUE"))
		Expect(fakeAsg.heartbeatCalls).To(Equal(0))
	})

	It("does not re-issue CompleteAction on a later reconcile of an already-released instance", func() {
		backendPods = []v1.Pod{podInPhase("drive", v1.PodSucceeded), podInPhase("compute", v1.PodSucceeded)}
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())
		Expect(fakeAsg.completeCalls).To(Equal(1))
		describeAfterRelease := fakeAsg.describeCalls

		// Second reconcile of the same instance is short-circuited by the released guard: no further
		// CompleteAction, and not even a DescribeInstance call.
		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())
		Expect(fakeAsg.completeCalls).To(Equal(1))
		Expect(fakeAsg.describeCalls).To(Equal(describeAfterRelease))
	})

	It("completes the lifecycle action when no backend pods remain on the node (all reaped)", func() {
		backendPods = []v1.Pod{}
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.completeCalls).To(Equal(1))
		Expect(fakeAsg.completeResult).To(Equal("CONTINUE"))
	})

	It("keeps holding (no release) when listing backend pods on the node fails", func() {
		kubeErr = errors.New("api down")
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.completeCalls).To(Equal(0))
		Expect(fakeAsg.heartbeatCalls).To(Equal(1))
	})

	It("makes no AWS call when SkipAwsTerminationLifecycleHook is set", func() {
		config.Config.SkipAwsTerminationLifecycleHook = true
		defer func() { config.Config.SkipAwsTerminationLifecycleHook = false }()
		r := newReconciler()

		Expect(r.reconcileAwsTerminationLifecycle(context.Background())).To(Succeed())

		Expect(fakeAsg.describeCalls).To(Equal(0))
		Expect(fakeAsg.heartbeatCalls).To(Equal(0))
		Expect(fakeAsg.completeCalls).To(Equal(0))
	})
})
