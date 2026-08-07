package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

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
	r, cl := newFakeReconciler(t, pool)

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	applyCalls := 0
	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			applyCalls++
			// The loop walks groups in spec order, so the first apply is the
			// base group's child.
			if applyCalls == 1 {
				return errors.New("simulated apply failure")
			}

			return c.Apply(ctx, obj, opts...)
		},
	})

	err := tryReconcilePool(r, pool)
	if err == nil {
		t.Fatal("Reconcile swallowed the group failure")
	}

	if !strings.Contains(err.Error(), testGroupBase) {
		t.Errorf("error %q does not name the failing group", err)
	}

	if childExists(t, cl, pool, testGroupBase) {
		t.Errorf("child for the failing group %s was created", testGroupBase)
	}

	if !childExists(t, cl, pool, testGroupSpot) {
		t.Errorf("group %s was not reconciled: the failing group blocked it", testGroupSpot)
	}
}
