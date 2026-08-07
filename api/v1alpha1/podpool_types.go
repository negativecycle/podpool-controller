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
	// constraints. An HPA targeting the pool's /scale subresource writes
	// this field directly.
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
	// +listType=map
	// +listMapKey=name
	Groups []GroupSpec `json:"groups"`

	// How long a group may sit short of its target before the pool reports
	// ProgressDeadlineExceeded. Mirrors a Deployment's
	// progressDeadlineSeconds. Set to 2147483647 to disable the deadline.
	// +optional
	// +kubebuilder:default=600
	// +kubebuilder:validation:Minimum=1
	ProgressDeadlineSeconds *int32 `json:"progressDeadlineSeconds,omitempty"`
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
	// Standard conditions summarising pool health. Ready is the single
	// top-level signal; Progressing, TargetDegraded, and GroupsReady
	// provide detail.
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The generation of the spec this status reflects. Lags
	// metadata.generation when the controller has not yet reconciled
	// the latest edit.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Total replicas reported by all child workloads.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Total ready replicas reported by all child workloads.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Total updated replicas reported by all child workloads.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`

	// Replicas that no group could accept without exceeding a ceiling the user
	// set. Non-zero only when every group is capped, in which case the pool
	// deliberately runs below spec.replicas rather than overspending on a tier
	// that was limited on purpose.
	// +optional
	UnplacedReplicas int32 `json:"unplacedReplicas,omitempty"`

	// Label selector serialized as a string, used by the /scale
	// subresource to identify pods belonging to this pool.
	// +optional
	Selector string `json:"selector,omitempty"`

	// Number of groups defined in the spec. Projected into a print
	// column so kubectl get shows the pool's fan-out at a glance.
	// +optional
	GroupCount int32 `json:"groupCount,omitempty"`

	// Per-group observed state, one entry per spec.groups entry.
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=name
	// +optional
	Groups []GroupStatus `json:"groups,omitempty"`
}

// GroupStatus reflects the observed state of a single group.
type GroupStatus struct {
	// Name of the group, matching the corresponding spec entry.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Whether this group has at least its target replicas ready. False
	// also covers "not yet known"; see Reason for which case applies.
	// +required
	Ready bool `json:"ready"`

	// Machine-readable reason for Ready. A closed set owned by this
	// API; values are the same vocabulary the pool-level conditions use.
	// +required
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`

	// Human-readable detail from the child workload's conditions.
	// Best-effort: present when the child publishes a standard condition
	// explaining the problem, absent for workload types that publish no
	// conditions (e.g. StatefulSet, DaemonSet).
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`

	// Replicas reported by this group's child workload.
	// +required
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`

	// What the distribution asked this group to run. Differs from
	// Replicas while a rollout is in progress or the child has not
	// yet converged.
	// +required
	// +kubebuilder:validation:Minimum=0
	TargetReplicas int32 `json:"targetReplicas"`

	// Ready replicas reported by this group's child workload.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Updated replicas reported by this group's child workload.
	// Tracked per group so that a group which fails to reconcile can keep its
	// last observed counts without dragging the pool total down.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`

	// This group's share of the pool's total observed replicas, as a percentage.
	// Calculated from observed replicas, not from the distribution target.
	// +optional
	SharePercent int32 `json:"sharePercent,omitempty"`

	// When this group last made progress toward its target. Nil when
	// the group is at target.
	// +optional
	LastProgressTime *metav1.Time `json:"lastProgressTime,omitempty"`

	// Reference to the child workload this group owns.
	// +optional
	WorkloadRef *WorkloadReference `json:"workloadRef,omitempty"`
}

// WorkloadReference identifies a child workload owned by the PodPool.
type WorkloadReference struct {
	// API group and version of the child workload, e.g. "apps/v1".
	// +required
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`

	// Kind of the child workload, e.g. "Deployment".
	// +required
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// Name of the child workload, always <pool>-<group>.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`,description="Replicas requested by spec"
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`,description="Replicas reported ready by child workloads"
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].message`,description="Summary of the Ready condition"
// +kubebuilder:printcolumn:name="Groups",type=integer,JSONPath=`.status.groupCount`,priority=1,description="Number of groups in the pool"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,priority=1,description="Machine-readable reason for the Ready condition"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,description="Time since the pool was created"

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
