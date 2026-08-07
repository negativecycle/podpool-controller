package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The prune list is empty until the first real rename, so these tests inject
// a retired entry to prove the mechanism, and restore the list afterwards.

func withRetiredType(t *testing.T, retired string) {
	t.Helper()

	saved := retiredConditionTypes
	retiredConditionTypes = append([]string{}, saved...)
	retiredConditionTypes = append(retiredConditionTypes, retired)

	t.Cleanup(func() { retiredConditionTypes = saved })
}

// TestRetiredConditionIsPruned is the mechanism: a stored pool carrying a
// type this controller no longer publishes loses it on the next write pass,
// because SetStatusCondition can only upsert and nothing else in the write
// path can express a deletion.
func TestRetiredConditionIsPruned(t *testing.T) {
	withRetiredType(t, "AncientCondition")

	pool := fakeTestPool()
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:   "AncientCondition",
		Status: metav1.ConditionTrue,
		Reason: "LeftOverFromAnOlderVersion",
	})

	setConditions(pool, conditionInputs{desired: 3})

	if meta.FindStatusCondition(pool.Status.Conditions, "AncientCondition") != nil {
		t.Error("retired condition type survived the write pass; a stored pool would carry it forever")
	}
}

// TestForeignConditionIsNotPruned guards the deliberate shape of the list: our
// own retired names, never "anything we do not publish". The conditions array
// is a shared contract, and other actors write to it.
func TestForeignConditionIsNotPruned(t *testing.T) {
	withRetiredType(t, "AncientCondition")

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
