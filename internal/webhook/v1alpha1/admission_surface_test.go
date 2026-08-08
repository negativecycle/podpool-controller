package v1alpha1

// The admission surface as a whole: the helpers every rule's tests share, the
// rejection messages a user actually reads, and the table asserting that the
// schema and the webhook agree about which scaling shapes are legal.

import (
	"fmt"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func templateRaw(kind string) runtime.RawExtension {
	return runtime.RawExtension{Raw: []byte(`{
		"apiVersion": "apps/v1",
		"kind": "` + kind + `",
		"spec": {"template": {"spec": {"containers": [{"name": "app", "image": "nginx"}]}}}
	}`)}
}

// poolWith builds a minimally valid pool so that a test asserting on one rule
// is not tripped by an unrelated one.
// poolWith builds a minimally valid pool so that a test asserting on one rule
// is not tripped by an unrelated one.
func poolWith(name string, replicas int32, kind string, groups ...podpoolsv1alpha1.GroupSpec) *podpoolsv1alpha1.PodPool {
	if len(groups) == 0 {
		groups = []podpoolsv1alpha1.GroupSpec{
			{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(1))}},
		}
	}

	return &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         replicas,
			WorkloadTemplate: templateRaw(kind),
			Groups:           groups,
		},
	}
}

func pctStr(pct int) *intstr.IntOrString {
	v := intstr.FromString(fmt.Sprintf("%d%%", pct))

	return &v
}

func scalingMessage(t *testing.T, s podpoolsv1alpha1.ScalingConstraints) string {
	t.Helper()

	errs := validateScaling(field.NewPath("spec", "groups").Index(0).Child("scaling"), &s)
	if len(errs) == 0 {
		t.Fatalf("expected validateScaling to reject %+v, it accepted", s)
	}

	return errs.ToAggregate().Error()
}

// The headline case: a *int32 rendered with %v is an address.
func TestScalingMessageHasNoPointerAddresses(t *testing.T) {
	t.Parallel()

	msg := scalingMessage(t, podpoolsv1alpha1.ScalingConstraints{
		Target:        pctStr(30),
		Opportunistic: ptr.To(true),
	})

	if strings.Contains(msg, "0x") {
		t.Errorf("message renders a pointer address: %s", msg)
	}

	if !strings.Contains(msg, "target=30%") {
		t.Errorf("message does not contain target=30%%: %s", msg)
	}
}

// TestScalingMessageSaysUnsetForAbsentFields pins the other half: a nil pointer
// currently renders as "<nil>", which reads as a value rather than an absence.
func TestScalingMessageSaysUnsetForAbsentFields(t *testing.T) {
	t.Parallel()

	msg := scalingMessage(t, podpoolsv1alpha1.ScalingConstraints{
		Target:        pctStr(30),
		Opportunistic: ptr.To(true),
	})

	for _, want := range []string{"min=unset", "max=unset"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not contain %q: %s", want, msg)
		}
	}
}

// TestScalingMessageRendersZeroNotEmpty guards the fix rather than the bug: a
// helper that returns "" for the zero value would satisfy the nil-only tests
// above while losing min=0, which is the value the defaulter injects and
// therefore the one most likely to appear.
func TestScalingMessageRendersZeroNotEmpty(t *testing.T) {
	t.Parallel()

	msg := scalingMessage(t, podpoolsv1alpha1.ScalingConstraints{
		Min:           ptr.To(int32(0)),
		Target:        pctStr(30),
		Opportunistic: ptr.To(true),
	})

	// Delimited deliberately: "min=0" is a prefix of "min=0x8567eae718", so an
	// undelimited Contains passes against the very bug this file exists to pin.
	if !strings.Contains(msg, "min=0 ") {
		t.Errorf("message does not render an explicit zero as min=0: %s", msg)
	}
}

func TestDefaulter(t *testing.T) {
	t.Parallel()

	pool := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:    testGroupBase,
					Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
				},
				{
					Name:    testGroupBurst,
					Scaling: podpoolsv1alpha1.ScalingConstraints{},
				},
			},
		},
	}

	d := &PodPoolCustomDefaulter{}
	if err := d.Default(t.Context(), pool); err != nil {
		t.Fatalf("Default returned error: %v", err)
	}

	burst := pool.Spec.Groups[1].Scaling
	if burst.Min == nil {
		t.Fatal("expected min to be defaulted to 0, got nil")
	}

	if *burst.Min != 0 {
		t.Errorf("expected min=0, got %d", *burst.Min)
	}

	base := pool.Spec.Groups[0].Scaling
	if *base.Min != 3 {
		t.Errorf("expected base min to remain 3, got %d", *base.Min)
	}
}

// The defaulter deliberately leaves min alone when max is set. A group that
// declares only a ceiling has already said what it wants; writing a floor into
// it puts a number in the stored object the user never asked for, and the
// arithmetic reads an absent min as zero anyway.
func TestDefaulterLeavesMinAloneWhenMaxIsSet(t *testing.T) {
	t.Parallel()

	pool := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{{
				Name:    testGroupBase,
				Scaling: podpoolsv1alpha1.ScalingConstraints{Max: ptr.To(int32(5))},
			}},
		},
	}

	if err := (&PodPoolCustomDefaulter{}).Default(t.Context(), pool); err != nil {
		t.Fatalf("Default: %v", err)
	}

	if got := pool.Spec.Groups[0].Scaling.Min; got != nil {
		t.Errorf("defaulter injected min=%d into a group that declared max; want it left unset", *got)
	}
}

// The cross-group rules: displaced replicas need somewhere to go.
func TestPoolNameLengthBoundary(t *testing.T) {
	t.Parallel()

	if len(nameAt63) != 63 || len(nameAt64) != 64 {
		t.Fatalf("fixture lengths wrong: %d, %d", len(nameAt63), len(nameAt64))
	}

	t.Run("63 is accepted", func(t *testing.T) {
		t.Parallel()

		_, err := (&PodPoolCustomValidator{}).ValidateCreate(t.Context(), poolWith(nameAt63, 3, "Deployment"))
		if err != nil {
			t.Errorf("63-character name rejected: %v", err)
		}
	})

	t.Run("64 is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := (&PodPoolCustomValidator{}).ValidateCreate(t.Context(), poolWith(nameAt64, 3, "Deployment"))
		if err == nil {
			t.Fatal("64-character name admitted; it becomes an over-long podpools.dev/pool label value and every group fails at apply time")
		}

		if !strings.Contains(err.Error(), "63") {
			t.Errorf("rejection does not state the limit: %v", err)
		}
	})
}

// TestOverLongNameOnUpdateWarnsButDoesNotBlock is the trap this item most needs
// a guard for. metadata.name is immutable, so rejecting on update would leave
// an already-created over-long pool permanently un-editable — the user could
// not even scale it to zero.
func TestOverLongNameOnUpdateWarnsButDoesNotBlock(t *testing.T) {
	t.Parallel()

	old := poolWith(nameAt64, 3, "Deployment")
	updated := poolWith(nameAt64, 5, "Deployment")

	warnings, err := (&PodPoolCustomValidator{}).ValidateUpdate(t.Context(), old, updated)
	if err != nil {
		t.Fatalf("update to an existing over-long pool was rejected, trapping the object: %v", err)
	}

	if len(warnings) == 0 {
		t.Error("update to an over-long pool produced no warning; the user gets no signal at all")
	}
}

const (
	nameAt63 = "a23456789012345678901234567890123456789012345678901234567890123"  // 63
	nameAt64 = "a234567890123456789012345678901234567890123456789012345678901234" // 64
)
