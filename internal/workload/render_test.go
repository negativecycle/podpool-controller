package workload

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

const (
	testGroupBurst = "burst"
)

func TestChildName(t *testing.T) {
	t.Parallel()

	if got := ChildName("my-pool", testGroupBurst); got != "my-pool-burst" {
		t.Errorf("ChildName = %q, want %q", got, "my-pool-burst")
	}
}

func TestExtractGVK(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"deployment", `{"apiVersion":"apps/v1","kind":"Deployment"}`, "apps/v1, Kind=Deployment", false},
		{"core group", `{"apiVersion":"v1","kind":"Pod"}`, "/v1, Kind=Pod", false},
		{"crd group", `{"apiVersion":"argoproj.io/v1alpha1","kind":"Rollout"}`, "argoproj.io/v1alpha1, Kind=Rollout", false},
		{"missing apiVersion", `{"kind":"Deployment"}`, "", true},
		{"missing kind", `{"apiVersion":"apps/v1"}`, "", true},
		{"malformed json", `{`, "", true},
		{"unparseable group version", `{"apiVersion":"a/b/c","kind":"X"}`, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gvk, err := ExtractGVK([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractGVK(%q) succeeded with %v, want error", tt.raw, gvk)
				}

				return
			}

			if err != nil {
				t.Fatalf("ExtractGVK(%q): %v", tt.raw, err)
			}

			if got := gvk.String(); got != tt.want {
				t.Errorf("ExtractGVK(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

const testGroupBase = "base"

func rawTemplate() []byte {
	return []byte(`{
		"apiVersion": "apps/v1",
		"kind": "Deployment",
		"spec": {
			"template": {
				"spec": {
					"containers": [{"name": "app", "image": "nginx:latest"}]
				}
			}
		}
	}`)
}

func testPool() *podpoolsv1alpha1.PodPool {
	return &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pool", Namespace: "prod", UID: "pool-uid-1"},
	}
}

func mustParse(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	tmpl, err := ParseTemplate(raw)
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}

	return tmpl
}

func TestParseTemplateRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	if _, err := ParseTemplate([]byte(`{`)); err == nil {
		t.Fatal("ParseTemplate accepted malformed JSON")
	}
}

func TestBuildChildWorkload(t *testing.T) {
	t.Parallel()

	group := podpoolsv1alpha1.GroupSpec{Name: testGroupBase}
	pool := testPool()

	child, err := BuildChildWorkload(mustParse(t, rawTemplate()), group, pool, 5)
	if err != nil {
		t.Fatalf("BuildChildWorkload: %v", err)
	}

	if got := child.GetName(); got != "my-pool-base" {
		t.Errorf("name = %q, want %q", got, "my-pool-base")
	}

	if got := child.GetNamespace(); got != "prod" {
		t.Errorf("namespace = %q, want %q", got, "prod")
	}

	replicas, found, err := unstructured.NestedInt64(child.Object, "spec", "replicas")
	if err != nil || !found {
		t.Fatalf("spec.replicas missing: found=%v err=%v", found, err)
	}

	if replicas != 5 {
		t.Errorf("spec.replicas = %d, want 5", replicas)
	}

	refs := child.GetOwnerReferences()
	if len(refs) != 1 {
		t.Fatalf("got %d ownerReferences, want 1", len(refs))
	}

	ref := refs[0]
	if ref.Kind != KindPodPool || ref.Name != "my-pool" || string(ref.UID) != "pool-uid-1" {
		t.Errorf("ownerReference = %+v, want PodPool/my-pool/pool-uid-1", ref)
	}

	if ref.Controller == nil || !*ref.Controller {
		t.Error("ownerReference is not a controller reference")
	}

	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Error("ownerReference does not block owner deletion")
	}
}

func TestBuildChildWorkloadRejectsMissingTemplate(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"apiVersion": "apps/v1", "kind": "Deployment", "spec": {}}`)

	_, err := BuildChildWorkload(mustParse(t, raw), podpoolsv1alpha1.GroupSpec{Name: testGroupBase}, testPool(), 1)
	if err == nil {
		t.Fatal("BuildChildWorkload accepted a workload with no .spec.template")
	}
}

// TestRenderIsIdempotent pins the parse-once, copy-per-group contract: the
// caller parses the template a single time, and BuildChildWorkload must
// neither mutate that shared map nor render differently on a second call.
func TestRenderIsIdempotent(t *testing.T) {
	t.Parallel()

	group := podpoolsv1alpha1.GroupSpec{Name: testGroupBase}
	pool := testPool()

	parsed := mustParse(t, rawTemplate())
	snapshot, _ := json.Marshal(parsed)

	first, err := BuildChildWorkload(parsed, group, pool, 5)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	if after, _ := json.Marshal(parsed); string(snapshot) != string(after) {
		t.Fatalf("BuildChildWorkload mutated the shared template:\n before: %s\n  after: %s", snapshot, after)
	}

	second, err := BuildChildWorkload(parsed, group, pool, 5)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}

	firstJSON, _ := json.Marshal(first.Object)

	secondJSON, _ := json.Marshal(second.Object)
	if string(firstJSON) != string(secondJSON) {
		t.Errorf("renders differ across calls:\n first: %s\nsecond: %s", firstJSON, secondJSON)
	}
}

// TestRenderDoesNotLeakBetweenGroups renders two groups from the same parsed
// template and checks neither build contaminated the other. Without the
// DeepCopyJSON both children share one map, and the second build's name
// overwrites the first's.
func TestRenderDoesNotLeakBetweenGroups(t *testing.T) {
	t.Parallel()

	pool := testPool()
	parsed := mustParse(t, rawTemplate())

	groupA := podpoolsv1alpha1.GroupSpec{Name: testGroupBase}
	groupB := podpoolsv1alpha1.GroupSpec{Name: testGroupBurst}

	childA, err := BuildChildWorkload(parsed, groupA, pool, 3)
	if err != nil {
		t.Fatalf("building group A: %v", err)
	}

	childB, err := BuildChildWorkload(parsed, groupB, pool, 2)
	if err != nil {
		t.Fatalf("building group B: %v", err)
	}

	if childA.GetName() == childB.GetName() {
		t.Errorf("children should have distinct names, both got %q", childA.GetName())
	}

	replicasA, _, _ := unstructured.NestedInt64(childA.Object, "spec", "replicas")
	if replicasA != 3 {
		t.Errorf("group A replicas = %d, want 3 (group B's build leaked in)", replicasA)
	}
}
