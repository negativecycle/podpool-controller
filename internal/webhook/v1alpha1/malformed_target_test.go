package v1alpha1

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// intTarget builds an Int-typed target: `target: 30` in YAML without quotes.
// Rejected by the CRD's CEL rule, so it reaches this layer only on an object
// stored before that rule existed — which validation ratcheting keeps alive
// indefinitely.
func intTarget() *intstr.IntOrString {
	t := intstr.FromInt32(30)

	return &t
}

func strTarget(s string) *intstr.IntOrString {
	t := intstr.FromString(s)

	return &t
}

// TestOrderingGuardAgreesWithTheDistributor is the regression test for #71, and
// it asserts an agreement rather than a verdict.
//
// The guard rejects a pool where a group placed before the opportunistic tier
// would intercept the replicas that tier cannot place. Whether it fires must
// track what the distributor actually does, and it did not: the guard decided
// boundedness by presence and the distributor by parsing, so a target nobody
// could read was a cap to one and an open sink to the other. The guard stayed
// silent about exactly the configuration it exists to reject.
//
// "Intercepts" is measured, not asserted: run the distribution twice with
// different observed capacity for the opportunistic tier and see whether base
// grows as that tier shrinks. A group that grows when the scavenger loses
// capacity is absorbing the scavenger's spill, which is the whole of what the
// guard is about.
//
// wantAbsorbs is spelled out per row as well as derived, so the test cannot go
// vacuously green if both layers ever break in the same direction.
func TestOrderingGuardAgreesWithTheDistributor(t *testing.T) {
	t.Parallel()

	const total = 20

	opp := podpoolsv1alpha1.GroupSpec{
		Name:    testGroupScavenger,
		Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: opportunisticPtr()},
	}
	overflow := podpoolsv1alpha1.GroupSpec{
		Name:    testGroupBurst,
		Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(50)},
	}

	tests := []struct {
		name        string
		baseTarget  *intstr.IntOrString
		wantAbsorbs bool
	}{
		{
			// The case the guard always caught, and must keep catching.
			name:        "an absent target really does intercept the spill",
			baseTarget:  nil,
			wantAbsorbs: true,
		},
		{
			// #71: before the distributor was fixed, base absorbed here too and
			// the guard said nothing.
			name:        "an int-typed target no longer intercepts",
			baseTarget:  intTarget(),
			wantAbsorbs: false,
		},
		{
			name:        "an out-of-range target no longer intercepts",
			baseTarget:  strTarget("0%"),
			wantAbsorbs: false,
		},
		{
			name:        "a non-numeric target no longer intercepts",
			baseTarget:  strTarget("abc%"),
			wantAbsorbs: false,
		},
		{
			name:        "a readable target does not intercept",
			baseTarget:  pctStr(20),
			wantAbsorbs: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			groups := []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{
					Min: ptr.To[int32](2), Target: tt.baseTarget,
				}},
				opp,
				overflow,
			}

			scarce := workload.ComputeGroupTargets(total, groups, map[string]int32{testGroupScavenger: 2})
			plentiful := workload.ComputeGroupTargets(total, groups, map[string]int32{testGroupScavenger: 10})

			absorbs := scarce.Targets[0] > plentiful.Targets[0]
			if absorbs != tt.wantAbsorbs {
				t.Fatalf("base = %d with scarce capacity and %d with plentiful, so absorbs = %v, want %v",
					scarce.Targets[0], plentiful.Targets[0], absorbs, tt.wantAbsorbs)
			}

			errs := validateOpportunistic(field.NewPath("spec", "groups"), groups)

			if got := len(errs) > 0; got != absorbs {
				t.Fatalf("guard rejected = %v but the distributor absorbs = %v; the two layers disagree (%v)",
					got, absorbs, errs)
			}

			if !absorbs {
				return
			}

			// Naming the group is the difference between a message an operator
			// can act on and one they have to reverse-engineer.
			if !strings.Contains(errs.ToAggregate().Error(), testGroupBase) {
				t.Errorf("error does not name the offending group %q: %v", testGroupBase, errs)
			}
		})
	}
}

// TestFullyCappedWarningSeesAnUnreadableTarget covers the third copy of the
// boundedness predicate, which had drifted the same way as the second.
//
// A pool whose only uncapped-looking group carries an unreadable target has no
// overflow sink at all: the distributor binds that group at zero and the
// remainder goes unplaced. The warning exists to say so before the pool runs
// below spec.replicas without explanation.
func TestFullyCappedWarningSeesAnUnreadableTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		baseTarget *intstr.IntOrString
		wantWarn   bool
	}{
		{
			name:       "an absent target is a real overflow bucket",
			baseTarget: nil,
			wantWarn:   false,
		},
		{
			name:       "an int-typed target is not an overflow bucket",
			baseTarget: intTarget(),
			wantWarn:   true,
		},
		{
			name:       "an out-of-range target is not an overflow bucket",
			baseTarget: strTarget("101%"),
			wantWarn:   true,
		},
		{
			name:       "an empty target is not an overflow bucket",
			baseTarget: strTarget(""),
			wantWarn:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pp := &podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					Replicas: 20,
					Groups: []podpoolsv1alpha1.GroupSpec{
						{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{
							Min: ptr.To[int32](0), Target: tt.baseTarget,
						}},
						{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{
							Min: ptr.To[int32](0), Target: pctStr(50),
						}},
					},
				},
			}

			if got := len(warnOnFullyCappedPool(pp)) > 0; got != tt.wantWarn {
				t.Errorf("warned = %v, want %v", got, tt.wantWarn)
			}
		})
	}
}

// TestUnreadableTargetWarnsRatherThanRejects pins the escape hatch.
//
// A pool with a stored malformed target is scalable but not editable: CEL
// ratcheting admits an update that leaves `scaling` alone, and re-runs the rule
// on one that does not. Scaling is therefore the only operation left, and it is
// the one an operator reaches for when a pool is overspending. Turning it into a
// rejection would bite hardest on the objects already in trouble.
func TestUnreadableTargetWarnsRatherThanRejects(t *testing.T) {
	t.Parallel()

	stored := poolWith("test-pool", 3)
	stored.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
		{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{
			Min: ptr.To[int32](1), Target: intTarget(),
		}},
	}

	// The ordinary scale: spec changes, so the short-circuit does not engage,
	// and `scaling` is untouched, so CEL would have ratcheted past it.
	scaled := stored.DeepCopy()
	scaled.Spec.Replicas = stored.Spec.Replicas + 5

	v := &PodPoolCustomValidator{}

	warnings, err := v.ValidateUpdate(t.Context(), stored, scaled)
	if err != nil {
		t.Fatalf("scaling a pool with a stored malformed target must stay possible: %v", err)
	}

	found := ""

	for _, w := range warnings {
		if strings.Contains(w, "not a percentage string") {
			found = w

			break
		}
	}

	if found == "" {
		t.Fatalf("expected a warning about the unreadable target, got: %v", warnings)
	}

	// The operator has to be able to find the group and see the value they
	// actually wrote.
	for _, want := range []string{testGroupBase, `"30"`, `"30%"`, "capped at 0"} {
		if !strings.Contains(found, want) {
			t.Errorf("warning does not mention %s: %q", want, found)
		}
	}

	// Create is wired too. Unreachable in a healthy cluster, because CEL runs
	// before validating admission and rejects the object outright — but a
	// cluster running a stale CRD is exactly how this population was produced,
	// and there the warning is the only thing that says anything at all.
	createWarnings, err := v.ValidateCreate(t.Context(), stored)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if !slices.ContainsFunc(createWarnings, func(w string) bool {
		return strings.Contains(w, "not a percentage string")
	}) {
		t.Errorf("ValidateCreate does not warn about the unreadable target: %v", createWarnings)
	}
}

// A readable target must stay silent, or the warning is noise on every pool.
func TestReadableTargetIsNotWarnedAbout(t *testing.T) {
	t.Parallel()

	pp := poolWith("test-pool", 3)
	pp.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
		{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{
			Min: ptr.To[int32](1), Target: pctStr(30),
		}},
		{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
	}

	if got := warnOnUnreadableTarget(pp); len(got) > 0 {
		t.Errorf("warned about a well-formed pool: %v", got)
	}
}

// One warning per offending group: a pool can have more than one.
func TestEveryUnreadableTargetIsNamed(t *testing.T) {
	t.Parallel()

	pp := poolWith("test-pool", 3)
	pp.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
		{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Target: intTarget()}},
		{Name: testGroupScavenger, Scaling: podpoolsv1alpha1.ScalingConstraints{Target: pctStr(20)}},
		{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Target: strTarget("abc%")}},
	}

	warnings := warnOnUnreadableTarget(pp)
	if len(warnings) != 2 {
		t.Fatalf("got %d warnings, want 2: %v", len(warnings), warnings)
	}

	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, testGroupBase) || !strings.Contains(joined, testGroupBurst) {
		t.Errorf("warnings do not name both offending groups: %v", warnings)
	}

	if strings.Contains(joined, testGroupScavenger) {
		t.Errorf("warned about the group with a valid target: %v", warnings)
	}
}
