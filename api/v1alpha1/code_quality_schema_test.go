package v1alpha1_test

import (
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
