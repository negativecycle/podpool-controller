package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// A pool scaled from two groups to one leaves the second group's child
// running unless the sweep deletes it; and the survivor must be untouched.
func TestSweepDeletesChildOfRemovedGroup(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, pool)

	reconcilePool(t, r, pool)

	if !childExists(t, cl, pool, testGroupSpot) {
		t.Fatal("precondition: spot child should exist after the first pass")
	}

	live := getPool(t, cl, pool)

	live.Spec.Groups = live.Spec.Groups[:1]
	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("updating pool: %v", err)
	}

	reconcilePool(t, r, live)

	if childExists(t, cl, pool, testGroupSpot) {
		t.Error("child of the removed group survived the sweep")
	}

	if !childExists(t, cl, pool, testGroupBase) {
		t.Error("child of the surviving group was deleted")
	}
}

func TestStaleWorkloadGVKs(t *testing.T) {
	current := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

	tests := []struct {
		name    string
		prev    []podpoolsv1alpha1.GroupStatus
		current schema.GroupVersionKind
		want    []schema.GroupVersionKind
	}{
		{
			name:    "nil refs produce nothing",
			prev:    []podpoolsv1alpha1.GroupStatus{{Name: "a"}, {Name: "b"}},
			current: current,
			want:    nil,
		},
		{
			name: "ref matching current is not stale",
			prev: []podpoolsv1alpha1.GroupStatus{
				{Name: testGroupBase, WorkloadRef: &podpoolsv1alpha1.WorkloadReference{
					APIVersion: testAppsV1, Kind: testDepKind, Name: "pool-base",
				}},
			},
			current: current,
			want:    nil,
		},
		{
			name: "two groups sharing a stale GVK deduplicate",
			prev: []podpoolsv1alpha1.GroupStatus{
				{Name: testGroupBase, WorkloadRef: &podpoolsv1alpha1.WorkloadReference{
					APIVersion: testAppsV1, Kind: testStsKind, Name: "pool-base",
				}},
				{Name: "burst", WorkloadRef: &podpoolsv1alpha1.WorkloadReference{
					APIVersion: testAppsV1, Kind: testStsKind, Name: "pool-burst",
				}},
			},
			current: current,
			want: []schema.GroupVersionKind{
				{Group: testAppsGroup, Version: "v1", Kind: testStsKind},
			},
		},
		{
			name: "malformed APIVersion is skipped",
			prev: []podpoolsv1alpha1.GroupStatus{
				{Name: "bad", WorkloadRef: &podpoolsv1alpha1.WorkloadReference{
					APIVersion: "not-a-valid-api-version/with/slashes", Kind: testDepKind, Name: "pool-bad",
				}},
			},
			current: current,
			want:    nil,
		},
		{
			name: "mix of current, stale, nil, and malformed",
			prev: []podpoolsv1alpha1.GroupStatus{
				{Name: "a", WorkloadRef: &podpoolsv1alpha1.WorkloadReference{
					APIVersion: testAppsV1, Kind: testDepKind, Name: "pool-a",
				}},
				{Name: "b"},
				{Name: "c", WorkloadRef: &podpoolsv1alpha1.WorkloadReference{
					APIVersion: testAppsV1, Kind: testStsKind, Name: "pool-c",
				}},
				{Name: "d", WorkloadRef: &podpoolsv1alpha1.WorkloadReference{
					APIVersion: "///", Kind: "X", Name: "pool-d",
				}},
			},
			current: current,
			want: []schema.GroupVersionKind{
				{Group: testAppsGroup, Version: "v1", Kind: testStsKind},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleWorkloadGVKs(tt.prev, tt.current)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
