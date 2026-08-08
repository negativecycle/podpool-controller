package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// Reconcile has three exits above its main body: paused, an unparseable
// workload template, and a watch that will not come up. The full path
// maintains invariants that none of them does, so tests here name the
// invariant rather than the exit: the next early return added will break the
// same ones.

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
// The scale subresource's selector
// ---------------------------------------------------------------------------

// The CRD points the scale subresource's selectorpath at status.selector, and
// the assignment sits above every early return. A pool created paused, or
// created with a template the controller cannot parse, has never run the full
// path, so it would otherwise expose an empty selector to anything reading
// /scale. The field is derived from the pool name alone, so no early path
// lacks anything it needs.
func TestSelectorIsSetOnEveryExit(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*podpoolsv1alpha1.PodPool)
	}{
		{
			name: "created paused",
			mut: func(pp *podpoolsv1alpha1.PodPool) {
				pp.Annotations = map[string]string{workload.AnnotationPaused: valueTrue}
			},
		},
		{
			name: "created with an unparseable template",
			mut: func(pp *podpoolsv1alpha1.PodPool) {
				pp.Spec.WorkloadTemplate.Raw = []byte(`{"spec":{"template":{}}}`)
			},
		},
		{
			name: "the ordinary path",
			// Green by construction: the point of the shared assignment is
			// that the full path and the exits cannot drift apart.
			mut: func(*podpoolsv1alpha1.PodPool) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := fakeTestPool()
			tt.mut(pool)

			r, cl := newFakeReconciler(t, nil, pool)

			// Errors are beside the point: an unparseable template is a
			// non-error early return, and the selector must survive either way.
			_ = tryReconcilePool(r, pool)

			got := getPool(t, cl, pool)

			want := labels.Set{workload.LabelPool: pool.Name}.String()
			if got.Status.Selector != want {
				t.Errorf("status.selector = %q, want %q: /scale reports this to "+
					"an HPA, which cannot find pods without it", got.Status.Selector, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The pause annotation's value
// ---------------------------------------------------------------------------

// Presence-only means podpools.dev/paused: "false" pauses the pool. That is a
// real trap for GitOps templating: a chart rendering `paused: {{ .Values.paused
// }}` produces the literal string false and silently freezes reconciliation
// while the manifest says the opposite.
//
// Unparseable values pause deliberately. An operator who typed something
// meaning to pause gets a pause rather than a silently running pool.
func TestIsPausedHonoursTheAnnotationValue(t *testing.T) {
	tests := []struct {
		value string
		want  bool
		why   string
	}{
		{value: "\x00absent", want: false, why: "no annotation at all"},
		{value: valueTrue, want: true},
		{value: "TRUE", want: true},
		{value: "1", want: true},
		{value: "t", want: true},
		{value: valueFalse, want: false, why: "the templating trap this rule exists for"},
		{value: "FALSE", want: false},
		{value: "0", want: false},
		{value: "f", want: false},
		{value: "", want: true, why: "unparseable, and the value people write to mean 'just pause'"},
		{value: "on", want: true, why: "unparseable: do not guess, and do not silently run"},
		{value: "paused", want: true, why: "unparseable"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			pool := fakeTestPool()
			if tt.value != "\x00absent" {
				pool.Annotations = map[string]string{workload.AnnotationPaused: tt.value}
			}

			if got := isPaused(pool); got != tt.want {
				t.Errorf("isPaused(%q) = %v, want %v (%s)", tt.value, got, tt.want, tt.why)
			}
		})
	}
}

// The same claim end to end, because isPaused could be right while Reconcile
// consults the annotation map directly.
func TestPausedFalseReconcilesNormally(t *testing.T) {
	pool := fakeTestPool()
	pool.Annotations = map[string]string{workload.AnnotationPaused: valueFalse}

	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)

	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("no Ready condition")
	}

	if ready.Reason == ReasonPaused {
		t.Error("a pool annotated paused=false is paused; a GitOps template " +
			"rendering a boolean freezes the pool while the manifest says it should run")
	}

	if len(got.Status.Groups) == 0 {
		t.Error("no groups were reconciled, so the pool did not actually run")
	}
}

// ---------------------------------------------------------------------------
// Metrics on every path that mutates conditions
// ---------------------------------------------------------------------------

// The pause exit mutates conditions and returns above the whole group loop.
// Publishing metrics from the status defer -- derived from pool.Status, not
// from reconcile-local aggregates -- is what makes these two pass by
// construction rather than by anyone remembering the new exit. In the real
// history this was a bug found after pause shipped: a dashboard showed a
// paused pool still progressing.
func TestPausedPoolPublishesItsMetrics(t *testing.T) {
	const ns, name = "metrics-pause-ns", "metrics-pause-pool"

	t.Cleanup(func() { deletePoolMetrics(ns, name) })

	pool := fakeTestPool()
	pool.Namespace = ns
	pool.Name = name

	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	if v := conditionSeries(t, ns, name)[ConditionProgressing+"/true"]; v != 1 {
		t.Fatalf("Progressing=true gauge is %v before the pause, want 1", v)
	}

	live := getPool(t, cl, pool)
	live.Annotations = map[string]string{workload.AnnotationPaused: valueTrue}

	if err := r.Update(t.Context(), live); err != nil {
		t.Fatalf("pausing: %v", err)
	}

	reconcilePool(t, r, pool)

	series := conditionSeries(t, ns, name)

	if v := series[ConditionProgressing+"/true"]; v != 0 {
		t.Errorf("Progressing=true gauge is %v after the pause, want 0: the metric "+
			"still reports the pool as making progress", v)
	}

	if v := series[ConditionProgressing+"/false"]; v != 1 {
		t.Errorf("Progressing=false gauge is %v after the pause, want 1", v)
	}
}

// The sharper case: a pool that has never run the full path has no series
// whatsoever, so it is invisible to monitoring rather than merely stale.
func TestPoolCreatedPausedPublishesMetricsAtAll(t *testing.T) {
	const ns, name = "metrics-born-paused-ns", "metrics-born-paused-pool"

	t.Cleanup(func() { deletePoolMetrics(ns, name) })

	pool := fakeTestPool()
	pool.Namespace = ns
	pool.Name = name
	pool.Annotations = map[string]string{workload.AnnotationPaused: valueTrue}

	r, _ := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	if n := seriesFor(specReplicas, ns, name); n == 0 {
		t.Error("a pool created paused publishes no metrics at all; it cannot be " +
			"alerted on, and its absence is indistinguishable from not existing")
	}

	if v := conditionSeries(t, ns, name)[ConditionReady+"/false"]; v != 1 {
		t.Errorf("Ready=false gauge is %v, want 1", v)
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
// above work, and a fourth early return that forgets would silently reopen it.
// Reading the code will not catch that; this will.
func TestEveryEarlyExitWritesReady(t *testing.T) {
	tests := []struct {
		name       string
		wantReason string
		build      func(*testing.T) (*PodPoolReconciler, *podpoolsv1alpha1.PodPool)
	}{
		{
			name:       "paused",
			wantReason: ReasonPaused,
			build: func(t *testing.T) (*PodPoolReconciler, *podpoolsv1alpha1.PodPool) {
				t.Helper()

				pool := fakeTestPool()
				pool.Annotations = map[string]string{workload.AnnotationPaused: valueTrue}
				r, _ := newFakeReconciler(t, nil, pool)

				return r, pool
			},
		},
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
