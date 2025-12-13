package v1alpha1

// ConditionType represents a typed condition name used on status conditions.
type ConditionType string

const (
	// Agent connection / readiness
	ConditionAgentConnected ConditionType = "AgentConnected"
	// Artifact lifecycle
	ConditionArtifactDownloaded ConditionType = "ArtifactDownloaded"
	// Process lifecycle
	ConditionProcessStarted ConditionType = "ProcessStarted"
	ConditionHealthy        ConditionType = "Healthy"

	// High-level rollout and availability
	ConditionAvailable   ConditionType = "Available"
	ConditionProgressing ConditionType = "Progressing"
)
