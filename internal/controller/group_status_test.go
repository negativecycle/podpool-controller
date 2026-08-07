package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
)

// TestGroupStatusRowsPopulated pins the per-group surface: one row per spec
// group, carrying the counts the child reported, the distribution's ask, the
// workloadRef that the stale-kind sweep depends on, and a share of the pool.
func TestGroupStatusRowsPopulated(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	// Report child status the way a Deployment controller would.
	for _, group := range []string{testGroupBase, testGroupSpot} {
		var dep appsv1.Deployment

		key := types.NamespacedName{Name: pool.Name + "-" + group, Namespace: testNamespace}
		if err := cl.Get(t.Context(), key, &dep); err != nil {
			t.Fatalf("getting child for group %s: %v", group, err)
		}

		dep.Status.Replicas = *dep.Spec.Replicas
		dep.Status.ReadyReplicas = *dep.Spec.Replicas

		dep.Status.UpdatedReplicas = *dep.Spec.Replicas
		if err := cl.Status().Update(t.Context(), &dep); err != nil {
			t.Fatalf("updating child status for group %s: %v", group, err)
		}
	}

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)

	if len(got.Status.Groups) != 2 {
		t.Fatalf("status.groups has %d rows, want 2: %+v", len(got.Status.Groups), got.Status.Groups)
	}

	base := findGroupStatus(got.Status.Groups, testGroupBase)
	if base == nil {
		t.Fatal("base row missing")
	}

	if base.Replicas != 2 || base.ReadyReplicas != 2 || base.TargetReplicas != 2 {
		t.Errorf("base counts = %d/%d target %d, want 2/2 target 2",
			base.Replicas, base.ReadyReplicas, base.TargetReplicas)
	}

	if base.WorkloadRef == nil || base.WorkloadRef.Kind != testDepKind ||
		base.WorkloadRef.Name != pool.Name+"-"+testGroupBase {
		t.Errorf("base workloadRef = %+v, want Deployment %s", base.WorkloadRef, pool.Name+"-"+testGroupBase)
	}

	if !base.Ready || base.Reason != ReasonAllReplicasReady {
		t.Errorf("base Ready/Reason = %v/%s, want true/AllReplicasReady", base.Ready, base.Reason)
	}

	// 2 of 3 total replicas.
	if base.SharePercent != 66 {
		t.Errorf("base sharePercent = %d, want 66", base.SharePercent)
	}

	if got.Status.Replicas != 3 || got.Status.ReadyReplicas != 3 {
		t.Errorf("pool totals = %d/%d, want 3/3", got.Status.Replicas, got.Status.ReadyReplicas)
	}

	if got.Status.GroupCount != 2 {
		t.Errorf("groupCount = %d, want 2", got.Status.GroupCount)
	}
}

// TestGroupReasonsFollowReadiness pins the reason ladder for a group that is
// short of its ask: not ready, ReplicasUpdating, and the failing row keeps
// the reason its error path assigned.
func TestGroupReasonsFollowReadiness(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)

	base := findGroupStatus(got.Status.Groups, testGroupBase)
	if base == nil || base.Ready || base.Reason != ReasonReplicasUpdating {
		t.Errorf("short group = %+v, want !Ready/ReplicasUpdating", base)
	}
}

// TestFailedGroupRowKeepsErrorReason: the error path names the failure class
// and assignGroupReasons must not overwrite it.
func TestFailedGroupRowKeepsErrorReason(t *testing.T) {
	pool := fakeTestPool()
	breakGroup(pool)

	r, cl := newFakeReconciler(t, nil, pool)

	_ = tryReconcilePool(r, pool)

	got := getPool(t, cl, pool)

	base := findGroupStatus(got.Status.Groups, testGroupBase)
	if base == nil || base.Reason != ReasonGroupReconcileFailed {
		t.Errorf("failed row = %+v, want reason GroupReconcileFailed", base)
	}

	spot := findGroupStatus(got.Status.Groups, testGroupSpot)
	if spot == nil {
		t.Error("healthy group missing from status.groups")
	}
}
