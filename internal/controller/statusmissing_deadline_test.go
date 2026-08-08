package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

const reasonStatusMissing = "StatusMissing"

// A Deployment carrying status.replicas but no status.readyReplicas is what
// ownedChild(..., n, 0) produces: readyReplicas is omitempty on every built-in
// workload type, so zero is written as *absent*. That is the whole reason this
// warning exists, and it is also the reason it is hard to get right.
func statusMissingReconciler(t *testing.T, pool *podpoolsv1alpha1.PodPool, ready int32) (*PodPoolReconciler, *events.FakeRecorder) {
	t.Helper()

	rec := events.NewFakeRecorder(64)
	r, _ := newFakeReconciler(t, nil, pool,
		ownedChild(pool, testGroupBase, 2, ready),
		ownedChild(pool, testGroupSpot, 1, 1),
	)
	r.Recorder = rec

	return r, rec
}

// The pool reports 0 ready for a kind that never publishes readiness, and
// cannot tell that from a kind that publishes an honest zero. Saying so once is
// the only thing standing between an operator and a permanently wrong number
// with no explanation.
func TestStatusMissingWarns(t *testing.T) {
	pool := fakeTestPool()
	r, rec := statusMissingReconciler(t, pool, 0)

	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, reasonStatusMissing); n != 1 {
		t.Errorf("got %d %s events, want 1; events: %v", n, reasonStatusMissing, evts)
	}
}

// Deduped by kind for the lifetime of the process. Whether a kind publishes
// readiness is a fact about the kind, so repeating it per pass would be spam
// and repeating it per pool would be spam at a slower rate.
func TestStatusMissingWarnsOncePerKind(t *testing.T) {
	pool := fakeTestPool()
	r, rec := statusMissingReconciler(t, pool, 0)

	reconcilePool(t, r, pool)
	reconcilePool(t, r, pool)
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, reasonStatusMissing); n != 1 {
		t.Errorf("got %d %s events across 3 reconciles, want 1; events: %v",
			n, reasonStatusMissing, evts)
	}
}

// The dedup is keyed on the GVK, not on the pool, so a second pool running the
// same kind is silent. This is the opposite call from the out-of-range warning,
// which is keyed per group because a child reporting nonsense is a fact about
// that object.
func TestStatusMissingIsSilentForASecondPoolOfTheSameKind(t *testing.T) {
	first := fakeTestPool()
	r, rec := statusMissingReconciler(t, first, 0)

	reconcilePool(t, r, first)
	drainEvents(rec.Events)

	second := fakeTestPool()
	second.Name = "other-pool"

	if err := r.Create(t.Context(), second); err != nil {
		t.Fatalf("creating second pool: %v", err)
	}

	if err := r.Create(t.Context(), ownedChild(second, testGroupBase, 2, 0)); err != nil {
		t.Fatalf("creating second pool's child: %v", err)
	}

	reconcilePool(t, r, second)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, reasonStatusMissing); n != 0 {
		t.Errorf("got %d %s events for a second pool of the same kind, want 0; events: %v",
			n, reasonStatusMissing, evts)
	}
}

// A group asked for nothing has nothing to be ready, so absent readiness there
// says nothing about the kind.
func TestStatusMissingIsSilentWhenNothingWasAsked(t *testing.T) {
	pool := fakeTestPool()
	pool.Spec.Replicas = 0
	pool.Spec.Groups[0].Scaling = podpoolsv1alpha1.ScalingConstraints{}
	pool.Spec.Groups[1].Scaling = podpoolsv1alpha1.ScalingConstraints{}

	r, rec := statusMissingReconciler(t, pool, 0)

	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, reasonStatusMissing); n != 0 {
		t.Errorf("got %d %s events for a pool asking for no replicas, want 0; events: %v",
			n, reasonStatusMissing, evts)
	}
}

// A kind that does publish readiness must never trip the warning, no matter how
// many passes run.
func TestStatusMissingIsSilentWhenReadinessIsPublished(t *testing.T) {
	pool := fakeTestPool()
	r, rec := statusMissingReconciler(t, pool, 2)

	reconcilePool(t, r, pool)
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, reasonStatusMissing); n != 0 {
		t.Errorf("got %d %s events for a child publishing readiness, want 0; events: %v",
			n, reasonStatusMissing, evts)
	}
}

// The map is keyed by the whole GVK, so a pool whose template names a different
// kind gets its own verdict. Otherwise the first CRD to misbehave would silence
// the diagnosis for every other kind in the cluster.
func TestStatusMissingKeyIsTheWholeGVK(t *testing.T) {
	r := &PodPoolReconciler{}
	pool := fakeTestPool()

	dep := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}
	sts := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testStsKind}

	rec := events.NewFakeRecorder(64)
	r.Recorder = rec

	r.reportStatusMissing(pool, testGroupBase, dep, "pool-base", false, 2)
	r.reportStatusMissing(pool, testGroupBase, dep, "pool-base", false, 2)
	r.reportStatusMissing(pool, testGroupBase, sts, "pool-base", false, 2)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, reasonStatusMissing); n != 2 {
		t.Errorf("got %d %s events for two distinct kinds reported twice each, want 2; events: %v",
			n, reasonStatusMissing, evts)
	}
}
