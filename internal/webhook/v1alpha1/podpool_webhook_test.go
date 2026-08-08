package v1alpha1

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

const (
	testGroupBase   = "base"
	testGroupBurst  = "burst"
	fieldSpec       = "spec"
	fieldAPIVersion = "apiVersion"
	fieldKind       = "kind"
	fieldTemplate   = "template"
	fieldContainers = "containers"
	fieldApp        = "app"
	fieldImage      = "image"
	fieldName       = "name"
	appsV1          = "apps/v1"
	kindDeployment  = "Deployment"
	imageNginx      = "nginx"
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
