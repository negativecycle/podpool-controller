package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// The create-path TOCTOU: reconcileWorkload checks ownership only when the
// cached Get finds the object. A same-name unowned workload created moments
// ago reads NotFound from the lagging informer, and the create path
// force-applies: SSA stamps our ownerReference onto the stranger. Worse, the
// next pass then sees a controller ref that is ours, so isControlledBy
// returns true and the adoption is invisible forever.
//
// These tests need the cache and the API server to *disagree*, which one fake
// client cannot express. splitView wires two views over one store.

func edgeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("adding client-go scheme: %v", err)
	}

	if err := podpoolsv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding podpools scheme: %v", err)
	}

	return s
}

// writeLog records the applies a reconcile issued, so a test can assert that a
// write was or was not made without inspecting the store afterwards.
type writeLog struct {
	applied []string
}

// splitView builds a store plus two views of it:
//
//   - cached: what r.Client sees. Keys in `hidden` return NotFound, standing in
//     for an informer that has not caught up.
//   - live:   what r.APIReader sees. The truth.
type splitView struct {
	store  client.WithWatch
	cached client.WithWatch
	log    *writeLog
}

func newSplitView(t *testing.T, hidden []string, objs ...client.Object) *splitView {
	t.Helper()

	s := edgeScheme(t)
	store := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&podpoolsv1alpha1.PodPool{}).
		WithObjects(objs...).
		Build()

	hide := make(map[string]bool, len(hidden))
	for _, h := range hidden {
		hide[h] = true
	}

	sv := &splitView{store: store, log: &writeLog{}}

	sv.cached = interceptor.NewClient(store, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey,
			obj client.Object, opts ...client.GetOption,
		) error {
			if hide[key.String()] {
				return apierrors.NewNotFound(
					schema.GroupResource{Group: "apps", Resource: "deployments"}, key.Name)
			}

			return c.Get(ctx, key, obj, opts...)
		},
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration,
			opts ...client.ApplyOption,
		) error {
			// client.ApplyConfigurationFromUnstructured returns an unexported
			// type that embeds *unstructured.Unstructured, so reach the name
			// through the method set rather than the concrete type.
			name := "<unknown>"
			if named, ok := obj.(interface{ GetName() string }); ok {
				name = named.GetName()
			}

			sv.log.applied = append(sv.log.applied, name)

			return c.Apply(ctx, obj, opts...)
		},
	})

	return sv
}

// reconciler returns a reconciler whose cached client lags and whose APIReader
// tells the truth. liveReader overrides the APIReader when a test needs the
// live read itself to fail.
func (sv *splitView) reconciler(liveReader client.Reader) *PodPoolReconciler {
	if liveReader == nil {
		liveReader = sv.store
	}

	return &PodPoolReconciler{
		Client:    sv.cached,
		Scheme:    sv.store.Scheme(),
		APIReader: liveReader,
		Clock:     clock.RealClock{},
	}
}

func splitChildKey(pool *podpoolsv1alpha1.PodPool) string {
	return types.NamespacedName{
		Namespace: pool.Namespace,
		Name:      fmt.Sprintf("%s-%s", pool.Name, testGroupBase),
	}.String()
}

// unownedDeployment is the case the refusal names as "exists and has no
// controller owner": a Deployment a human created by hand that happens to
// collide with the name this pool renders.
//
// No ownerReferences at all, deliberately. A Deployment already owned by
// another controller is protected by accident: SSA would add a second
// controller ref and the API server rejects that outright. The unowned case
// has no such backstop.
func unownedDeployment(pool *podpoolsv1alpha1.PodPool, group string) *appsv1.Deployment {
	labels := map[string]string{"stranger": "someone-elses"}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", pool.Name, group),
			Namespace: pool.Namespace,
			UID:       "stranger-uid",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](7),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: testImageNginx}}},
			},
		},
	}
}

// poolOwnedDeployment is a child this pool really does own, so a test can tell
// "the cache is merely behind" apart from "this is a stranger".
func poolOwnedDeployment(pool *podpoolsv1alpha1.PodPool, group string) *appsv1.Deployment {
	dep := unownedDeployment(pool, group)
	dep.Status = appsv1.DeploymentStatus{Replicas: 3, ReadyReplicas: 3}
	dep.UID = types.UID("owned-" + group)
	dep.Labels = map[string]string{workload.LabelPool: pool.Name, workload.LabelGroup: group, workload.LabelManagedBy: workload.ManagerName}
	dep.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: podpoolsv1alpha1.SchemeGroupVersion.String(),
		Kind:       workload.KindPodPool,
		Name:       pool.Name,
		UID:        pool.UID,
		Controller: ptr.To(true),
	}}

	return dep
}

func liveDeployment(t *testing.T, c client.Reader, pool *podpoolsv1alpha1.PodPool, group string) *appsv1.Deployment {
	t.Helper()

	var dep appsv1.Deployment

	key := types.NamespacedName{Namespace: pool.Namespace, Name: fmt.Sprintf("%s-%s", pool.Name, group)}
	if err := c.Get(t.Context(), key, &dep); err != nil {
		t.Fatalf("reading child %s: %v", key, err)
	}

	return &dep
}

func hasNotOwnedError(err error) bool {
	if err == nil {
		return false
	}

	var notOwned *workloadNotOwnedError
	if errors.As(err, &notOwned) {
		return true
	}
	// kerrors.Aggregate does not implement Unwrap, so errors.As cannot
	// traverse it. Walk the slice, as ownership_test.go does.
	agg, ok := err.(interface{ Errors() []error })
	if !ok {
		return false
	}

	for _, e := range agg.Errors() {
		if errors.As(e, &notOwned) {
			return true
		}
	}

	return false
}

// TestStaleCacheDoesNotAdoptUnownedChild is the defect itself. The informer
// has not seen the stranger yet, so the cached Get says NotFound and the
// create path would force-apply over it.
func TestStaleCacheDoesNotAdoptUnownedChild(t *testing.T) {
	pool := fakeTestPool()
	stranger := unownedDeployment(pool, testGroupBase)

	sv := newSplitView(t, []string{splitChildKey(pool)}, pool, stranger)
	r := sv.reconciler(nil)

	err := tryReconcilePool(r, pool)
	if !hasNotOwnedError(err) {
		t.Errorf("reconcile returned %v, want workloadNotOwnedError; "+
			"the cached Get missed an object the API server has, and SSA create-or-update "+
			"wrote to a workload this pool does not own", err)
	}
}

// TestStaleCacheAdoptionIsNotSelfConcealing is the severity argument.
//
// Once our ownerReference is stamped, the next pass's isControlledBy returns
// true (we are the controller we just wrote) so the pool manages the stranger
// forever with no error and nothing anywhere to show it happened. A refusal
// that can be skipped silently is not a refusal.
func TestStaleCacheAdoptionIsNotSelfConcealing(t *testing.T) {
	pool := fakeTestPool()
	stranger := unownedDeployment(pool, testGroupBase)

	sv := newSplitView(t, []string{splitChildKey(pool)}, pool, stranger)
	_ = tryReconcilePool(sv.reconciler(nil), pool)

	after := liveDeployment(t, sv.store, pool, testGroupBase)
	if owner := metav1.GetControllerOf(after); owner != nil {
		t.Errorf("stranger gained controller ownerReference %s/%s (uid %s); "+
			"from the next reconcile on, isControlledBy accepts it and the adoption is invisible",
			owner.Kind, owner.Name, owner.UID)
	}

	if got := after.GetLabels()[workload.LabelManagedBy]; got != "" {
		t.Errorf("stranger gained the %s=%q label", workload.LabelManagedBy, got)
	}
}

// TestStaleCacheFallsThroughForOwnChild is why the fix is an ownership
// re-check rather than a bare absence check.
//
// The live read legitimately finds an object whenever the informer is behind a
// create we issued ourselves. Erroring there would fail the first reconcile
// after every child creation. The correct behaviour is to fall through and
// keep converging the live object.
func TestStaleCacheFallsThroughForOwnChild(t *testing.T) {
	pool := fakeTestPool()
	ours := poolOwnedDeployment(pool, testGroupBase)

	sv := newSplitView(t, []string{splitChildKey(pool)}, pool, ours)
	r := sv.reconciler(nil)

	if err := tryReconcilePool(r, pool); err != nil {
		t.Fatalf("reconcile failed for a child this pool owns: %v", err)
	}

	applied := false

	for _, name := range sv.log.applied {
		if name == pool.Name+"-"+testGroupBase {
			applied = true
		}
	}

	if !applied {
		t.Error("the owned child was not applied; falling through must keep converging it")
	}

	// The fall-through also reports real counts one pass sooner: the live
	// object the confirm read is the same one the observation reads.
	got := getPool(t, sv.store, pool)

	base := findGroupStatus(got.Status.Groups, testGroupBase)
	if base == nil || base.Replicas != 3 {
		t.Errorf("group status = %+v, want replicas 3 from the live object; "+
			"the create branch discards counts the API server already has", base)
	}
}

// TestLiveReadErrorIsRetryableAndDoesNotApply pins the failing direction.
//
// Treating a transient read failure as absence reintroduces the bug on
// exactly the passes where the API server is least healthy, so the live read
// must fail closed: return the error, issue no apply.
func TestLiveReadErrorIsRetryableAndDoesNotApply(t *testing.T) {
	pool := fakeTestPool()
	key := splitChildKey(pool)

	sv := newSplitView(t, []string{key}, pool)

	brokenReader := interceptor.NewClient(sv.store, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey,
			obj client.Object, opts ...client.GetOption,
		) error {
			if k.String() == key {
				return apierrors.NewInternalError(errors.New("etcd unavailable"))
			}

			return c.Get(ctx, k, obj, opts...)
		},
	})

	err := tryReconcilePool(sv.reconciler(brokenReader), pool)
	if err == nil {
		t.Error("reconcile succeeded despite the live ownership read failing")
	}

	for _, name := range sv.log.applied {
		if name == fmt.Sprintf("%s-%s", pool.Name, testGroupBase) {
			t.Errorf("applied %s after the live read failed; absence was assumed, not confirmed", name)
		}
	}
}

// TestGenuineAbsenceStillCreates guards against over-refusing. Both views
// agree the object is missing, so the create must proceed exactly as before.
func TestGenuineAbsenceStillCreates(t *testing.T) {
	pool := fakeTestPool()

	sv := newSplitView(t, nil, pool)
	if err := tryReconcilePool(sv.reconciler(nil), pool); err != nil {
		t.Fatalf("reconcile failed on a genuinely empty namespace: %v", err)
	}

	if len(sv.log.applied) != len(pool.Spec.Groups) {
		t.Errorf("applied %d children, want %d", len(sv.log.applied), len(pool.Spec.Groups))
	}

	liveDeployment(t, sv.store, pool, testGroupBase)
}
