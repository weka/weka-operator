package reporter

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// crKind describes a single CR type to collect: how to make its list type, how
// to box list items into []client.Object, and (optionally) how to extract
// node-selectors from it.
type crKind struct {
	// name is used as the kind tag in the combined NDJSON stream.
	name    string
	newList func() client.ObjectList
	items   func(client.ObjectList) []client.Object
	// selectors extracts all node-selectors from the list (nil = no
	// node-selectors). Each map[string]string is one node-selector; a
	// nil/empty map means "all nodes".
	selectors func(client.ObjectList) []map[string]string
}

// crKinds is the set of CR types reported to Weka Home; the per-kind logic lives
// in the named funcs below.
var crKinds = []crKind{
	{"WekaCluster", newWekaClusterList, clusterItems, clusterSelectors},
	{"WekaContainer", newWekaContainerList, containerItems, nil},
	{"WekaClient", newWekaClientList, clientItems, clientSelectors},
	{"WekaPolicy", newWekaPolicyList, policyItems, policySelectors},
	{"WekaManualOperation", newWekaManualOperationList, manualOpItems, manualOpSelectors},
}

func newWekaClusterList() client.ObjectList         { return &weka.WekaClusterList{} }
func newWekaContainerList() client.ObjectList       { return &weka.WekaContainerList{} }
func newWekaClientList() client.ObjectList          { return &weka.WekaClientList{} }
func newWekaPolicyList() client.ObjectList          { return &weka.WekaPolicyList{} }
func newWekaManualOperationList() client.ObjectList { return &weka.WekaManualOperationList{} }

func clusterItems(l client.ObjectList) []client.Object {
	return objectsFrom(l.(*weka.WekaClusterList).Items)
}
func containerItems(l client.ObjectList) []client.Object {
	return objectsFrom(l.(*weka.WekaContainerList).Items)
}
func clientItems(l client.ObjectList) []client.Object {
	return objectsFrom(l.(*weka.WekaClientList).Items)
}
func policyItems(l client.ObjectList) []client.Object {
	return objectsFrom(l.(*weka.WekaPolicyList).Items)
}

func manualOpItems(l client.ObjectList) []client.Object {
	return objectsFrom(l.(*weka.WekaManualOperationList).Items)
}

// objectsFrom boxes a list's []T item values into []client.Object (PT is *T and
// implements client.Object).
func objectsFrom[T any, PT interface {
	*T
	client.Object
}](items []T) []client.Object {
	out := make([]client.Object, len(items))
	for i := range items {
		out[i] = PT(&items[i])
	}
	return out
}

// clusterSelectors harvests the global node-selector plus every per-role
// selector from each WekaCluster.
func clusterSelectors(l client.ObjectList) []map[string]string {
	list := l.(*weka.WekaClusterList)
	var out []map[string]string
	for i := range list.Items {
		spec := &list.Items[i].Spec
		out = append(out, spec.NodeSelector)
		rns := spec.RoleNodeSelector
		for _, ptr := range []*map[string]string{
			rns.Compute, rns.Drive, rns.S3, rns.Nfs, rns.Smbw, rns.DataServices,
		} {
			if ptr != nil {
				out = append(out, *ptr)
			}
		}
	}
	return out
}

// clientSelectors harvests the node-selector from each WekaClient.
func clientSelectors(l client.ObjectList) []map[string]string {
	list := l.(*weka.WekaClientList)
	var out []map[string]string
	for i := range list.Items {
		out = append(out, list.Items[i].Spec.NodeSelector)
	}
	return out
}

// policySelectors harvests node-selectors from every pod-creating WekaPolicy
// payload: the four common sub-payloads (via appendOpSelectors) plus the
// driver-distribution payload (WekaPolicy-only).
func policySelectors(l client.ObjectList) []map[string]string {
	list := l.(*weka.WekaPolicyList)
	var out []map[string]string
	for i := range list.Items {
		p := &list.Items[i].Spec.Payload
		out = appendOpSelectors(out, p.SignDrives, p.DiscoverDrives, p.EnsureNICs, p.RemoteTracesSession)
		if dd := p.DriverDistPayload; dd != nil {
			// NodeSelectors fans out builder pods per node ⇒ empty = all nodes;
			// append an explicit match-all so the default policy's nodes aren't dropped.
			if len(dd.NodeSelectors) == 0 {
				out = append(out, nil)
			} else {
				out = append(out, dd.NodeSelectors...)
			}
			// DistNodeSelector places a single dist container ⇒ skip when empty
			// (one scheduler-chosen node, not all).
			if len(dd.DistNodeSelector) > 0 {
				out = append(out, dd.DistNodeSelector)
			}
		}
	}
	return out
}

// manualOpSelectors harvests node-selectors from every pod-creating
// WekaManualOperation payload (the four common sub-payloads).
func manualOpSelectors(l client.ObjectList) []map[string]string {
	list := l.(*weka.WekaManualOperationList)
	var out []map[string]string
	for i := range list.Items {
		p := &list.Items[i].Spec.Payload
		out = appendOpSelectors(out, p.SignDrives, p.DiscoverDrives, p.EnsureNICs, p.RemoteTracesSessionConfig)
	}
	return out
}

// appendOpSelectors harvests the NodeSelector from the four pod-creating
// sub-payloads shared by WekaPolicy and WekaManualOperation.
//
// Explicit enumeration (no reflection): a newly-added pod-creating sub-payload
// must be added here by hand or its target nodes go unreported. The remote-traces
// field name differs by CR (WekaPolicy.RemoteTracesSession vs
// WekaManualOperation.RemoteTracesSessionConfig, same type), so each caller passes
// its own field.
func appendOpSelectors(
	out []map[string]string,
	sign *weka.SignDrivesPayload,
	disc *weka.DiscoverDrivesPayload,
	nics *weka.EnsureNICsPayload,
	traces *weka.RemoteTracesSessionConfig,
) []map[string]string {
	// Fan-out ops (one pod per matching node): empty selector ⇒ all nodes, append as-is.
	if sign != nil {
		out = append(out, sign.NodeSelector)
	}
	if disc != nil {
		out = append(out, disc.NodeSelector)
	}
	if nics != nil {
		out = append(out, nics.NodeSelector)
	}
	// Single-pod op: empty selector ⇒ one scheduler-chosen node (unresolvable), so
	// skip rather than over-report every node.
	if traces != nil && len(traces.NodeSelector) > 0 {
		out = append(out, traces.NodeSelector)
	}
	return out
}

// collectListRaw lists a kind cluster-wide (no namespace filter) and returns the
// raw ObjectList; the caller boxes items / extracts selectors from it.
func collectListRaw(ctx context.Context, c client.Client, kind crKind) (client.ObjectList, error) {
	list := kind.newList()
	if err := c.List(ctx, list); err != nil {
		return nil, fmt.Errorf("list %s: %w", kind.name, err)
	}
	return list, nil
}

// collectDeployment returns the operator's own Deployment(s) from the given
// namespace (labeled app.kubernetes.io/created-by=weka-operator).
func collectDeployment(ctx context.Context, c client.Client, namespace string) ([]client.Object, error) {
	list := &appsv1.DeploymentList{}
	if err := c.List(ctx, list,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/created-by": "weka-operator"},
	); err != nil {
		return nil, fmt.Errorf("list operator Deployment: %w", err)
	}
	return objectsFrom(list.Items), nil
}

// collectPods returns all Pods owned by the weka-operator (cluster-wide).
func collectPods(ctx context.Context, c client.Client) ([]client.Object, error) {
	list := &corev1.PodList{}
	if err := c.List(ctx, list,
		client.MatchingLabels{"app.kubernetes.io/created-by": "weka-operator"},
	); err != nil {
		return nil, fmt.Errorf("list operator Pods: %w", err)
	}
	return objectsFrom(list.Items), nil
}

// collectDaemonSets returns all DaemonSets created by the weka-operator
// (cluster-wide): the node-agent and the embedded-CSI node DaemonSet.
func collectDaemonSets(ctx context.Context, c client.Client) ([]client.Object, error) {
	list := &appsv1.DaemonSetList{}
	if err := c.List(ctx, list,
		client.MatchingLabels{"app.kubernetes.io/created-by": "weka-operator"},
	); err != nil {
		return nil, fmt.Errorf("list operator DaemonSets: %w", err)
	}
	return objectsFrom(list.Items), nil
}
