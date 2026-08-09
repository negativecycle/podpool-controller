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

package workload_test

// Groundwork for plans/16-per-group-ready-message.md.
//
// #16's childDetail reads a child workload's conditions out of unstructured
// JSON. Every one of its table cases is a hand-written map, so the whole table
// rests on those maps matching what k8s.io/api actually produces. This file
// pins that, and nothing else — childDetail cannot be tested until
// GroupStatus.Ready/Reason/Message exist, which they do not yet.
//
// These pass today and are expected to keep passing. They fail when a
// k8s.io/api bump changes the shape childDetail parses, which is the one way
// that function can rot without anybody touching it.

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// toUnstructured round-trips a typed object the way the controller actually
// receives a child — through the converter the client uses, NOT through
// encoding/json. See TestNumbersAreInt64NotFloat64 for why that distinction is
// load-bearing.
func toUnstructured(t *testing.T, obj runtime.Object) map[string]any {
	t.Helper()

	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}

	return out
}

// TestNumbersAreInt64NotFloat64 is the trap a hand-written fixture walks into.
//
// unstructured.NestedInt64 — which #16's generation check needs, and which
// ReadInt32 already uses for the replica counts — accepts int64 and errors on
// float64. The client's converter yields int64; a fixture built with
// encoding/json.Unmarshal yields float64 for the same field.
//
// So a childDetail test whose fixtures come from raw JSON would exercise a type
// path that never occurs in production, and the failure mode is quiet: the
// generation check would error, be discarded, and fall through to "assume
// current" — passing for the wrong reason.
func TestNumbersAreInt64NotFloat64(t *testing.T) {
	t.Parallel()

	dep := &appsv1.Deployment{Status: appsv1.DeploymentStatus{ObservedGeneration: 4}}

	viaConverter := toUnstructured(t, dep)
	if _, found, err := unstructured.NestedInt64(viaConverter, "status", "observedGeneration"); !found || err != nil {
		t.Errorf("converter output is not int64 (found=%v, err=%v)", found, err)
	}

	raw, err := json.Marshal(dep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var viaEncodingJSON map[string]any
	if err := json.Unmarshal(raw, &viaEncodingJSON); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, _, err := unstructured.NestedInt64(viaEncodingJSON, "status", "observedGeneration"); err == nil {
		t.Error("encoding/json fixture read as int64 — the trap this test documents has gone away, " +
			"and the warning in plans/16-per-group-ready-message.md can be dropped")
	}
}

// TestDeploymentConditionsLandWhereChildDetailLooks pins the JSON path and the
// field names. childDetail walks status.conditions[] and reads four strings off
// each element; if any of that moves, every fixture in #16's table becomes a
// test of nothing.
func TestDeploymentConditionsLandWhereChildDetailLooks(t *testing.T) {
	t.Parallel()

	dep := &appsv1.Deployment{
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 4,
			Conditions: []appsv1.DeploymentCondition{{
				Type:    appsv1.DeploymentAvailable,
				Status:  corev1.ConditionFalse,
				Reason:  "MinimumReplicasUnavailable",
				Message: "Deployment does not have minimum availability.",
			}},
		},
	}

	obj := toUnstructured(t, dep)

	conds, found, err := unstructured.NestedSlice(obj, "status", "conditions")
	if err != nil || !found || len(conds) != 1 {
		t.Fatalf("status.conditions not at the expected path (found=%v, err=%v)", found, err)
	}

	c, ok := conds[0].(map[string]any)
	if !ok {
		t.Fatalf("condition element is %T, want map[string]any", conds[0])
	}

	for key, want := range map[string]string{
		"type":    "Available",
		"status":  "False",
		"reason":  "MinimumReplicasUnavailable",
		"message": "Deployment does not have minimum availability.",
	} {
		got, isString := c[key].(string)
		if !isString {
			t.Errorf("condition[%q] is %T, want string — childDetail must comma-ok assert it", key, c[key])

			continue
		}

		if got != want {
			t.Errorf("condition[%q] = %q, want %q", key, got, want)
		}
	}

	// The generation check reads these two. Both must be int64 after the JSON
	// round trip, which is what unstructured.NestedInt64 requires.
	if _, found, err := unstructured.NestedInt64(obj, "status", "observedGeneration"); !found || err != nil {
		t.Errorf("status.observedGeneration not readable as int64 (found=%v, err=%v)", found, err)
	}
}

// TestConditionTypesAreAPIStableButReasonsAreNot records the boundary that
// makes #16 best-effort rather than contractual.
//
// The three condition *types* childDetail keys off are exported constants, so
// matching on them is safe across releases. The reason strings it surfaces —
// MinimumReplicasUnavailable, FailedCreate, NewReplicaSetAvailable — are not in
// k8s.io/api at all; they live in kubernetes/pkg/controller/deployment/util,
// which is not an importable dependency. They are therefore unversioned
// implementation detail that can change without an API review.
//
// Consequence for #16: match on type, never on reason, and never assert a
// specific reason string in a test that reads from a real controller.
func TestConditionTypesAreAPIStableButReasonsAreNot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		constant appsv1.DeploymentConditionType
		want     string
	}{
		{appsv1.DeploymentAvailable, "Available"},
		{appsv1.DeploymentProgressing, "Progressing"},
		{appsv1.DeploymentReplicaFailure, "ReplicaFailure"},
	} {
		if string(tc.constant) != tc.want {
			t.Errorf("condition type constant = %q, want %q", tc.constant, tc.want)
		}
	}
}

// TestStatefulSetAndDaemonSetDeclareConditionsButPublishNone pins the half of
// #16's "types that publish nothing" claim that is checkable without a running
// controller: the field exists and marshals to the same path, so an
// implementation cannot rely on its absence from the schema.
//
// The other half — that neither controller ever populates it — is a statement
// about kube-controller-manager, not about the API, and can only be settled in
// kwok. Until then #16's fallback path is reasoned rather than measured.
func TestStatefulSetAndDaemonSetDeclareConditionsButPublishNone(t *testing.T) {
	t.Parallel()

	t.Run("StatefulSet", func(t *testing.T) {
		t.Parallel()

		sts := &appsv1.StatefulSet{
			Status: appsv1.StatefulSetStatus{
				Replicas:      3,
				ReadyReplicas: 3,
				Conditions: []appsv1.StatefulSetCondition{{
					Type:   "SomeCondition",
					Status: corev1.ConditionTrue,
					Reason: "Whatever",
				}},
			},
		}

		obj := toUnstructured(t, sts)
		if _, found, _ := unstructured.NestedSlice(obj, "status", "conditions"); !found {
			t.Error("StatefulSetStatus.Conditions did not marshal to status.conditions")
		}
	})

	t.Run("DaemonSet", func(t *testing.T) {
		t.Parallel()

		ds := &appsv1.DaemonSet{
			Status: appsv1.DaemonSetStatus{
				Conditions: []appsv1.DaemonSetCondition{{
					Type:   "SomeCondition",
					Status: corev1.ConditionTrue,
				}},
			},
		}

		obj := toUnstructured(t, ds)
		if _, found, _ := unstructured.NestedSlice(obj, "status", "conditions"); !found {
			t.Error("DaemonSetStatus.Conditions did not marshal to status.conditions")
		}
	})

	// And the shape childDetail will actually meet for these kinds: counts
	// present, conditions absent entirely rather than an empty list.
	t.Run("no conditions set", func(t *testing.T) {
		t.Parallel()

		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Generation: 2},
			Status:     appsv1.StatefulSetStatus{Replicas: 3, ReadyReplicas: 3},
		}

		obj := toUnstructured(t, sts)
		if _, found, err := unstructured.NestedSlice(obj, "status", "conditions"); found || err != nil {
			t.Errorf("expected status.conditions to be absent, got found=%v err=%v", found, err)
		}

		if _, found, _ := unstructured.NestedInt64(obj, "status", "readyReplicas"); !found {
			t.Error("readyReplicas absent — the fixture does not resemble a real StatefulSet")
		}
	})
}
