package conditions

import (
	"time"

	v1alpha1 "github.com/apollo/praetor/api/azure.com/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FindCondition returns the first condition with the given type.
func FindCondition(conditions []metav1.Condition, conditionType v1alpha1.ConditionType) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == string(conditionType) {
			return &conditions[i]
		}
	}
	return nil
}

// SetCondition adds or updates a condition ensuring LastTransitionTime only changes when status does.
func SetCondition(conditions *[]metav1.Condition, condition metav1.Condition) {
	now := metav1.NewTime(time.Now())
	condition.LastTransitionTime = now

	for i := range *conditions {
		existing := &(*conditions)[i]
		if existing.Type != condition.Type {
			continue
		}

		// Preserve the transition time if status is unchanged.
		if existing.Status == condition.Status {
			condition.LastTransitionTime = existing.LastTransitionTime
		}
		*existing = condition
		return
	}

	*conditions = append(*conditions, condition)
}

// MarkTrue sets the given condition type to True with the provided reason/message.
func MarkTrue(conditions *[]metav1.Condition, conditionType v1alpha1.ConditionType, reason, message string) {
	SetCondition(conditions, metav1.Condition{
		Type:    string(conditionType),
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	})
}

// MarkFalse sets the given condition type to False with the provided reason/message.
func MarkFalse(conditions *[]metav1.Condition, conditionType v1alpha1.ConditionType, reason, message string) {
	SetCondition(conditions, metav1.Condition{
		Type:    string(conditionType),
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	})
}
