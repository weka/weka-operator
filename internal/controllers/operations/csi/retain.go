package csi

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// CsiNodeRetainLabelValue is the only value the retain label ever carries; its presence is what
// matters, not its content.
const CsiNodeRetainLabelValue = "true"

// GetCsiNodeRetainLabel returns the node label that keeps this client's csi-node pod scheduled on a
// node even after the node stops matching WekaClient.spec.nodeSelector.
//
// The operator sets it on every node where a client container is serving and releases it only once
// that container has finalized — i.e. after its mounts have drained. The csi-node DaemonSet's
// placement matches spec.nodeSelector OR this label, so removing the user's client-selector label
// from a node no longer deschedules the plugin out from under an active mount: it stays until there
// is nothing left to unmount.
//
// The key is per-client, not a single shared boolean, because several WekaClients can select
// overlapping nodes and each gets its own csi-node DaemonSet (see GetCSINodeDaemonSetNameForClient).
// With one shared key, the last container of client A finalizing on a node would release the label
// and strand client B's still-mounted plugin there.
func GetCsiNodeRetainLabel(clientNamespace, clientName string) string {
	// Dots are not valid in a label name segment, so they have to go -- but replacing them outright
	// would collapse "a.b" and "a-b" onto one key, which is exactly the cross-client stranding this
	// per-client key exists to prevent. Disambiguate with a hash of the raw name.
	sanitizedName := strings.ReplaceAll(clientName, ".", "-")
	if sanitizedName != clientName {
		sanitizedName += "-" + fmt.Sprintf("%x", sha256.Sum256([]byte(clientName)))[:8]
	}

	name := "csi-node-retain." + clientNamespace + "." + sanitizedName
	if len(name) > 63 {
		// Same truncate-and-hash guard as GetCSINodeDaemonSetNameForClient: a label name segment is
		// capped at 63 characters, and uniqueness across clients has to survive the truncation.
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:8]
		name = name[:63-9] + "-" + hash
	}
	name = strings.TrimRight(name, "-")
	return "weka.io/" + name
}
