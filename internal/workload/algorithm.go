package workload

import (
	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

type DistributionResult struct {
	Targets []int32
}

// ComputeGroupTargets distributes total replicas across groups using their
// scaling constraints.
//
// It is a pure function of its arguments, so it remains exhaustively testable
// without a cluster. The distribution phases land one at a time in the commits
// that follow; with none in place yet, every group receives zero.
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

	return DistributionResult{Targets: targets}
}
