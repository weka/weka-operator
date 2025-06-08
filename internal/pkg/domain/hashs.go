package domain

import (
	"crypto/sha256"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

func CalculateNodeDriveSignHash(node *corev1.Node) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(node.Status.NodeInfo.BootID)))
}
