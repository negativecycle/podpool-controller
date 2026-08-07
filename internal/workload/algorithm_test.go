package workload

import (
	"testing"

	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

func TestComputeGroupTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		total       int32
		groups      []podpoolsv1alpha1.GroupSpec
		wantTargets []int32
	}{
		{
			name:  "zero total",
			total: 0,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
			},
			wantTargets: []int32{0},
		},
		{
			name:        "no groups",
			total:       10,
			groups:      nil,
			wantTargets: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ComputeGroupTargets(tt.total, tt.groups)

			if tt.wantTargets == nil && result.Targets == nil {
				return
			}

			if len(result.Targets) != len(tt.wantTargets) {
				t.Fatalf("got %d targets, want %d", len(result.Targets), len(tt.wantTargets))
			}

			for i := range tt.wantTargets {
				if result.Targets[i] != tt.wantTargets[i] {
					t.Errorf("group %d: got %d, want %d (all targets=%v)", i, result.Targets[i], tt.wantTargets[i], result.Targets)
				}
			}
		})
	}
}
