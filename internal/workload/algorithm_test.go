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
			name:  "two groups total=5 — burst gets floor(5*0.70)=3, capped by remaining after min",
			total: 5,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)}},
			},
			wantTargets: []int32{3, 2},
		},
		{
			name:  "two groups total=10 — burst at cap",
			total: 10,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)}},
			},
			wantTargets: []int32{3, 7},
		},
		{
			name:        "three groups total=10",
			total:       10,
			groups:      threeGroupSpec(),
			wantTargets: []int32{3, 3, 4},
		},
		// max + target (round-up shape)
		{
			name:  "max+target at low scale — target enforced",
			total: 5,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Max: ptr.To[int32](5), Target: pctTarget(30)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)}},
			},
			wantTargets: []int32{2, 3},
		},
		{
			// Phase 2 hands out targets in list order and breaks the
			// moment `remaining` hits zero, so a later group can be left short
			// of its target purely by position. Consistent with the documented
			// cascade.
			//
			// total=10: base takes min 8, leaving 2. scav wants floor(10x0.30)
			// = 3 but only 2 remain, so it takes both and burst — which also
			// wants 5 — gets nothing.
			name:  "phase 2 break boundary: later groups lose by list position",
			total: 10,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](8)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(30)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(50)}},
			},
			wantTargets: []int32{8, 2, 0},
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

// TestRoundingMatchesLegacyPolarity sweeps a matrix of totals and targets for
// both shapes, asserting that the GroupTarget helper returns the value the
// inline arithmetic would. This settles the rounding question empirically
// rather than by argument: if a future refactor changes the rounding
// direction, exactly the right subset of this matrix goes red.
func TestRoundingMatchesLegacyPolarity(t *testing.T) {
	t.Parallel()

	pcts := []int32{1, 30, 33, 50, 70, 99, 100}

	for total := range int32(101) {
		for _, pct := range pcts {
			// target without max: truncating division (round down).
			s := podpoolsv1alpha1.ScalingConstraints{
				Min:    ptr.To[int32](0),
				Target: pctTarget(pct),
			}
			got, ok := GroupTarget(total, s)

			want := int32(int64(total) * int64(pct) / 100)
			if !ok {
				t.Errorf("total=%d target=%d%%: GroupTarget returned ok=false", total, pct)
			} else if got != want {
				t.Errorf("total=%d target=%d%%: GroupTarget=%d, legacy=%d", total, pct, got, want)
			}

			// target with max: ceiling division (round up), capped.
			maxVal := int32(50)
			s2 := podpoolsv1alpha1.ScalingConstraints{
				Max:    ptr.To[int32](maxVal),
				Target: pctTarget(pct),
			}
			got2, ok2 := GroupTarget(total, s2)

			want2 := min(int32((int64(total)*int64(pct)+99)/100), maxVal)
			if !ok2 {
				t.Errorf("total=%d target=%d%% max=%d: GroupTarget returned ok=false", total, pct, maxVal)
			} else if got2 != want2 {
				t.Errorf("total=%d target=%d%% max=%d: GroupTarget=%d, legacy=%d", total, pct, maxVal, got2, want2)
			}
		}
	}
}
