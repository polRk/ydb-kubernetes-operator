package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StorageSpec defines the desired state of Storage
type StorageSpec struct {
	// Number of nodes (pods) in the cluster
	// +required
	Nodes int32 `json:"nodes"`
	// Full list of configuration for the storage component
	// +optional
	ClusterConfig map[string]string `json:"config"`
	// Where cluster data should be kept
	// +required
	DataStore corev1.PersistentVolumeClaimSpec `json:"dataStore"`
	// (Optional) Storage container resource limits. Any container limits
	// can be specified.
	// Default: (not specified)
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// Container image information
	// +required
	Image PodImage `json:"image,omitempty"`
	// (Optional) YDBVersion sets the explicit version of the YDB image
	// Default: ""
	// +optional
	YDBVersion string `json:"version,omitempty"`
	// NodeSelector is a selector which must be true for the pod to fit on a node.
	// Selector which must match a node's labels for the pod to be scheduled on that node.
	// More info: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// (Optional) If specified, the pod's scheduling constraints
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// (Optional) If specified, the pod's tolerations.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// (Optional) Additional custom resource labels that are added to all resources
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Map of additional custom labels"
	// +optional
	AdditionalLabels map[string]string `json:"additionalLabels,omitempty"`
}

// StorageStatus defines the observed state of Storage
type StorageStatus struct {
	State      string             `json:"state"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.state",description="The status of this DB"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Storage is the Schema for the storages API
type Storage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec StorageSpec `json:"spec,omitempty"`

	// +kubebuilder:default:={state: "Pending"}
	Status StorageStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// StorageList contains a list of Storage
type StorageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Storage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Storage{}, &StorageList{})
}