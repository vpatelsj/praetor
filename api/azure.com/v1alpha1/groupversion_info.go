// Package v1alpha1 contains API Schema definitions for the azure.com v1alpha1 API group
// +kubebuilder:object:generate=true
// +groupName=azure.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: "azure.com", Version: "v1alpha1"}
	SchemeBuilder      = &scheme.Builder{GroupVersion: SchemeGroupVersion}
	AddToScheme        = SchemeBuilder.AddToScheme
)
