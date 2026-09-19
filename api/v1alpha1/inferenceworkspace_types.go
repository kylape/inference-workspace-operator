package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const ReadyCondition = "Ready"

type WorkspaceMode string

const (
	WorkspaceModeNamespace WorkspaceMode = "Namespace"
	WorkspaceModeVCluster  WorkspaceMode = "VCluster"
)

type WorkspaceSubject struct {
	// Kind is currently limited to ServiceAccount.
	// +kubebuilder:validation:Enum=ServiceAccount
	Kind string `json:"kind"`

	// Name is the service account name.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name"`

	// Namespace is the service account namespace.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`
}

type WorkspaceAccess struct {
	// Subjects are service accounts that receive mode-appropriate workspace access.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=kind
	// +listMapKey=namespace
	// +listMapKey=name
	Subjects []WorkspaceSubject `json:"subjects"`
}

type InferenceWorkspaceSpec struct {
	// Access declares identities selected by the trusted workspace provisioner.
	Access WorkspaceAccess `json:"access"`

	// Mode selects a direct host namespace or an isolated vCluster.
	// +kubebuilder:default=Namespace
	// +kubebuilder:validation:Enum=Namespace;VCluster
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="mode is immutable"
	Mode WorkspaceMode `json:"mode,omitempty"`

	// ClusterQueue is the Kueue ClusterQueue that backs the workspace's
	// default LocalQueue.
	// +kubebuilder:default=inference-workspaces
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="clusterQueue is immutable"
	ClusterQueue string `json:"clusterQueue,omitempty"`
}

type InferenceWorkspaceStatus struct {
	// NamespaceRef identifies the backing host-cluster namespace.
	NamespaceRef *corev1.LocalObjectReference `json:"namespaceRef,omitempty"`

	// KubeconfigSecretRef is populated only for a ready vCluster workspace.
	KubeconfigSecretRef *corev1.SecretReference `json:"kubeconfigSecretRef,omitempty"`

	// Conditions contains the aggregate Ready condition.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=iw
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 53",message="workspace names must be at most 53 characters to allow the workspace- prefix"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Namespace",type="string",JSONPath=".status.namespaceRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type InferenceWorkspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InferenceWorkspaceSpec   `json:"spec,omitempty"`
	Status InferenceWorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type InferenceWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceWorkspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InferenceWorkspace{}, &InferenceWorkspaceList{})
}
