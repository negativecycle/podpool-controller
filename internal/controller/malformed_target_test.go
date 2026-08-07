package controller

import (
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// intTarget builds an Int-typed target: `target: 30` written in YAML without
// quotes, which reads as obviously correct in a manifest and is the single most
// likely way a real user produces an unreadable one.
func intTarget(v int32) *intstr.IntOrString {
	t := intstr.FromInt32(v)

	return &t
}

// strTarget builds a String-typed target verbatim, including the shapes the
// CRD's CEL rule rejects. pctTarget only produces well-formed values.
func strTarget(s string) *intstr.IntOrString {
	t := intstr.FromString(s)

	return &t
}

// TestTargetBoundedness pins the grammar of target against the two questions
// every layer asks of it: what percentage does it name, and does it bind the
// group at all.
//
// One table rather than three because the regression it guards is precisely a
// future edit that makes the answers disagree again. A target the algorithm
// could not read used to leave the group unbounded, which made a typo on the
// most expensive tier into the pool's overflow sink.
//
// The three lenient rows ("+30%", "030%", bare "30") are honoured as 30% here
// and rejected by the CEL rule, so the two grammars are not the same grammar.
// That gap is deliberate and deferred: tightening it would flip stored values
// from "honoured as 30%" to "bound at 0", which is a behaviour change for real
// objects and belongs to its own decision. strconv.ParseInt accepting a leading
// "+" and leading zeroes is the reason they differ, so closing the gap means a
// regexp or an explicit digit check, not a stricter ParseInt.
func TestTargetBoundedness(t *testing.T) {
	const total = 100

	tests := []struct {
		name        string
		scaling     podpoolsv1alpha1.ScalingConstraints
		wantPct     int32
		wantParsed  bool
		wantLimit   int32
		wantBounded bool
	}{
		{
			// The case the fix must not break. A genuinely absent target is
			// genuinely unbounded, and the whole overflow design rests on it.
			name:        "absent target is unbounded",
			scaling:     podpoolsv1alpha1.ScalingConstraints{},
			wantBounded: false,
		},
		{
			name:        "int-typed target",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: intTarget(30)},
			wantBounded: true,
		},
		{
			name:        "well-formed percentage",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("30%")},
			wantPct:     30,
			wantParsed:  true,
			wantLimit:   30,
			wantBounded: true,
		},
		{
			name:        "leading plus is honoured",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("+30%")},
			wantPct:     30,
			wantParsed:  true,
			wantLimit:   30,
			wantBounded: true,
		},
		{
			name:        "leading zero is honoured",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("030%")},
			wantPct:     30,
			wantParsed:  true,
			wantLimit:   30,
			wantBounded: true,
		},
		{
			name:        "bare number without a percent sign is honoured",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("30")},
			wantPct:     30,
			wantParsed:  true,
			wantLimit:   30,
			wantBounded: true,
		},
		{
			name:        "zero percent is out of range",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("0%")},
			wantBounded: true,
		},
		{
			name:        "over one hundred percent is out of range",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("101%")},
			wantBounded: true,
		},
		{
			name:        "empty string",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("")},
			wantBounded: true,
		},
		{
			name:        "non-numeric",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("abc%")},
			wantBounded: true,
		},
		{
			name:        "embedded space",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Target: strTarget("30 %")},
			wantBounded: true,
		},
		{
			// max wins outright, so an unreadable target beside it is moot.
			name:        "max outranks an unreadable target",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Max: ptr.To[int32](7), Target: strTarget("abc%")},
			wantLimit:   7,
			wantBounded: true,
		},
		{
			// Opportunistic has no static ceiling: its bound will be whatever
			// the scheduler accepts, which GroupCeiling cannot express. The
			// distribution phase that sizes such groups arrives with the
			// capacity feature.
			name:        "opportunistic has no static ceiling",
			scaling:     podpoolsv1alpha1.ScalingConstraints{Opportunistic: ptr.To(true)},
			wantBounded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pct, parsed := workload.TargetPercent(tt.scaling.Target)
			if pct != tt.wantPct || parsed != tt.wantParsed {
				t.Errorf("TargetPercent = (%d, %v), want (%d, %v)", pct, parsed, tt.wantPct, tt.wantParsed)
			}

			limit, bounded := workload.GroupCeiling(total, tt.scaling)
			if limit != tt.wantLimit || bounded != tt.wantBounded {
				t.Errorf("GroupCeiling(%d) = (%d, %v), want (%d, %v)",
					total, limit, bounded, tt.wantLimit, tt.wantBounded)
			}
		})
	}
}

// malformedTargetSpec is the two-tier pool with base's ceiling under test: a
// reliable tier that declares a share, and a capped overflow. Every group is
// bounded except insofar as base's target is readable, so base is the only
// thing standing between the pool and an unplaced remainder.
func malformedTargetSpec(baseMin int32, baseTarget *intstr.IntOrString) []podpoolsv1alpha1.GroupSpec {
	return []podpoolsv1alpha1.GroupSpec{
		{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(baseMin), Target: baseTarget}},
		{Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(50)}},
	}
}

// TestMalformedTargetDoesNotAbsorbTheOverflow is the distribution consequence
// of TestTargetBoundedness, and the reason the fix is worth making.
//
// The user capped base at 30% of 20 = 6. With a target the distributor could
// not read, base became the unbounded group and silently ran 10 — an overspend
// on the tier the cap existed to protect. Quietly overspending on the
// expensive tier is the failure this controller exists to prevent.
func TestMalformedTargetDoesNotAbsorbTheOverflow(t *testing.T) {
	const total = 20

	tests := []struct {
		name         string
		baseMin      int32
		baseTarget   *intstr.IntOrString
		wantTargets  []int32
		wantUnplaced int32
	}{
		{
			// Control. Unaffected by the fix: base reaches its declared 6.
			name:         "well-formed target is honoured",
			baseMin:      1,
			baseTarget:   pctTarget(30),
			wantTargets:  []int32{6, 10},
			wantUnplaced: 4,
		},
		{
			// The regression. base falls back to its floor and the remainder
			// surfaces as unplaced instead of inflating the bill.
			name:         "int-typed target binds base at its floor",
			baseMin:      1,
			baseTarget:   intTarget(30),
			wantTargets:  []int32{1, 10},
			wantUnplaced: 9,
		},
		{
			name:         "out-of-range target binds base at its floor",
			baseMin:      1,
			baseTarget:   strTarget("0%"),
			wantTargets:  []int32{1, 10},
			wantUnplaced: 9,
		},
		{
			// The row that proves the fix must not break the overflow design.
			// An absent target is a deliberate statement that this group takes
			// whatever is left, and it must keep doing so.
			name:         "absent target still absorbs everything",
			baseMin:      1,
			baseTarget:   nil,
			wantTargets:  []int32{10, 10},
			wantUnplaced: 0,
		},
		{
			// A floor above the new ceiling. Phase 1 assigns floors
			// unconditionally and the overflow phase respects ceilings, so the
			// group lands above its own ceiling: the floor is the harder
			// guarantee. Recorded rather than discovered.
			name:         "a floor above the new ceiling still wins",
			baseMin:      5,
			baseTarget:   strTarget("abc%"),
			wantTargets:  []int32{5, 10},
			wantUnplaced: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workload.ComputeGroupTargets(total, malformedTargetSpec(tt.baseMin, tt.baseTarget))

			if fmt.Sprint(got.Targets) != fmt.Sprint(tt.wantTargets) {
				t.Errorf("targets = %v, want %v", got.Targets, tt.wantTargets)
			}

			if got.Unplaced != tt.wantUnplaced {
				t.Errorf("unplaced = %d, want %d", got.Unplaced, tt.wantUnplaced)
			}

			var sum int32
			for _, v := range got.Targets {
				sum += v
			}

			if sum+got.Unplaced != total {
				t.Errorf("targets sum to %d with %d unplaced, want %d accounted for",
					sum, got.Unplaced, total)
			}
		})
	}
}
