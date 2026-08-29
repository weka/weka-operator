package wekacontainer

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/services/kubernetes"
)

// fakeNodeKubeService is a KubeService test double for deleteIfNoNode /
// handleBackendNodeRemovalGrace: GetNode returns either the preset node (present) or the preset error
// (e.g. a NotFound), and GetNodes returns the preset surviving-node list used to infer the cluster's
// cloud provider once the affinity node is gone. The other interface methods are inherited from the
// embedded (nil) interface and would panic if called — none are, in these tests.
type fakeNodeKubeService struct {
	kubernetes.KubeService
	node  *v1.Node
	err   error
	nodes []v1.Node
}

func (f *fakeNodeKubeService) GetNode(ctx context.Context, nodeName k8sTypes.NodeName) (*v1.Node, error) {
	return f.node, f.err
}

func (f *fakeNodeKubeService) GetNodes(ctx context.Context, nodeSelector map[string]string) ([]v1.Node, error) {
	return f.nodes, nil
}

var _ = Describe("deleteIfNoNode / handleBackendNodeRemovalGrace", func() {
	var (
		scheme       *runtime.Scheme
		container    *weka.WekaContainer
		origMode     config.CleanupRemovedNodesMode
		nodeNotFound = apierrors.NewNotFound(v1.Resource("nodes"), "node1")
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(v1.AddToScheme(scheme)).To(Succeed())
		Expect(weka.AddToScheme(scheme)).To(Succeed())

		origMode = config.Config.CleanupRemovedNodes

		container = &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "c1",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{
					{APIVersion: "v1", Kind: "Owner", Name: "owner", UID: "owner-uid"},
				},
			},
			Spec: weka.WekaContainerSpec{
				Mode:         weka.WekaContainerModeDrive,
				NodeAffinity: "node1",
			},
		}
	})

	AfterEach(func() {
		config.Config.CleanupRemovedNodes = origMode
	})

	newReconciler := func(kubeSvc kubernetes.KubeService) *containerReconcilerLoop {
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(container).
			WithStatusSubresource(container).
			Build()
		return &containerReconcilerLoop{
			Client:      fakeClient,
			KubeService: kubeSvc,
			container:   container,
			Recorder:    record.NewFakeRecorder(10),
		}
	}

	// newReconcilerWithPod is like newReconciler but also seeds a pod object in the fake client and
	// attaches it to the reconciler, for tests exercising the terminal-pod reap during the Stale grace
	// window.
	newReconcilerWithPod := func(kubeSvc kubernetes.KubeService, pod *v1.Pod) *containerReconcilerLoop {
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(container, pod).
			WithStatusSubresource(container).
			Build()
		return &containerReconcilerLoop{
			Client:      fakeClient,
			KubeService: kubeSvc,
			container:   container,
			pod:         pod,
			Recorder:    record.NewFakeRecorder(10),
		}
	}

	containerExists := func(r *containerReconcilerLoop) bool {
		got := &weka.WekaContainer{}
		err := r.Get(context.Background(), client.ObjectKeyFromObject(container), got)
		if err != nil {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
			return false
		}
		return got.GetDeletionTimestamp().IsZero()
	}

	Context("backend container, mode Auto, node not found", func() {
		BeforeEach(func() {
			config.Config.CleanupRemovedNodes = config.CleanupRemovedNodesAuto
		})

		It("marks the container Stale and sets the grace stamp on first detection, without deleting it", func() {
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Status).To(Equal(weka.Stale))
			Expect(container.Status.Timestamps).To(HaveKey(nodeRemovedKey))
		})

		It("keeps the container Stale and does not delete it while within the grace period", func() {
			container.Status.Timestamps = map[string]metav1.Time{
				nodeRemovedKey: {Time: time.Now()},
			}
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Status).To(Equal(weka.Stale))
			Expect(container.Status.Timestamps).To(HaveKey(nodeRemovedKey))
		})

		It("deletes the container once the grace period has elapsed", func() {
			container.Status.Timestamps = map[string]metav1.Time{
				nodeRemovedKey: {Time: time.Now().Add(-25 * time.Hour)},
			}
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeFalse())
		})

		It("clears the grace stamp and does not delete the container once the node is present again", func() {
			container.Status.Timestamps = map[string]metav1.Time{
				nodeRemovedKey: {Time: time.Now()},
			}
			node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1"}}
			r := newReconciler(&fakeNodeKubeService{node: node})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).ToNot(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Timestamps).ToNot(HaveKey(nodeRemovedKey))
		})
	})

	Context("backend container, mode On, node not found", func() {
		It("deletes the container immediately, without a Stale grace period", func() {
			config.Config.CleanupRemovedNodes = config.CleanupRemovedNodesOn
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeFalse())
		})
	})

	Context("backend container, mode Off, node not found", func() {
		It("does not delete the container and does not set a grace stamp", func() {
			config.Config.CleanupRemovedNodes = config.CleanupRemovedNodesOff
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).ToNot(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Timestamps).ToNot(HaveKey(nodeRemovedKey))
		})
	})

	Context("non-backend (client) container, node not found", func() {
		It("deletes the container immediately regardless of mode, since the grace toggle does not apply", func() {
			config.Config.CleanupRemovedNodes = config.CleanupRemovedNodesAuto
			container.Spec.Mode = weka.WekaContainerModeClient
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeFalse())
		})
	})

	Context("backend container, mode Auto, node gone, provider inferred from surviving nodes", func() {
		var awsSurvivor []v1.Node

		BeforeEach(func() {
			config.Config.CleanupRemovedNodes = config.CleanupRemovedNodesAuto
			awsSurvivor = []v1.Node{{
				ObjectMeta: metav1.ObjectMeta{Name: "node2"},
				Spec:       v1.NodeSpec{ProviderID: "aws:///us-east-1a/i-0123456789abcdef0"},
			}}
		})

		It("holds the container Stale (does not delete) on first detection when a surviving cluster node is on aws", func() {
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound, nodes: awsSurvivor})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Status).To(Equal(weka.Stale))
			Expect(container.Status.Timestamps).To(HaveKey(nodeRemovedKey))
		})

		It("keeps the grace path when no surviving node is on a supported cloud", func() {
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound, nodes: []v1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}, // no ProviderID => non-cloud
			}})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Status).To(Equal(weka.Stale))
			Expect(container.Status.Timestamps).To(HaveKey(nodeRemovedKey))
		})

		It("deletes the container once the managed-cloud (30m) grace period has elapsed", func() {
			container.Status.Timestamps = map[string]metav1.Time{
				nodeRemovedKey: {Time: time.Now().Add(-31 * time.Minute)},
			}
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound, nodes: awsSurvivor})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeFalse())
		})

		It("keeps the container Stale while within the managed-cloud (30m) grace period", func() {
			container.Status.Timestamps = map[string]metav1.Time{
				nodeRemovedKey: {Time: time.Now().Add(-10 * time.Minute)},
			}
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound, nodes: awsSurvivor})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Status).To(Equal(weka.Stale))
		})

		It("at a ~40m-old stamp: deletes on cloud (past the 30m grace) but stays Stale on non-cloud (within the 24h grace)", func() {
			stamp := map[string]metav1.Time{
				nodeRemovedKey: {Time: time.Now().Add(-40 * time.Minute)},
			}

			container.Status.Timestamps = stamp
			cloudReconciler := newReconciler(&fakeNodeKubeService{err: nodeNotFound, nodes: awsSurvivor})
			err := cloudReconciler.deleteIfNoNode(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(containerExists(cloudReconciler)).To(BeFalse())

			container.Status.Timestamps = map[string]metav1.Time{
				nodeRemovedKey: {Time: time.Now().Add(-40 * time.Minute)},
			}
			nonCloudReconciler := newReconciler(&fakeNodeKubeService{err: nodeNotFound, nodes: []v1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}, // no ProviderID => non-cloud
			}})
			err = nonCloudReconciler.deleteIfNoNode(context.Background())
			Expect(err).To(HaveOccurred())
			Expect(containerExists(nonCloudReconciler)).To(BeTrue())
			Expect(container.Status.Status).To(Equal(weka.Stale))
		})

		It("reaps the terminal backend pod during the Stale window, without deleting the container", func() {
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pod1", Namespace: "default",
					Finalizers: []string{consts.WekaFinalizer},
				},
				Status: v1.PodStatus{Phase: v1.PodFailed},
			}
			r := newReconcilerWithPod(&fakeNodeKubeService{err: nodeNotFound, nodes: awsSurvivor}, pod)

			err := r.deleteIfNoNode(context.Background())

			Expect(err).To(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Status).To(Equal(weka.Stale))

			after := &v1.Pod{}
			getErr := r.Get(context.Background(), client.ObjectKeyFromObject(pod), after)
			if getErr == nil {
				Expect(controllerutil.ContainsFinalizer(after, consts.WekaFinalizer)).To(BeFalse(),
					"weka finalizer must be cleared so the terminal pod can be reaped immediately")
			} else {
				Expect(apierrors.IsNotFound(getErr)).To(BeTrue(), "terminal pod should be reaped once its finalizer is cleared")
			}
		})
	})

	Context("backend container, mode Off, node gone, surviving node on aws", func() {
		It("does not delete the container even when a surviving node is on aws", func() {
			config.Config.CleanupRemovedNodes = config.CleanupRemovedNodesOff
			r := newReconciler(&fakeNodeKubeService{err: nodeNotFound, nodes: []v1.Node{{
				ObjectMeta: metav1.ObjectMeta{Name: "node2"},
				Spec:       v1.NodeSpec{ProviderID: "aws:///us-east-1a/i-0123456789abcdef0"},
			}}})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).ToNot(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
		})
	})

	Context("backend container, mode Auto, node present with aws providerID", func() {
		It("does not delete while the node is present, even though it is on a supported cloud", func() {
			config.Config.CleanupRemovedNodes = config.CleanupRemovedNodesAuto
			node := &v1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node1"},
				Spec:       v1.NodeSpec{ProviderID: "aws:///us-east-1a/i-0123456789abcdef0"},
			}
			r := newReconciler(&fakeNodeKubeService{node: node})

			err := r.deleteIfNoNode(context.Background())

			Expect(err).ToNot(HaveOccurred())
			Expect(containerExists(r)).To(BeTrue())
			Expect(container.Status.Status).ToNot(Equal(weka.Stale))
		})
	})
})
