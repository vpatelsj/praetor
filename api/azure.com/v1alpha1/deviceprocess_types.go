// Copyright 2025 Apollo
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceProcessPhase represents lifecycle phase.
type DeviceProcessPhase string

const (
	DeviceProcessPhasePending   DeviceProcessPhase = "Pending"
	DeviceProcessPhaseRunning   DeviceProcessPhase = "Running"
	DeviceProcessPhaseSucceeded DeviceProcessPhase = "Succeeded"
	DeviceProcessPhaseFailed    DeviceProcessPhase = "Failed"
	DeviceProcessPhaseUnknown   DeviceProcessPhase = "Unknown"
)

// Condition types for DeviceProcess lifecycle.
const (
	ConditionArtifactDownloaded ConditionType = "ArtifactDownloaded"
	ConditionProcessStarted     ConditionType = "ProcessStarted"
	ConditionHealthy            ConditionType = "Healthy"
	ConditionAgentConnected     ConditionType = "AgentConnected"
)

// ConditionType aligns with metav1.Condition.Type for helpers.
type ConditionType string

// DeviceProcessSpec defines the desired state of DeviceProcess.
type DeviceProcessSpec struct {
	// Command is an opaque command or script reference to execute on the device.
	Command string `json:"command,omitempty"`
}

// DeviceProcessStatus defines the observed state of DeviceProcess.
type DeviceProcessStatus struct {
	Phase      DeviceProcessPhase `json:"phase,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	StartedAt  *metav1.Time       `json:"startedAt,omitempty"`
	FinishedAt *metav1.Time       `json:"finishedAt,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Namespaced
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DeviceProcess is the Schema for the device processes API.
type DeviceProcess struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceProcessSpec   `json:"spec,omitempty"`
	Status DeviceProcessStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DeviceProcessList contains a list of DeviceProcess.
type DeviceProcessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceProcess `json:"items"`
}

// DeviceProcessDeploymentSpec defines the desired state for deploying DeviceProcesses.
type DeviceProcessDeploymentSpec struct {
	// Selector identifies target devices.
	Selector map[string]string `json:"selector,omitempty"`
	// Template describes the DeviceProcess to run on matched devices.
	Template DeviceProcessSpec `json:"template"`
}

// DeviceProcessDeploymentStatus captures rollout state.
type DeviceProcessDeploymentStatus struct {
	Conditions           []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration   int64              `json:"observedGeneration,omitempty"`
	AvailableProcesses   int32              `json:"availableProcesses,omitempty"`
	ReadyProcesses       int32              `json:"readyProcesses,omitempty"`
	UnavailableProcesses int32              `json:"unavailableProcesses,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Namespaced
//+kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyProcesses`
//+kubebuilder:printcolumn:name="Available",type=integer,JSONPath=`.status.availableProcesses`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DeviceProcessDeployment is the Schema for the deployment API.
type DeviceProcessDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceProcessDeploymentSpec   `json:"spec,omitempty"`
	Status DeviceProcessDeploymentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DeviceProcessDeploymentList contains a list of DeviceProcessDeployment.
type DeviceProcessDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceProcessDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceProcess{}, &DeviceProcessList{}, &DeviceProcessDeployment{}, &DeviceProcessDeploymentList{})
}
