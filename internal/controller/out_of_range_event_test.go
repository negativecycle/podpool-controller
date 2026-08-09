package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

const reasonOutOfRange = "ChildStatusOutOfRange"

// reconcileWithRecorder wires a fake recorder onto a pool whose two children
// report the given counts, and returns the reconciler so callers can drive it
// more than once against the same in-memory gate.
func reconcileWithRecorder(
	t *testing.T, pool *podpoolsv1alpha1.PodPool, baseReady int32,
) (*PodPoolReconciler, *events.FakeRecorder) {
	t.Helper()

	rec := events.NewFakeRecorder(64)
	r, _ := newFakeReconciler(t, nil, pool,
		ownedChild(pool, testGroupBase, 1, baseReady),
		ownedChild(pool, testGroupSpot, 1, 1),
	)
	r.Recorder = rec

	return r, rec
}

// setChildReady rewrites what a group's child reports as ready, read-modify-write
// so the stored object keeps its resourceVersion and the change actually lands.
func setChildReady(t *testing.T, r *PodPoolReconciler, pool *podpoolsv1alpha1.PodPool, group string, ready int32) {
	t.Helper()

	var child appsv1.Deployment

	key := types.NamespacedName{
		Name:      workload.ChildName(pool.Name, group),
		Namespace: pool.Namespace,
	}
	if err := r.Get(t.Context(), key, &child); err != nil {
		t.Fatalf("getting child %s: %v", key.Name, err)
	}

	child.Status.ReadyReplicas = ready
	if err := r.Status().Update(t.Context(), &child); err != nil {
		t.Fatalf("updating child %s to ready=%d: %v", key.Name, ready, err)
	}
}

// A child publishing a count we cannot represent is clamped so the pool keeps
// working (#62), but clamping silently would leave an operator staring at a
// group stuck at 0 with nothing saying why. The warning is the only place that
// says the numbers are ours rather than the child's.
func TestChildStatusOutOfRangeEmitsWarning(t *testing.T) {
	pool := fakeTestPool()
	r, rec := reconcileWithRecorder(t, pool, -1)

	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if got := countEventsByReason(evts, reasonOutOfRange); got != 1 {
		t.Errorf("got %d %s events, want 1; events: %v", got, reasonOutOfRange, evts)
	}
}

// The child is reconciled on every heartbeat and every child event, so an
// ungated warning is an unbounded event stream against a pool that is
// otherwise healthy.
func TestChildStatusOutOfRangeEmitsOnlyOnTransition(t *testing.T) {
	pool := fakeTestPool()
	r, rec := reconcileWithRecorder(t, pool, -1)

	reconcilePool(t, r, pool)
	reconcilePool(t, r, pool)
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if got := countEventsByReason(evts, reasonOutOfRange); got != 1 {
		t.Errorf("got %d %s events across 3 reconciles, want 1 (transition only); events: %v",
			got, reasonOutOfRange, evts)
	}
}

// Latching the gate forever would mean a child that misbehaves, recovers, and
// misbehaves again is reported once and then never again.
func TestChildStatusOutOfRangeReArmsAfterRecovery(t *testing.T) {
	pool := fakeTestPool()
	r, rec := reconcileWithRecorder(t, pool, -1)

	reconcilePool(t, r, pool)

	// The child starts reporting a sane count again.
	setChildReady(t, r, pool, testGroupBase, 1)
	reconcilePool(t, r, pool)

	// And then regresses.
	setChildReady(t, r, pool, testGroupBase, -1)
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if got := countEventsByReason(evts, reasonOutOfRange); got != 2 {
		t.Errorf("got %d %s events, want 2 (once per transition into the bad state); events: %v",
			got, reasonOutOfRange, evts)
	}
}

// The guard must not fire on the counts every healthy pool publishes.
func TestNoOutOfRangeEventForRepresentableCounts(t *testing.T) {
	pool := fakeTestPool()
	r, rec := reconcileWithRecorder(t, pool, 1)

	reconcilePool(t, r, pool)
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if got := countEventsByReason(evts, reasonOutOfRange); got != 0 {
		t.Errorf("got %d %s events for in-range counts, want 0; events: %v",
			got, reasonOutOfRange, evts)
	}
}
