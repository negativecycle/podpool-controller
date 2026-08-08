package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/negativecycle/podpool-controller/internal/workload"
)

// The claim under test is a negative -- "a paused pool writes no children" --
// so the interceptor fails the test from inside any Apply rather than
// asserting on state afterwards, which a no-op reconcile would also satisfy.
func TestReconcilePausedSkipsChildren(t *testing.T) {
	pool := fakeTestPool()
	pool.Annotations = map[string]string{workload.AnnotationPaused: ""}

	r, _ := newFakeReconciler(t, nil, pool)

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(_ context.Context, _ client.WithWatch, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
			t.Error("Apply called: a paused pool must not write children")

			return nil
		},
	})

	res, err := r.Reconcile(t.Context(), reconcileRequestFor(pool))
	if err != nil {
		t.Fatalf("Reconcile returned %v, want nil", err)
	}

	if res != (ctrl.Result{}) {
		t.Errorf("Reconcile returned %+v, want empty result (no requeue while paused)", res)
	}
}

func TestReconcilePausedSetsConditions(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)

	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	if ready == nil || ready.Reason == ReasonPaused {
		t.Fatal("pool should not be paused before annotation is set")
	}

	got.Annotations = map[string]string{workload.AnnotationPaused: ""}

	if err := cl.Update(t.Context(), got); err != nil {
		t.Fatalf("adding paused annotation: %v", err)
	}

	reconcilePool(t, r, pool)

	got = getPool(t, cl, pool)

	progressing := meta.FindStatusCondition(got.Status.Conditions, ConditionProgressing)
	if progressing == nil {
		t.Fatal("Progressing condition missing")
	}

	if progressing.Status != metav1.ConditionFalse || progressing.Reason != ReasonPaused {
		t.Errorf("Progressing = %s/%s, want False/Paused", progressing.Status, progressing.Reason)
	}

	ready = meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("Ready condition missing")
	}

	if ready.Status != metav1.ConditionFalse || ready.Reason != ReasonPaused {
		t.Errorf("Ready = %s/%s, want False/Paused", ready.Status, ready.Reason)
	}
}

// Pausing must freeze status, not blank it. The replica counts and group rows
// written by the last healthy pass are still the truth about the cluster --
// the children keep running -- and wiping them would turn "stop acting" into
// "stop reporting".
func TestReconcilePausedCarriesForwardStatus(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	for _, group := range []string{testGroupBase, testGroupSpot} {
		var dep appsv1.Deployment

		key := types.NamespacedName{Name: pool.Name + "-" + group, Namespace: pool.Namespace}
		if err := cl.Get(t.Context(), key, &dep); err != nil {
			t.Fatalf("getting child %s: %v", group, err)
		}

		dep.Status.Replicas = *dep.Spec.Replicas
		dep.Status.ReadyReplicas = *dep.Spec.Replicas

		if err := cl.Status().Update(t.Context(), &dep); err != nil {
			t.Fatalf("updating child %s: %v", group, err)
		}
	}

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)
	if got.Status.Replicas != 3 || got.Status.ReadyReplicas != 3 {
		t.Fatalf("pre-pause: replicas=%d ready=%d, want 3/3",
			got.Status.Replicas, got.Status.ReadyReplicas)
	}

	got.Annotations = map[string]string{workload.AnnotationPaused: ""}
	if err := cl.Update(t.Context(), got); err != nil {
		t.Fatalf("adding paused annotation: %v", err)
	}

	reconcilePool(t, r, pool)

	got = getPool(t, cl, pool)
	if got.Status.Replicas != 3 || got.Status.ReadyReplicas != 3 {
		t.Errorf("paused: replicas=%d ready=%d, want 3/3 (carried forward)",
			got.Status.Replicas, got.Status.ReadyReplicas)
	}

	if len(got.Status.Groups) != 2 {
		t.Errorf("paused: %d group statuses, want 2 (carried forward)", len(got.Status.Groups))
	}
}

func TestReconcileResumesAfterUnpause(t *testing.T) {
	pool := fakeTestPool()
	pool.Annotations = map[string]string{workload.AnnotationPaused: ""}
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)

	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	if ready == nil || ready.Reason != ReasonPaused {
		t.Fatal("pool should be paused")
	}

	delete(got.Annotations, workload.AnnotationPaused)

	if err := cl.Update(t.Context(), got); err != nil {
		t.Fatalf("removing paused annotation: %v", err)
	}

	reconcilePool(t, r, pool)

	got = getPool(t, cl, pool)

	ready = meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	if ready == nil {
		t.Fatal("Ready condition missing after unpause")
	}

	if ready.Reason == ReasonPaused {
		t.Error("Ready still shows Paused after annotation was removed")
	}
}

func TestReconcilePausedNoRequeueOnChildChange(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)
	got.Annotations = map[string]string{workload.AnnotationPaused: ""}

	if err := cl.Update(t.Context(), got); err != nil {
		t.Fatalf("adding paused annotation: %v", err)
	}

	reconcilePool(t, r, pool)
	reconcilePool(t, r, pool)
	reconcilePool(t, r, pool)

	got = getPool(t, cl, pool)

	ready := meta.FindStatusCondition(got.Status.Conditions, ConditionReady)
	if ready == nil || ready.Reason != ReasonPaused {
		t.Error("pool should remain paused across multiple reconciles")
	}
}
