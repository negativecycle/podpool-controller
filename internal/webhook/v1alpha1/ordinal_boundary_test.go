package v1alpha1

// Test for plans/79-cleanup-batch.md item 4. Green today; its job is to hold
// still while the warning's length arithmetic
// (len(pp.Name) + 1 + len(g.Name) + 1 + maxOrdinalLen) is rewritten in terms
// of workload.ChildName. The existing TestStatefulSetOrdinalBudgetWarning
// checks 66 > 63; an off-by-one in the rewrite sails past that, which is why
// this one sits on the exact edge.

import (
	"strings"
	"testing"

	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

func TestStatefulSetOrdinalBudgetBoundary(t *testing.T) {
	t.Parallel()

	// hostname = <pool>-<group>-<ordinal>. With replicas=5 the widest ordinal
	// is one digit, so len(pool)+len(group) = 60 puts the hostname at exactly
	// 63 bytes (fits) and 61 puts it at 64 (warns).
	pool := strings.Repeat("p", 40)

	cases := []struct {
		name  string
		group string
		want  bool
	}{
		{"exactly 63 bytes fits", strings.Repeat("g", 20), false},
		{"64 bytes warns", strings.Repeat("g", 21), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			warnings, err := (&PodPoolCustomValidator{}).ValidateCreate(t.Context(),
				poolWith(pool, 5, "StatefulSet", podpoolsv1alpha1.GroupSpec{
					Name:    tc.group,
					Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(1))},
				}))
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}

			got := false

			for _, w := range warnings {
				if strings.Contains(w, "ordinal") || strings.Contains(w, "hostname") {
					got = true
				}
			}

			if got != tc.want {
				t.Errorf("ordinal warning present = %v, want %v; warnings: %v", got, tc.want, warnings)
			}
		})
	}
}
