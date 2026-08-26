package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	TierStub = "stub"
	// CallerDigestAnnotation binds an Oubliette to the authenticated lifecycle
	// caller that created it without persisting the caller's raw identity.
	CallerDigestAnnotation = "oubliette.tlrmchlsmth.github.io/caller-digest"

	ConditionProvisioned  = "Provisioned"
	ConditionReady        = "Ready"
	ConditionMetricsReady = "MetricsReady"
	ConditionExpiring     = "Expiring"
	ConditionForgotten    = "Forgotten"
)

// OublietteSpec describes an expiring virtual Kubernetes control plane.
type OublietteSpec struct {
	// Tier names an operator-owned resource profile.
	// +kubebuilder:validation:Enum=stub;gpu
	Tier string `json:"tier"`

	// ExpiresAt is the absolute deadline after which teardown begins.
	ExpiresAt metav1.Time `json:"expiresAt"`
}

// OublietteStatus projects lifecycle state without exposing credentials.
type OublietteStatus struct {
	ObservedGeneration       int64              `json:"observedGeneration,omitempty"`
	HostNamespace            string             `json:"hostNamespace,omitempty"`
	ProfileGeneration        string             `json:"profileGeneration,omitempty"`
	VirtualEndpoint          string             `json:"virtualEndpoint,omitempty"`
	MetricsEndpoint          string             `json:"metricsEndpoint,omitempty"`
	MetricsProfileGeneration string             `json:"metricsProfileGeneration,omitempty"`
	MetricsIsolationScope    string             `json:"metricsIsolationScope,omitempty"`
	MetricsTrustDomain       string             `json:"metricsTrustDomain,omitempty"`
	ForgottenAt              *metav1.Time       `json:"forgottenAt,omitempty"`
	Conditions               []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=oub
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Tier",type=string,JSONPath=`.spec.tier`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.spec.expiresAt`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="Forgotten",type=string,JSONPath=`.status.conditions[?(@.type=='Forgotten')].status`
type Oubliette struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OublietteSpec   `json:"spec,omitempty"`
	Status OublietteStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type OublietteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Oubliette `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Oubliette{}, &OublietteList{})
}
