package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/negativecycle/podpool-controller/internal/workload"
)

// The prune list is empty until the first real rename, so these tests inject
// a retired entry to prove the mechanism, and restore the list afterwards.

const (
	testRetiredType   = "AncientCondition"
	testRetiredReason = "LeftOverFromAnOlderVersion"
)

func withRetiredType(t *testing.T) {
	t.Helper()

	saved := retiredConditionTypes
	retiredConditionTypes = append([]string{}, saved...)
	retiredConditionTypes = append(retiredConditionTypes, testRetiredType)

	t.Cleanup(func() { retiredConditionTypes = saved })
}

// TestRetiredConditionIsPruned is the mechanism: a stored pool carrying a
// type this controller no longer publishes loses it on the next write pass,
// because SetStatusCondition can only upsert and nothing else in the write
// path can express a deletion.
func TestRetiredConditionIsPruned(t *testing.T) {
	withRetiredType(t)

	pool := fakeTestPool()
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:   testRetiredType,
		Status: metav1.ConditionTrue,
		Reason: testRetiredReason,
	})

	setConditions(pool, conditionInputs{desired: 3})

	if meta.FindStatusCondition(pool.Status.Conditions, testRetiredType) != nil {
		t.Error("retired condition type survived the write pass; a stored pool would carry it forever")
	}
}

// TestForeignConditionIsNotPruned guards the deliberate shape of the list: our
// own retired names, never "anything we do not publish". The conditions array
// is a shared contract, and other actors write to it.
func TestForeignConditionIsNotPruned(t *testing.T) {
	withRetiredType(t)

	pool := fakeTestPool()
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:   "vendor.io/Attested",
		Status: metav1.ConditionTrue,
		Reason: "WrittenBySomeoneElse",
	})

	setConditions(pool, conditionInputs{desired: 3})

	if meta.FindStatusCondition(pool.Status.Conditions, "vendor.io/Attested") == nil {
		t.Error("a foreign condition was deleted; pruning must be a list of our own retired names, " +
			"never an allow-list of what we publish")
	}
}

// A paused pool is exactly the one most likely to sit untouched for a long
// time carrying a stale type, so the pause path must prune too. This is
// end-to-end on purpose: it holds only if the paused exit goes through
// setConditions -- a hand-written pair of condition writes in Reconcile
// passes every other pause test while leaving retired types in place forever.
func TestRetiredConditionIsPrunedWhilePaused(t *testing.T) {
	withRetiredType(t)

	pool := fakeTestPool()
	pool.Annotations = map[string]string{workload.AnnotationPaused: valueTrue}
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:   testRetiredType,
		Status: metav1.ConditionTrue,
		Reason: testRetiredReason,
	})

	r, cl := newFakeReconciler(t, nil, pool)
	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)
	if meta.FindStatusCondition(got.Status.Conditions, testRetiredType) != nil {
		t.Error("a paused pool kept a retired condition type; the pause exit is " +
			"not writing conditions through setConditions")
	}
}

// The pause path inherits the foreign-condition guarantee along with the
// prune: stopping work must not start deleting other actors' conditions.
func TestForeignConditionSurvivesAPause(t *testing.T) {
	withRetiredType(t)

	pool := fakeTestPool()
	pool.Annotations = map[string]string{workload.AnnotationPaused: valueTrue}
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:   "vendor.io/Attested",
		Status: metav1.ConditionTrue,
		Reason: "WrittenBySomeoneElse",
	})

	r, cl := newFakeReconciler(t, nil, pool)
	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)
	if meta.FindStatusCondition(got.Status.Conditions, "vendor.io/Attested") == nil {
		t.Error("a foreign condition was deleted on the paused path")
	}
}
