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
	"k8s.io/apimachinery/pkg/util/intstr"
)

// PodPoolSpec defines the desired state of PodPool.
type PodPoolSpec struct {
	// Total number of pod replicas to distribute across all groups. Each
	// group receives a share of this total according to its scaling
	// constraints.
	// +required
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000
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
	// first group constrained only by min (no ceiling, not opportunistic)
	// absorbs whatever remains.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
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
	// +kubebuilder:validation:MaxLength=53
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
// Every group is a (floor, target, ceiling) triple. `min` and `max` are
// hard limits; `target` is best-effort. The ceiling is `max` if set,
// otherwise the target itself. The floor is `min`, defaulting to 0.
//
// +kubebuilder:validation:XValidation:rule="!has(self.min) || !has(self.max) || self.min <= self.max",message="min must not exceed max",reason=FieldValueInvalid
// +kubebuilder:validation:XValidation:rule="(!has(self.opportunistic) || !self.opportunistic) || (!has(self.max) && !has(self.target))",message="opportunistic is itself the ceiling; it pairs only with min",reason=FieldValueInvalid,fieldPath=".opportunistic"
// +kubebuilder:validation:XValidation:rule="!has(self.target) || (type(self.target) == string && self.target.matches('^([1-9]|[1-9][0-9]|100)%$'))",message="target must be a percentage string like \"30%\" (1%–100%)",reason=FieldValueInvalid,fieldPath=".target"
type ScalingConstraints struct {
	// Cascade threshold. Satisfy before filling later groups.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Min *int32 `json:"min,omitempty"`

	// Absolute ceiling. Never more than this many pods.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Max *int32 `json:"max,omitempty"`

	// Best-effort target. The group's desired share of the pool total, as
	// a percentage (1%–100%). Expressed as a string with a trailing percent
	// sign, following the same convention as maxSurge. When no max is set,
	// the target doubles as the ceiling; when max is set, the group may
	// grow past the target up to max via overflow.
	// +optional
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:validation:MaxLength=4
	Target *intstr.IntOrString `json:"target,omitempty"`

	// Size this group by whatever the scheduler will actually accept, rather
	// than by a declared number or percentage.
	//
	// For a group running in another tier's spare capacity (expendable
	// priority, pinned to nodes it does not own) the right size is "however
	// much happens to be free right now", which no static value can express.
	// The controller finds it by offering the group more replicas than it
	// expects to place and handing the ones the scheduler rejects to the next
	// group.
	//
	// Pairs with min only: this *is* the ceiling, so max and target would
	// contradict it. Requires a later group able to absorb what does not fit.
	// +optional
	Opportunistic *bool `json:"opportunistic,omitempty"`
}

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
