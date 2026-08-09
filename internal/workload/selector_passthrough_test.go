/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workload_test

// The rule under test: the controller never creates, modifies, or fills in
// any label selector inside spec.template.spec. Pod scheduling is expressed
// entirely by the top-level workloadTemplate and per-group overrides.
//
// These tests pin a deletion that was never made. Injecting pool labels into
// scheduling selectors looks like a completeness fix, and a reviewer reading
// "fills a selector only when absent" as a gap rather than a policy is
// exactly how the rule gets helpfully undone. Stating it as tests means a
// future scoping pass has to argue with a red suite, not a comment. An
// external test package (workload_test) deliberately: it uses only the
// exported surface, and it cannot collide with test files added to package
// workload.

import (
	"encoding/json"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

const (
	fieldAPIVersion        = "apiVersion"
	fieldKind              = "kind"
	fieldSpec              = "spec"
	fieldTemplate          = "template"
	fieldContainers        = "containers"
	fieldAffinity          = "affinity"
	fieldLabelSelector     = "labelSelector"
	fieldMatchLabels       = "matchLabels"
	fieldTopologySpread    = "topologySpreadConstraints"
	fieldMaxSkew           = "maxSkew"
	fieldWhenUnsatisfiable = "whenUnsatisfiable"
	fieldTopologyKey       = "topologyKey"
	affinityPodAnti        = "podAntiAffinity"
	affinityRequired       = "requiredDuringSchedulingIgnoredDuringExecution"
	affinityPreferred      = "preferredDuringSchedulingIgnoredDuringExecution"
	valAppsV1              = "apps/v1"
	valDeployment          = "Deployment"
	valDoNotSchedule       = "DoNotSchedule"
	valHostnameKey         = "kubernetes.io/hostname"
	valApp                 = "app"
	valWeb                 = "web"
	valBase                = "base"
	valDefault             = "default"
	valUID                 = "uid-1"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustParse(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	m, err := workload.ParseTemplate(raw)
	if err != nil {
		t.Fatalf("ParseTemplate: %v", err)
	}

	return m
}

// renderPodSpec builds a child from a pod spec and returns the rendered
// spec.template.spec, which is where every field this plan is about lives.
func renderPodSpec(t *testing.T, podSpec map[string]any) map[string]any {
	t.Helper()

	tmpl := map[string]any{
		fieldAPIVersion: valAppsV1,
		fieldKind:       valDeployment,
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				fieldSpec: podSpec,
			},
		},
	}

	raw, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: valDefault, UID: valUID},
	}
	group := podpoolsv1alpha1.GroupSpec{Name: valBase}

	child, err := workload.BuildChildWorkload(mustParse(t, raw), group, pool, 1)
	if err != nil {
		t.Fatalf("BuildChildWorkload: %v", err)
	}

	out, found, err := unstructured.NestedMap(child.Object, fieldSpec, fieldTemplate, fieldSpec)
	if err != nil || !found {
		t.Fatalf("rendered child has no spec.template.spec (found=%v, err=%v)", found, err)
	}

	return out
}

// containersOnly is the minimum a pod spec needs; every fixture embeds it so
// the rendered object stays a plausible Deployment.
func containersOnly() []any {
	return []any{map[string]any{"name": valApp, "image": "nginx"}}
}

func firstSpreadConstraint(t *testing.T, podSpec map[string]any) map[string]any {
	t.Helper()

	list, found, err := unstructured.NestedSlice(podSpec, fieldTopologySpread)
	if err != nil || !found || len(list) == 0 {
		t.Fatalf("no topologySpreadConstraints in rendered output (found=%v, err=%v)", found, err)
	}

	tsc, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("spread constraint is %T, want map", list[0])
	}

	return tsc
}

func firstRequiredTerm(t *testing.T, podSpec map[string]any, affinityKind string) map[string]any {
	t.Helper()

	list, found, err := unstructured.NestedSlice(podSpec,
		fieldAffinity, affinityKind, affinityRequired)
	if err != nil || !found || len(list) == 0 {
		t.Fatalf("no required %s terms in rendered output (found=%v, err=%v)", affinityKind, found, err)
	}

	term, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("affinity term is %T, want map", list[0])
	}

	return term
}

func assertSelectorEquals(t *testing.T, holder map[string]any, want map[string]any) {
	t.Helper()

	got, _ := holder[fieldLabelSelector].(map[string]any)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("labelSelector was rewritten\n got: %#v\nwant: %#v", got, want)
	}
}

// ---------------------------------------------------------------------------
// The headline case: a selector pointing at somebody else's pods
// ---------------------------------------------------------------------------

// TestCrossAppPodAffinityStaysSatisfiable is the case the review found. A
// required podAffinity toward another application can never match once this
// pool's labels are merged in — those pods do not carry them — so every pod in
// the group sits Pending forever, surfacing only via the progress deadline, milestones from now.
func TestCrossAppPodAffinityStaysSatisfiable(t *testing.T) {
	t.Parallel()

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldAffinity: map[string]any{
			"podAffinity": map[string]any{
				affinityRequired: []any{
					map[string]any{
						fieldTopologyKey: valHostnameKey,
						fieldLabelSelector: map[string]any{
							fieldMatchLabels: map[string]any{valApp: "redis"},
						},
					},
				},
			},
		},
	})

	assertSelectorEquals(t, firstRequiredTerm(t, rendered, "podAffinity"), map[string]any{
		fieldMatchLabels: map[string]any{valApp: "redis"},
	})
}

// TestCrossAppAntiAffinityStaysMeaningful is the silent half. Merging our
// labels into an anti-affinity aimed at another app makes it vacuous: it now
// repels only pods of this pool, which the pool was never near. Nothing fails,
// the protection just stops existing.
func TestCrossAppAntiAffinityStaysMeaningful(t *testing.T) {
	t.Parallel()

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldAffinity: map[string]any{
			affinityPodAnti: map[string]any{
				affinityRequired: []any{
					map[string]any{
						fieldTopologyKey: valHostnameKey,
						fieldLabelSelector: map[string]any{
							fieldMatchLabels: map[string]any{valApp: "noisy-neighbour"},
						},
					},
				},
			},
		},
	})

	assertSelectorEquals(t, firstRequiredTerm(t, rendered, affinityPodAnti), map[string]any{
		fieldMatchLabels: map[string]any{valApp: "noisy-neighbour"},
	})
}

// ---------------------------------------------------------------------------
// Shapes the merge corrupts in less obvious ways
// ---------------------------------------------------------------------------

// TestCrossNamespaceTermUntouched covers the shape most likely to be forgotten
// if someone reintroduces scoping. Pods in another namespace can never carry
// this pool's labels, so merging them in makes the term unsatisfiable by
// construction.
func TestCrossNamespaceTermUntouched(t *testing.T) {
	t.Parallel()

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldAffinity: map[string]any{
			"podAffinity": map[string]any{
				affinityRequired: []any{
					map[string]any{
						fieldTopologyKey: valHostnameKey,
						"namespaces":     []any{"other-ns"},
						fieldLabelSelector: map[string]any{
							fieldMatchLabels: map[string]any{valApp: "cache"},
						},
					},
				},
			},
		},
	})

	term := firstRequiredTerm(t, rendered, "podAffinity")
	assertSelectorEquals(t, term, map[string]any{
		fieldMatchLabels: map[string]any{valApp: "cache"},
	})

	if ns, _ := term["namespaces"].([]any); len(ns) != 1 || ns[0] != "other-ns" {
		t.Errorf("namespaces rewritten: %#v", term["namespaces"])
	}
}

// TestMatchExpressionsOnlySelectorUntouched pins the case where the merge does
// not overwrite anything but still changes meaning: pool/group land in
// matchLabels beside the user's expression, and the two are ANDed.
func TestMatchExpressionsOnlySelectorUntouched(t *testing.T) {
	t.Parallel()

	selector := map[string]any{
		"matchExpressions": []any{
			map[string]any{
				"key":      valApp,
				"operator": "In",
				"values":   []any{valWeb, "api"},
			},
		},
	}

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldTopologySpread: []any{
			map[string]any{
				fieldMaxSkew:           int64(1),
				fieldTopologyKey:       valHostnameKey,
				fieldWhenUnsatisfiable: valDoNotSchedule,
				fieldLabelSelector:     selector,
			},
		},
	})

	assertSelectorEquals(t, firstSpreadConstraint(t, rendered), selector)
}

// TestAbsentSelectorStaysAbsent is the "no defaults, ever" guarantee, and the
// one most likely to be helpfully undone. A PodAffinityTerm with no selector
// matches no pods and a spread constraint with none counts no pods — both are
// inert, and inventing a selector turns a no-op into a real constraint the user
// never wrote.
func TestAbsentSelectorStaysAbsent(t *testing.T) {
	t.Parallel()

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldTopologySpread: []any{
			map[string]any{
				fieldMaxSkew:           int64(1),
				fieldTopologyKey:       valHostnameKey,
				fieldWhenUnsatisfiable: "ScheduleAnyway",
			},
		},
	})

	tsc := firstSpreadConstraint(t, rendered)
	if _, present := tsc[fieldLabelSelector]; present {
		t.Errorf("a labelSelector was invented where the user wrote none: %#v", tsc[fieldLabelSelector])
	}
}

// TestEmptySelectorStaysEmpty is the sharpest single behaviour change in this
// plan, so it gets its own test rather than riding on the absent case. An
// explicit `labelSelector: {}` is matches-everything; filling it narrows it to
// this group.
func TestEmptySelectorStaysEmpty(t *testing.T) {
	t.Parallel()

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldTopologySpread: []any{
			map[string]any{
				fieldMaxSkew:           int64(1),
				fieldTopologyKey:       valHostnameKey,
				fieldWhenUnsatisfiable: valDoNotSchedule,
				fieldLabelSelector:     map[string]any{},
			},
		},
	})

	assertSelectorEquals(t, firstSpreadConstraint(t, rendered), map[string]any{})
}

// TestUserSuppliedPoolLabelSurvives is the odd one, and deliberately so. A user
// who writes podpools.dev/pool: some-other-pool in a spread selector is making
// a statement about which pods to count, and honouring it is the rule. Today
// the controller overwrites it with this pool's name.
func TestUserSuppliedPoolLabelSurvives(t *testing.T) {
	t.Parallel()

	selector := map[string]any{
		fieldMatchLabels: map[string]any{workload.LabelPool: "some-other-pool"},
	}

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldTopologySpread: []any{
			map[string]any{
				fieldMaxSkew:           int64(1),
				fieldTopologyKey:       valHostnameKey,
				fieldWhenUnsatisfiable: valDoNotSchedule,
				fieldLabelSelector:     selector,
			},
		},
	})

	assertSelectorEquals(t, firstSpreadConstraint(t, rendered), selector)
}

// TestPreferredTermsAreAlsoPassedThrough covers the second shape of the
// affinity walk: preferred terms nest the selector one level deeper, under
// podAffinityTerm, and are handled by a separate branch that could be fixed or
// missed independently of the required one.
func TestPreferredTermsAreAlsoPassedThrough(t *testing.T) {
	t.Parallel()

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldAffinity: map[string]any{
			affinityPodAnti: map[string]any{
				affinityPreferred: []any{
					map[string]any{
						"weight": int64(100),
						"podAffinityTerm": map[string]any{
							fieldTopologyKey: valHostnameKey,
							fieldLabelSelector: map[string]any{
								fieldMatchLabels: map[string]any{valApp: valWeb},
							},
						},
					},
				},
			},
		},
	})

	list, found, err := unstructured.NestedSlice(rendered,
		fieldAffinity, affinityPodAnti, affinityPreferred)
	if err != nil || !found || len(list) == 0 {
		t.Fatalf("no preferred terms in rendered output (found=%v, err=%v)", found, err)
	}

	weighted, _ := list[0].(map[string]any)

	pat, _ := weighted["podAffinityTerm"].(map[string]any)
	if pat == nil {
		t.Fatalf("preferred term has no podAffinityTerm: %#v", list[0])
	}

	assertSelectorEquals(t, pat, map[string]any{
		fieldMatchLabels: map[string]any{valApp: valWeb},
	})
}

// TestMatchLabelKeysRecipeRendersVerbatim pins the mechanism the documentation
// recommends in place of the deleted injection. If Recipe A does not survive
// rendering unchanged, the docs are wrong.
func TestMatchLabelKeysRecipeRendersVerbatim(t *testing.T) {
	t.Parallel()

	rendered := renderPodSpec(t, map[string]any{
		fieldContainers: containersOnly(),
		fieldTopologySpread: []any{
			map[string]any{
				fieldMaxSkew:           int64(1),
				fieldTopologyKey:       valHostnameKey,
				fieldWhenUnsatisfiable: valDoNotSchedule,
				fieldLabelSelector: map[string]any{
					fieldMatchLabels: map[string]any{valApp: valWeb},
				},
				"matchLabelKeys": []any{workload.LabelGroup},
			},
		},
	})

	tsc := firstSpreadConstraint(t, rendered)
	assertSelectorEquals(t, tsc, map[string]any{
		fieldMatchLabels: map[string]any{valApp: valWeb},
	})

	if keys, _ := tsc["matchLabelKeys"].([]any); len(keys) != 1 || keys[0] != workload.LabelGroup {
		t.Errorf("matchLabelKeys was rewritten: %#v", tsc["matchLabelKeys"])
	}
}

// ---------------------------------------------------------------------------
// What the controller still owns — these must keep passing
// ---------------------------------------------------------------------------

// TestControllerStillOwnsChildSelectorAndPodLabels is the counterweight. The
// plan is a deletion, and the risk of a deletion is taking too much: the
// child's spec.selector and the pod template's podpools.dev/* labels are
// genuinely controller-owned, and status.selector plus HPA depend on them.
func TestControllerStillOwnsChildSelectorAndPodLabels(t *testing.T) {
	t.Parallel()

	tmpl := map[string]any{
		fieldAPIVersion: valAppsV1,
		fieldKind:       valDeployment,
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				"metadata": map[string]any{"labels": map[string]any{valApp: valWeb}},
				fieldSpec:  map[string]any{fieldContainers: containersOnly()},
			},
		},
	}

	raw, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: valDefault, UID: valUID},
	}

	child, err := workload.BuildChildWorkload(mustParse(t, raw),
		podpoolsv1alpha1.GroupSpec{Name: valBase}, pool, 3)
	if err != nil {
		t.Fatalf("BuildChildWorkload: %v", err)
	}

	sel, _, _ := unstructured.NestedStringMap(child.Object, fieldSpec, "selector", fieldMatchLabels)
	if sel[workload.LabelPool] != "p" || sel[workload.LabelGroup] != valBase {
		t.Errorf("child spec.selector.matchLabels lost its controller labels: %#v", sel)
	}

	podLabels, _, _ := unstructured.NestedStringMap(child.Object, fieldSpec, fieldTemplate, "metadata", "labels")
	for k, want := range map[string]string{
		workload.LabelPool:      "p",
		workload.LabelGroup:     valBase,
		workload.LabelManagedBy: workload.ManagerName,
		valApp:                  valWeb,
	} {
		if podLabels[k] != want {
			t.Errorf("pod template label %s = %q, want %q", k, podLabels[k], want)
		}
	}
}

// TestOverrideSuppliedAffinityIsAlsoPassedThrough is what makes Recipe B work:
// overrides are merged before rendering, so an affinity block arriving that way
// must be as untouched as one in the shared template.
func TestOverrideSuppliedAffinityIsAlsoPassedThrough(t *testing.T) {
	t.Parallel()

	tmpl := map[string]any{
		fieldAPIVersion: valAppsV1,
		fieldKind:       valDeployment,
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{fieldSpec: map[string]any{fieldContainers: containersOnly()}},
		},
	}

	raw, err := json.Marshal(tmpl)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	overrides := map[string]any{
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				fieldSpec: map[string]any{
					fieldAffinity: map[string]any{
						affinityPodAnti: map[string]any{
							affinityRequired: []any{
								map[string]any{
									fieldTopologyKey: valHostnameKey,
									fieldLabelSelector: map[string]any{
										fieldMatchLabels: map[string]any{
											valApp:              valWeb,
											workload.LabelGroup: valBase,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	overrideRaw, err := json.Marshal(overrides)
	if err != nil {
		t.Fatalf("marshal overrides: %v", err)
	}

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: valDefault, UID: valUID},
	}
	group := podpoolsv1alpha1.GroupSpec{
		Name:      valBase,
		Overrides: &runtime.RawExtension{Raw: overrideRaw},
	}

	child, err := workload.BuildChildWorkload(mustParse(t, raw), group, pool, 1)
	if err != nil {
		t.Fatalf("BuildChildWorkload: %v", err)
	}

	rendered, _, _ := unstructured.NestedMap(child.Object, fieldSpec, fieldTemplate, fieldSpec)

	assertSelectorEquals(t, firstRequiredTerm(t, rendered, affinityPodAnti), map[string]any{
		fieldMatchLabels: map[string]any{
			valApp:              valWeb,
			workload.LabelGroup: valBase,
		},
	})
}
