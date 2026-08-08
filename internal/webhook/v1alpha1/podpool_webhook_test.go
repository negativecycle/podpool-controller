package v1alpha1

import (
	"encoding/json"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

const (
	testGroupBase      = "base"
	testGroupBurst     = "burst"
	testGroupScavenger = "scavenger"
	fieldSpec          = "spec"
	fieldAPIVersion    = "apiVersion"
	fieldKind          = "kind"
	fieldTemplate      = "template"
	fieldContainers    = "containers"
	fieldApp           = "app"
	fieldImage         = "image"
	fieldName          = "name"
	appsV1             = "apps/v1"
	kindDeployment     = "Deployment"
	imageNginx         = "nginx"
	shapeEmpty         = "empty"
)

func validWorkloadTemplate() runtime.RawExtension {
	tmpl := map[string]any{
		fieldAPIVersion: appsV1,
		fieldKind:       kindDeployment,
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				fieldSpec: map[string]any{
					fieldContainers: []any{
						map[string]any{fieldName: fieldApp, fieldImage: imageNginx + ":latest"},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(tmpl)

	return runtime.RawExtension{Raw: raw}
}

// rawTarget builds a target the CEL rule would reject, which is the only way
// such a value reaches this code: on a stored object admitted before the rule,
// or against a stale CRD.
func rawTarget(s string) *intstr.IntOrString {
	v := intstr.FromString(s)

	return &v
}

func opportunisticPtr() *bool {
	b := true

	return &b
}

func pctStr(pct int) *intstr.IntOrString {
	v := intstr.FromString(fmt.Sprintf("%d%%", pct))

	return &v
}

func TestValidateScaling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		scaling podpoolsv1alpha1.ScalingConstraints
		wantErr bool
	}{
		{name: "min only", scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
		{name: "min zero", scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
		{name: "min + target", scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(70)}},
		{name: "max + target", scaling: podpoolsv1alpha1.ScalingConstraints{Max: ptr.To[int32](5), Target: pctStr(30)}},
		{name: "min + max", scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](1), Max: ptr.To[int32](5)}},
		{name: shapeEmpty, scaling: podpoolsv1alpha1.ScalingConstraints{}},
		{name: "max only", scaling: podpoolsv1alpha1.ScalingConstraints{Max: ptr.To[int32](5)}},
		{name: "target only", scaling: podpoolsv1alpha1.ScalingConstraints{Target: pctStr(50)}},
		{name: "min + max + target", scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Max: ptr.To[int32](10), Target: pctStr(30)}},
		{
			name:    "min > max",
			scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](10), Max: ptr.To[int32](5)},
			wantErr: true,
		},
		{
			name:    "opportunistic + max",
			scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Max: ptr.To[int32](5), Opportunistic: opportunisticPtr()},
			wantErr: true,
		},
		{
			name:    "opportunistic + target",
			scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(30), Opportunistic: opportunisticPtr()},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := validateScaling(nil, &tt.scaling)
			if tt.wantErr && len(errs) == 0 {
				t.Error("expected validation error, got none")
			}

			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("unexpected validation errors: %v", errs)
			}
		})
	}
}

func TestValidatePodPoolSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pool    podpoolsv1alpha1.PodPool
		wantErr bool
	}{
		{
			name: "valid single group",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
						},
					},
				},
			},
		},
		{
			name: "valid two groups",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
						},
						{
							Name: testGroupBurst,
							Scaling: podpoolsv1alpha1.ScalingConstraints{
								Min:    ptr.To[int32](0),
								Target: pctStr(70),
							},
						},
					},
				},
			},
		},
		{
			name: "no groups",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
				},
			},
			wantErr: true,
		},
		{
			name: "duplicate group names",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
						},
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "empty workload template",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "workload template missing kind",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: runtime.RawExtension{Raw: []byte(`{"apiVersion":"apps/v1","spec":{}}`)},
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "workload template missing spec.template",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: runtime.RawExtension{Raw: []byte(`{"apiVersion":"apps/v1","kind":"Deployment","spec":{"replicas":1}}`)},
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "empty group name",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    "",
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "single char name — too short for DNS label",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    "b",
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "uppercase name — invalid DNS label",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    "Base",
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid scaling in group — min > max",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](10), Max: ptr.To[int32](5)},
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := validatePodPoolSpec(&tt.pool)
			if tt.wantErr && len(errs) == 0 {
				t.Error("expected validation error, got none")
			}

			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("unexpected validation errors: %v", errs)
			}
		})
	}
}

func TestValidateCreateAndUpdate(t *testing.T) {
	t.Parallel()

	valid := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:    testGroupBase,
					Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
				},
			},
		},
	}

	invalid := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{},
	}

	v := &PodPoolCustomValidator{}
	ctx := t.Context()

	if _, err := v.ValidateCreate(ctx, valid); err != nil {
		t.Errorf("ValidateCreate rejected valid pool: %v", err)
	}

	if _, err := v.ValidateCreate(ctx, invalid); err == nil {
		t.Error("ValidateCreate accepted invalid pool")
	}

	// Scaled rather than identical: #66 returns early on an unchanged spec, so
	// passing the same object twice would assert nothing about validation.
	scaled := valid.DeepCopy()
	scaled.Spec.Replicas = valid.Spec.Replicas + 1

	if _, err := v.ValidateUpdate(ctx, valid, scaled); err != nil {
		t.Errorf("ValidateUpdate rejected valid pool: %v", err)
	}

	if _, err := v.ValidateUpdate(ctx, valid, invalid); err == nil {
		t.Error("ValidateUpdate accepted invalid pool")
	}

	if _, err := v.ValidateDelete(ctx, valid); err != nil {
		t.Errorf("ValidateDelete returned error: %v", err)
	}
}

func TestValidateUpdateGVKImmutability(t *testing.T) {
	t.Parallel()

	deploymentPool := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:    testGroupBase,
					Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
				},
			},
		},
	}

	rolloutTmpl := map[string]any{
		fieldAPIVersion: "argoproj.io/v1alpha1",
		fieldKind:       "Rollout",
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				fieldSpec: map[string]any{
					fieldContainers: []any{
						map[string]any{fieldName: fieldApp, fieldImage: imageNginx + ":latest"},
					},
				},
			},
		},
	}
	rolloutBytes, _ := json.Marshal(rolloutTmpl)
	rolloutPool := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			WorkloadTemplate: runtime.RawExtension{Raw: rolloutBytes},
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:    testGroupBase,
					Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
				},
			},
		},
	}

	v := &PodPoolCustomValidator{}
	ctx := t.Context()

	_, err := v.ValidateUpdate(ctx, deploymentPool, rolloutPool)
	if err == nil {
		t.Error("ValidateUpdate should reject GVK change from Deployment to Rollout")
	}

	// The unchanged-GVK case has to reach the immutability check to prove
	// anything, so the spec must move in some other way: #66 short-circuits an
	// update whose spec is untouched.
	scaledDeploymentPool := deploymentPool.DeepCopy()
	scaledDeploymentPool.Spec.Replicas = deploymentPool.Spec.Replicas + 1

	_, err = v.ValidateUpdate(ctx, deploymentPool, scaledDeploymentPool)
	if err != nil {
		t.Errorf("ValidateUpdate should accept same GVK: %v", err)
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
func TestValidateOpportunisticAcrossGroups(t *testing.T) {
	t.Parallel()

	opp := podpoolsv1alpha1.GroupSpec{
		Name:    testGroupScavenger,
		Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: opportunisticPtr()},
	}
	overflow := podpoolsv1alpha1.GroupSpec{
		Name:    testGroupBurst,
		Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
	}
	secondOpp := podpoolsv1alpha1.GroupSpec{
		Name:    "scavenger-two",
		Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: opportunisticPtr()},
	}
	capped := podpoolsv1alpha1.GroupSpec{
		Name:    testGroupBase,
		Scaling: podpoolsv1alpha1.ScalingConstraints{Max: ptr.To[int32](5), Target: pctStr(30)},
	}

	tests := []struct {
		name    string
		groups  []podpoolsv1alpha1.GroupSpec
		wantErr bool
	}{
		{
			name:   "an opportunistic group followed by an overflow is fine",
			groups: []podpoolsv1alpha1.GroupSpec{opp, overflow},
		},
		{
			// The group ahead of it is capped on purpose. An unbounded one
			// would trip the second rule as well, and a row that two rules
			// can satisfy proves neither.
			name:    "an opportunistic group last has nowhere to spill",
			groups:  []podpoolsv1alpha1.GroupSpec{capped, opp},
			wantErr: true,
		},
		{
			// Overflow goes to the first unbounded group in list order. An
			// uncapped group ahead of the opportunistic one intercepts the
			// displaced replicas — demonstrated on kwok, where an uncapped
			// base absorbed the whole pool.
			name: "an unbounded group before the opportunistic one steals the spill",
			groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2)}},
				opp,
				overflow,
			},
			wantErr: true,
		},
		{
			name: "a capped group before the opportunistic one is fine",
			groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2), Target: pctStr(20)}},
				opp,
				overflow,
			},
		},
		{
			name:   "a max+target group before the opportunistic one is fine",
			groups: []podpoolsv1alpha1.GroupSpec{capped, opp, overflow},
		},
		{
			// An opportunistic group cannot steal another one's spill, because
			// it has no static ceiling to absorb it with — phase 4 skips both.
			// This is the row that distinguishes the shared IsBounded from
			// asking GroupCeiling, which reports an opportunistic group as
			// unbounded and would reject this legal pool.
			name:   "an opportunistic group before another one is not an interceptor",
			groups: []podpoolsv1alpha1.GroupSpec{opp, secondOpp, overflow},
		},
		{
			// A target nobody can parse is still a cap: the distributor binds
			// the group at zero, so it cannot intercept anything. Reading it
			// as unbounded would reject a legal pool — and reading it the
			// other way round, in the distributor, is the bug that made this
			// predicate shared in the first place.
			name: "an unreadable target before the opportunistic one is still a cap",
			groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{
					Min: ptr.To[int32](2), Target: rawTarget("thirty percent"),
				}},
				opp,
				overflow,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := validateOpportunistic(field.NewPath("spec", "groups"), tt.groups)
			if got := len(errs) > 0; got != tt.wantErr {
				t.Errorf("error = %v, want %v (%v)", got, tt.wantErr, errs)
			}
		})
	}
}
