package controller

import (
	"fmt"
	"math"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

const (
	ConditionAvailable      = "Available"
	ConditionProgressing    = "Progressing"
	ConditionTargetDegraded = "TargetDegraded"
	ConditionGroupsReady    = "GroupsReady"
	ConditionReady          = "Ready"

	ReasonScaledToZero               = "ScaledToZero"
	ReasonMinimumReplicasAvailable   = "MinimumReplicasAvailable"
	ReasonNoReplicasAvailable        = "NoReplicasAvailable"
	ReasonReplicasUpdating           = "ReplicasUpdating"
	ReasonAllReplicasReady           = "AllReplicasReady"
	ReasonAbsoluteConstraintOverride = "AbsoluteConstraintOverride"
	ReasonCeilingsBelowDesired       = "CeilingsBelowDesired"
	ReasonTargetsSatisfied           = "TargetsSatisfied"
	ReasonAllGroupsReconciled        = "AllGroupsReconciled"
	ReasonGroupReconcileFailed       = "GroupReconcileFailed"
	ReasonGroupSpecInvalid           = "GroupSpecInvalid"
	ReasonWorkloadNotOwned           = "WorkloadNotOwned"
	ReasonProgressDeadlineExceeded   = "ProgressDeadlineExceeded"
	ReasonPoolReady                  = "PoolReady"
	ReasonWorkloadUpdating           = "WorkloadUpdating"
	ReasonWorkloadTemplateInvalid    = "WorkloadTemplateInvalid"
	ReasonPaused                     = "Paused"
	// Used as both a condition reason and an event reason, from one constant so
	// the two cannot drift.
	ReasonWatchSetupFailed = "WatchSetupFailed"
)

// retiredConditionTypes are condition types this controller used to publish
// and no longer does. They are removed from status on every reconcile, because
// meta.SetStatusCondition can only upsert: nothing else in the write path can
// express a deletion, so a pool stored before a type was retired would carry
// it forever and keep reporting a signal nobody computes.
//
// The list is empty today, and building the mechanism before the first rename
// needs it is the point. Renaming a condition type is not a refactor; it is a
// change to a persisted, user-visible API surface, and it needs the same
// treatment as renaming a status field: write the new one, retire the old one,
// clean up what the old one left behind. Retire-the-old is the missing
// two-thirds of every rename, and by the time it is needed the person doing
// the rename is thinking about the new name, not the stored objects.
//
// This is deliberately a list of our own retired names rather than "prune
// anything outside the set we publish". The conditions array is a shared,
// extensible contract: kstatus, policy engines, and GitOps tooling read it and
// some write to it. Deleting another actor's condition on every reconcile is a
// far worse bug than leaving one stale entry, and nearly undiagnosable from
// the other side. TestForeignConditionIsNotPruned exists to fail loudly if
// anyone later "simplifies" this into an allow-list.
var retiredConditionTypes = []string{}

type conditionInputs struct {
	targetDegraded bool
	unplaced       int32
	ready          int32
	desired        int32
	failedGroups   []string
	stalledGroups  []string

	// notOwnedGroups are groups whose child exists but is controlled by
	// something else. Kept separate from terminalGroups because the failure is
	// not terminal (deleting the conflicting object resolves it) and not the
	// ordinary retryable class either: it is the one failure that means the
	// controller is deliberately refusing to write.
	notOwnedGroups []string

	// terminalGroups is the subset of failedGroups whose failure cannot
	// succeed without a spec change. A subset, not a partition: the arithmetic
	// below compares its length against failedGroups' to decide whether the
	// whole pool is waiting on a human.
	terminalGroups []string

	// poolInvalid means the failure is the pool's own (e.g. an unparseable
	// workloadTemplate), not any group's. It makes GroupsReady report the pool
	// without inventing a group name.
	poolInvalid bool

	// watchFailed means the pool's spec parsed but no informer could be
	// established for the workload GVK, so nothing below that point ran. It
	// exists so the watch-failure exit leaves an honest Ready condition rather
	// than whatever the previous pass wrote. It does not stamp
	// ObservedGeneration.
	watchFailed bool
}

// haltedConditions reports a pool that stopped before doing any work, leaving
// Available, TargetDegraded and GroupsReady at whatever they last said: this
// pass learned nothing about them, and overwriting them with guesses would be
// worse than leaving them stale.
//
// The conditions carry the generation last acted on rather than the current
// one, because this pass acted on nothing. That is the property worth having
// a named function for. Inline, the next exit to be added would copy these
// lines and quietly disagree about the generation.
func haltedConditions(pool *podpoolsv1alpha1.PodPool, reason, message string) {
	for _, t := range []string{ConditionProgressing, ConditionReady} {
		meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
			Type:               t,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: pool.Status.ObservedGeneration,
		})
	}
}

// setConditions publishes the standard conditions and stamps
// ObservedGeneration. ObservedGeneration is set here, not in Reconcile,
// so the two can never disagree: a top-level observedGeneration beside
// conditions stamped from a different generation would be a worse signal
// than having neither.
func setConditions(pool *podpoolsv1alpha1.PodPool, in conditionInputs) {
	gen := pool.Generation

	// Above every SetStatusCondition call, so a retired type can never be
	// pruned and re-added in the same pass. That cannot happen today, since a
	// retired type is by definition not in the live set, but the ordering
	// makes it structural rather than incidental. Here rather than in
	// Reconcile because conditions have one writer.
	for _, t := range retiredConditionTypes {
		meta.RemoveStatusCondition(&pool.Status.Conditions, t)
	}

	// Deliberately above the stamp. observedGeneration == generation is the
	// API's way of saying the controller has acted on this spec, and no
	// informer means nothing below the watch exit ran, so it has not.
	//
	// Stamping here would also break the recovery. Reconcile derives
	// genChanged from the stored observedGeneration and stampGroupProgress
	// uses it to restart LastProgressTime; stamping during an outage makes
	// genChanged false when the CRD comes back, so the progress deadline is
	// measured from before the outage and a long one fails the rollout that
	// follows it.
	if in.watchFailed {
		haltedConditions(pool, ReasonWatchSetupFailed,
			"No watch could be established for the workload type")

		return
	}

	pool.Status.ObservedGeneration = gen

	available := metav1.Condition{
		Type:               ConditionAvailable,
		ObservedGeneration: gen,
	}

	switch {
	case in.desired == 0:
		available.Status = metav1.ConditionTrue
		available.Reason = ReasonScaledToZero
		available.Message = "Pool is scaled to zero replicas"
	case in.ready > 0:
		available.Status = metav1.ConditionTrue
		available.Reason = ReasonMinimumReplicasAvailable
		available.Message = fmt.Sprintf("%d/%d replicas ready", in.ready, in.desired)
	default:
		available.Status = metav1.ConditionFalse
		available.Reason = ReasonNoReplicasAvailable
		available.Message = fmt.Sprintf("0/%d replicas are ready", in.desired)
	}

	progressing := metav1.Condition{
		Type:               ConditionProgressing,
		ObservedGeneration: gen,
	}

	switch {
	case in.unplaced > 0:
		progressing.Status = metav1.ConditionFalse
		progressing.Reason = ReasonCeilingsBelowDesired
		progressing.Message = fmt.Sprintf(
			"%d replicas are permanently unplaced: every group is capped",
			in.unplaced)
	case len(in.stalledGroups) > 0:
		progressing.Status = metav1.ConditionFalse
		progressing.Reason = ReasonProgressDeadlineExceeded
		progressing.Message = fmt.Sprintf(
			"Group(s) %s exceeded progress deadline",
			formatGroupNames(in.stalledGroups))
	case in.ready < in.desired:
		progressing.Status = metav1.ConditionTrue
		progressing.Reason = ReasonReplicasUpdating
		progressing.Message = fmt.Sprintf("%d/%d replicas ready", in.ready, in.desired)
	default:
		progressing.Status = metav1.ConditionFalse
		progressing.Reason = ReasonAllReplicasReady
		progressing.Message = "All replicas are up to date"
	}

	degraded := metav1.Condition{
		Type:               ConditionTargetDegraded,
		ObservedGeneration: gen,
	}

	switch {
	case in.unplaced > 0:
		degraded.Status = metav1.ConditionTrue
		degraded.Reason = ReasonCeilingsBelowDesired
		degraded.Message = fmt.Sprintf(
			"%d/%d replicas are unplaced: every group has a max or target, and the ceilings do not reach spec.replicas",
			in.unplaced, in.desired)
	case in.targetDegraded:
		degraded.Status = metav1.ConditionTrue
		degraded.Reason = ReasonAbsoluteConstraintOverride
		degraded.Message = "An absolute min or max is holding a group away from its target"
	default:
		degraded.Status = metav1.ConditionFalse
		degraded.Reason = ReasonTargetsSatisfied
		degraded.Message = "All target constraints are satisfied"
	}

	groupsReady := metav1.Condition{
		Type:               ConditionGroupsReady,
		ObservedGeneration: gen,
	}

	switch {
	case in.poolInvalid:
		groupsReady.Status = metav1.ConditionFalse
		groupsReady.Reason = ReasonGroupSpecInvalid
		groupsReady.Message = "The pool's workloadTemplate is invalid; no group could be reconciled"
	case len(in.failedGroups) == 0:
		groupsReady.Status = metav1.ConditionTrue
		groupsReady.Reason = ReasonAllGroupsReconciled
		groupsReady.Message = "All groups reconciled"
	// Above the terminal arm, though nothing today can reach both: an
	// ownership conflict is never terminal, so the two sets are disjoint and
	// this ordering is defensive rather than load-bearing. What actually
	// produces the behaviour worth having is the arm below: a pool with one
	// not-owned group and one spec-invalid group reports the ownership
	// conflict, because the terminal arm requires EVERY failure to be terminal
	// and this pool has two classes. The spec error is demoted to a count, and
	// notOwnedMessage says how many so the summary does not imply ownership is
	// the only problem.
	case len(in.notOwnedGroups) > 0:
		groupsReady.Status = metav1.ConditionFalse
		groupsReady.Reason = ReasonWorkloadNotOwned
		groupsReady.Message = notOwnedMessage(in.notOwnedGroups, in.failedGroups)
	// Only when EVERY failure is terminal. One retryable failure among them
	// means the pool may still resolve itself, and saying "requires a change"
	// would send an operator to edit a spec that is not the problem.
	case len(in.terminalGroups) == len(in.failedGroups):
		groupsReady.Status = metav1.ConditionFalse
		groupsReady.Reason = ReasonGroupSpecInvalid
		groupsReady.Message = fmt.Sprintf(
			"Group(s) %s have spec errors that require a change to resolve",
			strings.Join(in.terminalGroups, ", "))
	default:
		groupsReady.Status = metav1.ConditionFalse
		groupsReady.Reason = ReasonGroupReconcileFailed
		groupsReady.Message = "Failed to reconcile group(s): " + strings.Join(in.failedGroups, ", ")
	}

	meta.SetStatusCondition(&pool.Status.Conditions, available)
	meta.SetStatusCondition(&pool.Status.Conditions, progressing)
	meta.SetStatusCondition(&pool.Status.Conditions, degraded)
	meta.SetStatusCondition(&pool.Status.Conditions, groupsReady)
	meta.SetStatusCondition(&pool.Status.Conditions, summaryReady(
		gen, in.ready, in.desired, in.unplaced, in.failedGroups, in.terminalGroups, in.stalledGroups))
}

const readyMessageBudget = 60

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}

	if maxLen <= 3 {
		return s[:maxLen]
	}

	return s[:maxLen-3] + "..."
}

// formatGroupNamesBudgeted is used only by summaryReady, where the message is
// projected into a kubectl column. It truncates each name to keep the assembled
// message readable and includes a total count so the operator knows how many
// groups are affected even when names are elided.
func formatGroupNamesBudgeted(groups []string) string {
	const maxNameLen = 20

	if len(groups) == 1 {
		return truncate(groups[0], maxNameLen)
	}

	return fmt.Sprintf("%s +%d more (%d total)", truncate(groups[0], maxNameLen), len(groups)-1, len(groups))
}

// summaryReady collapses the four detail conditions into the single
// question "is this pool doing what its spec asked?". The switch is a
// precedence table, ordered most-serious-first: a new state is one row,
// not another branch. Messages are deliberately short: this condition is
// projected into a kubectl print column where anything over ~60
// characters is unreadable.
func summaryReady(gen int64, ready, desired, unplaced int32, failedGroups, terminalGroups, stalledGroups []string) metav1.Condition {
	mk := func(status metav1.ConditionStatus, reason, message string) metav1.Condition {
		return metav1.Condition{
			Type:               ConditionReady,
			Status:             status,
			Reason:             reason,
			Message:            truncate(message, readyMessageBudget),
			ObservedGeneration: gen,
		}
	}

	switch {
	case desired == 0:
		return mk(metav1.ConditionTrue, ReasonScaledToZero, "Scaled to zero")
	// Above the generic failure arm: the print column has room for one verdict,
	// and "needs an edit" is more actionable than "failed".
	case len(terminalGroups) > 0:
		return mk(metav1.ConditionFalse, ReasonGroupSpecInvalid,
			"Group spec invalid: "+formatGroupNamesBudgeted(terminalGroups))
	case len(failedGroups) > 0:
		return mk(metav1.ConditionFalse, ReasonGroupReconcileFailed,
			"Group reconcile failed: "+formatGroupNamesBudgeted(failedGroups))
	case unplaced > 0:
		return mk(metav1.ConditionFalse, ReasonCeilingsBelowDesired,
			fmt.Sprintf("%d/%d unplaced by group ceilings", unplaced, desired))
	case ready == 0:
		return mk(metav1.ConditionFalse, ReasonNoReplicasAvailable,
			fmt.Sprintf("0/%d ready", desired))
	case len(stalledGroups) > 0:
		return mk(metav1.ConditionFalse, ReasonProgressDeadlineExceeded,
			"Stalled: "+formatGroupNamesBudgeted(stalledGroups))
	case ready < desired:
		return mk(metav1.ConditionFalse, ReasonReplicasUpdating,
			fmt.Sprintf("%d/%d ready", ready, desired))
	default:
		return mk(metav1.ConditionTrue, ReasonPoolReady,
			fmt.Sprintf("%d/%d ready", ready, desired))
	}
}

// clampInt32 narrows a widened accumulator back to the int32 the API stores.
//
// Callers sum group counts in int64 precisely so the sum is representable
// before it is narrowed here deliberately, rather than wrapping silently at
// the point of addition.
func clampInt32(v int64) int32 {
	if v < 0 {
		return 0
	}

	if v > math.MaxInt32 {
		return math.MaxInt32
	}

	return int32(v)
}

// notOwnedMessage summarises an ownership conflict.
//
// "Failed to reconcile group(s): base" is a poor description of "another
// controller owns that workload and we are refusing to touch it", which is a
// deliberate safety refusal rather than an error. When other groups failed for
// unrelated reasons it says how many, so promoting ownership to the headline
// does not read as a claim that nothing else is wrong.
func notOwnedMessage(notOwned, failed []string) string {
	msg := fmt.Sprintf("Group(s) %s are managed by another controller",
		formatGroupNames(notOwned))

	if others := len(failed) - len(notOwned); others > 0 {
		msg += fmt.Sprintf("; %d other group(s) also failed", others)
	}

	return msg
}

func formatGroupNames(groups []string) string {
	switch len(groups) {
	case 1:
		return groups[0]
	case 2:
		return groups[0] + ", " + groups[1]
	default:
		return fmt.Sprintf("%s, %s +%d more", groups[0], groups[1], len(groups)-2)
	}
}

// shareOfTotal returns replicas as a whole-number percentage of total, clamped
// to [0,100]. The clamp is load-bearing: total is the sum of child-reported
// status.replicas, and a CRD workload's status is written by its own controller
// against whatever schema that CRD declares. A negative or absurd count would
// otherwise reach an int32(float64) conversion whose result the Go spec says is
// implementation-dependent when the value does not fit. No nolint:gosec needed
// because the replicas >= total early return keeps the final conversion provably
// in range.
func shareOfTotal(replicas, total int32) int32 {
	if total <= 0 || replicas <= 0 {
		return 0
	}

	if replicas >= total {
		return 100
	}

	return int32(float64(replicas) / float64(total) * 100)
}
