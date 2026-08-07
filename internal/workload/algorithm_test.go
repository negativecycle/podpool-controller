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
		name         string
		total        int32
		groups       []podpoolsv1alpha1.GroupSpec
		wantTargets  []int32
		wantDegraded bool
		wantUnplaced int32
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
		{
			name:  "single group min only",
			total: 5,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
			},
			wantTargets: []int32{5},
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
		{
			name:        "three groups total=20 — near steady state",
			total:       20,
			groups:      threeGroupSpec(),
			wantTargets: []int32{4, 6, 10},
		},
		{
			name:        "three groups total=30 — steady state 20/30/50",
			total:       30,
			groups:      threeGroupSpec(),
			wantTargets: []int32{6, 9, 15},
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
			name:  "max+target at high scale — max dominates, target violated",
			total: 20,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Max: ptr.To[int32](5), Target: pctTarget(30)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)}},
			},
			// group 0 is capped at max=5 (25%, below its target of 30%).
			// group 1's ceiling is floor(20 x 0.70) = 14, so the 20th replica
			// has nowhere legal to go and stays unplaced.
			wantTargets:  []int32{5, 14},
			wantDegraded: true,
			wantUnplaced: 1,
		},
		{
			// A min larger than the target allows is the one remaining way to
			// exceed a target, and it is deliberate: absolute beats percentage.
			// 3 of 4 is 75%, well past the 50% ceiling, so TargetDegraded fires
			// while nothing is unplaced.
			name:  "an absolute min may still exceed a target",
			total: 4,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3), Target: pctTarget(50)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
			wantTargets:  []int32{3, 1},
			wantDegraded: true,
		},
		{
			name:  "high replica count does not overflow int32 arithmetic",
			total: 25_000_000,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Max: ptr.To[int32](5_000_000), Target: pctTarget(30)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)}},
			},
			wantTargets:  []int32{5_000_000, 17_500_000},
			wantDegraded: true,
			wantUnplaced: 2_500_000,
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
		{
			// The canonical on-demand/spot split, and the reason this shape is
			// pinned: a group capped at 50% must not take 100% of the pods
			// while the uncapped group meant to absorb them gets none, putting
			// every replica on the expensive tier.
			name:  "capped first group does not swallow the overflow",
			total: 10,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(50)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
			wantTargets: []int32{5, 5},
		},
		{
			// Every group capped and the ceilings sum to 70%, so 3 replicas
			// have nowhere legal to go. They stay unplaced rather than being
			// forced onto a tier the user limited on purpose.
			name:  "all groups capped below 100% leaves the surplus unplaced",
			total: 10,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(20)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(50)}},
			},
			wantTargets:  []int32{2, 5},
			wantUnplaced: 3,
		},
		{
			// Mirrors the e2e lifecycle fixture: base(min=3, target=50%) +
			// burst(min=0). At total=6 base sits at its min which equals its
			// target ceiling; burst absorbs the rest.
			name:  "e2e fixture at initial scale",
			total: 6,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3), Target: pctTarget(50)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
			wantTargets: []int32{3, 3},
		},
		{
			// Same fixture scaled to 10: base grows to its 50% ceiling, burst
			// absorbs the overflow.
			name:  "e2e fixture at scaled total",
			total: 10,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3), Target: pctTarget(50)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
			wantTargets: []int32{5, 5},
		},
		{
			// An uncapped group anywhere in the list absorbs everything, so
			// this shape must stay correct.
			name:  "an uncapped group still absorbs the remainder",
			total: 10,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(30)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(50)}},
			},
			wantTargets: []int32{2, 3, 5},
		},
		{
			// Two unbounded groups: the remainder lands on the FIRST. This is
			// what the spec.groups field doc promises; the single-uncapped
			// cases above cannot tell first from last.
			name:  "overflow lands on the first of two unbounded groups",
			total: 10,
			groups: []podpoolsv1alpha1.GroupSpec{
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
				{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
			wantTargets: []int32{10, 0},
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

			if result.TargetDegraded != tt.wantDegraded {
				t.Errorf("targetDegraded: got %v, want %v", result.TargetDegraded, tt.wantDegraded)
			}

			if result.Unplaced != tt.wantUnplaced {
				t.Errorf("unplaced: got %d, want %d", result.Unplaced, tt.wantUnplaced)
			}

			// Every replica is either given to a group or explicitly reported
			// as unplaced. This is deliberately not `sum == total`, which
			// stopped being an invariant the moment ceilings were honoured —
			// the weaker-looking version is the stronger check: it catches a
			// target that vanished *and* a shortfall that was silently
			// swallowed, which two separate equalities would not.
			var sum int32
			for _, v := range result.Targets {
				sum += v
			}

			if sum+result.Unplaced != tt.total {
				t.Errorf("targets sum to %d with %d unplaced, want %d accounted for",
					sum, result.Unplaced, tt.total)
			}

			// No group may exceed its own ceiling — except by its own min,
			// since phase 1 satisfies cascade thresholds before targets apply
			// and absolute constraints deliberately beat targets. Asserted
			// for every case rather than per-fixture, since it is the property
			// the overflow phase exists to establish.
			for i, g := range tt.groups {
				limit, bounded := GroupCeiling(tt.total, g.Scaling)
				if !bounded {
					continue
				}

				allowed := limit
				if g.Scaling.Min != nil && *g.Scaling.Min > allowed {
					allowed = *g.Scaling.Min
				}

				if result.Targets[i] > allowed {
					t.Errorf("group %d: target %d exceeds ceiling %d (min allowance %d)",
						i, result.Targets[i], limit, allowed)
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

// TestComputeGroupTargetsScalingTrace validates the algorithm against the
// direct computation formulas from REQUIREMENTS.md:
//
//	scavTarget  = floor(total * 0.30)
//	burstTarget = floor(total * 0.50)
//	baseTarget  = total - scavTarget - burstTarget
func TestComputeGroupTargetsScalingTrace(t *testing.T) {
	t.Parallel()

	groups := threeGroupSpec()

	expected := []struct {
		total int32
		base  int32
		scav  int32
		burst int32
	}{
		{1, 1, 0, 0},
		{3, 3, 0, 0},
		{4, 3, 1, 0},
		{5, 3, 1, 1},
		{7, 3, 2, 2},
		{10, 3, 3, 4},
		{12, 3, 3, 6},
		{15, 4, 4, 7},
		{20, 4, 6, 10},
		{25, 6, 7, 12},
		{30, 6, 9, 15},
	}

	for _, e := range expected {
		t.Run(fmt.Sprintf("total=%d", e.total), func(t *testing.T) {
			t.Parallel()

			result := ComputeGroupTargets(e.total, groups)

			got := result.Targets
			if got[0] != e.base || got[1] != e.scav || got[2] != e.burst {
				t.Errorf("total=%d: got base=%d scav=%d burst=%d, want base=%d scav=%d burst=%d",
					e.total, got[0], got[1], got[2], e.base, e.scav, e.burst)
			}
		})
	}
}

// cappedGroupSpec has no (min)-only group, so nothing can absorb overflow:
// ceilings of 20% + 50% leave 30% of any total unplaced.
func cappedGroupSpec() []podpoolsv1alpha1.GroupSpec {
	return []podpoolsv1alpha1.GroupSpec{
		{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(20)}},
		{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(50)}},
	}
}

// TestComputeGroupTargetsCappedTrace pins the shortfall across a range rather
// than at a single total, which is where rounding interactions show up.
//
// Each group takes floor(total x target%); the rest is unplaced. Nothing is ever
// forced past a ceiling, and nothing is ever lost — targets plus unplaced must
// always account for the whole total.
func TestComputeGroupTargetsCappedTrace(t *testing.T) {
	t.Parallel()

	groups := cappedGroupSpec()

	for total := range int32(61) {
		t.Run(fmt.Sprintf("total=%d", total), func(t *testing.T) {
			t.Parallel()

			r := ComputeGroupTargets(total, groups)

			wantA := int32(float64(total) * 0.20)

			wantB := int32(float64(total) * 0.50)
			if r.Targets[0] != wantA || r.Targets[1] != wantB {
				t.Errorf("targets = %v, want [%d %d]", r.Targets, wantA, wantB)
			}

			wantUnplaced := total - wantA - wantB
			if r.Unplaced != wantUnplaced {
				t.Errorf("unplaced = %d, want %d", r.Unplaced, wantUnplaced)
			}

			if r.Targets[0]+r.Targets[1]+r.Unplaced != total {
				t.Errorf("%v + %d unplaced does not account for %d", r.Targets, r.Unplaced, total)
			}

			// A pool this shape is always short — except at zero, where there
			// is nothing to place.
			if total > 0 && r.Unplaced == 0 {
				t.Errorf("expected a shortfall at total=%d", total)
			}
		})
	}
}
