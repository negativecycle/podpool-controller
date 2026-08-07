package workload

import (
	"strconv"
	"strings"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type DistributionResult struct {
	Targets        []int32
	TargetDegraded bool

	// Unplaced counts replicas that no group's ceiling could accept. Non-zero
	// only when every group is capped; see GroupCeiling. The pool then runs
	// below spec.replicas rather than breaching a ceiling the user set, and
	// says so through status.unplacedReplicas.
	Unplaced int32
}

// GroupFloor returns the cascade threshold (min) for a group, defaulting to 0.
func GroupFloor(s podpoolsv1alpha1.ScalingConstraints) int32 {
	if s.Min != nil {
		return *s.Min
	}

	return 0
}

// TargetPercent extracts the percentage value from a Target field. Returns the
// percentage (1–100) and true, or 0 and false if the target is nil or invalid.
func TargetPercent(t *intstr.IntOrString) (int32, bool) {
	if t == nil || t.Type != intstr.String {
		return 0, false
	}

	str := strings.TrimSuffix(t.StrVal, "%")

	pct, err := strconv.ParseInt(str, 10, 32)
	if err != nil || pct < 1 || pct > 100 {
		return 0, false
	}

	return int32(pct), true
}

// GroupTarget returns the target for a group and whether one exists. The
// rounding direction follows the ceiling rule: round down when the target
// is itself the ceiling (no max), round up when the ceiling is elsewhere
// (max is set) and the target acts as a soft floor.
func GroupTarget(total int32, s podpoolsv1alpha1.ScalingConstraints) (int32, bool) {
	pct, ok := TargetPercent(s.Target)
	if !ok {
		return 0, false
	}

	var t int32
	if s.Max != nil {
		t = ceilDiv64(int64(total)*int64(pct), 100)
		t = min(t, *s.Max)
	} else {
		t = percentOf(total, pct)
	}

	return t, true
}

// ComputeGroupTargets distributes total replicas across groups using their
// scaling constraints.
//
// Algorithm:
//  1. Satisfy cascade thresholds (floor) in list order, constrained by total.
//  2. Chase percentage-based targets for groups that have one.
//  3. Distribute the remainder in list order, up to each group's ceiling.
//
// Anything still unplaced after the overflow phase stays unplaced. Ceilings
// are honoured absolutely: a user who put a limit on every tier meant it, and
// quietly overspending on the expensive one is the failure this controller
// exists to prevent.
//
// It is a pure function of its arguments, so it remains exhaustively testable
// without a cluster.
func ComputeGroupTargets(
	total int32,
	groups []podpoolsv1alpha1.GroupSpec,
) DistributionResult {
	n := len(groups)
	if n == 0 {
		return DistributionResult{}
	}

	targets := make([]int32, n)

	if total <= 0 {
		return DistributionResult{Targets: targets}
	}

	remaining := total

	// Phase 1: satisfy cascade thresholds (floor) in list order.
	for i, g := range groups {
		floor := GroupFloor(g.Scaling)
		if floor > 0 {
			t := min(floor, remaining)
			targets[i] = t
			remaining -= t
		}
	}

	if remaining <= 0 {
		return DistributionResult{
			Targets:        targets,
			TargetDegraded: checkTargetDegraded(total, targets, groups),
		}
	}

	// Phase 2: chase percentage-based targets.
	for i, g := range groups {
		target, ok := GroupTarget(total, g.Scaling)
		if !ok {
			continue
		}

		additional := target - targets[i]
		if additional <= 0 {
			continue
		}

		give := min(additional, remaining)
		targets[i] += give
		remaining -= give

		if remaining <= 0 {
			break
		}
	}

	// The overflow phase: distribute the remainder in list order, respecting
	// ceilings.
	//
	// Earlier groups are filled first, so the overflow lands on the tier the
	// user ranked highest: the same cascade as phase 1.
	for i := range groups {
		if remaining <= 0 {
			break
		}

		limit, bounded := GroupCeiling(total, groups[i].Scaling)
		if !bounded {
			targets[i] += remaining
			remaining = 0

			continue
		}

		headroom := limit - targets[i]
		if headroom <= 0 {
			continue
		}

		give := min(headroom, remaining)
		targets[i] += give
		remaining -= give
	}

	return DistributionResult{
		Targets:        targets,
		TargetDegraded: checkTargetDegraded(total, targets, groups),
		Unplaced:       remaining,
	}
}

// targetTolerancePct is the slack allowed before a target counts as violated.
//
// Pods are integers and percentages are not, so a distribution that is as
// correct as it can be still lands a fraction of a point off. One pod out of
// three is 33.33%, and a target of 33% must not read as violated. Half a
// percentage point is below the granularity of any pool small enough for
// rounding to matter, and negligible on pools large enough that it is not.
const targetTolerancePct = 0.5

// checkTargetDegraded reports whether any target constraint ended up violated
// by a hard bound.
//
// When max is absent the target is itself the ceiling, and only a min larger
// than the target can push the group above it. When max is present the target
// is a soft floor, and only the max can pin the group below it.
func checkTargetDegraded(total int32, targets []int32, groups []podpoolsv1alpha1.GroupSpec) bool {
	if total <= 0 {
		return false
	}

	for i, g := range groups {
		s := g.Scaling

		pct, ok := TargetPercent(s.Target)
		if !ok {
			continue
		}

		// Float on purpose. This asks "how far is the outcome from the declared
		// percentage", a tolerance check, not "how many pods is the percentage",
		// a sizing rule. Rewriting it in the integer percentOf shape was tried
		// and refuted with counterexamples: integer truncation eats the
		// sub-point differences this tolerance exists to measure.
		actualPct := float64(targets[i]) / float64(total) * 100.0
		targetPct := float64(pct)

		if s.Max == nil && actualPct > targetPct+targetTolerancePct {
			return true
		}

		if s.Max != nil && actualPct < targetPct-targetTolerancePct {
			return true
		}
	}

	return false
}

// GroupCeiling reports the largest target a group may hold, and whether it is
// bounded at all.
//
// The ceiling is max if set, otherwise the target itself (resolved at the
// current total). A group with neither is unbounded: the overflow bucket. That
// case must stay exactly as broad as it is — an absent target is a deliberate
// statement that this tier absorbs what the others cannot, and the whole
// overflow design rests on it.
func GroupCeiling(total int32, s podpoolsv1alpha1.ScalingConstraints) (limit int32, bounded bool) {
	if s.Max != nil {
		return *s.Max, true
	}

	pct, ok := TargetPercent(s.Target)
	if ok {
		return percentOf(total, pct), true
	}

	return 0, false
}

// percentOf returns total × pct / 100 rounded down.
func percentOf(total, pct int32) int32 {
	return int32(int64(total) * int64(pct) / 100) //nolint:gosec // pct ≤ 100, so product/100 ≤ total ≤ MaxInt32
}

// ceilDiv64 divides rounding up, for non-negative operands.
func ceilDiv64(a, b int64) int32 {
	return int32((a + b - 1) / b) //nolint:gosec // callers pass a ≤ int64(MaxInt32)×100 and b=100, so the quotient fits int32
}
