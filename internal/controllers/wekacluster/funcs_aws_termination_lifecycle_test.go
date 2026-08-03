package wekacluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-steps-engine/throttling"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-k8s-api/api/v1alpha1/condition"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	awslib "github.com/weka/weka-operator/internal/services/aws"
)

// fakeManager implements ctrl.Manager just enough for tests: GetClient() returns the given fake
// client; all other methods are inherited from the nil embedded interface and would panic if called
// (none are, in these tests).
type fakeManager struct {
	ctrl.Manager
	client client.Client
}

func (f *fakeManager) GetClient() client.Client { return f.client }

// TestWekaClusterSuite is the Ginkgo entry point for the wekacluster package (no other _test.go file
// in this package registers one).
func TestWekaClusterSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "WekaCluster Suite")
}

// fakeClusterLifecycleClient is a test double for awslib.LifecycleClient.
type fakeClusterLifecycleClient struct {
	asgName        string
	lifecycleState string

	describeCalls int

	describeInstanceErr error

	putErr   error
	putCalls int
}

func (f *fakeClusterLifecycleClient) DescribeInstance(ctx context.Context, instanceID string) (string, string, error) {
	f.describeCalls++
	if f.describeInstanceErr != nil {
		return "", "", f.describeInstanceErr
	}
	return f.asgName, f.lifecycleState, nil
}

func (f *fakeClusterLifecycleClient) RecordHeartbeat(ctx context.Context, hookName, asgName, instanceID string) error {
	return nil
}

func (f *fakeClusterLifecycleClient) CompleteAction(ctx context.Context, hookName, asgName, instanceID, result string) error {
	return nil
}

func (f *fakeClusterLifecycleClient) PutTerminationHook(ctx context.Context, asgName, hookName string, heartbeatTimeout int32) error {
	f.putCalls++
	return f.putErr
}

// multiInstanceLifecycleClient is a test double for awslib.LifecycleClient that routes calls to a
// per-instance-ID fakeClusterLifecycleClient, for tests with more than one AWS instance behaving
// differently (e.g. one real ASG member, one Karpenter-style instance with no ASG).
type multiInstanceLifecycleClient struct {
	byInstanceID map[string]*fakeClusterLifecycleClient
}

func (m *multiInstanceLifecycleClient) DescribeInstance(ctx context.Context, instanceID string) (string, string, error) {
	return m.byInstanceID[instanceID].DescribeInstance(ctx, instanceID)
}

func (m *multiInstanceLifecycleClient) RecordHeartbeat(ctx context.Context, hookName, asgName, instanceID string) error {
	return m.byInstanceID[instanceID].RecordHeartbeat(ctx, hookName, asgName, instanceID)
}

func (m *multiInstanceLifecycleClient) CompleteAction(ctx context.Context, hookName, asgName, instanceID, result string) error {
	return m.byInstanceID[instanceID].CompleteAction(ctx, hookName, asgName, instanceID, result)
}

func (m *multiInstanceLifecycleClient) PutTerminationHook(ctx context.Context, asgName, hookName string, heartbeatTimeout int32) error {
	// PutTerminationHook is keyed by ASG, not instance ID; route by matching asgName against the
	// per-instance fakes (only one entry will match a given asgName in these tests).
	for _, f := range m.byInstanceID {
		if f.asgName == asgName {
			return f.PutTerminationHook(ctx, asgName, hookName, heartbeatTimeout)
		}
	}
	return nil
}

func driveContainerOnNode(name, node string) *weka.WekaContainer {
	c := &weka.WekaContainer{ObjectMeta: metav1.ObjectMeta{Name: name}}
	c.Spec.Mode = weka.WekaContainerModeDrive
	c.Spec.NodeAffinity = weka.NodeName(node)
	return c
}

var _ = Describe("ensureAwsTerminationLifecycleHook", func() {
	var (
		scheme   *runtime.Scheme
		node     *v1.Node
		cluster  *weka.WekaCluster
		fakeAsg  *fakeClusterLifecycleClient
		recorder *record.FakeRecorder
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(v1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())

		node = &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node1"},
			Spec: v1.NodeSpec{
				ProviderID: "aws:///eu-west-1a/i-0123456789abcdef0",
			},
		}

		cluster = &weka.WekaCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster1", UID: "cluster-uid"}}

		fakeAsg = &fakeClusterLifecycleClient{asgName: "my-asg"}
		newClusterLifecycleClient = func(region string) awslib.LifecycleClient { return fakeAsg }

		recorder = record.NewFakeRecorder(10)

		verifiedHookASGs.Reset()
		verifiedHookNodes.Reset()

		ensureAttemptMu.Lock()
		lastEnsureAttempt = map[string]time.Time{}
		ensureAttemptMu.Unlock()
	})

	AfterEach(func() {
		newClusterLifecycleClient = awslib.NewLifecycleClient

		verifiedHookASGs.Reset()
		verifiedHookNodes.Reset()

		ensureAttemptMu.Lock()
		lastEnsureAttempt = map[string]time.Time{}
		ensureAttemptMu.Unlock()
	})

	newLoop := func(initialProvisioning bool) *wekaClusterReconcilerLoop {
		if !initialProvisioning {
			meta_SetClusterCreatedTrue(cluster)
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(node).
			Build()

		globalThrottler := throttling.NewSyncMapThrottler()

		return &wekaClusterReconcilerLoop{
			Manager:         &fakeManager{client: fakeClient},
			Recorder:        recorder,
			cluster:         cluster,
			containers:      []*weka.WekaContainer{driveContainerOnNode("c1", "node1")},
			GlobalThrottler: globalThrottler,
			Throttler:       globalThrottler.WithPartition("cluster/cluster-uid"),
		}
	}

	It("no-ops (no Put) when the node is not on AWS", func() {
		node.Spec.ProviderID = "gce://my-project/us-central1-a/instance-1"
		loop := newLoop(false)

		Expect(loop.ensureAwsTerminationLifecycleHook(context.Background())).To(Succeed())

		Expect(fakeAsg.putCalls).To(Equal(0))
	})

	It("ensures the hook successfully", func() {
		loop := newLoop(false)

		Expect(loop.ensureAwsTerminationLifecycleHook(context.Background())).To(Succeed())

		Expect(fakeAsg.putCalls).To(Equal(1))
	})

	It("returns an error and records a Warning on initial provisioning when Put fails", func() {
		fakeAsg.putErr = fmt.Errorf("AccessDenied")
		loop := newLoop(true)

		err := loop.ensureAwsTerminationLifecycleHook(context.Background())

		Expect(err).To(HaveOccurred())
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Warning")))
	})

	It("fails open (returns nil) on a running cluster when Put fails, but still records a Warning", func() {
		fakeAsg.putErr = fmt.Errorf("AccessDenied")
		loop := newLoop(false)

		err := loop.ensureAwsTerminationLifecycleHook(context.Background())

		Expect(err).ToNot(HaveOccurred())
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Warning")))
	})

	It("returns a WaitError when a backend container has no NodeAffinity yet during initial provisioning", func() {
		loop := newLoop(true)
		loop.containers = []*weka.WekaContainer{driveContainerOnNode("c1", "")}

		err := loop.ensureAwsTerminationLifecycleHook(context.Background())

		Expect(err).To(HaveOccurred())
		var waitErr *lifecycle.WaitError
		Expect(err).To(BeAssignableToTypeOf(waitErr))
	})

	It("makes no further Put call on a second reconcile after a successful verify (cache hit)", func() {
		loop := newLoop(false)

		Expect(loop.ensureAwsTerminationLifecycleHook(context.Background())).To(Succeed())
		Expect(fakeAsg.putCalls).To(Equal(1))

		Expect(loop.ensureAwsTerminationLifecycleHook(context.Background())).To(Succeed())
		Expect(fakeAsg.putCalls).To(Equal(1))
	})

	It("throttles AWS retries: after a failed attempt, a second reconcile within the interval makes no new Put and still gates (WaitError) on initial provisioning", func() {
		fakeAsg.putErr = fmt.Errorf("AccessDenied")
		loop := newLoop(true)

		// First attempt reaches AWS and fails.
		Expect(loop.ensureAwsTerminationLifecycleHook(context.Background())).To(HaveOccurred())
		Expect(fakeAsg.putCalls).To(Equal(1))

		// Second reconcile within ensureRetryInterval: throttled — no new AWS call, but the gate still
		// holds (WaitError) so FormCluster stays blocked.
		err := loop.ensureAwsTerminationLifecycleHook(context.Background())
		var waitErr *lifecycle.WaitError
		Expect(err).To(BeAssignableToTypeOf(waitErr))
		Expect(fakeAsg.putCalls).To(Equal(1))
	})

	It("skips a node the AWS API reports is not in any ASG, with no error, even during initial provisioning", func() {
		fakeAsg.asgName = "" // AWS reports no ASG for this instance
		loop := newLoop(true)

		Expect(loop.ensureAwsTerminationLifecycleHook(context.Background())).To(Succeed())

		Expect(fakeAsg.describeCalls).To(Equal(1))
		Expect(fakeAsg.putCalls).To(Equal(0))
		Expect(verifiedHookNodes.Has("node1")).To(BeTrue())
		// Not-in-ASG is logged, not evented: no Warning is recorded for it.
		Expect(recorder.Events).To(HaveLen(0))
	})

	It("skips every not-in-ASG node in a multi-node cluster without erroring or eventing", func() {
		node2 := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node2"},
			Spec:       v1.NodeSpec{ProviderID: "aws:///eu-west-1a/i-0fedcba9876543210"},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(node, node2).
			Build()

		multiAsg := &multiInstanceLifecycleClient{
			byInstanceID: map[string]*fakeClusterLifecycleClient{
				"i-0123456789abcdef0": {asgName: ""},
				"i-0fedcba9876543210": {asgName: ""},
			},
		}
		newClusterLifecycleClient = func(region string) awslib.LifecycleClient { return multiAsg }

		globalThrottler := throttling.NewSyncMapThrottler()
		loop := &wekaClusterReconcilerLoop{
			Manager:  &fakeManager{client: fakeClient},
			Recorder: recorder,
			cluster:  cluster,
			containers: []*weka.WekaContainer{
				driveContainerOnNode("c1", "node1"),
				driveContainerOnNode("c2", "node2"),
			},
			GlobalThrottler: globalThrottler,
			Throttler:       globalThrottler.WithPartition("cluster/cluster-uid"),
		}

		Expect(loop.ensureAwsTerminationLifecycleHook(context.Background())).To(Succeed())

		Expect(multiAsg.byInstanceID["i-0123456789abcdef0"].putCalls).To(Equal(0))
		Expect(multiAsg.byInstanceID["i-0fedcba9876543210"].putCalls).To(Equal(0))
		Expect(verifiedHookNodes.Has("node1")).To(BeTrue())
		Expect(verifiedHookNodes.Has("node2")).To(BeTrue())
		Expect(recorder.Events).To(HaveLen(0))
	})

	It("still hard-errors on initial provisioning when DescribeInstance fails (fail-closed preserved)", func() {
		fakeAsg.describeInstanceErr = fmt.Errorf("AccessDenied: not authorized to perform autoscaling:DescribeAutoScalingInstances")
		loop := newLoop(true)

		err := loop.ensureAwsTerminationLifecycleHook(context.Background())

		Expect(err).To(HaveOccurred())
		Expect(fakeAsg.putCalls).To(Equal(0))
		Eventually(recorder.Events).Should(Receive(ContainSubstring("ASGResolutionFailed")))
	})

	It("fails open (returns nil) on an already-formed cluster when DescribeInstance fails", func() {
		fakeAsg.describeInstanceErr = fmt.Errorf("AccessDenied: not authorized to perform autoscaling:DescribeAutoScalingInstances")
		loop := newLoop(false)

		err := loop.ensureAwsTerminationLifecycleHook(context.Background())

		Expect(err).ToNot(HaveOccurred())
		Expect(fakeAsg.putCalls).To(Equal(0))
		Eventually(recorder.Events).Should(Receive(ContainSubstring("ASGResolutionFailed")))
	})

	It("only ensures the hook on the real ASG node in a mixed cluster (one Karpenter-style node, one ASG-backed node)", func() {
		node2 := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node2"},
			Spec: v1.NodeSpec{
				ProviderID: "aws:///eu-west-1a/i-0fedcba9876543210",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(node, node2).
			Build()

		multiAsg := &multiInstanceLifecycleClient{
			byInstanceID: map[string]*fakeClusterLifecycleClient{
				"i-0123456789abcdef0": {asgName: "my-asg"},
				"i-0fedcba9876543210": {asgName: ""},
			},
		}
		newClusterLifecycleClient = func(region string) awslib.LifecycleClient { return multiAsg }

		globalThrottler := throttling.NewSyncMapThrottler()
		loop := &wekaClusterReconcilerLoop{
			Manager:  &fakeManager{client: fakeClient},
			Recorder: recorder,
			cluster:  cluster,
			containers: []*weka.WekaContainer{
				driveContainerOnNode("c1", "node1"),
				driveContainerOnNode("c2", "node2"),
			},
			GlobalThrottler: globalThrottler,
			Throttler:       globalThrottler.WithPartition("cluster/cluster-uid"),
		}

		Expect(loop.ensureAwsTerminationLifecycleHook(context.Background())).To(Succeed())

		Expect(multiAsg.byInstanceID["i-0123456789abcdef0"].putCalls).To(Equal(1))
		Expect(multiAsg.byInstanceID["i-0fedcba9876543210"].putCalls).To(Equal(0))
		Expect(verifiedHookNodes.Has("node1")).To(BeTrue())
		Expect(verifiedHookNodes.Has("node2")).To(BeTrue())
		Expect(recorder.Events).To(HaveLen(0))
	})
})

// meta_SetClusterCreatedTrue marks the CondClusterCreated condition True on cluster, simulating an
// already-formed (running) cluster.
func meta_SetClusterCreatedTrue(cluster *weka.WekaCluster) {
	cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
		Type:               condition.CondClusterCreated,
		Status:             metav1.ConditionTrue,
		Reason:             "Ready",
		LastTransitionTime: metav1.Now(),
	})
}
