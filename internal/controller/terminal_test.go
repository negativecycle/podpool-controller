package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

const testDepResPlur = "deployments"

// The wire shape of an SSA schema rejection: code 500 with an EMPTY reason.
// Constructed literally rather than via NewInternalError, which would set
// reason=InternalError and dodge the unknown-reason + code==500 fallback
// branch the real error matches through.
var errSSATypedPatch = &apierrors.StatusError{ErrStatus: metav1.Status{
	Code:    500,
	Message: `failed to create typed patch object (default/x; apps/v1, Kind=Deployment): .spec.replicaz: field not declared in schema`,
}}

// A child type's own failing webhook is also a 500 -- and it must stay
// retryable, because it heals without a spec change.
var errWebhookDown = &apierrors.StatusError{ErrStatus: metav1.Status{
	Code:    500,
	Reason:  metav1.StatusReasonInternalError,
	Message: `Internal error occurred: failed calling webhook "x.kruise.io": connection refused`,
}}

// failApplyWith makes every child apply return the given error.
func failApplyWith(t *testing.T, r *PodPoolReconciler, apiErr error) {
	t.Helper()

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(_ context.Context, _ client.WithWatch, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
			return apiErr
		},
	})
}

func TestIsTerminalAPIError(t *testing.T) {
	const name = "my-sts"

	gk := schema.GroupKind{Group: testAppsGroup, Kind: testStsKind}
	gr := schema.GroupResource{Group: gk.Group, Resource: "statefulsets"}

	invalidErr := apierrors.NewInvalid(gk, name, nil)
	forbiddenErr := apierrors.NewForbidden(gr, name, errors.New("RBAC denied"))
	conflictErr := apierrors.NewConflict(gr, name, errors.New("resourceVersion mismatch"))
	notFoundErr := apierrors.NewNotFound(gr, name)
	tooLargeErr := apierrors.NewRequestEntityTooLargeError("body too large")

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"Invalid", invalidErr, true},
		{"Invalid wrapped", fmt.Errorf("applying child: %w", invalidErr), true},
		{"Forbidden", forbiddenErr, false},
		{"Forbidden wrapped", fmt.Errorf("applying child: %w", forbiddenErr), false},
		{"Conflict", conflictErr, false},
		{"Conflict wrapped", fmt.Errorf("applying child: %w", conflictErr), false},
		{"NotFound", notFoundErr, false},
		{"RequestEntityTooLarge", tooLargeErr, true},
		{"RequestEntityTooLarge wrapped", fmt.Errorf("too big: %w", tooLargeErr), true},
		{"SSA typed-patch 500", errSSATypedPatch, true},
		{"SSA typed-patch 500 wrapped", fmt.Errorf("applying child: %w", errSSATypedPatch), true},
		// The near-miss that documents the design: NOT all 500s are terminal.
		{"webhook-down 500", errWebhookDown, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTerminalAPIError(tt.err); got != tt.want {
				t.Errorf("isTerminalAPIError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The whole point: a mistyped template field comes back as a 500, so the pool
// must reach the same verdict it reaches for an unrenderable override.
func TestReconcileStopsRetryingSSASchemaRejection(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)
	failApplyWith(t, r, errSSATypedPatch)

	if err := tryReconcilePool(r, pool); err != nil {
		t.Fatalf("Reconcile returned error %v, want nil (an SSA schema rejection is terminal)", err)
	}

	got := getPool(t, cl, pool)

	cond := conditionByType(got, ConditionGroupsReady)
	if cond == nil {
		t.Fatal("GroupsReady not set")
	}

	if cond.Status != metav1.ConditionFalse {
		t.Errorf("GroupsReady = %s, want False", cond.Status)
	}

	if cond.Reason != ReasonGroupSpecInvalid {
		t.Errorf("reason = %s, want %s", cond.Reason, ReasonGroupSpecInvalid)
	}
}

// The near-miss, and the reason the match is on the message rather than on the
// status code: a child type's own webhook being down is also a 500, and it
// heals without anyone touching the spec.
func TestReconcileKeepsRetryingWebhookDown(t *testing.T) {
	pool := fakeTestPool()
	r, _ := newFakeReconciler(t, nil, pool)
	failApplyWith(t, r, errWebhookDown)

	if err := tryReconcilePool(r, pool); err == nil {
		t.Fatal("Reconcile returned nil for a webhook-down 500, want non-nil (transient 500s must be retried)")
	}
}

// 403 stays retryable on purpose. RBAC is fixed by an admin without touching
// the spec, and nothing here watches ClusterRoles to notice when they do.
func TestReconcileKeepsRetryingForbidden(t *testing.T) {
	pool := fakeTestPool()
	r, _ := newFakeReconciler(t, nil, pool)
	failApplyWith(t, r, apierrors.NewForbidden(
		schema.GroupResource{Group: testAppsGroup, Resource: testDepResPlur},
		pool.Name+"-"+testGroupBase, errors.New("RBAC denied")))

	if err := tryReconcilePool(r, pool); err == nil {
		t.Fatal("Reconcile returned nil for Forbidden, want non-nil (Forbidden must be retried)")
	}
}

// A 422 is terminal too, and always was the case people expect. It is here so
// the classifier's two arms are both exercised through Reconcile.
func TestReconcileStopsRetryingInvalid(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)
	failApplyWith(t, r, apierrors.NewInvalid(
		schema.GroupKind{Group: testAppsGroup, Kind: testDepKind},
		pool.Name+"-"+testGroupBase, nil))

	if err := tryReconcilePool(r, pool); err != nil {
		t.Fatalf("Reconcile returned error %v, want nil (Invalid is terminal)", err)
	}

	got := getPool(t, cl, pool)
	if cond := conditionByType(got, ConditionGroupsReady); cond == nil || cond.Reason != ReasonGroupSpecInvalid {
		t.Errorf("GroupsReady = %+v, want reason %s", cond, ReasonGroupSpecInvalid)
	}
}

func TestTerminalWrapping(t *testing.T) {
	orig := errors.New("bad spec")
	wrapped := terminal(orig)

	if !isTerminal(wrapped) {
		t.Error("isTerminal should return true for a terminal-wrapped error")
	}

	if !errors.Is(wrapped, orig) {
		t.Error("terminal wrapping should preserve the original error via Unwrap")
	}

	if isTerminal(orig) {
		t.Error("isTerminal should return false for a non-terminal error")
	}

	// The one that matters in practice. Every error leaving reconcileGroup is
	// wrapped at least once by the group loop, so a classifier that only reads
	// the outermost error never fires on anything real.
	doubleWrapped := fmt.Errorf("group base: %w", wrapped)
	if !isTerminal(doubleWrapped) {
		t.Error("isTerminal should traverse wrapping layers")
	}
}

// The suppression itself. A group whose overrides delete .spec.template cannot
// be rendered on this pass or any later one, so returning the error would back
// the pool off toward the rate limiter's cap and bury it among pools that are
// merely slow.
func TestReconcileStopsRetryingTerminalGroup(t *testing.T) {
	pool := fakeTestPool()
	breakGroup(pool)

	r, cl := newFakeReconciler(t, nil, pool)

	if err := tryReconcilePool(r, pool); err != nil {
		t.Fatalf("Reconcile returned error %v, want nil (terminal errors are suppressed)", err)
	}

	got := getPool(t, cl, pool)

	cond := conditionByType(got, ConditionGroupsReady)
	if cond == nil {
		t.Fatal("GroupsReady not set")
	}

	if cond.Status != metav1.ConditionFalse {
		t.Errorf("GroupsReady = %s, want False", cond.Status)
	}

	if cond.Reason != ReasonGroupSpecInvalid {
		t.Errorf("reason = %s, want %s", cond.Reason, ReasonGroupSpecInvalid)
	}

	// The summary condition is the one projected into kubectl's print column,
	// and it has its own precedence table. "Group reconcile failed" there would
	// send an operator to wait for a retry that is never coming.
	ready := conditionByType(got, ConditionReady)
	if ready == nil {
		t.Fatal("Ready not set")
	}

	if ready.Reason != ReasonGroupSpecInvalid {
		t.Errorf("Ready reason = %s, want %s (message: %q)", ready.Reason, ReasonGroupSpecInvalid, ready.Message)
	}

	if !strings.Contains(ready.Message, testGroupBase) {
		t.Errorf("Ready message %q does not name the group that needs the edit", ready.Message)
	}
}

// Suppression is all-or-nothing. One retryable failure alongside a terminal one
// means the pool may still resolve itself, so the workqueue is still needed --
// and the condition must not tell an operator to go and edit a spec that is not
// the problem.
func TestMixedTerminalAndRetryableStillRetries(t *testing.T) {
	pool := fakeTestPool()
	breakGroup(pool)

	r, cl := newFakeReconciler(t, nil, pool)
	failApplyForChild(t, r, pool.Name+"-"+testGroupSpot)

	err := tryReconcilePool(r, pool)
	if err == nil {
		t.Fatal("Reconcile returned nil, want error when some failures are retryable")
	}

	got := getPool(t, cl, pool)

	cond := conditionByType(got, ConditionGroupsReady)
	if cond == nil {
		t.Fatal("GroupsReady not set")
	}

	if cond.Reason != ReasonGroupReconcileFailed {
		t.Errorf("reason = %s, want %s (mixed failures use the retryable reason)", cond.Reason, ReasonGroupReconcileFailed)
	}

	if ready := conditionByType(got, ConditionReady); ready == nil || ready.Reason != ReasonGroupSpecInvalid {
		t.Errorf("Ready = %+v, want reason %s: one group does need an edit, and the print "+
			"column has room for the more actionable verdict", ready, ReasonGroupSpecInvalid)
	}

	if !strings.Contains(cond.Message, testGroupBase) || !strings.Contains(cond.Message, testGroupSpot) {
		t.Errorf("message %q should name both groups", cond.Message)
	}
}

// Suppressing the requeue must not suppress recovery. The pool is still woken
// by its own spec change, which is the only thing that can fix a terminal
// failure, and the condition has to clear when it does.
func TestTerminalClearsOnSpecFix(t *testing.T) {
	pool := fakeTestPool()
	breakGroup(pool)

	r, cl := newFakeReconciler(t, nil, pool)

	if err := tryReconcilePool(r, pool); err != nil {
		t.Fatalf("expected terminal suppression, got error: %v", err)
	}

	got := getPool(t, cl, pool)

	cond := conditionByType(got, ConditionGroupsReady)
	if cond == nil {
		t.Fatal("GroupsReady not set after terminal failure")
	}

	if cond.Status != metav1.ConditionFalse {
		t.Errorf("GroupsReady = %s, want False", cond.Status)
	}

	live := getPool(t, cl, pool)

	live.Spec.Groups[0].Overrides = nil
	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("updating pool: %v", err)
	}

	reconcilePool(t, r, live)

	after := getPool(t, cl, pool)

	cond = conditionByType(after, ConditionGroupsReady)
	if cond == nil {
		t.Fatal("GroupsReady missing after fix")
	}

	if cond.Status != metav1.ConditionTrue {
		t.Errorf("GroupsReady = %s after fix, want True (reason=%s)", cond.Status, cond.Reason)
	}
}

// The pool-level version of the same idea: a template with no addressable GVK
// is not going to become addressable on a retry.
func TestReconcileMalformedGVKSetsCondition(t *testing.T) {
	pool := fakeTestPool()
	pool.Spec.WorkloadTemplate.Raw = []byte(`{"not":"valid"}`)

	r, cl := newFakeReconciler(t, nil, pool)

	err := tryReconcilePool(r, pool)
	if err != nil {
		t.Fatalf("malformed GVK should be terminal, got error: %v", err)
	}

	got := getPool(t, cl, pool)

	cond := conditionByType(got, ConditionGroupsReady)
	if cond == nil {
		t.Fatal("GroupsReady was not set for malformed GVK")
	}

	if cond.Status != metav1.ConditionFalse {
		t.Errorf("GroupsReady = %s, want False", cond.Status)
	}

	if cond.Reason != ReasonGroupSpecInvalid {
		t.Errorf("reason = %s, want %s", cond.Reason, ReasonGroupSpecInvalid)
	}
}

func TestSetConditionsTerminalReason(t *testing.T) {
	t.Run("all groups terminal", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{}
		setConditions(pool, conditionInputs{
			desired:        10,
			failedGroups:   []string{testGroupBase, testGroupSpot},
			terminalGroups: []string{testGroupBase, testGroupSpot},
		})

		got := conditionByType(pool, ConditionGroupsReady)
		if got == nil {
			t.Fatal("GroupsReady not set")
		}

		if got.Reason != ReasonGroupSpecInvalid {
			t.Errorf("reason = %s, want %s", got.Reason, ReasonGroupSpecInvalid)
		}

		for _, name := range []string{testGroupBase, testGroupSpot} {
			if !strings.Contains(got.Message, name) {
				t.Errorf("message %q does not name terminal group %s", got.Message, name)
			}
		}
	})

	t.Run("mixed terminal and retryable uses retryable reason", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{}
		setConditions(pool, conditionInputs{
			desired:        10,
			failedGroups:   []string{testGroupBase, testGroupSpot},
			terminalGroups: []string{testGroupBase},
		})

		got := conditionByType(pool, ConditionGroupsReady)
		if got == nil {
			t.Fatal("GroupsReady not set")
		}

		if got.Reason != ReasonGroupReconcileFailed {
			t.Errorf("reason = %s, want %s", got.Reason, ReasonGroupReconcileFailed)
		}
	})
}

// The counting trap. errs collects sweep failures too, and a failed orphan
// delete is never in terminalGroups -- so comparing counts rather than checking
// "no retryable group" is what keeps a destructive operation that failed from
// being swallowed alongside a spec error.
func TestOrphanDeleteFailureRetries(t *testing.T) {
	pool := fakeTestPool()
	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	live := getPool(t, cl, pool)
	breakGroup(live)

	live.Spec.Groups = live.Spec.Groups[:1]
	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("updating pool: %v", err)
	}

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.DeleteOption) error {
			return errors.New("simulated API server error")
		},
	})

	err := tryReconcilePool(r, live)
	if err == nil {
		t.Fatal("Reconcile returned nil, want error: orphan-delete failure must not be suppressed by terminal group errors")
	}

	if !strings.Contains(err.Error(), "orphaned") {
		t.Errorf("error %q should mention orphaned workloads", err)
	}
}
