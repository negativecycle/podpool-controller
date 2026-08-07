package controller

import (
	"testing"
)

// A pool scaled from two groups to one leaves the second group's child
// running unless the sweep deletes it; and the survivor must be untouched.
func TestSweepDeletesChildOfRemovedGroup(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, pool)

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
