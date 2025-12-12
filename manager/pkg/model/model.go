package model

import (
	"time"

	"manager/pkg/types"
)

// Device captures minimal metadata tracked for a managed device.
type Device struct {
	ID         string            `json:"deviceId"`
	DeviceType types.DeviceType  `json:"deviceType"`
	Labels     map[string]string `json:"labels"`
	LastSeen   time.Time         `json:"lastSeen"`
	Online     bool              `json:"online"`
}

// DeviceStatus reflects the agent-reported execution state.
type DeviceStatus struct {
	DeviceID string `json:"deviceId"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Message  string `json:"message"`
}

// RolloutSpec defines desired rollout state.
type RolloutSpec struct {
	Version     string            `json:"version"`
	Command     []string          `json:"command"`
	Selector    map[string]string `json:"selector"`
	MaxFailures float64           `json:"maxFailures"`
}

// RolloutStatus captures rollout execution progress.
type RolloutStatus struct {
	Generation         int64             `json:"generation"`
	ObservedGeneration int64             `json:"observedGeneration"`
	UpdatedDevices     map[string]bool   `json:"updatedDevices"`
	FailedDevices      map[string]string `json:"failedDevices"`
	TotalTargets       int               `json:"totalTargets"`
	SuccessCount       int               `json:"successCount"`
	FailureCount       int               `json:"failureCount"`
	State              string            `json:"state"`
}

// Rollout models a device-type-scoped rollout resource.
type Rollout struct {
	Name       string           `json:"name"`
	DeviceType types.DeviceType `json:"deviceType"`
	CreatedAt  time.Time        `json:"createdAt"`
	Spec       RolloutSpec      `json:"spec"`
	Status     RolloutStatus    `json:"status"`
}
