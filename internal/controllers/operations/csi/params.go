package csi

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"github.com/weka/weka-operator/pkg/weka-k8s-api/util"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/resources"
)

// DeploymentParams is everything the CSI builders need from the WekaClient asking for the
// installation, as opposed to operator-wide config or resolved settings. It is the seam between
// "who asked" and "what gets rendered".
//
// Advanced is carried as the API pointer rather than pre-resolved values so the builders keep
// their existing "if Advanced != nil" branches: the controller merges ControllerLabels in the
// builder but assigns them in the hash function, and the node plugin drops the default created-by
// label. Those inconsistencies are observable - the reporter selects DaemonSets on created-by - so
// this reproduces them rather than quietly correcting them.
type DeploymentParams struct {
	// CsiGroup identifies the installation; CsiDriverName and every resource name derive from it.
	CsiGroup string

	// OwnerName/Namespace/UID populate the weka.io/csi-*-owner* pod-template annotations. They are
	// annotations rather than ownerReferences because the owner and the workloads live in
	// different namespaces.
	OwnerName      string
	OwnerNamespace string
	OwnerUID       string
	// OwnerLabels are the owning object's labels, propagated into the rendered objects by
	// GetCsiLabels.
	OwnerLabels map[string]string

	// Advanced mirrors a WekaClient's csiConfig.advanced. May be nil.
	Advanced *weka.AdvancedCsiConfig

	// NodeSelector constrains the node plugin, ControllerNodeSelector the controller. The
	// One WekaClient field sets both,
	// because the node plugin must be wherever volumes mount while the controller need not be.
	NodeSelector           map[string]string
	ControllerNodeSelector map[string]string

	// BaseTolerations are the owner's own tolerations, already expanded from the weka shorthand.
	// The controller starts from these; the node plugin tolerates everything regardless.
	BaseTolerations []corev1.Toleration

	DisableControllerCreation bool

	// WekafsContainerName is the local weka container the plugin talks to. Empty means there is
	// none on these nodes, and then neither --wekafscontainername nor WEKAFS_CONTAINER_NAME is
	// rendered. They must be omitted together: the flag's value is $(WEKAFS_CONTAINER_NAME), and
	// with the env var absent Kubernetes leaves that literal string in argv.
	WekafsContainerName string

	// ManageNodeTopologyLabels hands the node plugin ownership of the topology.<driver>/* node
	// labels via --managenodetopologylabels. False where the operator stamps them from a client
	// container's health; never both at once.
	ManageNodeTopologyLabels bool

	// ForceNfs makes the plugin mount over NFS unconditionally (--usenfs), rather than only where
	// it finds no running weka client.
	//
	// Derived, never configured. It is set exactly when the installation has no weka client
	// containers, because then NFS is the only transport that can work. It cannot be left to
	// allowNfsFailback: that defers to the plugin, which asks whether the weka driver is loaded
	// and has any frontend at all — so on a converged node, where backend or protocol containers
	// supply one, the plugin keeps the native transport and every mount fails.
	ForceNfs bool

	// NodeDaemonSetName is passed in rather than derived, because the two paths name their
	// DaemonSets differently and must never render the same object.
	NodeDaemonSetName string

	// RetainLabel keeps the node plugin scheduled on a node while that owner's mounts drain.
	// Empty where there are no weka client containers: nothing to drain, and nobody to release it.
	RetainLabel string

	// SecretRef is the API credentials Secret the storage classes point at.
	SecretRef client.ObjectKey
	// CreateStorageClasses reflects whether this installation owns storage classes at all.
	CreateStorageClasses bool
	// StorageClassFilesystem is the weka filesystem the generated storage classes provision into.
	StorageClassFilesystem string
}

// CsiDriverName returns the CSIDriver object name for this installation.
func (p *DeploymentParams) CsiDriverName() string { return GetCsiDriverName(p.CsiGroup) }

// ParamsFromWekaClient reproduces exactly what the embedded CSI path reads from a WekaClient
// today. It is the backward-compatibility contract for the whole refactor: a change here changes
// the rendered spec, and therefore the pod-template hash, of every existing CSI installation.
func ParamsFromWekaClient(wekaClient *weka.WekaClient, csiGroupName string, secret client.ObjectKey) *DeploymentParams {
	var advanced *weka.AdvancedCsiConfig
	if wekaClient.Spec.CsiConfig != nil {
		advanced = wekaClient.Spec.CsiConfig.Advanced
	}

	emptyRef := weka.ObjectReference{}

	// Everything that differs when there are no weka client containers is decided here, once.
	// Scattering "does this client use NFS?" through the builders is how one of these gets
	// forgotten - and each of them fails quietly rather than loudly.
	var (
		// The plugin is told which local weka container to talk to. There is none, and the flag
		// and its env var must then be omitted together.
		wekafsContainerName = resources.GetWekaClientContainerName(wekaClient)
		// The operator stamps topology.<driver>/* labels from a client container's health. With no
		// container nothing would stamp them, and the provisioner runs with Topology=true, so
		// volumes would never schedule. Hand the job to the plugin instead.
		manageNodeTopologyLabels = false
		// The retain label keeps the node plugin on a node while that client's mounts drain.
		// Nothing mounts through a weka client here, and nobody would ever release the label.
		retainLabel = GetCsiNodeRetainLabel(wekaClient.Namespace, wekaClient.Name)
	)
	// ForceNfs rides along with the rest: no client containers means NFS is the only transport
	// that can work, so the user does not get asked a second time in a second object.
	forceNfs := false
	if wekaClient.Spec.UseNfs {
		wekafsContainerName = ""
		manageNodeTopologyLabels = true
		retainLabel = ""
		forceNfs = true
	}

	// Storage classes need credentials to point at, from either source.
	hasTargetCluster := wekaClient.Spec.TargetCluster != emptyRef && wekaClient.Spec.TargetCluster.Name != ""

	return &DeploymentParams{
		CsiGroup:       csiGroupName,
		OwnerName:      wekaClient.Name,
		OwnerNamespace: wekaClient.Namespace,
		OwnerUID:       string(wekaClient.GetUID()),
		OwnerLabels:    wekaClient.Labels,
		Advanced:       advanced,
		// One field drives both, as it always has.
		NodeSelector:           wekaClient.Spec.NodeSelector,
		ControllerNodeSelector: wekaClient.Spec.NodeSelector,
		BaseTolerations: util.ExpandTolerations(
			[]corev1.Toleration{}, wekaClient.Spec.Tolerations, wekaClient.Spec.RawTolerations),
		DisableControllerCreation: wekaClient.Spec.CsiConfig != nil &&
			wekaClient.Spec.CsiConfig.DisableControllerCreation,
		WekafsContainerName:      wekafsContainerName,
		ManageNodeTopologyLabels: manageNodeTopologyLabels,
		ForceNfs:                 forceNfs,
		NodeDaemonSetName: GetCSINodeDaemonSetNameForClient(
			csiGroupName, wekaClient.Name, wekaClient.Namespace),
		RetainLabel:            retainLabel,
		SecretRef:              secret,
		CreateStorageClasses:   hasTargetCluster,
		StorageClassFilesystem: config.Consts.CsiFileSystemName,
	}
}

// CsiSecretForWekaClient is the Secret the embedded path points its storage classes at. Lifted
// verbatim from the operation that used to compute it inline, so both paths resolve it the same
// way; the key is only computed, never verified.
//
// csiGroupName must be the RESOLVED group (csi.ResolveGroup), not the client-derived one: the
// fallback branch names the Secret after it, and the two differ whenever a target cluster is
// reachable.
func CsiSecretForWekaClient(wekaClient *weka.WekaClient, csiGroupName string) client.ObjectKey {
	emptyRef := weka.ObjectReference{}
	if wekaClient.Spec.TargetCluster != emptyRef && wekaClient.Spec.TargetCluster.Name != "" {
		return client.ObjectKey{
			Name:      weka.GetCsiSecretName(wekaClient.Spec.TargetCluster.Name),
			Namespace: wekaClient.Spec.TargetCluster.Namespace,
		}
	}
	return client.ObjectKey{
		Name:      weka.GetCsiSecretName(csiGroupName),
		Namespace: wekaClient.Namespace,
	}
}
