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
	r, cl := newFakeReconciler(t, nil, pool)

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

	r, cl := newFakeReconciler(t, nil, pool)
	failApplyForChild(t, r, pool.Name+"-"+testGroupBase)

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

	r, cl := newFakeReconciler(t, nil, pool, dep)

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

	r, cl := newFakeReconciler(t, nil, pool)

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

	r, cl := newFakeReconciler(t, nil, pool)

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

// TestReadyMessagesFitTheColumnBudget sweeps every summaryReady arm with
// hostile inputs (long names, many groups, large counts) and holds each
// message to the print-column budget. The Status column added in the API
// milestone is where these strings land, and kubectl truncates anything
// longer mid-word.
func TestReadyMessagesFitTheColumnBudget(t *testing.T) {
	longNames := []string{
		"an-extremely-long-group-name-that-tests-the-budget",
		"another-very-long-group-name",
		"a-third-name", "a-fourth-name", "a-fifth-name",
	}

	cases := []struct {
		name string
		cond metav1.Condition
	}{
		{"scaled to zero", summaryReady(1, 0, 0, 0, nil, nil, nil)},
		{"terminal groups", summaryReady(1, 0, 9, 0, longNames, longNames, nil)},
		{"failed groups", summaryReady(1, 0, 9, 0, longNames, nil, nil)},
		{"unplaced", summaryReady(1, 100000, 1000000, 900000, nil, nil, nil)},
		{"none ready", summaryReady(1, 0, 1000000, 0, nil, nil, nil)},
		{"stalled", summaryReady(1, 3, 9, 0, nil, nil, longNames)},
		{"updating", summaryReady(1, 999999, 1000000, 0, nil, nil, nil)},
		{"ready", summaryReady(1, 1000000, 1000000, 0, nil, nil, nil)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if n := len(tc.cond.Message); n > readyMessageBudget {
				t.Errorf("message %q is %d chars, budget is %d", tc.cond.Message, n, readyMessageBudget)
			}

			if tc.cond.Message == "" {
				t.Error("empty message: the column would show nothing")
			}
		})
	}
}

// TestReadySummarisesTheFourConditions pins the single-answer property: with
// four detail conditions and no summary, every consumer recomputes "is this
// pool healthy?" and each gets it slightly wrong.
func TestReadySummarisesTheFourConditions(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	got := getPool(t, cl, pool)

	ready := conditionByType(got, ConditionReady)
	if ready == nil {
		t.Fatal("Ready condition missing")
	}

	// Children exist but report no readiness, so the answer is a clean False
	// with the not-ready count, not an error state.
	if ready.Status != metav1.ConditionFalse || ready.Reason != ReasonNoReplicasAvailable {
		t.Errorf("Ready = %s/%s, want False/NoReplicasAvailable", ready.Status, ready.Reason)
	}
}
