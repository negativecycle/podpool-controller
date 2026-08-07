package controller

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// confirmOrphan is the measure-twice step in front of the only destructive
// action this controller takes: it re-reads a delete candidate straight from
// the API server because the cached list that nominated it can be stale.
// These tests cover the two abnormal answers that read can give.

var orphanGVK = schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

func orphanCandidate(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(orphanGVK)
	u.SetName(name)
	u.SetNamespace(testNamespace)

	return u
}

func deleteAll(string) bool { return false }

// TestConfirmOrphanTreatsNotFoundAsAlreadyGone covers the race the function
// exists for: the candidate vanished between the cached list and the direct
// read. Losing that race to another deleter is success, not an error — an
// error here would abort the whole sweep and strand the remaining orphans
// until the next pass.
func TestConfirmOrphanTreatsNotFoundAsAlreadyGone(t *testing.T) {
	pool := fakeTestPool()
	r, _ := newFakeReconciler(t, nil, pool)

	fresh, err := r.confirmOrphan(t.Context(), pool, orphanCandidate("gone"), orphanGVK, deleteAll)
	if err != nil {
		t.Fatalf("confirmOrphan on a vanished candidate: %v, want nil", err)
	}

	if fresh != nil {
		t.Errorf("fresh = %v, want nil: there is nothing left to delete", fresh)
	}
}

// TestConfirmOrphanPropagatesReadErrors pins the conservative default:
// "could not verify" must surface as an error that aborts the sweep, never
// degrade into deleting on the strength of the stale cache alone.
func TestConfirmOrphanPropagatesReadErrors(t *testing.T) {
	pool := fakeTestPool()
	readFailure := errors.New("simulated apiserver read failure")

	cl := fake.NewClientBuilder().
		WithScheme(clientgoscheme.Scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return readFailure
			},
		}).
		Build()
	r := &PodPoolReconciler{APIReader: cl}

	fresh, err := r.confirmOrphan(t.Context(), pool, orphanCandidate("unreachable"), orphanGVK, deleteAll)
	if !errors.Is(err, readFailure) {
		t.Fatalf("confirmOrphan error = %v, want the injected read failure", err)
	}

	if fresh != nil {
		t.Errorf("fresh = %v, want nil on an unverified candidate", fresh)
	}
}

// TestConfirmOrphanReturnsAConfirmedCandidate is the positive control: with
// the object present, pool-controlled, and unwanted by keep, the function
// must hand it back for deletion. Without this case the two above could pass
// against a broken fixture (wrong key, wrong GVK) and prove nothing.
func TestConfirmOrphanReturnsAConfirmedCandidate(t *testing.T) {
	pool := fakeTestPool()

	child := orphanCandidate("orphaned-child")
	child.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: podpoolsv1alpha1.SchemeGroupVersion.String(),
		Kind:       workload.KindPodPool,
		Name:       pool.Name,
		UID:        pool.UID,
		Controller: ptr.To(true),
	}})

	r, _ := newFakeReconciler(t, nil, pool, child)

	fresh, err := r.confirmOrphan(t.Context(), pool, orphanCandidate("orphaned-child"), orphanGVK, deleteAll)
	if err != nil {
		t.Fatalf("confirmOrphan on a present orphan: %v", err)
	}

	if fresh == nil || fresh.GetName() != "orphaned-child" {
		t.Fatalf("fresh = %v, want the confirmed candidate back", fresh)
	}
}
