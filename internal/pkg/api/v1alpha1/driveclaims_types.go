package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type DriveClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DriveClaimSpec   `json:"spec,omitempty"`
	Status            DriveClaimStatus `json:"status,omitempty"`
}

type DriveClaimSpec struct {
	Owner string `json:"owner"`
}

type DriveClaimStatus struct {
}

// +kubebuilder:object:root=true
type DriveClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DriveClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DriveClaim{}, &DriveClaimList{})
}
