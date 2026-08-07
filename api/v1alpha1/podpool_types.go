/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// PodPoolSpec defines the desired state of PodPool.
type PodPoolSpec struct {
	// Total number of pod replicas to distribute across all groups. Each
	// group receives a share of this total according to its scaling
	// constraints.
	// +required
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	// Template for the child workload each group creates. Accepts any
	// workload kind with a pod template at .spec.template: Deployment,
	// StatefulSet, or a CRD such as Argo Rollout or Kruise CloneSet.
	// The controller copies this template per group, injects
	// group-specific overrides, and applies it with server-side apply.
	// +required
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	WorkloadTemplate runtime.RawExtension `json:"workloadTemplate"`

	// Ordered list of groups that divide the pool's replicas. Groups are
	// filled in list order: earlier groups are satisfied first, and the
	// first group constrained only by min (no ceiling) absorbs whatever
	// remains.
	// +required
	// +kubebuilder:validation:MinItems=1
	Groups []GroupSpec `json:"groups"`
}

// GroupSpec defines a single group within a PodPool.
type GroupSpec struct {
	// DNS-label name that identifies this group within the pool. Becomes
	// part of the child workload's name (<pool>-<name>) and its label
	// selector, so renaming a group replaces its child entirely.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]*[a-z0-9]$`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// How many of the pool's total replicas this group should receive.
	// +required
	Scaling ScalingConstraints `json:"scaling"`

	// Partial workload object deep-merged on top of the pool's template
	// before the child is created. Use this to differentiate groups:
	// node selectors, tolerations, resource requests, or any field the
	// workload kind supports. Null values delete the corresponding key
	// from the template.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Overrides *runtime.RawExtension `json:"overrides,omitempty"`
}

// ScalingConstraints defines the distribution constraints for a group.
//
// Defined incrementally: each constraint field arrives with the
// distribution behavior that honours it. An empty constraint set is legal
// and means the group takes whatever the distributor assigns.
type ScalingConstraints struct{}

// PodPoolStatus defines the observed state of PodPool.
type PodPoolStatus struct {
	// conditions represent the current state of the PodPool resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PodPool is the Schema for the podpools API.
type PodPool struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec PodPoolSpec `json:"spec,omitzero"`

	// +optional
	Status PodPoolStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PodPoolList contains a list of PodPool.
type PodPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`

	Items []PodPool `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PodPool{}, &PodPoolList{})

		return nil
	})
}
