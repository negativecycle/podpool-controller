package workload

import (
	"strconv"
	"strings"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

type DistributionResult struct {
	Targets []int32
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
//
// It is a pure function of its arguments, so it remains exhaustively testable
// without a cluster. Later phases distribute what the targets leave over.
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
		return DistributionResult{Targets: targets}
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

	return DistributionResult{Targets: targets}
}

// percentOf returns total × pct / 100 rounded down.
func percentOf(total, pct int32) int32 {
	return int32(int64(total) * int64(pct) / 100) //nolint:gosec // pct ≤ 100, so product/100 ≤ total ≤ MaxInt32
}

// ceilDiv64 divides rounding up, for non-negative operands.
func ceilDiv64(a, b int64) int32 {
	return int32((a + b - 1) / b) //nolint:gosec // callers pass a ≤ int64(MaxInt32)×100 and b=100, so the quotient fits int32
}
