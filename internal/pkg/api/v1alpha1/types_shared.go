package v1alpha1

import (
	"fmt"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func NewContainerName(role string) string {
	guid := string(uuid.NewUUID())
	return fmt.Sprintf("%s-%s", role, guid)
}
