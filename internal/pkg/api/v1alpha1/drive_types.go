package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Drive struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DriveSpec   `json:"spec,omitempty"`
	Status            DriveStatus `json:"status,omitempty"`
}

type DriveSpec struct {
	// Node Name is the name of the node the drive is attached to
	NodeName string `json:"nodeName"`
	Name     string `json:"name"`
}

type DriveStatus struct {
	// DriveID is the unique identifier of the Drive
	DriveID string `json:"driveID,omitempty"`
	// Path is the path to the drive.
	Path string `json:"path,omitempty"`
	// UUID is the UUID of the drive.
	UUID string `json:"uuid,omitempty"`
	// Allocated indicates if the drive is in use by weka
	Allocated bool `json:"allocated"`
}

// +kubebuilder:object:root=true
type DriveList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Drive `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Drive{}, &DriveList{})
}
