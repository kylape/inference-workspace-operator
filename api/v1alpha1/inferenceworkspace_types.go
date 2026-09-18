package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const ReadyCondition = "Ready"

// WorkspaceSubject identifies an identity that receives access to a workspace.
// +kubebuilder:validation:XValidation:rule="(self.kind == 'User' && !has(self.namespace)) || (self.kind == 'ServiceAccount' && has(self.namespace) && self.namespace != '')",message="User subjects must omit namespace; ServiceAccount subjects must specify namespace"
type WorkspaceSubject struct {
	// Kind is either User or ServiceAccount.
	// +kubebuilder:validation:Enum=User;ServiceAccount
	Kind string `json:"kind"`

	// Name is the Kubernetes user or service account name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is required for ServiceAccount and omitted for User.
	Namespace string `json:"namespace,omitempty"`
}

type InferenceWorkspaceSpec struct {
	// Subjects receive namespaced access to the workspace.
	// +kubebuilder:validation:MinItems=1
	Subjects []WorkspaceSubject `json:"subjects"`
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
