package workload

import (
	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
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

// ComputeGroupTargets distributes total replicas across groups using their
// scaling constraints.
//
// Algorithm:
//  1. Satisfy cascade thresholds (floor) in list order, constrained by total.
//
// It is a pure function of its arguments, so it remains exhaustively testable
// without a cluster. Later phases distribute what the floors leave over.
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

	return DistributionResult{Targets: targets}
}
