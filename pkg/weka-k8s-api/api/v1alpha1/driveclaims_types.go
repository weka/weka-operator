package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Owner",type="string",JSONPath=".spec.owner",description="Owner",priority=0
// +kubebuilder:printcolumn:name="Device",type="string",JSONPath=".spec.device",description="Device",priority=0
// +kubebuilder:printcolumn:name="Drive UUID",type="string",JSONPath=".spec.driveUuid",description="Drive UUUD",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Creation time",priority=0
type DriveClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DriveClaimSpec   `json:"spec,omitempty"`
	Status            DriveClaimStatus `json:"status,omitempty"`
}

type DriveClaimSpec struct {
	Owner     string `json:"owner"`
	Device    string `json:"device"`
	DriveUuid string `json:"driveUuid"`
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
