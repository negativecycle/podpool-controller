package v1alpha1_test

import (
	"os"
	"path/filepath"
	"testing"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// Structural assertions on the generated CRD.
//
// Deliberately separate from a regenerate-and-diff drift check. That check
// compares generated output to its own source, so deleting a marker and
// regenerating passes happily — the output matches the (now weaker) input.
// Only an assertion about what the schema must *contain* notices a rule
// going missing.

const crdPath = "../../config/crd/bases/podpools.dev_podpools.yaml"

func loadPodPoolCRD(t *testing.T) *apiextv1.CustomResourceDefinition {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(crdPath))
	if err != nil {
		t.Fatalf("reading generated CRD: %v (run `make manifests`)", err)
	}

	var crd apiextv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatalf("unmarshalling CRD: %v", err)
	}

	return &crd
}

// scalingSchema returns the schema node for spec.groups[].scaling in the served
// version, which is where ScalingConstraints' type-level rules render.
func scalingSchema(t *testing.T, crd *apiextv1.CustomResourceDefinition) *apiextv1.JSONSchemaProps {
	t.Helper()

	for _, v := range crd.Spec.Versions {
		if !v.Served || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}

		spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			continue
		}

		groups, ok := spec.Properties["groups"]
		if !ok || groups.Items == nil || groups.Items.Schema == nil {
			continue
		}

		scaling, ok := groups.Items.Schema.Properties["scaling"]
		if !ok {
			continue
		}

		return &scaling
	}

	t.Fatalf("no served version exposes spec.groups[].scaling")

	return nil
}

// TestScalingSchemaCarriesCELRules is the assertion the schema rules exist to
// satisfy: the combination rules must live in the schema, so they survive the
// webhook being unavailable or disabled with ENABLE_WEBHOOKS=false.
//
// Three rules rather than one, so each violation gets a targeted message —
// a single monolithic expression yields one generic rejection for every
// mistake, and the CEL message is what the user actually reads, because schema
// validation runs before the validating webhook.
func TestScalingSchemaCarriesCELRules(t *testing.T) {
	const wantRules = 3

	scaling := scalingSchema(t, loadPodPoolCRD(t))

	if len(scaling.XValidations) != wantRules {
		t.Errorf("spec.groups[].scaling has %d x-kubernetes-validations rules, want %d; "+
			"the legal-combination rules are enforced only by the webhook, so a cluster "+
			"running with ENABLE_WEBHOOKS=false admits invalid specs",
			len(scaling.XValidations), wantRules)
	}

	for i, rule := range scaling.XValidations {
		if rule.Rule == "" {
			t.Errorf("rule %d has an empty expression", i)
		}

		if rule.Message == "" && rule.MessageExpression == "" {
			t.Errorf("rule %d (%q) has no message; the CEL message is what the user sees, "+
				"since schema validation rejects before the validating webhook runs",
				i, rule.Rule)
		}
	}
}

// TestGroupsRetainMaxItemsForCELCostBudget guards a dependency that is invisible
// at the point it would break.
//
// The CEL cost estimator multiplies a rule's per-evaluation cost by the maximum
// number of items it can run against. MaxItems=32 on spec.groups is what bounds
// that; without it the estimator falls back to a bound derived from the request
// size limit, and a CRD whose estimated cost exceeds the budget is rejected
// **at install time**. Measured while writing this commit: without the cap the
// target rule prices at 2.3x over budget and envtest refuses to install the
// CRD at all. The symptom is `make install` failing on the CRD itself — every
// existing pool unaffected, but no new version installable — which does not
// look like it was caused by removing a cap.
//
// Passes today. It is here so that removing the cap fails loudly and locally.
func TestGroupsRetainMaxItemsForCELCostBudget(t *testing.T) {
	crd := loadPodPoolCRD(t)

	for _, v := range crd.Spec.Versions {
		if !v.Served || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}

		groups, ok := v.Schema.OpenAPIV3Schema.Properties["spec"].Properties["groups"]
		if !ok {
			t.Fatalf("version %s has no spec.groups", v.Name)
		}

		if groups.MaxItems == nil {
			t.Errorf("version %s: spec.groups has no maxItems; CEL cost estimation for the "+
				"scaling rules is unbounded without it and the CRD may be rejected at install",
				v.Name)

			continue
		}

		if *groups.MaxItems > 32 {
			t.Errorf("version %s: spec.groups maxItems is %d; raising it re-opens the CEL "+
				"cost-estimate question and must be checked against a real API server",
				v.Name, *groups.MaxItems)
		}
	}
}
