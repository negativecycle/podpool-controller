package v1alpha1

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

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
	fieldSelector      = "selector"
	fieldMatchLabels   = "matchLabels"
	fieldMetadata      = "metadata"
	valueMine          = "mine"
	argoV1alpha1       = "argoproj.io/v1alpha1"
	kindRollout        = "Rollout"
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

func opportunisticPtr() *bool {
	b := true

	return &b
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
			name: "two unbounded groups — second is a dead overflow sink",
			pool: podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					WorkloadTemplate: validWorkloadTemplate(),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{
							Name:    testGroupBase,
							Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
						},
						{
							Name:    testGroupBurst,
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
		fieldAPIVersion: argoV1alpha1,
		fieldKind:       kindRollout,
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
					Min: ptr.To[int32](2), Target: strTarget("thirty percent"),
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

func TestGroupRemovalWarning(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	v := &PodPoolCustomValidator{}

	oldPool := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         10,
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
				{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
		},
	}
	oldPool.Name = "my-pool"

	newPool := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         10,
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
			},
		},
	}
	newPool.Name = "my-pool"

	warnings, err := v.ValidateUpdate(ctx, oldPool, newPool)
	if err != nil {
		t.Fatalf("removing a group should warn, not reject: %v", err)
	}

	found := false

	for _, w := range warnings {
		if strings.Contains(w, testGroupBurst) && strings.Contains(w, "removed") {
			found = true

			break
		}
	}

	if !found {
		t.Errorf("expected warning about removed group %q, got: %v", testGroupBurst, warnings)
	}
}

// The overflow-sink rule: at most one group may be unbounded.
//
// Phase 4 of the distributor absorbs the entire remainder into the FIRST
// unbounded group. A second unbounded group receives zero overflow at every
// scale — its unbounded status is provably dead.

// TestWarnOnFullyCappedPool covers the advisory warning when every group is capped.
//
// Of the three legal scaling shapes only (min) alone is unbounded, so a pool
// where every group carries a max or a target cannot absorb overflow and may
// leave replicas unplaced. That is legitimate, so it warns rather than
// rejecting — and the rejection half is asserted here too, because a warning
// that quietly became an error would break running pools.
func TestWarnOnFullyCappedPool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		groups   []podpoolsv1alpha1.GroupSpec
		wantWarn bool
	}{
		{
			name: "every group capped by target",
			groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(20)}},
				{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(50)}},
			},
			wantWarn: true,
		},
		{
			name: "mixed max and target, still no overflow bucket",
			groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Max: ptr.To[int32](5), Target: pctStr(30)}},
				{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(50)}},
			},
			wantWarn: true,
		},
		{
			name: "a (min)-only group absorbs overflow",
			groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
				{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(50)}},
			},
			wantWarn: false,
		},
		{
			name: "an opportunistic group is not an overflow bucket",
			groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: ptr.To(true)}},
				{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(50)}},
			},
			wantWarn: true,
		},
		{
			// Order must not matter: the bucket is wherever it appears.
			name: "the uncapped group is last",
			groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(50)}},
				{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
			wantWarn: false,
		},
	}

	v := &PodPoolCustomValidator{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := &podpoolsv1alpha1.PodPool{
				Spec: podpoolsv1alpha1.PodPoolSpec{
					Replicas:         10,
					WorkloadTemplate: validWorkloadTemplate(),
					Groups:           tt.groups,
				},
			}

			warnings, err := v.ValidateCreate(t.Context(), pool)
			if err != nil {
				t.Fatalf("a fully-capped pool must be admitted, not rejected: %v", err)
			}

			if got := len(warnings) > 0; got != tt.wantWarn {
				t.Errorf("warned = %v, want %v (warnings=%v)", got, tt.wantWarn, warnings)
			}

			// Updates warn on the same terms, or an existing pool edited into
			// this shape would say nothing.
			//
			// The old pool has to differ: #66 returns early when the spec did
			// not move, so passing the same object twice would assert nothing
			// about the warning. Scaling the pool is the smallest real edit.
			edited := pool.DeepCopy()
			edited.Spec.Replicas = pool.Spec.Replicas + 1

			warnings, err = v.ValidateUpdate(t.Context(), pool, edited)
			if err != nil {
				t.Fatalf("update rejected: %v", err)
			}

			if got := len(warnings) > 0; got != tt.wantWarn {
				t.Errorf("update warned = %v, want %v", got, tt.wantWarn)
			}
		})
	}
}

// The overflow-sink rule: at most one group may be unbounded.
//
// Phase 4 of the distributor absorbs the entire remainder into the FIRST
// unbounded group. A second unbounded group receives zero overflow at every
// scale — its unbounded status is provably dead.
func TestValidateOverflowSink(t *testing.T) {
	t.Parallel()

	unbounded := func(name string) podpoolsv1alpha1.GroupSpec {
		return podpoolsv1alpha1.GroupSpec{
			Name:    name,
			Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
		}
	}
	capped := func(name string) podpoolsv1alpha1.GroupSpec {
		return podpoolsv1alpha1.GroupSpec{
			Name:    name,
			Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctStr(50)},
		}
	}
	opp := podpoolsv1alpha1.GroupSpec{
		Name:    testGroupScavenger,
		Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: opportunisticPtr()},
	}

	tests := []struct {
		name    string
		groups  []podpoolsv1alpha1.GroupSpec
		wantErr bool
	}{
		{
			name:   "one unbounded group is fine",
			groups: []podpoolsv1alpha1.GroupSpec{capped(testGroupBase), unbounded(testGroupBurst)},
		},
		{
			name:    "two unbounded groups rejected",
			groups:  []podpoolsv1alpha1.GroupSpec{unbounded(testGroupBase), unbounded(testGroupBurst)},
			wantErr: true,
		},
		{
			name:    "three unbounded groups — second and third rejected",
			groups:  []podpoolsv1alpha1.GroupSpec{unbounded(testGroupBase), unbounded(testGroupBurst), unbounded(testGroupScavenger)},
			wantErr: true,
		},
		{
			name:   "unbounded + opportunistic is fine — opportunistic is bounded",
			groups: []podpoolsv1alpha1.GroupSpec{capped(testGroupBase), opp, unbounded(testGroupBurst)},
		},
		{
			name:   "all groups capped is fine — no overflow sink, but warnOnFullyCappedPool handles that",
			groups: []podpoolsv1alpha1.GroupSpec{capped(testGroupBase), capped(testGroupBurst)},
		},
		{
			name:   "single unbounded group alone",
			groups: []podpoolsv1alpha1.GroupSpec{unbounded(testGroupBase)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := validateOverflowSink(field.NewPath("spec", "groups"), tt.groups)
			if got := len(errs) > 0; got != tt.wantErr {
				t.Errorf("error = %v, want %v (%v)", got, tt.wantErr, errs)
			}
		})
	}
}

// TestValidateOverflowSinkIntegration verifies the rule fires through
// ValidateCreate and ValidateUpdate, not just as a unit.
func TestValidateOverflowSinkIntegration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	v := &PodPoolCustomValidator{}

	twoUnbounded := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         10,
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
				{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
		},
	}

	if _, err := v.ValidateCreate(ctx, twoUnbounded); err == nil {
		t.Error("ValidateCreate should reject two unbounded groups")
	}

	valid := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         10,
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3), Target: pctStr(50)}},
				{Name: testGroupBurst, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
			},
		},
	}

	if _, err := v.ValidateUpdate(ctx, valid, twoUnbounded); err == nil {
		t.Error("ValidateUpdate should reject two unbounded groups")
	}
}

func templateJSON(obj map[string]any) runtime.RawExtension {
	raw, _ := json.Marshal(obj)

	return runtime.RawExtension{Raw: raw}
}

func validDeploymentMap() map[string]any {
	return map[string]any{
		fieldAPIVersion: appsV1,
		fieldKind:       kindDeployment,
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				fieldSpec: map[string]any{
					fieldContainers: []any{
						map[string]any{fieldName: fieldApp, fieldImage: imageNginx},
					},
				},
			},
		},
	}
}

func TestRenderedChildTypedDecode(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	v := &PodPoolCustomValidator{}

	pool := func(tmpl runtime.RawExtension) *podpoolsv1alpha1.PodPool {
		return &podpoolsv1alpha1.PodPool{
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         3,
				WorkloadTemplate: tmpl,
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
				},
			},
		}
	}

	t.Run("type error rejects", func(t *testing.T) {
		t.Parallel()

		tmpl := validDeploymentMap()
		tmpl[fieldSpec].(map[string]any)["minReadySeconds"] = "ten"

		_, err := v.ValidateCreate(ctx, pool(templateJSON(tmpl)))
		if err == nil {
			t.Error("expected rejection for minReadySeconds: \"ten\"")
		}
	})

	t.Run("bad quantity rejects", func(t *testing.T) {
		t.Parallel()

		tmpl := validDeploymentMap()
		containers := tmpl[fieldSpec].(map[string]any)["template"].(map[string]any)[fieldSpec].(map[string]any)["containers"].([]any)
		containers[0].(map[string]any)["resources"] = map[string]any{
			"limits": map[string]any{"cpu": "not-a-quantity"},
		}

		_, err := v.ValidateCreate(ctx, pool(templateJSON(tmpl)))
		if err == nil {
			t.Error("expected rejection for cpu: \"not-a-quantity\"")
		}
	})

	t.Run("unknown field warns, not rejects", func(t *testing.T) {
		t.Parallel()

		tmpl := validDeploymentMap()
		tmpl[fieldSpec].(map[string]any)["containerz"] = "typo"

		warnings, err := v.ValidateCreate(ctx, pool(templateJSON(tmpl)))
		if err != nil {
			t.Errorf("unknown fields must warn, not reject (version skew): %v", err)
		}

		if len(warnings) == 0 {
			t.Error("expected warning about unknown field")
		}
	})

	t.Run("CRD GVK skips typed decode", func(t *testing.T) {
		t.Parallel()

		tmpl := map[string]any{
			fieldAPIVersion: argoV1alpha1,
			fieldKind:       kindRollout,
			fieldSpec: map[string]any{
				"minReadySeconds": "would-fail-if-decoded",
				fieldTemplate: map[string]any{
					fieldSpec: map[string]any{
						fieldContainers: []any{
							map[string]any{fieldName: fieldApp, fieldImage: imageNginx},
						},
					},
				},
			},
		}

		_, err := v.ValidateCreate(ctx, pool(templateJSON(tmpl)))
		if err != nil {
			t.Errorf("CRD GVK should skip typed decode: %v", err)
		}
	})
}

func TestPodPoolAsTemplate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	v := &PodPoolCustomValidator{}

	tmpl := map[string]any{
		fieldAPIVersion: "podpools.dev/v1alpha1",
		fieldKind:       "PodPool",
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				fieldSpec: map[string]any{
					fieldContainers: []any{
						map[string]any{fieldName: fieldApp, fieldImage: imageNginx},
					},
				},
			},
		},
	}

	pool := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         3,
			WorkloadTemplate: templateJSON(tmpl),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)}},
			},
		},
	}

	_, err := v.ValidateCreate(ctx, pool)
	if err == nil {
		t.Error("expected rejection for PodPool-as-template")
	}

	if !strings.Contains(err.Error(), "PodPool") {
		t.Errorf("rejection should mention PodPool: %v", err)
	}
}

func TestControllerOwnedPathOverrides(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	v := &PodPoolCustomValidator{}

	poolWith := func(overrides map[string]any) *podpoolsv1alpha1.PodPool {
		raw, _ := json.Marshal(overrides)

		return &podpoolsv1alpha1.PodPool{
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         3,
				WorkloadTemplate: validWorkloadTemplate(),
				Groups: []podpoolsv1alpha1.GroupSpec{
					{
						Name:      testGroupBase,
						Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
						Overrides: &runtime.RawExtension{Raw: raw},
					},
				},
			},
		}
	}

	tests := []struct {
		name      string
		overrides map[string]any
		wantErr   bool
	}{
		{
			name:      "spec.selector.matchLabels is controller-owned",
			overrides: map[string]any{fieldSpec: map[string]any{fieldSelector: map[string]any{fieldMatchLabels: map[string]any{fieldApp: valueMine}}}},
			wantErr:   true,
		},
		{
			name:      "spec.selector.matchExpressions passes through",
			overrides: map[string]any{fieldSpec: map[string]any{fieldSelector: map[string]any{"matchExpressions": []any{map[string]any{"key": "env", "operator": "In", "values": []any{"prod"}}}}}},
		},
		{
			name:      "spec.replicas is controller-owned",
			overrides: map[string]any{fieldSpec: map[string]any{"replicas": 5}},
			wantErr:   true,
		},
		{
			name:      "metadata.name is controller-owned",
			overrides: map[string]any{fieldMetadata: map[string]any{fieldName: "my-name"}},
			wantErr:   true,
		},
		{
			name:      "metadata.ownerReferences is controller-owned",
			overrides: map[string]any{fieldMetadata: map[string]any{"ownerReferences": []any{map[string]any{fieldName: "other"}}}},
			wantErr:   true,
		},
		{
			name:      "podpools.dev/* labels are controller-owned",
			overrides: map[string]any{fieldMetadata: map[string]any{"labels": map[string]any{"podpools.dev/custom": valueMine}}},
			wantErr:   true,
		},
		{
			name:      "non-podpools labels are fine",
			overrides: map[string]any{fieldMetadata: map[string]any{"labels": map[string]any{"app.kubernetes.io/name": valueMine}}},
		},
		{
			name:      "spec.minReadySeconds is fine",
			overrides: map[string]any{fieldSpec: map[string]any{"minReadySeconds": 30}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := v.ValidateCreate(ctx, poolWith(tt.overrides))
			if tt.wantErr && err == nil {
				t.Error("expected rejection for controller-owned path override")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("unexpected rejection: %v", err)
			}
		})
	}
}

func TestPreExistingBrokenPoolUpdate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	v := &PodPoolCustomValidator{}

	brokenOverride, _ := json.Marshal(map[string]any{
		fieldSpec: map[string]any{fieldSelector: map[string]any{fieldMatchLabels: map[string]any{fieldApp: valueMine}}},
	})

	oldPool := &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         3,
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      testGroupBase,
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](3)},
					Overrides: &runtime.RawExtension{Raw: brokenOverride},
				},
			},
		},
	}

	t.Run("same broken override on update is downgraded to warning", func(t *testing.T) {
		t.Parallel()

		newPool := oldPool.DeepCopy()
		newPool.Spec.Replicas = 5

		warnings, err := v.ValidateUpdate(ctx, oldPool, newPool)
		if err != nil {
			t.Fatalf("pre-existing broken override must not block updates: %v", err)
		}

		hasPreExisting := false

		for _, w := range warnings {
			if strings.Contains(w, "pre-existing") {
				hasPreExisting = true

				break
			}
		}

		if !hasPreExisting {
			t.Errorf("expected a pre-existing warning, got: %v", warnings)
		}
	})

	t.Run("new failure on update is still rejected", func(t *testing.T) {
		t.Parallel()

		newBrokenOverride, _ := json.Marshal(map[string]any{
			fieldSpec: map[string]any{
				fieldSelector: map[string]any{fieldMatchLabels: map[string]any{fieldApp: valueMine}},
				"replicas":    5,
			},
		})

		newPool := oldPool.DeepCopy()
		newPool.Spec.Groups[0].Overrides = &runtime.RawExtension{Raw: newBrokenOverride}

		_, err := v.ValidateUpdate(ctx, oldPool, newPool)
		if err == nil {
			t.Error("a newly introduced failure must still be rejected")
		}
	})
}

// --------------------------------------------------------------------------
// the authorization guard
// --------------------------------------------------------------------------

const (
	sarTestUser = "developer@example.com"
	sarTestNS   = "production"
)

func sarAdmissionCtx() context.Context {
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UserInfo: authenticationv1.UserInfo{
				Username: sarTestUser,
				Groups:   []string{"system:authenticated"},
			},
			Namespace: sarTestNS,
		},
	}

	return admission.NewContextWithRequest(context.Background(), req)
}

func sarInterceptor(allowed bool, sarErr error) client.WithWatch {
	return (&sarProbe{allowed: allowed, err: sarErr}).client()
}

// sarProbe stands in for the authorizer and records what the admission guard
// actually asked it.
//
// Both halves are load-bearing. The count is #63's: an outcome assertion cannot
// distinguish "the check ran and allowed" from "the check never ran", so a
// regression that re-skips the guard passes every allow-path test that only
// looks at the returned error. The captured review is #64's: the guard can
// issue a perfectly well-formed SAR about entirely the wrong resource, and the
// only way to catch that is to read the ResourceAttributes it sent.
type sarProbe struct {
	// allowed is the verdict to return; err makes the review itself fail.
	allowed bool
	err     error

	count int
	last  *authzv1.SubjectAccessReview

	// allow, when set, decides per review instead of the flat allowed field,
	// so a test can grant one resource and deny every other.
	allow func(*authzv1.SubjectAccessReview) bool
}

func (p *sarProbe) client() client.WithWatch {
	s := runtime.NewScheme()
	_ = authzv1.AddToScheme(s)
	_ = podpoolsv1alpha1.AddToScheme(s)

	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	return interceptor.NewClient(fakeClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			sar, ok := obj.(*authzv1.SubjectAccessReview)
			if !ok {
				return c.Create(ctx, obj, opts...)
			}

			p.count++
			p.last = sar.DeepCopy()

			if p.err != nil {
				return p.err
			}

			if p.allow != nil {
				sar.Status.Allowed = p.allow(sar)
			} else {
				sar.Status.Allowed = p.allowed
			}

			return nil
		},
	})
}

func sarPool() *podpoolsv1alpha1.PodPool {
	pp := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pool", Namespace: sarTestNS},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         3,
			WorkloadTemplate: validWorkloadTemplate(),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](1)}},
			},
		},
	}

	return pp
}

func TestSARGuardAllowedWorkload(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{Client: sarInterceptor(true, nil)}
	ctx := sarAdmissionCtx()

	_, err := v.ValidateCreate(ctx, sarPool())
	if err != nil {
		t.Errorf("expected allowed workload to pass: %v", err)
	}
}

func TestSARGuardDeniedWorkload(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{Client: sarInterceptor(false, nil)}
	ctx := sarAdmissionCtx()

	_, err := v.ValidateCreate(ctx, sarPool())
	if err == nil {
		t.Fatal("expected denied workload to be rejected")
	}

	if !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("rejection message should mention authorization: %v", err)
	}

	if !strings.Contains(err.Error(), sarTestUser) {
		t.Errorf("rejection message should name the user: %v", err)
	}
}

func TestSARGuardFailClosed(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{Client: sarInterceptor(false, errors.New("API server unreachable"))}
	ctx := sarAdmissionCtx()

	_, err := v.ValidateCreate(ctx, sarPool())
	if err == nil {
		t.Fatal("expected SAR failure to reject (fail-closed)")
	}

	if !strings.Contains(err.Error(), "authorization check failed") {
		t.Errorf("rejection should distinguish operational failure from denial: %v", err)
	}
}

// A kind whose resource name cannot be resolved cannot be authorized, and an
// authorization that cannot be performed is a denial. Failing open here would
// be the whole escalation path back: point a pool at any CRD workload the
// resolver does not know and the guard stops asking.
func TestSARGuardUnresolvablePluralDenies(t *testing.T) {
	t.Parallel()

	pp := sarPool()
	pp.Spec.WorkloadTemplate = templateJSON(map[string]any{
		fieldAPIVersion: argoV1alpha1,
		fieldKind:       kindRollout,
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				fieldSpec: map[string]any{
					fieldContainers: []any{
						map[string]any{fieldName: fieldApp, fieldImage: imageNginx},
					},
				},
			},
		},
	})

	probe := &sarProbe{allowed: true}
	v := &PodPoolCustomValidator{Client: probe.client()}

	_, err := v.ValidateCreate(sarAdmissionCtx(), pp)
	if err == nil {
		t.Fatal("a workload kind whose resource name cannot be resolved was admitted; " +
			"the guard silently stopped applying")
	}

	if !strings.Contains(err.Error(), "Rollout") {
		t.Errorf("rejection should name the unresolvable kind: %v", err)
	}

	if probe.count != 0 {
		t.Errorf("a SubjectAccessReview was sent for an unresolved resource (%d); "+
			"it would have authorized the wrong noun", probe.count)
	}
}

func TestSARGuardNoClientIsNoOp(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{}
	ctx := sarAdmissionCtx()

	_, err := v.ValidateCreate(ctx, sarPool())
	if err != nil {
		t.Errorf("without a client the SAR check should be a no-op: %v", err)
	}
}

func TestSARGuardNoAdmissionContextIsNoOp(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{Client: sarInterceptor(false, nil)}

	_, err := v.ValidateCreate(t.Context(), sarPool())
	if err != nil {
		t.Errorf("without admission context the SAR check should be a no-op: %v", err)
	}
}
