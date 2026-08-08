package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// The gate under test here is per group. Every case pairs two groups, or two
// passes over one group, because a single group failing once cannot tell a
// per-group gate from a pool-level one: they agree on everything except which
// events a *neighbour's* movement releases.

// errFlaky is a plain error, which classifyGroupError sorts into the generic
// retryable class.
var errFlaky = errors.New("simulated transient apply failure")

// failApplyFor makes Apply fail with a retryable error while the switch is on,
// which puts the affected groups in the GroupReconcileFailed class.
func failApplyFor(t *testing.T, r *PodPoolReconciler, on *bool) {
	t.Helper()

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, ac runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			if *on {
				return errFlaky
			}

			return c.Apply(ctx, ac, opts...)
		},
	})
}

// adoptedByAnother plants a child at the name this pool's group would use,
// controlled by something else. reconcileWorkload refuses to touch it, which is
// the WorkloadNotOwned class.
func adoptedByAnother(t *testing.T, cl client.Client, name string) {
	t.Helper()

	if err := cl.Create(t.Context(), foreignDeployment(name, testNamespace)); err != nil {
		t.Fatalf("planting foreign child %s: %v", name, err)
	}
}

// The headline case.
//
// A group that is already failing retryably starts failing because another
// controller owns its child. The pool-level GroupsReady tuple does not move —
// same status, same reason, same failing group names — so a gate that reads it
// stays shut and "Refusing to manage group base" is never emitted. The state
// transition is real and is persisted to status.groups[].reason; only the
// announcement is lost.
func TestOwnershipConflictIsAnnouncedWhileAlreadyFailing(t *testing.T) {
	pool := singleGroupPool()
	rec := events.NewFakeRecorder(64)
	r, cl := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	failing := true
	failApplyFor(t, r, &failing)

	// Pass one: a retryable apply failure.
	_ = tryReconcilePool(r, pool)

	first := drainEvents(rec.Events)
	if n := countEventsByReason(first, ReasonGroupReconcileFailed); n != 1 {
		t.Fatalf("pass one: got %d %s events, want 1; events: %v", n, ReasonGroupReconcileFailed, first)
	}

	// Pass two: same group, same failing set, different class.
	adoptedByAnother(t, cl, pool.Name+"-"+testGroupBase)

	_ = tryReconcilePool(r, pool)

	second := drainEvents(rec.Events)
	if n := countEventsByReason(second, ReasonWorkloadNotOwned); n != 1 {
		t.Errorf("pass two: got %d %s events, want 1 — the ownership refusal was suppressed by an unchanged pool-level tuple; events: %v",
			n, ReasonWorkloadNotOwned, second)
	}
}

// The same suppression in the other direction, which is what shows the bug is
// about failure classes rather than about ownership specifically.
func TestGenericFailureIsAnnouncedAfterAnOwnershipConflict(t *testing.T) {
	pool := singleGroupPool()
	rec := events.NewFakeRecorder(64)
	r, cl := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	childName := pool.Name + "-" + testGroupBase
	adoptedByAnother(t, cl, childName)

	// Pass one: the child belongs to someone else.
	_ = tryReconcilePool(r, pool)

	first := drainEvents(rec.Events)
	if n := countEventsByReason(first, ReasonWorkloadNotOwned); n != 1 {
		t.Fatalf("pass one: got %d %s events, want 1; events: %v", n, ReasonWorkloadNotOwned, first)
	}

	// Pass two: the conflict is gone but applies are failing.
	if err := cl.Delete(t.Context(), foreignDeployment(childName, testNamespace)); err != nil {
		t.Fatalf("removing foreign child: %v", err)
	}

	failing := true
	failApplyFor(t, r, &failing)

	_ = tryReconcilePool(r, pool)

	second := drainEvents(rec.Events)
	if n := countEventsByReason(second, ReasonGroupReconcileFailed); n != 1 {
		t.Errorf("pass two: got %d %s events, want 1; events: %v", n, ReasonGroupReconcileFailed, second)
	}
}

// The property that keeps the per-group gate from regressing the anti-spam
// guarantee, and the reason the gate has to
// be per group in both directions rather than merely more sensitive.
//
// base is refused for ownership throughout. On pass two spot starts failing
// too, which grows the failing set and so moves the pool-level message. A
// pool-level gate takes that as licence to flush every buffered event and
// re-announces base's unchanged conflict. A per-group gate announces spot and
// leaves base alone.
func TestPerGroupGateDoesNotReEmitForAnUnchangedGroup(t *testing.T) {
	pool := fakeTestPool()
	rec := events.NewFakeRecorder(64)
	r, cl := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	// base is refused before any apply is attempted, so the interceptor below
	// only ever affects spot.
	adoptedByAnother(t, cl, pool.Name+"-"+testGroupBase)

	failing := false
	failApplyFor(t, r, &failing)

	// Pass one: base refused, spot reconciles cleanly.
	_ = tryReconcilePool(r, pool)

	first := drainEvents(rec.Events)
	if n := countEventsByReason(first, ReasonWorkloadNotOwned); n != 1 {
		t.Fatalf("pass one: got %d %s events, want 1; events: %v", n, ReasonWorkloadNotOwned, first)
	}

	// Pass two: base unchanged, spot newly failing in a different class.
	failing = true

	_ = tryReconcilePool(r, pool)

	second := drainEvents(rec.Events)

	if n := countEventsByReason(second, ReasonWorkloadNotOwned); n != 0 {
		t.Errorf("pass two: got %d %s events, want 0 — base has not changed class and a wider failing set is not news about base; events: %v",
			n, ReasonWorkloadNotOwned, second)
	}

	if n := countEventsByReason(second, ReasonGroupReconcileFailed); n != 1 {
		t.Errorf("pass two: got %d %s events, want 1 (spot); events: %v",
			n, ReasonGroupReconcileFailed, second)
	}
}

// TestGroupEventChangedTable pins the gate itself: the two rows a pool-level
// gate gets wrong, and the ones that keep the anti-spam property.
func TestGroupEventChangedTable(t *testing.T) {
	const group = testGroupBase

	withReason := func(reason string) []podpoolsv1alpha1.GroupStatus {
		return []podpoolsv1alpha1.GroupStatus{{Name: group, Reason: reason}}
	}

	tests := []struct {
		name     string
		previous []podpoolsv1alpha1.GroupStatus
		reason   string
		want     bool
	}{
		{
			// A group absent from the previous status has never reported, so
			// its first failure is always news.
			name:     "group absent from the previous status",
			previous: nil,
			reason:   ReasonGroupReconcileFailed,
			want:     true,
		},
		{
			name:     "a group under a different name is not this group",
			previous: []podpoolsv1alpha1.GroupStatus{{Name: testGroupSpot, Reason: ReasonWorkloadNotOwned}},
			reason:   ReasonWorkloadNotOwned,
			want:     true,
		},
		{
			// #54's property: an unchanging failure announces once.
			name:     "the same failure class repeats",
			previous: withReason(ReasonGroupReconcileFailed),
			reason:   ReasonGroupReconcileFailed,
			want:     false,
		},
		{
			name:     "the same ownership conflict repeats",
			previous: withReason(ReasonWorkloadNotOwned),
			reason:   ReasonWorkloadNotOwned,
			want:     false,
		},
		{
			// The two rows this commit exists for. Neither moves
			// the pool-level tuple, so both were silent before.
			name:     "a retryable failure becomes an ownership conflict",
			previous: withReason(ReasonGroupReconcileFailed),
			reason:   ReasonWorkloadNotOwned,
			want:     true,
		},
		{
			name:     "an ownership conflict becomes a retryable failure",
			previous: withReason(ReasonWorkloadNotOwned),
			reason:   ReasonGroupReconcileFailed,
			want:     true,
		},
		{
			name:     "a healthy group starts failing",
			previous: withReason(ReasonAllReplicasReady),
			reason:   ReasonGroupReconcileFailed,
			want:     true,
		},
		{
			// A group whose previous status carries no reason at all. Treated
			// as changed, which errs toward announcing rather than swallowing.
			name:     "the previous status carries no reason",
			previous: withReason(""),
			reason:   ReasonGroupReconcileFailed,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := groupEventChanged(tt.previous, group, tt.reason); got != tt.want {
				t.Errorf("groupEventChanged = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestFirstFailureAlwaysEmits guards the silent-failure mode.
//
// The gate compares against `before`, the deep copy taken at the top of
// Reconcile. Comparing against pool.Status.Groups instead would always find the
// reason equal to itself, silence every group event, and leave every
// "emits exactly one" test passing with zero. This is the assertion that
// notices.
func TestFirstFailureAlwaysEmits(t *testing.T) {
	pool := singleGroupPool()
	rec := events.NewFakeRecorder(64)
	r, cl := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	adoptedByAnother(t, cl, pool.Name+"-"+testGroupBase)

	_ = tryReconcilePool(r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, ReasonWorkloadNotOwned); n < 1 {
		t.Fatalf("the very first failure emitted nothing; the gate is comparing against this pass's own status. events: %v", evts)
	}
}

// groupsReady returns the GroupsReady condition a pool would publish for the
// given inputs.
func groupsReadyFor(in conditionInputs) *metav1.Condition {
	pool := &podpoolsv1alpha1.PodPool{}
	setConditions(pool, in)

	return meta.FindStatusCondition(pool.Status.Conditions, ConditionGroupsReady)
}

// The condition's half of the same idea.
//
// "Failed to reconcile group(s): base" describes a retry, not a refusal. An
// operator reading it has no reason to suspect another controller owns the
// object, which is the one failure class they cannot fix by waiting.
func TestGroupsReadyNamesAnOwnershipConflict(t *testing.T) {
	tests := []struct {
		name         string
		in           conditionInputs
		wantReason   string
		wantInMsg    []string
		wantNotInMsg []string
	}{
		{
			name:       "no failures",
			in:         conditionInputs{},
			wantReason: ReasonAllGroupsReconciled,
		},
		{
			name: "an ownership conflict is named",
			in: conditionInputs{
				failedGroups:   []string{testGroupBase},
				notOwnedGroups: []string{testGroupBase},
			},
			wantReason: ReasonWorkloadNotOwned,
			wantInMsg:  []string{testGroupBase, "another controller"},
		},
		{
			// The ordering trade-off, pinned. Ownership outranks a spec error
			// because it points at another actor in the cluster, but the spec
			// error must not vanish from the summary. The trade-off is that it
			// is demoted to a count; notOwnedMessage says how many so the
			// summary does not imply ownership is the only problem.
			name: "ownership outranks a spec error and still counts it",
			in: conditionInputs{
				failedGroups:   []string{testGroupBase, testGroupSpot},
				terminalGroups: []string{testGroupSpot},
				notOwnedGroups: []string{testGroupBase},
			},
			wantReason: ReasonWorkloadNotOwned,
			wantInMsg:  []string{testGroupBase, "1 other group"},
		},
		{
			// One conflict and nothing else wrong: no misleading tail.
			name: "a lone conflict does not claim other failures",
			in: conditionInputs{
				failedGroups:   []string{testGroupBase},
				notOwnedGroups: []string{testGroupBase},
			},
			wantReason:   ReasonWorkloadNotOwned,
			wantNotInMsg: []string{"other group"},
		},
		{
			// Unchanged: the ordinary retryable class keeps its reason.
			name: "a retryable failure is unchanged",
			in: conditionInputs{
				failedGroups: []string{testGroupBase},
			},
			wantReason: ReasonGroupReconcileFailed,
		},
		{
			name: "an all-terminal pool reports the spec error",
			in: conditionInputs{
				failedGroups:   []string{testGroupBase},
				terminalGroups: []string{testGroupBase},
			},
			wantReason: ReasonGroupSpecInvalid,
		},
		{
			// The pool's own template is broken, so no group name is invented.
			name:       "a pool-level failure outranks everything",
			in:         conditionInputs{poolInvalid: true, notOwnedGroups: []string{testGroupBase}},
			wantReason: ReasonGroupSpecInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := groupsReadyFor(tt.in)
			if got == nil {
				t.Fatal("no GroupsReady condition was published")
			}

			if got.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q (message: %q)", got.Reason, tt.wantReason, got.Message)
			}

			for _, want := range tt.wantInMsg {
				if !strings.Contains(got.Message, want) {
					t.Errorf("message %q does not mention %q", got.Message, want)
				}
			}

			for _, unwanted := range tt.wantNotInMsg {
				if strings.Contains(got.Message, unwanted) {
					t.Errorf("message %q should not mention %q", got.Message, unwanted)
				}
			}
		})
	}
}

// The condition has to reflect a real reconcile, not just a hand-built input
// struct: it is the wiring from classifyGroupError through to setConditions
// that matters, and a table over conditionInputs alone would not exercise it.
func TestOwnershipConflictReachesTheCondition(t *testing.T) {
	pool := singleGroupPool()
	r, cl := newFakeReconciler(t, nil, pool)
	r.Recorder = events.NewFakeRecorder(64)

	adoptedByAnother(t, cl, pool.Name+"-"+testGroupBase)

	_ = tryReconcilePool(r, pool)

	live := getPool(t, cl, pool)

	got := meta.FindStatusCondition(live.Status.Conditions, ConditionGroupsReady)
	if got == nil {
		t.Fatal("no GroupsReady condition was published")
	}

	if got.Reason != ReasonWorkloadNotOwned {
		t.Errorf("GroupsReady reason = %q, want %q (message: %q)",
			got.Reason, ReasonWorkloadNotOwned, got.Message)
	}
}
