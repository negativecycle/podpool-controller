package controller

import (
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

func TestIsControlledBy(t *testing.T) {
	poolUID := types.UID("pool-abc-123")

	ownerRef := func(uid types.UID, controller bool) []metav1.OwnerReference {
		return []metav1.OwnerReference{{
			APIVersion: podpoolsv1alpha1.GroupVersion.String(),
			Kind:       workload.KindPodPool,
			Name:       testPoolName,
			UID:        uid,
			Controller: ptr.To(controller),
		}}
	}

	tests := []struct {
		name string
		obj  metav1.Object
		pool *podpoolsv1alpha1.PodPool
		want bool
	}{
		{
			name: "controlled by this pool",
			obj:  &metav1.ObjectMeta{OwnerReferences: ownerRef(poolUID, true)},
			pool: &podpoolsv1alpha1.PodPool{ObjectMeta: metav1.ObjectMeta{UID: poolUID}},
			want: true,
		},
		{
			name: "controller owner with different UID",
			obj:  &metav1.ObjectMeta{OwnerReferences: ownerRef("other-uid", true)},
			pool: &podpoolsv1alpha1.PodPool{ObjectMeta: metav1.ObjectMeta{UID: poolUID}},
			want: false,
		},
		{
			name: "owner reference present but not controller",
			obj:  &metav1.ObjectMeta{OwnerReferences: ownerRef(poolUID, false)},
			pool: &podpoolsv1alpha1.PodPool{ObjectMeta: metav1.ObjectMeta{UID: poolUID}},
			want: false,
		},
		{
			name: "no owner references",
			obj:  &metav1.ObjectMeta{},
			pool: &podpoolsv1alpha1.PodPool{ObjectMeta: metav1.ObjectMeta{UID: poolUID}},
			want: false,
		},
		{
			name: "pool with empty UID",
			obj:  &metav1.ObjectMeta{OwnerReferences: ownerRef("", true)},
			pool: &podpoolsv1alpha1.PodPool{ObjectMeta: metav1.ObjectMeta{UID: ""}},
			want: false,
		},
		{
			// A pool deleted and recreated under the same name gets a new UID.
			// Children of the old pool must read as strangers to the new one.
			name: "right name but stale UID",
			obj: &metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{
				APIVersion: podpoolsv1alpha1.GroupVersion.String(),
				Kind:       workload.KindPodPool,
				Name:       testPoolName,
				UID:        "old-uid-before-recreate",
				Controller: ptr.To(true),
			}}},
			pool: &podpoolsv1alpha1.PodPool{ObjectMeta: metav1.ObjectMeta{Name: testPoolName, UID: poolUID}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isControlledBy(tt.obj, tt.pool); got != tt.want {
				t.Errorf("isControlledBy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestErrWorkloadNotOwned(t *testing.T) {
	const testChildName = "pool-base"

	t.Run("no controller owner", func(t *testing.T) {
		e := &workloadNotOwnedError{kind: testStsKind, name: testChildName}
		if !strings.Contains(e.Error(), "has no controller owner") {
			t.Errorf("unexpected message: %s", e.Error())
		}
	})

	t.Run("foreign controller", func(t *testing.T) {
		e := &workloadNotOwnedError{
			kind:  testStsKind,
			name:  testChildName,
			owner: &metav1.OwnerReference{Kind: "ReplicaSet", Name: "rs-abc"},
		}
		if !strings.Contains(e.Error(), "is controlled by ReplicaSet/rs-abc") {
			t.Errorf("unexpected message: %s", e.Error())
		}
	})

	t.Run("errors.As unwraps", func(t *testing.T) {
		orig := &workloadNotOwnedError{kind: testStsKind, name: testChildName}
		wrapped := errors.Join(orig)

		var target *workloadNotOwnedError
		if !errors.As(wrapped, &target) {
			t.Error("errors.As should unwrap workloadNotOwnedError")
		}
	})
}

func foreignDeployment(name, ns string) *appsv1.Deployment {
	labels := map[string]string{"foreign": "yes"}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "other.io/v1",
				Kind:       "ForeignController",
				Name:       "foreign-owner",
				UID:        "foreign-uid",
				Controller: ptr.To(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: testImageNginx}}},
			},
		},
	}
}

func TestReconcileRefusesForeignChild(t *testing.T) {
	pool := fakeTestPool()
	dep := foreignDeployment(pool.Name+"-"+testGroupBase, testNamespace)

	r, _ := newFakeReconciler(t, nil, pool, dep)

	err := tryReconcilePool(r, pool)
	if err == nil {
		t.Fatal("expected reconcile to fail when child is owned by someone else")
	}

	// kerrors.Aggregate does not implement Unwrap, so errors.As cannot
	// traverse it. Walk the aggregate's Errors() slice instead.
	agg, ok := err.(interface{ Errors() []error })
	if !ok {
		t.Fatalf("expected aggregate error, got %T: %v", err, err)
	}

	found := false

	for _, e := range agg.Errors() {
		var notOwned *workloadNotOwnedError
		if errors.As(e, &notOwned) {
			found = true

			break
		}
	}

	if !found {
		t.Fatalf("expected workloadNotOwnedError in aggregate, got: %v", err)
	}
}

func TestChildCountsIgnoresForeignObject(t *testing.T) {
	pool := fakeTestPool()
	dep := foreignDeployment(pool.Name+"-"+testGroupBase, testNamespace)

	r, _ := newFakeReconciler(t, nil, []client.Object{pool, dep}...)

	gvk := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

	obs, err := r.childCounts(t.Context(), pool, gvk, testGroupBase)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if obs.found {
		t.Error("childCounts should return found=false for a foreign object")
	}

	if !obs.foreign {
		t.Error("childCounts should mark a foreign object as such; reported as merely " +
			"absent it reads as a cold start and is offered the whole remainder")
	}
}
