package workload

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

const (
	testGroupBurst = "burst"

	fieldStatus   = "status"
	fieldReplicas = "replicas"
	fieldMetadata = "metadata"
	fieldUID      = "uid"
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

// TestBuildChildWorkloadStripsPastedMetadata renders a template pasted from a
// live object and checks nothing instance-specific survives into the child.
func TestBuildChildWorkloadStripsPastedMetadata(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"apiVersion": "apps/v1",
		"kind": "Deployment",
		"metadata": {
			"name": "pasted",
			"uid": "pasted-uid",
			"resourceVersion": "12345",
			"generation": 7,
			"creationTimestamp": "2026-01-01T00:00:00Z",
			"managedFields": [{"manager": "kubectl"}],
			"ownerReferences": [{"apiVersion": "v1", "kind": "Foo", "name": "x", "uid": "y"}],
			"annotations": {
				"kubectl.kubernetes.io/last-applied-configuration": "{}"
			}
		},
		"spec": {
			"template": {"spec": {"containers": [{"name": "app", "image": "nginx:latest"}]}}
		},
		"status": {"readyReplicas": 9}
	}`)

	child, err := BuildChildWorkload(mustParse(t, raw), podpoolsv1alpha1.GroupSpec{Name: testGroupBase}, testPool(), 2)
	if err != nil {
		t.Fatalf("BuildChildWorkload: %v", err)
	}

	md := child.Object["metadata"].(map[string]any)
	for _, key := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields", "annotations"} {
		if _, present := md[key]; present {
			t.Errorf("metadata.%s survived the strip", key)
		}
	}

	if _, present := child.Object["status"]; present {
		t.Error("status survived the strip; the child would be created claiming another object's state")
	}

	refs := child.GetOwnerReferences()
	if len(refs) != 1 || refs[0].Kind != KindPodPool {
		t.Errorf("pasted ownerReferences were not replaced by the pool's: %+v", refs)
	}
}

func TestReadInt32(t *testing.T) {
	t.Parallel()

	child := &unstructured.Unstructured{Object: map[string]any{
		fieldStatus: map[string]any{
			fieldReplicas:  int64(4),
			"readyBadType": "three",
		},
	}}

	tests := []struct {
		name   string
		fields []string
		want   int32
		wantOK bool
	}{
		{"present", []string{fieldStatus, fieldReplicas}, 4, true},
		// omitempty on the built-in types makes absent the ordinary state of
		// a healthy child reporting zero; ok=false is not a diagnosis.
		{"absent leaf", []string{fieldStatus, "readyReplicas"}, 0, false},
		{"absent branch", []string{"nowhere", fieldReplicas}, 0, false},
		{"wrong type", []string{fieldStatus, "readyBadType"}, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ReadInt32(child, tt.fields...)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ReadInt32(%v) = (%d, %v), want (%d, %v)",
					tt.fields, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestMergeMaps(t *testing.T) {
	t.Parallel()

	base := map[string]any{
		"a": "1",
		"b": map[string]any{
			"x": "2",
			"y": "3",
		},
		"c": "keep",
	}
	patch := map[string]any{
		"a": "overridden",
		"b": map[string]any{
			"x": "changed",
			"z": "added",
		},
		"c": nil,
	}

	result := MergeMaps(base, patch)

	if result["a"] != "overridden" {
		t.Errorf("a: got %v, want overridden", result["a"])
	}

	b := result["b"].(map[string]any)
	if b["x"] != "changed" {
		t.Errorf("b.x: got %v, want changed", b["x"])
	}

	if b["y"] != "3" {
		t.Errorf("b.y: got %v, want 3 (preserved from base)", b["y"])
	}

	if b["z"] != "added" {
		t.Errorf("b.z: got %v, want added", b["z"])
	}

	if _, ok := result["c"]; ok {
		t.Error("c should have been deleted by null patch")
	}

	if base["a"] != "1" || base["b"].(map[string]any)["x"] != "2" {
		t.Error("MergeMaps mutated its base; a shared template merged twice would corrupt")
	}
}

func TestBuildChildWorkloadWithOverrides(t *testing.T) {
	t.Parallel()

	override := map[string]any{
		"spec": map[string]any{
			"minReadySeconds": float64(30),
		},
	}
	overrideBytes, _ := json.Marshal(override)

	group := podpoolsv1alpha1.GroupSpec{
		Name:      testGroupBase,
		Overrides: &runtime.RawExtension{Raw: overrideBytes},
	}

	child, err := BuildChildWorkload(mustParse(t, rawTemplate()), group, testPool(), 5)
	if err != nil {
		t.Fatalf("BuildChildWorkload: %v", err)
	}

	minReady, found, _ := unstructured.NestedFloat64(child.Object, "spec", "minReadySeconds")
	if !found || minReady != 30 {
		t.Errorf("minReadySeconds: got %v (found=%v), want 30", minReady, found)
	}
}

// TestRenderStripsMetadataFromOverrides closes the loophole the strip left
// open: pasted metadata can arrive through an override just as easily as
// through the template, and it is exactly as wrong there.
func TestRenderStripsMetadataFromOverrides(t *testing.T) {
	t.Parallel()

	override := map[string]any{
		fieldMetadata: map[string]any{
			fieldUID:          "override-uid",
			"resourceVersion": "99999",
		},
	}
	overrideBytes, _ := json.Marshal(override)

	group := podpoolsv1alpha1.GroupSpec{
		Name:      testGroupBase,
		Overrides: &runtime.RawExtension{Raw: overrideBytes},
	}

	child, err := BuildChildWorkload(mustParse(t, rawTemplate()), group, testPool(), 1)
	if err != nil {
		t.Fatalf("BuildChildWorkload: %v", err)
	}

	meta, _, _ := unstructured.NestedMap(child.Object, fieldMetadata)
	if _, ok := meta[fieldUID]; ok {
		t.Error("uid introduced by override should have been stripped")
	}

	if _, ok := meta["resourceVersion"]; ok {
		t.Error("resourceVersion introduced by override should have been stripped")
	}
}

func TestBuildChildWorkloadRejectsMalformedOverrides(t *testing.T) {
	t.Parallel()

	group := podpoolsv1alpha1.GroupSpec{
		Name:      testGroupBase,
		Overrides: &runtime.RawExtension{Raw: []byte(`{`)},
	}

	_, err := BuildChildWorkload(mustParse(t, rawTemplate()), group, testPool(), 1)
	if err == nil {
		t.Fatal("BuildChildWorkload accepted malformed override JSON")
	}
}
