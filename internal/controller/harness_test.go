package controller

// Package-wide harness for the fake-client tier: the reconciler constructor,
// pool fixtures, reconcile drivers, and event helpers that the stdlib tests in
// this package share. Feature files keep their feature-specific fakes; a helper used
// across files lives here, so deleting a feature test cannot orphan the
// harness underneath the rest of the package.
//
// Tests in this package stay serial on purpose: both pool fixtures share one
// pool name, Reconcile writes per pool gauges to the process global metrics
// registry, and the envtest suite runs in the same binary.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// statusPatches counts status patches that actually reach the API server, as
// opposed to the ones patchStatus discards for being empty.
type statusPatches struct{ n int }

// newFakeReconciler builds a reconciler over a fake client, so Reconcile runs
// end to end without a manager or an API server.
func newFakeReconciler(t *testing.T, counter *statusPatches, objs ...client.Object) (*PodPoolReconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("adding client-go scheme: %v", err)
	}

	if err := podpoolsv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding podpools scheme: %v", err)
	}

	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&podpoolsv1alpha1.PodPool{}).
		WithObjects(objs...)

	if counter != nil {
		builder = builder.WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subResource string,
				obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption,
			) error {
				if subResource == "status" {
					counter.n++
				}

				return c.SubResource(subResource).Patch(ctx, obj, patch, opts...)
			},
		})
	}

	cl := builder.Build()

	return &PodPoolReconciler{Client: cl, Scheme: scheme, APIReader: cl, Clock: clock.RealClock{}}, cl
}

func fakeTestPool() *podpoolsv1alpha1.PodPool {
	minTwo := int32(2)
	minOne := int32(1)

	return &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: testNamespace, Generation: 1, UID: "fake-pool-uid"},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         3,
			WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testUserLabelKey),
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

func opportunistic() *bool {
	b := true

	return &b
}

// threeTierSpec is the target configuration: a reliable tier with a declared
// share, an opportunistic tier sized by real capacity, and an unbounded overflow.
func threeTierSpec() []podpoolsv1alpha1.GroupSpec {
	return []podpoolsv1alpha1.GroupSpec{
		{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3), Target: pctTarget(35)}},
		{Name: testGroupScav, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: opportunistic()}},
		{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
	}
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
	_, err := r.Reconcile(context.Background(), reconcileRequestFor(pool))

	return err
}

// reconcileRequestFor is for tests that need the ctrl.Result as well as the
// error, which the drivers above discard.
func reconcileRequestFor(pool *podpoolsv1alpha1.PodPool) ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{Name: pool.Name, Namespace: pool.Namespace},
	}
}

// drainEvents empties a FakeRecorder's channel without blocking. Reading with a
// plain range would block forever: the channel is never closed, because the
// recorder does not know when the test has finished with it.
func drainEvents(ch <-chan string) []string {
	var result []string

	for {
		select {
		case e := <-ch:
			result = append(result, e)
		default:
			return result
		}
	}
}

func countEventsByReason(evts []string, reason string) int {
	n := 0

	for _, e := range evts {
		if strings.Contains(e, reason) {
			n++
		}
	}

	return n
}

// singleGroupPool keeps event counting unambiguous: with one group, "how many
// events" and "how many events about this group" are the same number.
func singleGroupPool() *podpoolsv1alpha1.PodPool {
	minThree := int32(3)

	return &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool", Namespace: testNamespace, Generation: 1, UID: "fake-pool-uid"},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         3,
			WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minThree}},
			},
		},
	}
}
