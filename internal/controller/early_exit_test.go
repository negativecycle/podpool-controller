package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// Reconcile has two exits above its main body: an unparseable workload
// template, and a watch that will not come up. The full path maintains
// invariants that neither of them does, so tests here name the invariant
// rather than the exit: the next early return added will break the same ones.

// pendingInformer reports unsynced until synced is flipped. The embedded nil
// panics on anything ensureWatch does not call, which is the point: it fails
// loudly if this path grows a dependency the fake does not model.
type pendingInformer struct {
	cache.Informer

	synced *atomic.Bool
}

func (p pendingInformer) HasSynced() bool { return p.synced.Load() }
func (p pendingInformer) IsStopped() bool { return false }

// syncCache hands the same informer back for every GVK.
type syncCache struct {
	cache.Cache

	inf pendingInformer
}

func (c syncCache) GetInformer(_ context.Context, _ client.Object,
	_ ...cache.InformerGetOption,
) (cache.Informer, error) {
	return c.inf, nil
}

// newSyncingReconciler wires a reconciler whose informer starts unsynced. The
// GVK is pre-registered in watchStates so ctrl.Watch is never reached: this is
// about what the sync check does, not about watch registration.
func newSyncingReconciler(t *testing.T, pool *podpoolsv1alpha1.PodPool) (
	*PodPoolReconciler, *atomic.Bool, *clocktesting.FakePassiveClock,
) {
	t.Helper()

	synced := &atomic.Bool{}
	inf := pendingInformer{synced: synced}
	fake := clocktesting.NewFakePassiveClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	r, _ := newFakeReconciler(t, nil, pool)
	r.Clock = fake
	r.ctrl = &watchCounter{}
	r.Cache = syncCache{inf: inf}
	r.watchStates = map[schema.GroupVersionKind]cache.Informer{
		{Group: testAppsGroup, Version: "v1", Kind: testDepKind}: inf,
	}

	return r, synced, fake
}

// TestInformerStillSyncingIsNotAFailure pins the startup case.
//
// GetInformer is deliberately asked not to block, so on the first reconcile
// for any GVK the informer was created by that very call and cannot have
// synced. Treating that as a watch failure makes every manager start report a
// problem per workload kind that resolves milliseconds later, and back the
// pool off waiting for it.
func TestInformerStillSyncingIsNotAFailure(t *testing.T) {
	pool := fakeTestPool()

	r, _, _ := newSyncingReconciler(t, pool)

	res, err := r.Reconcile(t.Context(), reconcileRequestFor(pool))
	if err != nil {
		t.Errorf("Reconcile returned %v: an informer filling its initial cache is "+
			"a normal startup state, and returning an error puts the pool into "+
			"exponential backoff waiting for something that takes milliseconds", err)
	}

	if res.RequeueAfter <= 0 {
		t.Errorf("RequeueAfter = %v, want a short positive delay: nothing else will "+
			"wake this pool once the cache is warm", res.RequeueAfter)
	}
}

// TestInformerNeverSyncingIsReported is the assertion that stops the fix
// swallowing a genuine failure.
//
// A missing CRD is indistinguishable from a normal sync at any single
// instant: GetInformer succeeds, the informer exists, and its ListWatch fails
// silently against an unregistered resource. Only elapsed time separates
// them, which is why the grace window exists and why it has to expire.
func TestInformerNeverSyncingIsReported(t *testing.T) {
	pool := fakeTestPool()

	r, _, fake := newSyncingReconciler(t, pool)

	if _, err := r.Reconcile(t.Context(), reconcileRequestFor(pool)); err != nil {
		t.Fatalf("first pass should be quiet: %v", err)
	}

	fake.SetTime(fake.Now().Add(2 * watchSyncGrace))

	if _, err := r.Reconcile(t.Context(), reconcileRequestFor(pool)); err == nil {
		t.Error("an informer that never syncs was reported as fine; a missing CRD " +
			"would requeue quietly forever with nothing anywhere saying so")
	}
}

// TestGraceWindowStartsWhenTheGVKIsFirstSeen guards the obvious way to write
// the window wrong. Timing it from the pool, or from process start, expires
// it before a kind first named late in a manager's life has had any of it.
func TestGraceWindowStartsWhenTheGVKIsFirstSeen(t *testing.T) {
	pool := fakeTestPool()

	r, _, fake := newSyncingReconciler(t, pool)

	// Long before the workload kind is ever mentioned.
	fake.SetTime(fake.Now().Add(100 * watchSyncGrace))

	if _, err := r.Reconcile(t.Context(), reconcileRequestFor(pool)); err != nil {
		t.Errorf("Reconcile returned %v on this GVK's first sighting: the window "+
			"has to start when the informer does, not when the process did", err)
	}
}

// TestInformerSyncCompletesQuietly is the pairing the grace window exists to
// protect: the ordinary startup, where the second pass finds a warm cache and
// the pool proceeds as if nothing happened.
func TestInformerSyncCompletesQuietly(t *testing.T) {
	pool := fakeTestPool()

	r, synced, _ := newSyncingReconciler(t, pool)

	if _, err := r.Reconcile(t.Context(), reconcileRequestFor(pool)); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	synced.Store(true)

	res, err := r.Reconcile(t.Context(), reconcileRequestFor(pool))
	if err != nil {
		t.Fatalf("second pass with a warm cache: %v", err)
	}

	if res.RequeueAfter == watchSyncRequeue {
		t.Error("the pool is still on the fast sync requeue after the cache warmed; " +
			"a converged pool should be back on its normal interval")
	}

	if len(getPool(t, r.Client, pool).Status.Groups) == 0 {
		t.Error("no groups were reconciled, so the pass exited early on a watch " +
			"that is now perfectly healthy")
	}
}

// ---------------------------------------------------------------------------
// The watch-failure exit
// ---------------------------------------------------------------------------

// brokenWatchAfterHealthyPass builds the case that matters: a pool that ran
// normally and published a healthy status, whose workload kind then stopped
// being servable. A CRD uninstalled under a running pool, or RBAC revoked.
// The pool's stored status is the thing the failing exit has to contradict,
// so a pool that was never healthy would not exercise the bug at all.
func brokenWatchAfterHealthyPass(t *testing.T) (*PodPoolReconciler, *podpoolsv1alpha1.PodPool) {
	t.Helper()

	pool := fakeTestPool()
	r, synced, fake := newSyncingReconciler(t, pool)

	synced.Store(true)
	reconcilePool(t, r, pool)

	// The kind goes away. One quiet pass opens the grace window, then time
	// passes: the window only starts when the GVK is first seen unsynced, so
	// advancing the clock before that sighting would do nothing.
	synced.Store(false)
	reconcilePool(t, r, pool)
	fake.SetTime(fake.Now().Add(2 * watchSyncGrace))

	return r, pool
}

// TestWatchFailureLeavesAnHonestReadyCondition is the half of this that is
// not a filed bug and is the worse of the two.
//
// The exit never called setConditions, so patchStatus saw no diff and wrote
// nothing. The pool went on reporting the status of its last healthy pass:
// Ready=True, replica counts and all, while the controller could not see a
// single one of its children. An operator reading kubectl get podpool has no
// reason to look further.
func TestWatchFailureLeavesAnHonestReadyCondition(t *testing.T) {
	r, pool := brokenWatchAfterHealthyPass(t)

	before := getPool(t, r.Client, pool)
	if ready := meta.FindStatusCondition(before.Status.Conditions, ConditionReady); ready == nil {
		t.Fatal("fixture never published a Ready condition, so there is nothing stale to contradict")
	}

	_ = tryReconcilePool(r, pool)

	got := getPool(t, r.Client, pool)

	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("no Ready condition after a watch failure")
	}

	if ready.Reason != ReasonWatchSetupFailed {
		t.Errorf("Ready reason is %q, want %q: nothing in the object says why the "+
			"pool is not running", ready.Reason, ReasonWatchSetupFailed)
	}
}

// TestWatchFailureDoesNotObserveTheGeneration applies the paused-pool rule to
// this exit. The obvious way to write the branch is to copy the code below
// it, which stamps; the pool would then report an edit it never acted on as
// reconciled, and anything gating on observedGeneration == generation would
// read a blind pool as settled.
func TestWatchFailureDoesNotObserveTheGeneration(t *testing.T) {
	r, pool := brokenWatchAfterHealthyPass(t)

	live := getPool(t, r.Client, pool)
	live.Generation = pool.Generation + 1
	live.Spec.Replicas = 7

	if err := r.Update(t.Context(), live); err != nil {
		t.Fatalf("editing the spec: %v", err)
	}

	_ = tryReconcilePool(r, pool)

	got := getPool(t, r.Client, pool)
	if got.Status.ObservedGeneration == got.Generation {
		t.Errorf("observedGeneration = %d at generation %d: no informer means "+
			"nothing below the exit ran, so this spec has not been acted on",
			got.Status.ObservedGeneration, got.Generation)
	}
}

// TestEveryEarlyExitWritesReady is a class guard rather than a bug
// reproducer. Every exit leaving a Ready condition is what makes the fix
// above work, and a third early return that forgets would silently reopen it.
// Reading the code will not catch that; this will.
func TestEveryEarlyExitWritesReady(t *testing.T) {
	tests := []struct {
		name       string
		wantReason string
		build      func(*testing.T) (*PodPoolReconciler, *podpoolsv1alpha1.PodPool)
	}{
		{
			name: "unparseable workload template",
			// Ready aggregates rather than naming the fault; GroupsReady is
			// where the invalid template is reported. What matters here is
			// that this exit writes Ready at all.
			wantReason: ReasonNoReplicasAvailable,
			build: func(t *testing.T) (*PodPoolReconciler, *podpoolsv1alpha1.PodPool) {
				t.Helper()

				pool := fakeTestPool()
				pool.Spec.WorkloadTemplate.Raw = []byte(`{"spec":{"template":{}}}`)
				r, _ := newFakeReconciler(t, nil, pool)

				return r, pool
			},
		},
		{
			name:       "no informer for the workload type",
			wantReason: ReasonWatchSetupFailed,
			build:      brokenWatchAfterHealthyPass,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, pool := tt.build(t)

			_ = tryReconcilePool(r, pool)

			got := getPool(t, r.Client, pool)

			ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
			if ready == nil {
				t.Fatal("the exit left no Ready condition at all")
			}

			if ready.Reason != tt.wantReason {
				t.Errorf("Ready reason is %q, want %q", ready.Reason, tt.wantReason)
			}
		})
	}
}
