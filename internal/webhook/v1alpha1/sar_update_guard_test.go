package v1alpha1

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// #63: ValidateUpdate ran no authorization check at all, and the only thing
// standing on the update path was the GVK immutability check, which needs BOTH
// the stored and the new template to parse. A stored template that does not
// parse disabled it, so a principal holding only `update podpools` could point
// a broken pool at any workload kind the manager reconciles, and the manager
// would create it under its own cluster-wide credentials.
//
// The control this defeated is the one SECURITY.md documents as enforced.

// templateWithoutTypeMeta is the realistic unparseable case: a perfectly good
// object that has lost its apiVersion and kind. Reachable via a pool created
// before validateWorkloadTemplate landed, a later-ordered mutating webhook that
// rewrites spec.workloadTemplate, or a window where the
// ValidatingWebhookConfiguration was absent. spec.workloadTemplate is Schemaless
// with PreserveUnknownFields, so the API server never checks it.
func templateWithoutTypeMeta() runtime.RawExtension {
	tmpl := map[string]any{
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

// templateWithBadGroupVersion is the third ExtractGVK failure mode: valid JSON,
// present type meta, but an apiVersion ParseGroupVersion rejects.
func templateWithBadGroupVersion() runtime.RawExtension {
	tmpl := map[string]any{
		fieldAPIVersion: "apps/v1/beta1",
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

func templateForKind(apiVersion, kind string) runtime.RawExtension {
	tmpl := map[string]any{
		fieldAPIVersion: apiVersion,
		fieldKind:       kind,
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

// wantNotAuthorized is the substring every denial-by-SAR case looks for.
const wantNotAuthorized = "not authorized"

func poolWithTemplate(tmpl runtime.RawExtension) *podpoolsv1alpha1.PodPool {
	pp := sarPool()
	pp.Spec.WorkloadTemplate = tmpl

	return pp
}

// TestSARUpdateGuard is #63's table. Counting the SARs is not decoration: the
// outcome alone cannot tell "the check ran and allowed" from "the check never
// ran", so a regression that re-skips the guard would pass every allow-path
// assertion on outcome alone.
func TestSARUpdateGuard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		oldTmpl    runtime.RawExtension
		newTmpl    runtime.RawExtension
		sarAllowed bool
		sarErr     error
		wantErr    string // substring; empty means admitted
		wantSARs   int
	}{
		{
			name: "unchanged GVK does not pay for a SAR",
			// The create-time decision still vouches for this type, so a spec
			// edit that leaves the GVK alone must not cost a round-trip.
			oldTmpl: validWorkloadTemplate(), newTmpl: validWorkloadTemplate(),
			sarAllowed: false, wantErr: "", wantSARs: 0,
		},
		{
			name:    "unparseable stored template re-authorizes and denies",
			oldTmpl: templateWithoutTypeMeta(), newTmpl: validWorkloadTemplate(),
			sarAllowed: false, wantErr: wantNotAuthorized, wantSARs: 1,
		},
		{
			name:    "unparseable stored template re-authorizes and allows",
			oldTmpl: templateWithoutTypeMeta(), newTmpl: validWorkloadTemplate(),
			sarAllowed: true, wantErr: "", wantSARs: 1,
		},
		{
			name: "repair onto a different kind is authorized against that kind",
			// The bypass in its sharpest form: the stored pool was a Deployment
			// nobody can read, and the update retargets it at StatefulSets.
			oldTmpl: templateWithoutTypeMeta(), newTmpl: templateForKind(appsV1, "StatefulSet"),
			sarAllowed: false, wantErr: wantNotAuthorized, wantSARs: 1,
		},
		{
			name: "unparseable to unparseable is rejected before any SAR",
			// validatePodPoolSpec rejects the new template, so there is nothing
			// to authorize and no round-trip to spend.
			oldTmpl: templateWithoutTypeMeta(), newTmpl: templateWithoutTypeMeta(),
			sarAllowed: false, wantErr: "apiVersion", wantSARs: 0,
		},
		{
			name:    "empty stored Raw re-authorizes",
			oldTmpl: runtime.RawExtension{}, newTmpl: validWorkloadTemplate(),
			sarAllowed: false, wantErr: wantNotAuthorized, wantSARs: 1,
		},
		{
			name:    "unparseable stored groupVersion re-authorizes",
			oldTmpl: templateWithBadGroupVersion(), newTmpl: validWorkloadTemplate(),
			sarAllowed: false, wantErr: wantNotAuthorized, wantSARs: 1,
		},
		{
			name: "parseable GVK change is rejected, and still authorized",
			// The redundant SAR on an already-doomed request is deliberate. It
			// keeps the authorization independent of the immutability check
			// rather than nested behind it.
			oldTmpl: validWorkloadTemplate(), newTmpl: templateForKind(appsV1, "StatefulSet"),
			sarAllowed: true, wantErr: "immutable", wantSARs: 1,
		},
		{
			name:    "a broken authorization check fails closed",
			oldTmpl: templateWithoutTypeMeta(), newTmpl: validWorkloadTemplate(),
			sarErr: errors.New("API server unreachable"),
			// Operational failure must deny, and must read differently from a
			// decision the user could act on.
			wantErr: "authorization check failed", wantSARs: 1,
		},
		{
			name: "an unresolvable kind fails closed before the SAR",
			// No RESTMapper is wired in these tests, so a non-builtin kind is
			// unresolvable, which is what an uninstalled CRD looks like.
			oldTmpl: templateWithoutTypeMeta(), newTmpl: templateForKind("example.com/v1", "Widget"),
			sarAllowed: true,
			wantErr:    "cannot verify authorization", wantSARs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			probe := &sarProbe{allowed: tt.sarAllowed, err: tt.sarErr}

			// RESTMapper stays nil: every kind under test is either in
			// builtinPluralResources or deliberately unresolvable.
			v := &PodPoolCustomValidator{Client: probe.client()}

			oldPool := poolWithTemplate(tt.oldTmpl)
			newPool := poolWithTemplate(tt.newTmpl)

			// #66 returns early when the spec did not move, which two rows here
			// would otherwise trip by using the same template on both sides.
			// Every row is about what this guard does once the spec HAS moved,
			// so give them all a spec change that is not the template. Without
			// this the rows still pass, on the short-circuit rather than on the
			// behaviour they name. The short-circuit's own zero-SAR assertion
			// lives in spec_shortcircuit_test.go.
			newPool.Spec.Replicas = oldPool.Spec.Replicas + 1

			_, err := v.ValidateUpdate(sarAdmissionCtx(), oldPool, newPool)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("expected the update to be admitted, got: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("expected rejection containing %q, got no error", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("rejection = %v, want it to contain %q", err, tt.wantErr)
			}

			if probe.count != tt.wantSARs {
				t.Errorf("SubjectAccessReviews issued = %d, want %d", probe.count, tt.wantSARs)
			}
		})
	}
}

// The update rejection has to read differently from the create one. On create
// the user knows they asked for a workload; on update they were repairing a
// pool and would otherwise be told they are "not authorized to create"
// something they never mentioned.
func TestSARUpdateDenialExplainsWhyItReauthorized(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{Client: (&sarProbe{}).client()}

	_, err := v.ValidateUpdate(sarAdmissionCtx(),
		poolWithTemplate(templateWithoutTypeMeta()), poolWithTemplate(validWorkloadTemplate()))
	if err == nil {
		t.Fatal("expected the update to be rejected")
	}

	for _, want := range []string{
		"stored workloadTemplate could not be read",
		"must be re-authorized",
		sarTestUser,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("rejection should contain %q: %v", want, err)
		}
	}
}
