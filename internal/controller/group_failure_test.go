package controller

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// breakGroup makes one group unbuildable, the way a user would: a null in the
// overrides deletes the key it targets, and deleting .spec.template leaves
// nothing to build a workload from. The JSON stays valid, so the failure lands
// in BuildChildWorkload rather than in serialization.
func breakGroup(pool *podpoolsv1alpha1.PodPool) {
	for i := range pool.Spec.Groups {
		if pool.Spec.Groups[i].Name == testGroupBase {
			pool.Spec.Groups[i].Overrides = &runtime.RawExtension{
				Raw: []byte(`{"spec":{"template":null}}`),
			}

			return
		}
	}
}

func childExists(t *testing.T, cl client.Client, pool *podpoolsv1alpha1.PodPool, group string) bool {
	t.Helper()

	var dep appsv1.Deployment

	key := types.NamespacedName{Name: pool.Name + "-" + group, Namespace: pool.Namespace}

	err := cl.Get(t.Context(), key, &dep)
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("getting child for group %s: %v", group, err)
	}

	return err == nil
}

// A group that cannot be reconciled used to abort the whole loop, so every
// later group stopped being reconciled: one bad group froze the pool. Failures
// are collected instead, later groups proceed, and the aggregate comes back
// naming each failed group.
func TestReconcileContinuesPastFailingGroup(t *testing.T) {
	pool := fakeTestPool()
	breakGroup(pool)

	r, cl := newFakeReconciler(t, pool)

	err := tryReconcilePool(r, pool)
	if err == nil {
		t.Fatal("Reconcile swallowed the group failure")
	}

	if !strings.Contains(err.Error(), testGroupBase) {
		t.Errorf("error %q does not name the failing group", err)
	}

	if childExists(t, cl, pool, testGroupBase) {
		t.Errorf("child for the broken group %s was created", testGroupBase)
	}

	if !childExists(t, cl, pool, testGroupSpot) {
		t.Errorf("group %s was not reconciled: the failing group blocked it", testGroupSpot)
	}
}
