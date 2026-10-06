// Package v1alpha1 contains the BusinessWorkItem API.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the API group and version served by this controller.
	GroupVersion = schema.GroupVersion{Group: "tokens.jeder.github.com", Version: "v1alpha1"}

	// SchemeBuilder registers the API types with a Kubernetes scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds all BusinessWorkItem types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &BusinessWorkItem{}, &BusinessWorkItemList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
