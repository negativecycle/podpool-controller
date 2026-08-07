package workload

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

func pctTarget(pct int32) *intstr.IntOrString {
	v := intstr.FromString(fmt.Sprintf("%d%%", pct))

	return &v
}

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
		// Two groups: base(min:3) + burst(min:0, target:70%)
		{
			name:  "two groups total=1",
			total: 1,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)}},
			},
			wantTargets: []int32{1, 0},
		},
		{
			name:  "two groups total=3 — base min satisfied",
			total: 3,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)}},
			},
			wantTargets: []int32{3, 0},
		},
		{
			name:        "three groups total=3 — all in base, no target violations (target is ceiling)",
			total:       3,
			groups:      threeGroupSpec(),
			wantTargets: []int32{3, 0, 0},
		},
		{
			name:  "total less than sum of mins — earlier groups filled first",
			total: 2,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2), Target: pctTarget(50)}},
			},
			wantTargets: []int32{2, 0},
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

func threeGroupSpec() []podpoolsv1alpha1.GroupSpec {
	return []podpoolsv1alpha1.GroupSpec{
		{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
		{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(30)}},
		{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(50)}},
	}
}
