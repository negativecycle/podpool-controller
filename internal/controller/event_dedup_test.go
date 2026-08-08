package controller

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Every assertion in this file counts events across *several* reconciles,
// because one reconcile cannot distinguish "emits on transition" from "emits
// every pass". The pool is reconciled on a requeue floor whether or not anyone
// touches it, so an ungated warning is not a small excess: it is one event per
// interval, per pool, for as long as the fault lasts.

// failingCache satisfies cache.Cache but returns an error from GetInformer.
// Only safe when ensureWatch is the sole cache consumer on the code path —
// the embedded nil panics on any other method.
type failingCache struct {
	cache.Cache
}

func (f *failingCache) GetInformer(_ context.Context, _ client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	return nil, errors.New("simulated informer failure")
}

// alwaysInvalidApply makes every child apply fail the same way on every pass,
// which is the shape a real broken pool has: unchanging, and reconciled again
// and again.
func alwaysInvalidApply(t *testing.T, r *PodPoolReconciler) {
	t.Helper()

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(_ context.Context, _ client.WithWatch, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
			return apierrors.NewInvalid(
				schema.GroupKind{Group: testAppsGroup, Kind: testDepKind},
				"pool-base", nil)
		},
	})
}

// A steady group failure must emit an event on the first reconcile (the
// transition from no condition to GroupsReady=False) and suppress all
// subsequent events while the condition tuple is unchanged.
func TestGroupFailureEmitsOnlyOnTransition(t *testing.T) {
	pool := singleGroupPool()
	rec := events.NewFakeRecorder(64)
	r, _ := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	alwaysInvalidApply(t, r)

	_ = tryReconcilePool(r, pool)
	_ = tryReconcilePool(r, pool)

	evts := drainEvents(rec.Events)

	count := countEventsByReason(evts, ReasonGroupReconcileFailed)
	if count != 1 {
		t.Errorf("got %d %s events across 2 reconciles, want 1 (emit on transition only); events: %v",
			count, ReasonGroupReconcileFailed, evts)
	}
}

// A group that recovers does not make its still-failing neighbour news again.
//
// This test used to assert the opposite, because the gate used to be
// pool-level: a shrinking failing set changed the GroupsReady message, which
// counted as a tuple change and flushed every buffered event including the ones
// belonging to groups that had not moved. The gate is now per group, so base
// announces once and stays quiet until its own failure class changes. Spot's
// recovery is not announced either; there is no recovery event.
func TestRecoveredNeighbourDoesNotReAnnounceAFailingGroup(t *testing.T) {
	pool := fakeTestPool()
	rec := events.NewFakeRecorder(64)
	r, _ := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	applyCount := 0
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, ac runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			applyCount++
			// Reconcile 1: calls 1,2 (base,spot) both fail.
			// Reconcile 2: call 3 (base) fails, call 4 (spot) succeeds.
			if applyCount <= 3 {
				return apierrors.NewInvalid(
					schema.GroupKind{Group: testAppsGroup, Kind: testDepKind},
					"x", nil)
			}

			return c.Apply(ctx, ac, opts...)
		},
	})

	// First reconcile: both groups fail, one event each.
	_ = tryReconcilePool(r, pool)
	firstEvts := drainEvents(rec.Events)

	firstCount := countEventsByReason(firstEvts, ReasonGroupReconcileFailed)
	if firstCount != 2 {
		t.Errorf("first reconcile: got %d %s events, want 2 (one per failing group); events: %v",
			firstCount, ReasonGroupReconcileFailed, firstEvts)
	}

	// Second reconcile: spot recovers, base fails exactly as before. The
	// pool-level message changes, but nothing about base did.
	_ = tryReconcilePool(r, pool)
	secondEvts := drainEvents(rec.Events)

	secondCount := countEventsByReason(secondEvts, ReasonGroupReconcileFailed)
	if secondCount != 0 {
		t.Errorf("second reconcile: got %d %s events, want 0 — base's failure is unchanged and "+
			"a neighbour recovering is not news about base; events: %v",
			secondCount, ReasonGroupReconcileFailed, secondEvts)
	}
}

// The two events that record something having *happened to the cluster*, as
// opposed to something being wrong. Neither is gated on a status diff, because
// neither describes a state: a creation and a deletion each happen once and
// obs.created / the delete call are already edge-triggered.
//
// The deletion is the one that has to be there. It is the only user-visible
// record that this controller removed a running workload, and the log line it
// sits beside is not visible to whoever is looking at the pool.
func TestChildLifecycleEventsAreEmitted(t *testing.T) {
	pool := fakeTestPool()
	rec := events.NewFakeRecorder(64)
	r, cl := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	reconcilePool(t, r, pool)

	created := drainEvents(rec.Events)
	if n := countEventsByReason(created, "ChildCreated"); n != len(pool.Spec.Groups) {
		t.Errorf("got %d ChildCreated events for %d groups, want %d; events: %v",
			n, len(pool.Spec.Groups), len(pool.Spec.Groups), created)
	}

	// Second pass: the children already exist, so nothing was created.
	reconcilePool(t, r, pool)

	if n := countEventsByReason(drainEvents(rec.Events), "ChildCreated"); n != 0 {
		t.Errorf("got %d ChildCreated events on a pass that created nothing, want 0", n)
	}

	// Drop a group so the sweep deletes its child.
	live := getPool(t, cl, pool)

	live.Spec.Groups = live.Spec.Groups[:1]
	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("dropping a group: %v", err)
	}

	reconcilePool(t, r, live)

	swept := drainEvents(rec.Events)
	if n := countEventsByReason(swept, "OrphanDeleted"); n != 1 {
		t.Errorf("got %d OrphanDeleted events, want 1 — deleting a running workload with no "+
			"event leaves the operator with nothing to find; events: %v", n, swept)
	}
}

// An ownership conflict is a different kind of failure from a broken apply and
// says so in the event, not only in the condition. The two travel together
// because one classifier decides both, and this pins the event half: it is the
// message that tells an operator another controller owns the name, which the
// generic "failed to reconcile" wording actively hides.
func TestNotOwnedGroupEmitsItsOwnReason(t *testing.T) {
	pool := fakeTestPool()
	dep := foreignDeployment(pool.Name+"-"+testGroupBase, testNamespace)

	rec := events.NewFakeRecorder(64)
	r, _ := newFakeReconciler(t, nil, pool, dep)
	r.Recorder = rec

	_ = tryReconcilePool(r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, ReasonWorkloadNotOwned); n != 1 {
		t.Errorf("got %d %s events, want 1; events: %v", n, ReasonWorkloadNotOwned, evts)
	}

	if n := countEventsByReason(evts, ReasonGroupReconcileFailed); n != 0 {
		t.Errorf("got %d %s events for an ownership conflict, want 0 — the generic reason "+
			"hides the one fact that matters; events: %v", n, ReasonGroupReconcileFailed, evts)
	}
}

// A malformed workloadTemplate emits one WorkloadTemplateInvalid event on the
// transition into the error state and stays silent on subsequent reconciles.
func TestBadGVKEmitsOnce(t *testing.T) {
	pool := fakeTestPool()
	pool.Spec.WorkloadTemplate.Raw = []byte(`{"not":"valid"}`)

	rec := events.NewFakeRecorder(64)
	r, _ := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	_ = tryReconcilePool(r, pool)
	_ = tryReconcilePool(r, pool)

	evts := drainEvents(rec.Events)

	count := countEventsByReason(evts, ReasonWorkloadTemplateInvalid)
	if count != 1 {
		t.Errorf("got %d %s events across 2 reconciles, want 1; events: %v",
			count, ReasonWorkloadTemplateInvalid, evts)
	}
}

// fail → recover → fail must produce two emissions: one on the initial
// transition and one when the recovered condition transitions back to failing.
// A gate that latched on first emission would give one.
func TestRecoveryThenRefailEmitsTwice(t *testing.T) {
	pool := fakeTestPool()
	breakGroup(pool)

	rec := events.NewFakeRecorder(64)
	r, cl := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	// Phase 1: base group broken → GroupsReady nil→False → event.
	_ = tryReconcilePool(r, pool)

	// Phase 2: fix → GroupsReady False→True (no pending events to flush).
	live := getPool(t, cl, pool)

	live.Spec.Groups[0].Overrides = nil
	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("fixing pool: %v", err)
	}

	reconcilePool(t, r, live)

	// Phase 3: break again → GroupsReady True→False → event.
	live2 := getPool(t, cl, pool)
	breakGroup(live2)

	if err := cl.Update(t.Context(), live2); err != nil {
		t.Fatalf("re-breaking pool: %v", err)
	}

	_ = tryReconcilePool(r, live2)

	evts := drainEvents(rec.Events)

	count := countEventsByReason(evts, ReasonGroupReconcileFailed)
	if count != 2 {
		t.Errorf("got %d %s events across fail→recover→fail, want 2; events: %v",
			count, ReasonGroupReconcileFailed, evts)
	}
}

// Watch-failure events are gated per GVK by a process-lifetime map rather than
// by a status diff: the first failure emits, repeats are silent, and a fresh
// outage after recovery emits again. Nothing about a failed watch reaches
// status, so there is no diff to gate on.
func TestWatchFailureEmitsOncePerOutage(t *testing.T) {
	pool := fakeTestPool()
	rec := events.NewFakeRecorder(64)
	r, _ := newFakeReconciler(t, nil, pool)
	r.Recorder = rec
	r.ctrl = &watchCounter{}
	r.Cache = &failingCache{}

	gvk := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

	if err := tryReconcilePool(r, pool); err == nil {
		t.Fatal("expected error from watch failure")
	}

	if err := tryReconcilePool(r, pool); err == nil {
		t.Fatal("expected error from watch failure")
	}

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, ReasonWatchSetupFailed); n != 1 {
		t.Errorf("got %d WatchSetupFailed events from 2 failures, want 1; events: %v", n, evts)
	}

	// Simulate recovery: ensureWatch clears the entry on success.
	r.watchMu.Lock()
	delete(r.watchFailureEmitted, gvk)
	r.watchMu.Unlock()

	if err := tryReconcilePool(r, pool); err == nil {
		t.Fatal("expected error from watch failure")
	}

	evts2 := drainEvents(rec.Events)
	if n := countEventsByReason(evts2, ReasonWatchSetupFailed); n != 1 {
		t.Errorf("got %d WatchSetupFailed events after recovery+refail, want 1; events: %v", n, evts2)
	}
}
