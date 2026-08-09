package controller

import (
	"context"
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// A terminating pool must be invisible to the controller: no child applies, no
// status writes, no requeue. Recreating a child the GC just deleted turns
// foreground deletion into a fight the GC cannot win.
func TestReconcileSkipsTerminatingPool(t *testing.T) {
	pool := fakeTestPool()
	pool.Finalizers = []string{"test.podpools.dev/hold"}

	r, cl := newFakeReconciler(t, pool)

	// Delete through the client so the fake sets deletionTimestamp; the
	// finalizer holds the object, same as the real server.
	if err := cl.Delete(t.Context(), pool); err != nil {
		t.Fatalf("deleting pool: %v", err)
	}

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(_ context.Context, _ client.WithWatch, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
			t.Error("Apply called: a terminating pool must not write children")

			return nil
		},
	})

	res, err := r.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile returned %v, want nil", err)
	}

	if res != (ctrl.Result{}) {
		t.Errorf("Reconcile returned %+v, want empty result (no requeue for a dying pool)", res)
	}

	got := getPool(t, cl, pool)
	if !apiequality.Semantic.DeepEqual(got.Status, podpoolsv1alpha1.PodPoolStatus{}) {
		t.Errorf("status was written on a terminating pool: %+v", got.Status)
	}
}
