package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestStatusPatchSkippedWhenUnchanged pins the only-when-changed half: a
// converged pool reconciled again must issue no status write at all.
// Unconditional writes round-trip on every pass, and the controller's own
// write event wakes it again, a quiet self-sustaining loop.
func TestStatusPatchSkippedWhenUnchanged(t *testing.T) {
	counter := &statusPatches{}
	pool := fakeTestPool()
	r, _ := newFakeReconciler(t, counter, pool)

	reconcilePool(t, r, pool)

	first := counter.n
	if first == 0 {
		t.Fatal("first pass issued no status patch; nothing was published")
	}

	reconcilePool(t, r, pool)

	if counter.n != first {
		t.Errorf("a converged pool issued %d further status patches, want 0", counter.n-first)
	}
}

// TestStatusPatchPreservesLastTransitionTime is why once-and-only-when-changed
// matters beyond efficiency: rewriting identical conditions on every pass
// resets their LastTransitionTime, destroying the one field that says how long
// a state has held.
func TestStatusPatchPreservesLastTransitionTime(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	before := conditionByType(getPool(t, cl, pool), ConditionGroupsReady)
	if before == nil {
		t.Fatal("GroupsReady missing after first pass")
	}

	reconcilePool(t, r, pool)

	after := conditionByType(getPool(t, cl, pool), ConditionGroupsReady)
	if after == nil || !after.LastTransitionTime.Equal(&before.LastTransitionTime) {
		t.Errorf("LastTransitionTime moved from %v to %v across a no-op pass",
			before.LastTransitionTime, after.LastTransitionTime)
	}
}

// The deferred patch aggregates its own failure into the returned error. That
// wiring is easy to get wrong with named returns, so pin it.
func TestReconcileSurfacesStatusPatchError(t *testing.T) {
	pool := fakeTestPool()
	r, _ := newFakeReconciler(t, nil, pool)

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	patchErr := errors.New("status patch exploded")
	failing := interceptor.NewClient(base, interceptor.Funcs{
		SubResourcePatch: func(_ context.Context, _ client.Client, _ string,
			_ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption,
		) error {
			return patchErr
		},
	})
	r.Client = failing

	err := tryReconcilePool(r, pool)
	if err == nil {
		t.Fatal("Reconcile returned nil despite the status patch failing")
	}

	if !strings.Contains(err.Error(), patchErr.Error()) {
		t.Errorf("error %q does not carry the patch failure", err)
	}
}
