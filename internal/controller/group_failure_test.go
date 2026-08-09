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

// failApplyForChild makes one group's child apply fail with a plain error,
// which is the ordinary retryable class: nothing about it says a spec edit is
// needed, so the pool keeps the workqueue.
func failApplyForChild(t *testing.T, r *PodPoolReconciler, childName string) {
	t.Helper()

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, ac runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ac)
			if err == nil {
				if md, ok := u["metadata"].(map[string]any); ok && md["name"] == childName {
					return errors.New("simulated transient apply failure")
				}
			}

			return c.Apply(ctx, ac, opts...)
		},
	})
}

// A group that cannot be reconciled used to abort the whole loop, so every
// later group stopped being reconciled: one bad group froze the pool. Failures
// are collected instead, later groups proceed, and the aggregate comes back
// naming each failed group.
//
// A retryable failure deliberately: a terminal one would be suppressed at the
// tail of Reconcile, and this test is about the loop, not the requeue.
func TestReconcileContinuesPastFailingGroup(t *testing.T) {
	pool := fakeTestPool()

	r, cl := newFakeReconciler(t, nil, pool)
	failApplyForChild(t, r, pool.Name+"-"+testGroupBase)

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
