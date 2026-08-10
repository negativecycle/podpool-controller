package v1alpha1_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// Self-contained on purpose: it defines its own CRD loader rather than reusing
// crd_schema_test.go's, so the two files can be edited independently.

func loadCRDForCodeQuality(t *testing.T) *apiextv1.CustomResourceDefinition {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean("../../config/crd/bases/podpools.dev_podpools.yaml"))
	if err != nil {
		t.Fatalf("reading generated CRD: %v (run `make manifests`)", err)
	}

	var crd apiextv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("unmarshalling CRD: %v", err)
	}

	return &crd
}

// servedSpecSchema returns the schema node for .spec in the served version.
func servedSpecSchema(t *testing.T, crd *apiextv1.CustomResourceDefinition) *apiextv1.JSONSchemaProps {
	t.Helper()

	for _, v := range crd.Spec.Versions {
		if !v.Served || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}

		if spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]; ok {
			return &spec
		}
	}

	t.Fatal("no served version with a spec schema")

	return nil
}

// A schema default is preferred over an in-code fallback wherever the schema
// can express it: kubectl explain and the stored object both then say what
// the interval is, instead of the answer hiding in a Go constant. The in-code
// defaultProgressDeadlineSeconds still exists for objects stored before the
// default did, and this pin stops the marker being dropped as redundant once
// that fallback is noticed.
func TestProgressDeadlineHasASchemaDefault(t *testing.T) {
	spec := servedSpecSchema(t, loadCRDForCodeQuality(t))

	f, ok := spec.Properties["progressDeadlineSeconds"]
	if !ok {
		t.Fatal("spec.progressDeadlineSeconds is missing from the schema")
	}

	if f.Default == nil {
		t.Error("progressDeadlineSeconds lost its schema default (+kubebuilder:default=600)")
	}
}

// The value, not merely the presence of a default, is what is asserted here.
//
// The controller keeps an in-code fallback (defaultOpportunisticHeartbeatSeconds
// in internal/controller) for objects stored before the schema default
// existed, and a pool that never sets the field must mean the same interval to
// the schema and to that fallback. Asserting Default != nil would hold for 30
// or 99999 and leave the agreement unguarded.
//
// Cross-checking the Go constant from here would mean api/ importing
// internal/controller, which is the wrong dependency direction, so the value
// is pinned literally from both sides.
func TestOpportunisticHeartbeatDefaultsTo300(t *testing.T) {
	const (
		field = "opportunisticHeartbeatSeconds"
		want  = 300
	)

	spec := servedSpecSchema(t, loadCRDForCodeQuality(t))

	f, ok := spec.Properties[field]
	if !ok {
		t.Fatalf("spec.%s is missing from the schema", field)
	}

	if f.Default == nil {
		t.Fatalf("spec.%s has no schema default, so kubectl explain and the "+
			"stored object both stay silent about the interval", field)
	}

	var got int
	if err := json.Unmarshal(f.Default.Raw, &got); err != nil {
		t.Fatalf("spec.%s default %q is not an integer: %v", field, f.Default.Raw, err)
	}

	if got != want {
		t.Errorf("spec.%s default = %d, want %d; pools that never set the field "+
			"rely on this matching defaultOpportunisticHeartbeatSeconds",
			field, got, want)
	}
}
