package wekacontainer

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/weka/weka-operator/internal/services"
)

// A client pod is created in restricted mode with the cluster's low-privilege `regular` user, so it
// cannot run `weka cluster ...` itself — the cluster-side image check has to reach a backend of the
// client's target cluster instead. And a client container is owned by a WekaClient, so the ordinary
// getClusterContainers() route (owner UID matched against WekaClusters) never resolves for it. These
// specs pin that routing, plus the skip when there is no target cluster to reach.
var _ = Describe("resolveClusterExecContainer", func() {
	const ns = "weka-operator-system"

	var scheme *runtime.Scheme

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		Expect(weka.AddToScheme(scheme)).To(Succeed())
	})

	// newClientLoop wires a client WekaContainer owned by wekaClient, with the given objects seeded.
	newClientLoop := func(wekaClient *weka.WekaClient, objs ...client.Object) *containerReconcilerLoop {
		container := &weka.WekaContainer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "client-0",
				Namespace: ns,
				UID:       types.UID("client-container-uid"),
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: weka.GroupVersion.String(),
					Kind:       "WekaClient",
					Name:       wekaClient.Name,
					UID:        wekaClient.UID,
					Controller: ptr(true),
				}},
			},
			Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeClient},
		}

		all := append([]client.Object{container, wekaClient}, objs...)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(all...).
			// discovery.GetClusterContainers filters on the metadata.ownerReferences.uid field
			// index; the real manager registers it in cmd/manager/main.go (setupContainerIndexes),
			// and the fake client rejects the field selector unless we mirror it here.
			WithIndex(&weka.WekaContainer{}, "metadata.ownerReferences.uid", func(rawObj client.Object) []string {
				wc, ok := rawObj.(*weka.WekaContainer)
				if !ok {
					return nil
				}
				owner := metav1.GetControllerOf(wc)
				if owner == nil {
					return nil
				}
				return []string{string(owner.UID)}
			}).
			Build()

		return &containerReconcilerLoop{Client: fakeClient, container: container}
	}

	It("skips the check for a client with no target cluster", func() {
		wekaClient := &weka.WekaClient{
			ObjectMeta: metav1.ObjectMeta{Name: "my-client", Namespace: ns, UID: types.UID("client-uid")},
		}

		r := newClientLoop(wekaClient)

		execIn, err := r.resolveClusterExecContainer(context.Background())
		Expect(err).NotTo(HaveOccurred(), "a client without a target cluster is not an error, there is just nothing to query")
		Expect(execIn).To(BeNil())
	})

	It("routes a client to an operational backend of its target cluster", func() {
		cluster := &weka.WekaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "my-cluster", Namespace: ns, UID: types.UID("cluster-uid")},
		}
		wekaClient := &weka.WekaClient{
			ObjectMeta: metav1.ObjectMeta{Name: "my-client", Namespace: ns, UID: types.UID("client-uid")},
			Spec: weka.WekaClientSpec{
				TargetCluster: weka.ObjectReference{Name: cluster.Name, Namespace: cluster.Namespace},
			},
		}
		backend := operationalBackend("drive-0", ns, cluster)

		r := newClientLoop(wekaClient, cluster, backend)

		execIn, err := r.resolveClusterExecContainer(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(execIn).NotTo(BeNil(), "the client's own restricted pod cannot run cluster CLI commands, a backend must be picked")
		Expect(execIn.Name).To(Equal("drive-0"))
		Expect(r.targetCluster).NotTo(BeNil(), "the target cluster is resolved lazily, without relying on the CSI-gated flow step")
	})

	It("does not fall back to the client container itself when the target cluster has no containers", func() {
		cluster := &weka.WekaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "my-cluster", Namespace: ns, UID: types.UID("cluster-uid")},
		}
		wekaClient := &weka.WekaClient{
			ObjectMeta: metav1.ObjectMeta{Name: "my-client", Namespace: ns, UID: types.UID("client-uid")},
			Spec: weka.WekaClientSpec{
				TargetCluster: weka.ObjectReference{Name: cluster.Name, Namespace: cluster.Namespace},
			},
		}

		r := newClientLoop(wekaClient, cluster)

		execIn, err := r.resolveClusterExecContainer(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(execIn).To(BeNil())
	})

	It("errors when the target cluster reference does not resolve", func() {
		wekaClient := &weka.WekaClient{
			ObjectMeta: metav1.ObjectMeta{Name: "my-client", Namespace: ns, UID: types.UID("client-uid")},
			Spec: weka.WekaClientSpec{
				TargetCluster: weka.ObjectReference{Name: "missing-cluster", Namespace: ns},
			},
		}

		r := newClientLoop(wekaClient)

		_, err := r.resolveClusterExecContainer(context.Background())
		Expect(err).To(HaveOccurred(), "a dangling target cluster reference must be reported, not silently skipped")
	})
})

// The image tag is the only statement of the target version the operator has. utils.GetSoftwareVersion
// strips the build suffix and so matches sw_version, while the untrimmed tag matches sw_release_string —
// and two images can share a sw_version while differing only in that suffix, so a custom build must be
// compared against the release string or a wrong image would pass the gate.
var _ = Describe("versionsToCompare", func() {
	DescribeTable("picks the pair that can actually disagree",
		func(image, swVersion, releaseString, wantExpected, wantReported string) {
			cc := &services.WekaClusterContainer{SwVersion: swVersion, SwReleaseString: releaseString}

			expected, reported := versionsToCompare(image, cc)

			Expect(expected).To(Equal(wantExpected))
			Expect(reported).To(Equal(wantReported))
		},
		Entry("plain release: sw_version against the trimmed tag",
			"quay.io/weka.io/weka-in-container:5.1.31", "5.1.31", "5.1.31",
			"5.1.31", "5.1.31"),
		Entry("custom build: sw_release_string against the untrimmed tag",
			"quay.io/weka.io/weka-in-container:1.2.3.4-custom-build", "1.2.3.4", "1.2.3.4-custom-build",
			"1.2.3.4-custom-build", "1.2.3.4-custom-build"),
		Entry("custom build on the wrong image: the suffix is what disagrees",
			"quay.io/weka.io/weka-in-container:1.2.3.4-other-build", "1.2.3.4", "1.2.3.4-custom-build",
			"1.2.3.4-other-build", "1.2.3.4-custom-build"),
		Entry("digest-pinned image: nothing to compare on the image side",
			"quay.io/weka.io/weka-in-container@sha256:9f2c3a1b4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8", "5.1.31", "5.1.31",
			"", "5.1.31"),
		Entry("weka reports no version: nothing to compare on the cluster side",
			"quay.io/weka.io/weka-in-container:5.1.31", "", "",
			"5.1.31", ""),
	)
})

// operationalBackend builds a drive container that discovery.IsContainerOperational accepts, so
// SelectActiveContainer picks it on its first pass rather than through its random fallback.
func operationalBackend(name, namespace string, cluster *weka.WekaCluster) *weka.WekaContainer {
	containerID := 4
	return &weka.WekaContainer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name + "-uid"),
			Labels:    map[string]string{"weka.io/mode": weka.WekaContainerModeDrive},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: weka.GroupVersion.String(),
				Kind:       "WekaCluster",
				Name:       cluster.Name,
				UID:        cluster.UID,
				Controller: ptr(true),
			}},
		},
		Spec: weka.WekaContainerSpec{Mode: weka.WekaContainerModeDrive},
		Status: weka.WekaContainerStatus{
			Status:             weka.Running,
			InternalStatus:     "READY",
			ClusterContainerID: &containerID,
			ManagementIPs:      []string{"10.100.5.49"},
			Allocations:        &weka.ContainerAllocations{WekaPort: 15000},
		},
	}
}

func ptr[T any](v T) *T { return &v }
