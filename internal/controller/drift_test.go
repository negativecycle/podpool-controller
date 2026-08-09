package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// getChild reads the child workload the base group renders to.
func getChild(t *testing.T, cl client.Client, poolName string) *appsv1.Deployment {
	t.Helper()

	var dep appsv1.Deployment

	key := types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: testNamespace}
	if err := cl.Get(t.Context(), key, &dep); err != nil {
		t.Fatalf("getting child %s: %v", key.Name, err)
	}

	return &dep
}

// TestReconcileRevertsChildSpecDrift pins the property that the controller owns
// its children continuously, not just at the moment they are created.
//
// No change-detection scheme the controller could run against its own last
// write can see an external mutation: the render is the same and the last
// write is the same. Server-side apply repairs the rendered fields regardless,
// because ownership is tracked field by field on the server.
func TestReconcileRevertsChildSpecDrift(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, pool)

	reconcilePool(t, r, pool)

	before := getChild(t, cl, pool.Name)
	wantReplicas := *before.Spec.Replicas
	wantImage := before.Spec.Template.Spec.Containers[0].Image

	// Stand in for anything that writes to the child behind the controller's
	// back: an HPA pointed at the Deployment instead of the pool, a kubectl
	// edit, another operator.
	drifted := before.DeepCopy()
	drifted.Spec.Replicas = ptr.To[int32](99)

	drifted.Spec.Template.Spec.Containers[0].Image = "nginx:tampered"
	if err := cl.Update(t.Context(), drifted); err != nil {
		t.Fatalf("applying external drift: %v", err)
	}

	reconcilePool(t, r, pool)

	after := getChild(t, cl, pool.Name)
	if got := *after.Spec.Replicas; got != wantReplicas {
		t.Errorf("replica drift not reverted: got %d, want %d", got, wantReplicas)
	}

	if got := after.Spec.Template.Spec.Containers[0].Image; got != wantImage {
		t.Errorf("image drift not reverted: got %q, want %q", got, wantImage)
	}
}

// TestReconcileIsStableWithoutDrift is the other half: repeated reconciles of a
// converged pool must keep rendering the same child.
//
// This asserts on content, not on resourceVersion. Whether a converged apply is
// a genuine no-op is the property that matters, and it cannot be observed here:
// the fake client neither tracks managedFields nor models server-side apply
// merging, so it bumps resourceVersion on every apply even when the object is
// byte-identical. That assertion lives in the envtest suite, against a real API
// server — see "does not rewrite a converged child" in drift_envtest_test.go.
func TestReconcileIsStableWithoutDrift(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, pool)

	reconcilePool(t, r, pool)
	first := getChild(t, cl, pool.Name)

	reconcilePool(t, r, pool)
	second := getChild(t, cl, pool.Name)

	if !equality.Semantic.DeepEqual(first.Spec, second.Spec) {
		t.Errorf("converged pool re-rendered a different child spec:\nfirst:  %+v\nsecond: %+v",
			first.Spec, second.Spec)
	}
}
