// Copyright 2025 Apollo
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceRefKind enumerates supported device kinds.
// +kubebuilder:validation:Enum=Server;NetworkSwitch;SOC;BMC
type DeviceRefKind string

const (
	DeviceRefKindServer        DeviceRefKind = "Server"
	DeviceRefKindNetworkSwitch DeviceRefKind = "NetworkSwitch"
	DeviceRefKindSOC           DeviceRefKind = "SOC"
	DeviceRefKindBMC           DeviceRefKind = "BMC"
)

// DeviceRef identifies the target device resource.
type DeviceRef struct {
	// Kind is the device kind (Server, NetworkSwitch, SOC, BMC).
	Kind DeviceRefKind `json:"kind"`
	// Name of the device resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Namespace of the device resource. Omit when using the same namespace.
	Namespace string `json:"namespace,omitempty"`
}

// ArtifactType enumerates supported artifact sources.
// +kubebuilder:validation:Enum=oci;http;file
type ArtifactType string

const (
	ArtifactTypeOCI  ArtifactType = "oci"
	ArtifactTypeHTTP ArtifactType = "http"
	ArtifactTypeFile ArtifactType = "file"
)

// DeviceProcessArtifact describes the artifact that will be fetched and executed.
type DeviceProcessArtifact struct {
	// Type of artifact reference (oci, http, file).
	Type ArtifactType `json:"type"`
	// URL locates the artifact (registry reference, http(s) URL, or file path).
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
	// ChecksumSHA256 is an optional SHA256 checksum for integrity verification.
	// +kubebuilder:validation:Pattern=`^[A-Fa-f0-9]{64}$`
	ChecksumSHA256 string `json:"checksumSHA256,omitempty"`
}

// DeviceProcessEnvVar is a simple name/value environment variable.
type DeviceProcessEnvVar struct {
	// Name of the variable.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Value assigned to the variable.
	Value string `json:"value,omitempty"`
}

// DeviceProcessBackend enumerates execution backends.
// +kubebuilder:validation:Enum=systemd;initd;container
type DeviceProcessBackend string

const (
	DeviceProcessBackendSystemd   DeviceProcessBackend = "systemd"
	DeviceProcessBackendInitd     DeviceProcessBackend = "initd"
	DeviceProcessBackendContainer DeviceProcessBackend = "container"
)

// DeviceProcessExecution describes how the process is launched.
type DeviceProcessExecution struct {
	// Backend is the execution mechanism (systemd, initd, container).
	Backend DeviceProcessBackend `json:"backend"`
	// Command is the executable and required arguments.
	// +kubebuilder:validation:MinItems=1
	Command []string `json:"command"`
	// Args are appended to the command.
	Args []string `json:"args,omitempty"`
	// Env sets environment variables for the process.
	Env []DeviceProcessEnvVar `json:"env,omitempty"`
	// WorkingDir sets the working directory for the process.
	WorkingDir string `json:"workingDir,omitempty"`
	// User is the user to run the process as.
	User string `json:"user,omitempty"`
}

// DeviceProcessRestartPolicy defines when the process should restart.
// +kubebuilder:validation:Enum=Always;OnFailure;Never
type DeviceProcessRestartPolicy string

const (
	DeviceProcessRestartPolicyAlways    DeviceProcessRestartPolicy = "Always"
	DeviceProcessRestartPolicyOnFailure DeviceProcessRestartPolicy = "OnFailure"
	DeviceProcessRestartPolicyNever     DeviceProcessRestartPolicy = "Never"
)

// DeviceProcessExecAction runs a command for health checking.
type DeviceProcessExecAction struct {
	// Command to run for the health check.
	// +kubebuilder:validation:MinItems=1
	Command []string `json:"command"`
}

// DeviceProcessHealthCheck configures periodic liveness probing via exec.
type DeviceProcessHealthCheck struct {
	// Exec is the exec action used for health checking.
	Exec DeviceProcessExecAction `json:"exec"`
	// PeriodSeconds is the time between probes.
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=1
	PeriodSeconds int32 `json:"periodSeconds,omitempty"`
	// TimeoutSeconds is the probe timeout.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
	// SuccessThreshold is the minimum consecutive successes for the probe to be considered successful.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	SuccessThreshold int32 `json:"successThreshold,omitempty"`
	// FailureThreshold is the number of consecutive failures to treat the process as unhealthy.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	FailureThreshold int32 `json:"failureThreshold,omitempty"`
}

// DeviceProcessSpec defines the desired state of DeviceProcess.
type DeviceProcessSpec struct {
	// DeviceRef points to the device where this process should run.
	DeviceRef DeviceRef `json:"deviceRef"`
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

// DeviceProcessPhase represents lifecycle phase.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed;Unknown
type DeviceProcessPhase string

const (
	DeviceProcessPhasePending   DeviceProcessPhase = "Pending"
	DeviceProcessPhaseRunning   DeviceProcessPhase = "Running"
	DeviceProcessPhaseSucceeded DeviceProcessPhase = "Succeeded"
	DeviceProcessPhaseFailed    DeviceProcessPhase = "Failed"
	DeviceProcessPhaseUnknown   DeviceProcessPhase = "Unknown"
)

// DeviceProcessStatus defines the observed state of DeviceProcess.
type DeviceProcessStatus struct {
	// Phase is a high-level summary of process state.
	Phase DeviceProcessPhase `json:"phase,omitempty"`
	// Conditions capture granular state transitions.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ArtifactVersion is the resolved artifact version (tag or digest).
	ArtifactVersion string `json:"artifactVersion,omitempty"`
	// PID is the process identifier on the target device.
	PID int64 `json:"pid,omitempty"`
	// StartTime is when the process started.
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// LastTransitionTime is when the phase last changed.
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
	// RestartCount is the number of times the process has restarted.
	RestartCount int32 `json:"restartCount,omitempty"`
	// LastTerminationReason describes why the process last exited.
	LastTerminationReason string `json:"lastTerminationReason,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Namespaced
//+kubebuilder:printcolumn:name="DEVICE",type=string,JSONPath=`.spec.deviceRef.name`
//+kubebuilder:printcolumn:name="KIND",type=string,JSONPath=`.spec.deviceRef.kind`
//+kubebuilder:printcolumn:name="PHASE",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="VERSION",type=string,JSONPath=`.status.artifactVersion`
//+kubebuilder:printcolumn:name="RESTARTS",type=integer,JSONPath=`.status.restartCount`
//+kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DeviceProcess is the Schema for the device processes API.
type DeviceProcess struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DeviceProcessSpec   `json:"spec"`
	Status DeviceProcessStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DeviceProcessList contains a list of DeviceProcess.
type DeviceProcessList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DeviceProcess `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DeviceProcess{}, &DeviceProcessList{}, &DeviceProcessDeployment{}, &DeviceProcessDeploymentList{})
}
