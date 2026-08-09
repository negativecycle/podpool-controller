package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// A pool scaled from two groups to one leaves the second group's child
// running unless the sweep deletes it; and the survivor must be untouched.
func TestSweepDeletesChildOfRemovedGroup(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

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

// TestSweepIgnoresDriftedGroupLabel pins the keep decision to the child's
// name. The group label is user-writable and read through a cache: a drifted
// label on a healthy child must never read as "orphan". The name is derived
// from spec and immutable, so a stale read can at worst defer a real orphan.
func TestSweepIgnoresDriftedGroupLabel(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	// Drift the label the way a user (or a chaotic controller) would; SSA
	// would repair it next pass, but the sweep may be reading the pre-repair
	// copy.
	child := getChild(t, cl, pool.Name)

	child.Labels[workload.LabelGroup] = "no-such-group"
	if err := cl.Update(t.Context(), child); err != nil {
		t.Fatalf("drifting group label: %v", err)
	}

	reconcilePool(t, r, pool)

	if !childExists(t, cl, pool, testGroupBase) {
		t.Error("a drifted group label got a healthy child deleted; the sweep must key on the name")
	}
}

// TestSweepDeleteCarriesUIDPrecondition pins the last inch of the hardening:
// confirm and delete are two calls, and between them the orphan can be
// replaced at the same name. The delete must be bound to the UID that was
// confirmed, so a newcomer at the same name survives the race.
func TestSweepDeleteCarriesUIDPrecondition(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	spotChild := &appsv1.Deployment{}

	spotKey := types.NamespacedName{Name: pool.Name + "-" + testGroupSpot, Namespace: testNamespace}
	if err := cl.Get(t.Context(), spotKey, spotChild); err != nil {
		t.Fatalf("getting spot child: %v", err)
	}

	live := getPool(t, cl, pool)

	live.Spec.Groups = live.Spec.Groups[:1]
	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("updating pool: %v", err)
	}

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	var captured []client.DeleteOption

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			captured = opts

			return c.Delete(ctx, obj, opts...)
		},
	})

	reconcilePool(t, r, live)

	if childExists(t, cl, pool, testGroupSpot) {
		t.Fatal("orphan was not deleted")
	}

	found := false

	for _, opt := range captured {
		p, ok := opt.(client.Preconditions)
		if !ok {
			continue
		}

		if p.UID != nil && *p.UID == spotChild.UID {
			found = true
		}
	}

	if !found {
		t.Errorf("delete options %v carried no UID precondition for %s; "+
			"a name reused between confirm and delete would take the newcomer", captured, spotChild.UID)
	}
}
