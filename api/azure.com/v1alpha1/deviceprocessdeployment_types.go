// Copyright 2025 Apollo
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// DeviceProcessDeploymentStrategyType enumerates deployment strategies.
// +kubebuilder:validation:Enum=RollingUpdate;Recreate
type DeviceProcessDeploymentStrategyType string

const (
	DeviceProcessDeploymentStrategyRollingUpdate DeviceProcessDeploymentStrategyType = "RollingUpdate"
	DeviceProcessDeploymentStrategyRecreate      DeviceProcessDeploymentStrategyType = "Recreate"
)

// DeviceProcessRollingUpdate configures rolling update behavior.
type DeviceProcessRollingUpdate struct {
	// MaxUnavailable is the maximum number or percentage of unavailable targets during the update.
	// +kubebuilder:default="10%"
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

// DeviceProcessDeploymentStrategy describes the deployment strategy.
type DeviceProcessDeploymentStrategy struct {
	// Type is the strategy type.
	// +kubebuilder:default=RollingUpdate
	Type DeviceProcessDeploymentStrategyType `json:"type,omitempty"`
	// RollingUpdate holds settings for RollingUpdate strategy.
	// +kubebuilder:validation:XValidation:rule="self.type != 'RollingUpdate' || has(self.rollingUpdate)",message="rollingUpdate must be set when type is RollingUpdate"
	RollingUpdate *DeviceProcessRollingUpdate `json:"rollingUpdate,omitempty"`
}

// DeviceProcessTemplateMetadata carries labels for the templated DeviceProcess.
type DeviceProcessTemplateMetadata struct {
	// Labels are applied to DeviceProcess instances created by this deployment.
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are forwarded to DeviceProcess instances created by this deployment.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// DeviceProcessTemplateSpec matches DeviceProcessSpec without deviceRef.
type DeviceProcessTemplateSpec struct {
	// Artifact describes the artifact to fetch and run.
	Artifact DeviceProcessArtifact `json:"artifact"`
	// Execution describes how to execute the artifact.
	Execution DeviceProcessExecution `json:"execution"`
	// RestartPolicy controls restart behavior.
	// +kubebuilder:default=Always
	RestartPolicy DeviceProcessRestartPolicy `json:"restartPolicy,omitempty"`
	// HealthCheck configures optional periodic health probes.
	HealthCheck *DeviceProcessHealthCheck `json:"healthCheck,omitempty"`
}

// DeviceProcessTemplate defines the template used for each DeviceProcess instance.
type DeviceProcessTemplate struct {
	// Metadata provides labels to carry forward.
	Metadata DeviceProcessTemplateMetadata `json:"metadata,omitempty"`
	// Spec is the desired DeviceProcess specification for each target.
	Spec DeviceProcessTemplateSpec `json:"spec"`
}

// DeviceProcessDeploymentSpec defines the desired state for deploying DeviceProcesses.
type DeviceProcessDeploymentSpec struct {
	// Selector identifies target devices.
	Selector metav1.LabelSelector `json:"selector"`
	// UpdateStrategy defines how updates roll out.
	UpdateStrategy DeviceProcessDeploymentStrategy `json:"updateStrategy,omitempty"`
	// Template describes the DeviceProcess to run on matched devices.
	Template DeviceProcessTemplate `json:"template"`
}

// DeviceProcessDeploymentStatus captures rollout state.
type DeviceProcessDeploymentStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// DesiredNumberScheduled is the total number of processes that should be scheduled.
	DesiredNumberScheduled int32 `json:"desiredNumberScheduled,omitempty"`
	// CurrentNumberScheduled is the number of processes currently scheduled.
	CurrentNumberScheduled int32 `json:"currentNumberScheduled,omitempty"`
	// UpdatedNumberScheduled is the number of updated processes.
	UpdatedNumberScheduled int32 `json:"updatedNumberScheduled,omitempty"`
	// NumberReady is the count of ready processes.
	NumberReady int32 `json:"numberReady,omitempty"`
	// NumberAvailable is the count of available processes.
	NumberAvailable int32 `json:"numberAvailable,omitempty"`
	// NumberUnavailable is the count of unavailable processes.
	NumberUnavailable int32 `json:"numberUnavailable,omitempty"`
	// Conditions track rollout state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Namespaced
//+kubebuilder:printcolumn:name="DESIRED",type=integer,JSONPath=`.status.desiredNumberScheduled`
//+kubebuilder:printcolumn:name="CURRENT",type=integer,JSONPath=`.status.currentNumberScheduled`
//+kubebuilder:printcolumn:name="UPDATED",type=integer,JSONPath=`.status.updatedNumberScheduled`
//+kubebuilder:printcolumn:name="READY",type=integer,JSONPath=`.status.numberReady`
//+kubebuilder:printcolumn:name="AVAILABLE",type=integer,JSONPath=`.status.numberAvailable`
//+kubebuilder:printcolumn:name="UNAVAILABLE",type=integer,JSONPath=`.status.numberUnavailable`
//+kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DeviceProcessDeployment is the Schema for the deployment API.
type DeviceProcessDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceProcessDeploymentSpec   `json:"spec"`
	Status DeviceProcessDeploymentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DeviceProcessDeploymentList contains a list of DeviceProcessDeployment.
type DeviceProcessDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceProcessDeployment `json:"items"`
}
