package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

func conditionByType(pool *podpoolsv1alpha1.PodPool, condType string) *metav1.Condition {
	return meta.FindStatusCondition(pool.Status.Conditions, condType)
}

// A healthy pool with children created but no readiness reported yet: groups
// reconciled, nothing available, still progressing.
func TestConditionsOnFreshPool(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, pool)

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)

	gr := conditionByType(got, ConditionGroupsReady)
	if gr == nil || gr.Status != metav1.ConditionTrue || gr.Reason != ReasonAllGroupsReconciled {
		t.Errorf("GroupsReady = %+v, want True/AllGroupsReconciled", gr)
	}

	av := conditionByType(got, ConditionAvailable)
	if av == nil || av.Status != metav1.ConditionFalse || av.Reason != ReasonNoReplicasAvailable {
		t.Errorf("Available = %+v, want False/NoReplicasAvailable (children report no status yet)", av)
	}

	pr := conditionByType(got, ConditionProgressing)
	if pr == nil || pr.Status != metav1.ConditionTrue || pr.Reason != ReasonReplicasUpdating {
		t.Errorf("Progressing = %+v, want True/ReplicasUpdating", pr)
	}

	if got.Status.ObservedGeneration != got.Generation {
		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
}

// A failing group flips GroupsReady and names the group; the healthy group's
// child still exists, which TestReconcileContinuesPastFailingGroup pins.
func TestConditionsNameTheFailingGroup(t *testing.T) {
	pool := fakeTestPool()
	breakGroup(pool)

	r, cl := newFakeReconciler(t, pool)

	_ = tryReconcilePool(r, pool)

	got := getPool(t, cl, pool)

	gr := conditionByType(got, ConditionGroupsReady)
	if gr == nil || gr.Status != metav1.ConditionFalse || gr.Reason != ReasonGroupReconcileFailed {
		t.Errorf("GroupsReady = %+v, want False/GroupReconcileFailed", gr)
	}
}

// An ownership conflict is a refusal, not an ordinary failure, and the
// condition says so ahead of any other failing group.
func TestConditionsPromoteOwnershipConflict(t *testing.T) {
	pool := fakeTestPool()
	dep := foreignDeployment(pool.Name+"-"+testGroupBase, testNamespace)

	r, cl := newFakeReconciler(t, pool, dep)

	_ = tryReconcilePool(r, pool)

	got := getPool(t, cl, pool)

	gr := conditionByType(got, ConditionGroupsReady)
	if gr == nil || gr.Reason != ReasonWorkloadNotOwned {
		t.Errorf("GroupsReady = %+v, want reason WorkloadNotOwned", gr)
	}
}

// A template without an addressable GVK is the pool's own failure: GroupsReady
// blames the pool without inventing a group name, and nothing retries an
// error only a spec edit can fix.
func TestConditionsOnInvalidTemplate(t *testing.T) {
	pool := fakeTestPool()
	pool.Spec.WorkloadTemplate = runtime.RawExtension{Raw: []byte(`{"spec": {}}`)}

	r, cl := newFakeReconciler(t, pool)

	if err := tryReconcilePool(r, pool); err != nil {
		t.Fatalf("Reconcile returned %v, want nil: a spec error is reported, not retried", err)
	}

	got := getPool(t, cl, pool)

	gr := conditionByType(got, ConditionGroupsReady)
	if gr == nil || gr.Status != metav1.ConditionFalse || gr.Reason != ReasonGroupSpecInvalid {
		t.Errorf("GroupsReady = %+v, want False/GroupSpecInvalid", gr)
	}
}

// The unplaced count drives both TargetDegraded and Progressing: a fully
// capped pool is deliberately short, and that is a terminal statement about
// the spec, not progress that has yet to happen.
func TestConditionsOnFullyCappedPool(t *testing.T) {
	pool := fakeTestPool()
	pool.Spec.Replicas = 10
	pool.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
		{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Target: pctTarget(20)}},
		{Name: testGroupSpot, Scaling: podpoolsv1alpha1.ScalingConstraints{Target: pctTarget(50)}},
	}

	r, cl := newFakeReconciler(t, pool)

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)

	td := conditionByType(got, ConditionTargetDegraded)
	if td == nil || td.Status != metav1.ConditionTrue || td.Reason != ReasonCeilingsBelowDesired {
		t.Errorf("TargetDegraded = %+v, want True/CeilingsBelowDesired", td)
	}

	pr := conditionByType(got, ConditionProgressing)
	if pr == nil || pr.Status != metav1.ConditionFalse || pr.Reason != ReasonCeilingsBelowDesired {
		t.Errorf("Progressing = %+v, want False/CeilingsBelowDesired", pr)
	}
}
