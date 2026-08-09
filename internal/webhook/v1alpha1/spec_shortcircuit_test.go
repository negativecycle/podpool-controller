package v1alpha1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// Tests for plans/66-validateupdate-spec-shortcircuit.md.
//
// #66: ValidateUpdate revalidated the whole spec on every update, including
// updates that did not touch spec at all. Pause is a metadata annotation, so
// setting it was gated by full spec validation, which means a pool was
// unpausable in exactly the states that make an operator want to pause it: a
// template the controller cannot read, a group name a newer rule rejects, any
// spec admitted under an older ruleset.
//
// The general shape is worse than the pause case: any validation rule added
// after a pool was admitted converted every future metadata write to that pool
// into a rejection. The API server solved the same problem for CRD schemas with
// validation ratcheting; a webhook has to implement it itself.

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// paused returns a copy carrying the pause annotation and nothing else changed:
// the metadata-only update this item exists for.
// annotationPaused is the annotation the controller will read to pause a pool.
// Spelled out here because the constant arrives with pause support; this test
// only needs a metadata key that admission does not interpret.
const annotationPaused = "podpools.dev/paused"

func paused(pp *podpoolsv1alpha1.PodPool) *podpoolsv1alpha1.PodPool {
	out := pp.DeepCopy()
	if out.Annotations == nil {
		out.Annotations = map[string]string{}
	}

	out.Annotations[annotationPaused] = "true"

	return out
}

// poolWithBadGroupName trips a per-group rule in validatePodPoolSpec. A pool can
// hold this today: the rule postdates objects admitted before it existed.
func poolWithBadGroupName() *podpoolsv1alpha1.PodPool {
	pp := sarPool()
	pp.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
		{Name: "x", Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](1)}},
	}

	return pp
}

// poolWithBadScaling trips validateScaling: opportunistic is itself the
// ceiling, so combining it with max is contradictory.
func poolWithBadScaling() *podpoolsv1alpha1.PodPool {
	pp := sarPool()
	pp.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
		{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{
			Min:           ptr.To[int32](1),
			Max:           ptr.To[int32](5),
			Opportunistic: ptr.To(true),
		}},
	}

	return pp
}

func poolWithLongName() *podpoolsv1alpha1.PodPool {
	pp := sarPool()
	pp.Name = strings.Repeat("a", 70)

	return pp
}

// ---------------------------------------------------------------------------
// the escape hatch
// ---------------------------------------------------------------------------

func TestMetadataOnlyUpdateIsNotGatedOnSpecValidity(t *testing.T) {
	t.Parallel()

	withLabel := func(pp *podpoolsv1alpha1.PodPool) *podpoolsv1alpha1.PodPool {
		out := pp.DeepCopy()
		out.Labels = map[string]string{"team": "platform"}
		out.Finalizers = []string{"example.com/drain"}

		return out
	}

	tests := []struct {
		name    string
		old     *podpoolsv1alpha1.PodPool
		change  func(*podpoolsv1alpha1.PodPool) *podpoolsv1alpha1.PodPool
		wantErr string // substring; empty means admitted
	}{
		{
			name: "a pool whose template cannot be read can still be paused",
			// The headline case. The controller cannot act on this pool, which
			// is the whole reason an operator reaches for pause.
			old:    poolWithTemplate(templateWithoutTypeMeta()),
			change: paused,
		},
		{
			name: "a pool tripping a per-group rule can still be paused",
			// Stands in for every rule tightened after a pool was admitted.
			old:    poolWithBadGroupName(),
			change: paused,
		},
		{
			name:   "a pool with a contradictory scaling combination can still be paused",
			old:    poolWithBadScaling(),
			change: paused,
		},
		{
			name:   "a healthy pool can be paused",
			old:    sarPool(),
			change: paused,
		},
		{
			name: "pause can be removed again",
			old:  paused(sarPool()),
			change: func(pp *podpoolsv1alpha1.PodPool) *podpoolsv1alpha1.PodPool {
				out := pp.DeepCopy()
				delete(out.Annotations, annotationPaused)

				return out
			},
		},
		{
			name: "labels and finalizers are metadata too",
			// Finalizer removal is a metadata-only update, so this path is what
			// would decide whether a spec-invalid pool can ever be deleted once
			// anything puts a finalizer on PodPool.
			old:    poolWithTemplate(templateWithoutTypeMeta()),
			change: withLabel,
		},
		{
			name: "a broken pool cannot be edited under cover of the annotation",
			// The anti-regression half: the short-circuit must be unreachable
			// the moment the spec actually moves.
			old: poolWithTemplate(templateWithoutTypeMeta()),
			change: func(pp *podpoolsv1alpha1.PodPool) *podpoolsv1alpha1.PodPool {
				out := paused(pp)
				out.Spec.Replicas = 9

				return out
			},
			wantErr: "apiVersion",
		},
		{
			name: "a spec change is still fully validated",
			old:  sarPool(),
			change: func(pp *podpoolsv1alpha1.PodPool) *podpoolsv1alpha1.PodPool {
				out := pp.DeepCopy()
				out.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
					{Name: "x", Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](1)}},
				}

				return out
			},
			wantErr: "DNS label",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			v := &PodPoolCustomValidator{Client: (&sarProbe{allowed: true}).client()}

			_, err := v.ValidateUpdate(sarAdmissionCtx(), tt.old, tt.change(tt.old))

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("expected the update to be admitted, got: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("expected rejection containing %q, got no error", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("rejection = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestMetadataOnlyUpdateReturnsNoWarnings pins the deliberate half of the early
// return. warnPoolNameUpdate is derived from metadata.name, which is immutable,
// so the warning cannot be newly true; re-emitting it on every unrelated
// annotation write is noise about something the user cannot fix without
// recreating the pool.
func TestMetadataOnlyUpdateReturnsNoWarnings(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{Client: (&sarProbe{allowed: true}).client()}
	pool := poolWithLongName()

	warnings, err := v.ValidateUpdate(sarAdmissionCtx(), pool, paused(pool))
	if err != nil {
		t.Fatalf("expected the update to be admitted: %v", err)
	}

	if len(warnings) != 0 {
		t.Errorf("metadata-only update returned warnings %v, want none", warnings)
	}
}

// TestMetadataOnlyUpdateIssuesNoSAR is the coordination with #63.
//
// The outcome cannot see this: a short-circuit that silently fails to engage
// still admits the request and returns no error, so every outcome-only
// assertion passes whether or not the fix works. The count is the only thing
// that separates "short-circuited" from "ran everything and happened to agree".
//
// The probe denies, so a SAR that does run would also flip the outcome. Both
// assertions are kept anyway: the count is what survives a future change that
// makes denial non-fatal.
func TestMetadataOnlyUpdateIssuesNoSAR(t *testing.T) {
	t.Parallel()

	probe := &sarProbe{allowed: false}
	v := &PodPoolCustomValidator{Client: probe.client()}

	// An unparseable stored template is #63's motivating case, and the one
	// where gvkVouchedFor is false. Reaching the guard would re-authorize and,
	// with this probe, deny.
	pool := poolWithTemplate(templateWithoutTypeMeta())

	if _, err := v.ValidateUpdate(sarAdmissionCtx(), pool, paused(pool)); err != nil {
		t.Fatalf("expected the metadata-only update to be admitted: %v", err)
	}

	if probe.count != 0 {
		t.Errorf("SubjectAccessReviews issued = %d, want 0: an update that cannot "+
			"change the workload type has nothing to authorize", probe.count)
	}
}

// ---------------------------------------------------------------------------
// what the short-circuit rests on
// ---------------------------------------------------------------------------

// TestSpecEqualitySemantics pins the comparison itself, because the safety of
// the early return is exactly the claim "equal spec implies identical verdict".
//
// The two false rows are not defects. A byte-level difference that is
// semantically irrelevant falls through to full validation, which is the safe
// direction: the failure mode is a redundant validation, never a skipped one.
// That is why this is a DeepEqual and not a canonicalising comparison.
func TestSpecEqualitySemantics(t *testing.T) {
	t.Parallel()

	remarshalled := func() *podpoolsv1alpha1.PodPool {
		pp := sarPool()
		pp.Spec.WorkloadTemplate = remarshalTemplate(t, pp.Spec.WorkloadTemplate)

		return pp
	}

	tests := []struct {
		name string
		mut  func() *podpoolsv1alpha1.PodPool
		want bool
		why  string
	}{
		{
			name: "a DeepCopy carrying only an annotation change",
			mut:  func() *podpoolsv1alpha1.PodPool { return paused(sarPool()) },
			want: true,
			why:  "the common path: this is what kubectl annotate produces",
		},
		{
			name: "a template round-tripped through map[string]any",
			mut:  remarshalled,
			want: true,
			why:  "encoding/json emits map keys sorted, so a round-trip is byte-identical",
		},
		{
			name: "leading whitespace in the template bytes",
			mut: func() *podpoolsv1alpha1.PodPool {
				pp := sarPool()
				pp.Spec.WorkloadTemplate.Raw = append([]byte(" "), pp.Spec.WorkloadTemplate.Raw...)

				return pp
			},
			want: false,
			why:  "fails safe: a redundant validation, not a skipped one",
		},
		{
			name: "a changed replica count",
			mut: func() *podpoolsv1alpha1.PodPool {
				pp := sarPool()
				pp.Spec.Replicas = 9

				return pp
			},
			want: false,
			why:  "the spec moved; full validation must run",
		},
		{
			name: "RawExtension.Object populated on one side",
			mut: func() *podpoolsv1alpha1.PodPool {
				pp := sarPool()
				pp.Spec.WorkloadTemplate.Object = &podpoolsv1alpha1.PodPool{}

				return pp
			},
			want: false,
			why:  "fails safe, same as whitespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := sarPool()

			got := apiequality.Semantic.DeepEqual(base.Spec, tt.mut().Spec)
			if got != tt.want {
				t.Errorf("Semantic.DeepEqual = %v, want %v (%s)", got, tt.want, tt.why)
			}

			// Semantic and reflect agree on PodPoolSpec as it stands, so the
			// choice is style, not correctness. Semantic is the convention and
			// is the one that stays right if a Quantity or a Time is ever added
			// to the spec: do not "simplify" it to reflect.
			if r := reflect.DeepEqual(base.Spec, tt.mut().Spec); r != got {
				t.Errorf("reflect.DeepEqual = %v but Semantic = %v; the two have "+
					"diverged, which means the spec gained a type with custom "+
					"equality semantics", r, got)
			}
		})
	}
}

// remarshalTemplate reserialises the template through a generic map, the way a
// client that decodes and re-encodes an object would.
func remarshalTemplate(t *testing.T, in runtime.RawExtension) runtime.RawExtension {
	t.Helper()

	var generic map[string]any
	if err := json.Unmarshal(in.Raw, &generic); err != nil {
		t.Fatalf("unmarshalling the template: %v", err)
	}

	raw, err := json.Marshal(generic)
	if err != nil {
		t.Fatalf("remarshalling the template: %v", err)
	}

	return runtime.RawExtension{Raw: raw}
}

// A create has no old object to compare against, so nothing here may be
// symmetrised onto it. Pinned because the two functions sit next to each other
// and the temptation is obvious.
func TestCreateIsNeverShortCircuited(t *testing.T) {
	t.Parallel()

	probe := &sarProbe{allowed: true}
	v := &PodPoolCustomValidator{Client: probe.client()}

	if _, err := v.ValidateCreate(sarAdmissionCtx(), sarPool()); err != nil {
		t.Fatalf("expected the create to be admitted: %v", err)
	}

	if probe.count != 1 {
		t.Errorf("SubjectAccessReviews issued on create = %d, want 1", probe.count)
	}

	pool := poolWithBadGroupName()
	if _, err := v.ValidateCreate(sarAdmissionCtx(), pool); err == nil {
		t.Error("an invalid spec was admitted on create; only updates ratchet")
	}
}
