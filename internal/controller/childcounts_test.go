package controller

import (
	"context"
	errs "errors"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// childCounts answered four materially different questions with one boolean.
// "No child yet" is the cold start and phase 3 answers it by offering the group
// everything left over. A transient read error, a child invisible to the
// label-scoped cache, and a child owned by someone else all returned the same
// false, so each of them was read as a licence to grow, and the remainder they
// took was subtracted from every group after them in list order.

// errTransientRead stands in for whatever the API server was unable to do.
var errTransientRead = errs.New("etcdserver: request timed out")

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// coldStartPool is a reliable tier followed by an opportunistic one, so a test
// can watch the opportunistic group take the whole remainder.
func coldStartPool() *podpoolsv1alpha1.PodPool {
	pool := fakeTestPool()
	pool.Spec.Replicas = 10
	pool.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
		{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2)}},
		{Name: testGroupScav, Scaling: podpoolsv1alpha1.ScalingConstraints{
			Min: ptr.To[int32](0), Opportunistic: opportunistic(),
		}},
	}

	return pool
}

// scavengerFirstPool puts the opportunistic group ahead of an unbounded one, so
// what phase 3 hands the scavenger is visibly taken from the burst group.
func scavengerFirstPool() *podpoolsv1alpha1.PodPool {
	pool := coldStartPool()
	pool.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
		{Name: testGroupScav, Scaling: podpoolsv1alpha1.ScalingConstraints{
			Min: ptr.To[int32](0), Opportunistic: opportunistic(),
		}},
		{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
	}

	return pool
}

// ownedChild is a child Deployment this pool controls, with its counts already
// published.
//
// Status is set on the object directly: the fake client registers a status
// subresource for PodPool only, so a Deployment's status round-trips through
// WithObjects intact.
func ownedChild(pool *podpoolsv1alpha1.PodPool, group string, replicas, ready int32) *appsv1.Deployment {
	labels := map[string]string{workload.LabelPool: pool.Name, workload.LabelGroup: group}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", pool.Name, group),
			Namespace: pool.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: podpoolsv1alpha1.GroupVersion.String(),
				Kind:       workload.KindPodPool,
				Name:       pool.Name,
				UID:        pool.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: testContainer, Image: testImageNginx}}},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      replicas,
			ReadyReplicas: ready,
		},
	}
}

func childReplicas(t *testing.T, c client.Reader, pool *podpoolsv1alpha1.PodPool, group string) int32 {
	t.Helper()

	var dep appsv1.Deployment

	key := types.NamespacedName{
		Namespace: pool.Namespace,
		Name:      workload.ChildName(pool.Name, group),
	}
	if err := c.Get(t.Context(), key, &dep); err != nil {
		t.Fatalf("reading child %s: %v", key, err)
	}

	if dep.Spec.Replicas == nil {
		return 0
	}

	return *dep.Spec.Replicas
}

// flakyReader fails the first Get of one key and passes everything else
// through, which is what a throttle or a dropped connection looks like: the
// object is there, one read did not see it. Failing every read would also break
// the write path's own Get, aborting the pass for a different reason and hiding
// what is under test.
func flakyReader(t *testing.T, key string, objs ...client.Object) (*PodPoolReconciler, client.WithWatch) {
	t.Helper()

	remaining := 1

	store := fake.NewClientBuilder().
		WithScheme(edgeScheme(t)).
		WithStatusSubresource(&podpoolsv1alpha1.PodPool{}).
		WithObjects(objs...).
		Build()

	cached := interceptor.NewClient(store, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey,
			obj client.Object, opts ...client.GetOption,
		) error {
			if k.String() == key && remaining > 0 {
				remaining--

				return apierrors.NewInternalError(errTransientRead)
			}

			return c.Get(ctx, k, obj, opts...)
		},
	})

	return &PodPoolReconciler{
		Client:    cached,
		Scheme:    store.Scheme(),
		APIReader: store,
		Clock:     clock.RealClock{},
	}, store
}

// ---------------------------------------------------------------------------
// the cold start, which is the behaviour worth protecting
// ---------------------------------------------------------------------------

// TestColdStartOffersTheRemainder pins what "absent" is supposed to mean, so
// that a fix which makes every miss cautious is caught immediately. This is the
// one state that genuinely deserves the whole remainder.
func TestColdStartOffersTheRemainder(t *testing.T) {
	pool := coldStartPool()

	r, store := newFakeReconciler(t, nil, pool)

	if err := tryReconcilePool(r, pool); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := childReplicas(t, store, pool, testGroupScav); got != 8 {
		t.Errorf("scavenger asked for %d, want 8: a group with no child at all has "+
			"never been sized and should be offered everything the reliable tier left", got)
	}
}

// ---------------------------------------------------------------------------
// the three states that are not a cold start
// ---------------------------------------------------------------------------

// TestTransientChildReadAbortsThePass is the headline bug. The child exists and
// is ours; one read of it failed. Sizing the pool from that produces a target
// derived from data the controller does not have, and SSA writes it in the same
// pass with no confirmation.
func TestTransientChildReadAbortsThePass(t *testing.T) {
	pool := coldStartPool()
	base := ownedChild(pool, testGroupBase, 2, 2)
	scav := ownedChild(pool, testGroupScav, 3, 3)

	key := types.NamespacedName{Namespace: pool.Namespace, Name: scav.Name}.String()

	r, store := flakyReader(t, key, pool, base, scav)

	if err := tryReconcilePool(r, pool); err == nil {
		t.Error("Reconcile succeeded despite failing to read a child; a pass that " +
			"cannot see the cluster must retry, not size the pool from a guess")
	}

	if got := childReplicas(t, store, pool, testGroupScav); got != 3 {
		t.Errorf("scavenger was rewritten to %d, want it left at 3: an unreadable "+
			"child is not an absent one, and the remainder is not its capacity", got)
	}

	// base is not opportunistic and was read perfectly well, which is the
	// point: phase 3 subtracts what it grants, so a capacity map missing one
	// entry misprices every group after it. Read as absent, the scavenger
	// takes the whole remainder and base is left at its floor; the pass has to
	// abort for all of them, not just for the group that could not be read.
	if got := childReplicas(t, store, pool, testGroupBase); got != 2 {
		t.Errorf("base was rewritten to %d, want it left at 2: one unreadable child "+
			"must not resize the groups that were readable", got)
	}
}

// TestForeignChildDoesNotShrinkLaterGroups is the damage-spreads half. The
// scavenger's name is taken by an object this controller will refuse to write,
// so it has no capacity to offer. Reading that as a cold start hands it the
// whole remainder and starves the burst group behind it, for a group that
// cannot place a single replica.
func TestForeignChildDoesNotShrinkLaterGroups(t *testing.T) {
	pool := scavengerFirstPool()
	foreign := foreignDeployment(workload.ChildName(pool.Name, testGroupScav), pool.Namespace)

	r, store := newFakeReconciler(t, nil, pool, foreign)

	// The scavenger group fails, loudly and by design (workloadNotOwnedError).
	// The pool's other groups must still be sized correctly around it.
	_ = tryReconcilePool(r, pool)

	if got := childReplicas(t, store, pool, testGroupBurst); got != 10 {
		t.Errorf("burst asked for %d, want 10: the scavenger's share was taken by a "+
			"group whose child belongs to someone else", got)
	}
}

// TestUnreadObservationDoesNotResolveAProbe is the second bug, one layer up and
// worse than a bad target: it corrupts state that outlives the pass.
//
// An unread observation is the zero value, and an outstanding probe resolves on
// ready >= asked. Against zeroes that is 0 >= 0, so the controller records that
// the scheduler accepted a replica it never looked at, and the next heartbeat
// is biased toward growth on the strength of it.
func TestUnreadObservationDoesNotResolveAProbe(t *testing.T) {
	pool := probePool()
	r := &PodPoolReconciler{
		Clock:  clocktesting.NewFakePassiveClock(probeTestBase),
		probes: map[string]probeState{probeKey(pool, testGroupScav): {outstanding: true}},
	}

	d := r.decideProbe(pool, testGroupScav, 4, opportunisticObservation{}, probeTestBase)

	if !r.probeOutstanding(pool, testGroupScav) {
		t.Error("the probe was marked resolved by an observation that was never read; " +
			"a verdict nobody looked at is not a verdict")
	}

	if d.target != 4 || d.awaitVerdict {
		t.Errorf("target=%d awaitVerdict=%v, want 4 false: an unreadable group gets no new "+
			"probe either", d.target, d.awaitVerdict)
	}
}

// ---------------------------------------------------------------------------
// the read states, as one table, since the item is that they were one case
// ---------------------------------------------------------------------------

func TestChildCountsReadStates(t *testing.T) {
	gvk := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

	tests := []struct {
		name string
		// build returns a reconciler and the pool to read the scavenger's
		// child from.
		build     func(*testing.T, *podpoolsv1alpha1.PodPool) *PodPoolReconciler
		want      opportunisticObservation
		wantErr   bool
		wantPhase string
	}{
		{
			name: "no object anywhere is the cold start",
			build: func(t *testing.T, pool *podpoolsv1alpha1.PodPool) *PodPoolReconciler {
				t.Helper()
				r, _ := newFakeReconciler(t, nil, pool)

				return r
			},
			want:      opportunisticObservation{},
			wantPhase: "absent from the capacity map, offered the remainder",
		},
		{
			name: "an owned child reports its real counts",
			build: func(t *testing.T, pool *podpoolsv1alpha1.PodPool) *PodPoolReconciler {
				t.Helper()
				r, _ := newFakeReconciler(t, nil, pool, ownedChild(pool, testGroupScav, 5, 4))

				return r
			},
			want:      opportunisticObservation{found: true, asked: 5, ready: 4},
			wantPhase: "real capacity",
		},
		{
			name: "an owned child the scoped cache cannot see is read uncached",
			// The workload cache is scoped by managed-by, so a child whose
			// label a user stripped is absent from it rather than stale. The
			// write path has confirmed absence uncached since the ownership
			// milestone; this is the read path catching up.
			build: func(t *testing.T, pool *podpoolsv1alpha1.PodPool) *PodPoolReconciler {
				t.Helper()

				sv := newSplitView(t,
					[]string{childKey(pool, testGroupScav)},
					pool, ownedChild(pool, testGroupScav, 5, 4))

				return sv.reconciler(nil)
			},
			want:      opportunisticObservation{found: true, asked: 5, ready: 4},
			wantPhase: "real capacity, via APIReader",
		},
		{
			name: "a foreign child offers no capacity and is not a cold start",
			build: func(t *testing.T, pool *podpoolsv1alpha1.PodPool) *PodPoolReconciler {
				t.Helper()
				r, _ := newFakeReconciler(t, nil, pool,
					foreignDeployment(workload.ChildName(pool.Name, testGroupScav), pool.Namespace))

				return r
			},
			want:      opportunisticObservation{foreign: true},
			wantPhase: "present with zero capacity",
		},
		{
			name: "a failed read is an error, not an answer",
			build: func(t *testing.T, pool *podpoolsv1alpha1.PodPool) *PodPoolReconciler {
				t.Helper()

				key := types.NamespacedName{
					Namespace: pool.Namespace,
					Name:      workload.ChildName(pool.Name, testGroupScav),
				}.String()

				// A non-NotFound error is returned as-is: the uncached
				// retry exists to confirm absence, not to paper over a
				// failure.
				r, _ := flakyReader(t, key, pool, ownedChild(pool, testGroupScav, 5, 4))

				return r
			},
			want:      opportunisticObservation{},
			wantErr:   true,
			wantPhase: "the pass aborts, nothing is written",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := coldStartPool()
			r := tt.build(t, pool)

			got, err := r.childCounts(t.Context(), pool, gvk, testGroupScav)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != tt.want {
				t.Errorf("childCounts = %+v, want %+v (phase 3 should see: %s)",
					got, tt.want, tt.wantPhase)
			}
		})
	}
}
