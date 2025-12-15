package v1alpha1

// ConditionType represents a typed condition name used on status conditions.
type ConditionType string

const (
	// Agent connection / readiness
	ConditionAgentConnected ConditionType = "AgentConnected"
	// Spec observation / drift tracking
	ConditionSpecObserved ConditionType = "SpecObserved"
	// Spec warnings (e.g., semantic mismatches or deprecated fields)
	ConditionSpecWarning ConditionType = "SpecWarning"
	// Artifact lifecycle
	ConditionArtifactDownloaded ConditionType = "ArtifactDownloaded"
	// Process lifecycle
	ConditionProcessStarted ConditionType = "ProcessStarted"
	ConditionHealthy        ConditionType = "Healthy"

	// High-level rollout and availability
	ConditionAvailable   ConditionType = "Available"
	ConditionProgressing ConditionType = "Progressing"
)
