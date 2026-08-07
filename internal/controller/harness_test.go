package controller

// Package-wide harness for the fake-client tier: the reconciler constructor,
// pool fixtures, and reconcile drivers the stdlib tests in this package
// share. Feature files keep their feature-specific fakes; a helper used
// across files lives here, so deleting a feature test cannot orphan the
// harness underneath the rest of the package.
//
// Tests in this package stay serial on purpose: both pool fixtures share one
// pool name, and the envtest suite runs in the same binary.

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// newFakeReconciler builds a reconciler over a fake client, so Reconcile runs
// end to end without a manager or an API server.
func newFakeReconciler(t *testing.T, objs ...client.Object) (*PodPoolReconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("adding client-go scheme: %v", err)
	}

	if err := podpoolsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding podpools scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&podpoolsv1alpha1.PodPool{}).
		WithObjects(objs...).
		Build()

	return &PodPoolReconciler{Client: cl, Scheme: scheme}, cl
}

func fakeTestPool() *podpoolsv1alpha1.PodPool {
	minTwo := int32(2)
	minOne := int32(1)

	return &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: testNamespace, Generation: 1, UID: "fake-pool-uid"},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         3,
			WorkloadTemplate: workloadTemplateJSON("apps/v1", "Deployment", "app", testImageNginx),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
				{Name: testGroupSpot, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minOne}},
			},
		},
	}
}

func pctTarget(pct int32) *intstr.IntOrString {
	v := intstr.FromString(fmt.Sprintf("%d%%", pct))

	return &v
}

func getPool(t *testing.T, cl client.Client, pool *podpoolsv1alpha1.PodPool) *podpoolsv1alpha1.PodPool {
	t.Helper()

	var got podpoolsv1alpha1.PodPool

	key := types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace}
	if err := cl.Get(t.Context(), key, &got); err != nil {
		t.Fatalf("getting pool: %v", err)
	}

	return &got
}

func reconcilePool(t *testing.T, r *PodPoolReconciler, pool *podpoolsv1alpha1.PodPool) {
	t.Helper()

	if err := tryReconcilePool(r, pool); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func tryReconcilePool(r *PodPoolReconciler, pool *podpoolsv1alpha1.PodPool) error {
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	})

	return err
}
