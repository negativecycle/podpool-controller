package v1alpha1

import (
	"errors"
	"strings"
	"testing"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// #64: pluralResource consulted a hardcoded Kind-to-plural map before asking
// discovery, and the map was keyed on Kind alone. Kind is only unique within a
// group, so a CRD declaring "Deployment" in its own group resolved to
// "deployments" and the SubjectAccessReview went out asking about a resource
// that need not exist.
//
// A plural is whatever the CRD's spec.names.plural says. It is cluster state,
// not a naming rule, which is why meta.RESTMapper exists.

const (
	crdGroup      = "example.com"
	crdVersion    = "v1"
	crdPlural     = "deploys" // deliberately NOT the kubebuilder default
	appsPluralDep = "deployments"
)

// crdMapper knows example.com/v1 Kind=Deployment as plural "deploys", the
// collision the old Kind-keyed map could not see.
func crdMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: crdGroup, Version: crdVersion}})
	m.AddSpecific(
		schema.GroupVersionKind{Group: crdGroup, Version: crdVersion, Kind: kindDeployment},
		schema.GroupVersionResource{Group: crdGroup, Version: crdVersion, Resource: crdPlural},
		schema.GroupVersionResource{Group: crdGroup, Version: crdVersion, Resource: crdPlural},
		meta.RESTScopeNamespace,
	)

	return m
}

func appsMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Group: groupApps, Version: "v1"}})
	m.AddSpecific(
		schema.GroupVersionKind{Group: groupApps, Version: "v1", Kind: kindDeployment},
		schema.GroupVersionResource{Group: groupApps, Version: "v1", Resource: appsPluralDep},
		schema.GroupVersionResource{Group: groupApps, Version: "v1", Resource: appsPluralDep},
		meta.RESTScopeNamespace,
	)

	return m
}

// failingMapper stands in for a discovery outage.
type failingMapper struct {
	meta.RESTMapper
}

func (failingMapper) RESTMapping(_ schema.GroupKind, _ ...string) (*meta.RESTMapping, error) {
	return nil, errors.New("discovery unavailable")
}

func TestPluralResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mapper  meta.RESTMapper
		gvk     schema.GroupVersionKind
		want    string
		wantErr bool
	}{
		{
			name:   "builtin resolves through discovery when a mapper is present",
			mapper: appsMapper(),
			gvk:    schema.GroupVersionKind{Group: groupApps, Version: "v1", Kind: kindDeployment},
			want:   appsPluralDep,
		},
		{
			name: "builtin falls back to the table with no mapper",
			// The five older SAR tests run in exactly this shape.
			mapper: nil,
			gvk:    schema.GroupVersionKind{Group: groupApps, Version: "v1", Kind: kindDeployment},
			want:   appsPluralDep,
		},
		{
			name: "builtin survives a discovery outage",
			// The only job the table has left: apps/v1 plurals cannot change,
			// so an outage must not take the built-in workload kinds down.
			mapper: failingMapper{},
			gvk:    schema.GroupVersionKind{Group: groupApps, Version: "v1", Kind: kindDeployment},
			want:   appsPluralDep,
		},
		{
			name: "a CRD kind colliding with a builtin resolves to its own plural",
			// The bug: this returned "deployments" before the fix.
			mapper: crdMapper(),
			gvk:    schema.GroupVersionKind{Group: crdGroup, Version: crdVersion, Kind: kindDeployment},
			want:   crdPlural,
		},
		{
			name:    "a colliding CRD kind fails closed with no mapper",
			mapper:  nil,
			gvk:     schema.GroupVersionKind{Group: crdGroup, Version: crdVersion, Kind: kindDeployment},
			wantErr: true,
		},
		{
			name: "a colliding CRD kind fails closed on a discovery outage",
			// Must NOT fall back to "deployments": that is the bug on the
			// error path.
			mapper:  failingMapper{},
			gvk:     schema.GroupVersionKind{Group: crdGroup, Version: crdVersion, Kind: kindDeployment},
			wantErr: true,
		},
		{
			name:    "an unknown kind fails closed",
			mapper:  failingMapper{},
			gvk:     schema.GroupVersionKind{Group: crdGroup, Version: crdVersion, Kind: "Widget"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			v := &PodPoolCustomValidator{RESTMapper: tt.mapper}

			got, err := v.pluralResource(tt.gvk)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected a resolution failure, got %q", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got != tt.want {
				t.Errorf("pluralResource(%s) = %q, want %q", tt.gvk, got, tt.want)
			}
		})
	}
}

// TestSARNamesTheResourceBeingCreated reads what the guard actually asked the
// authorizer. Resolution can be wrong while the admission outcome looks
// perfectly reasonable, so the outcome alone cannot catch this.
func TestSARNamesTheResourceBeingCreated(t *testing.T) {
	t.Parallel()

	probe := &sarProbe{allowed: true}
	v := &PodPoolCustomValidator{Client: probe.client(), RESTMapper: crdMapper()}

	pool := poolWithTemplate(templateForKind(crdGroup+"/"+crdVersion, kindDeployment))

	if _, err := v.ValidateCreate(sarAdmissionCtx(), pool); err != nil {
		t.Fatalf("expected the create to be admitted: %v", err)
	}

	if probe.last == nil {
		t.Fatal("no SubjectAccessReview was issued")
	}

	attrs := probe.last.Spec.ResourceAttributes
	if attrs.Resource != crdPlural {
		t.Errorf("SAR asked about resource %q, want %q: the authorization decision "+
			"is about a resource nobody is creating", attrs.Resource, crdPlural)
	}

	if attrs.Group != crdGroup {
		t.Errorf("SAR asked about group %q, want %q", attrs.Group, crdGroup)
	}
}

// TestSARDoesNotAcceptAnAppsGrantForACRDWorkload is the false-allow direction,
// end to end and at the level an operator would experience it.
//
// The over-broad Role is the realistic trigger: `apiGroups: ["*"], resources:
// ["deployments"]` is a plausible rule someone writes meaning apps/v1. Before
// the fix it authorized creating any CRD workload whose Kind happened to be
// "Deployment", in any group, whatever that CRD's real plural was.
func TestSARDoesNotAcceptAnAppsGrantForACRDWorkload(t *testing.T) {
	t.Parallel()

	probe := &sarProbe{
		allow: func(sar *authzv1.SubjectAccessReview) bool {
			// The user holds create on "deployments" and nothing else.
			return sar.Spec.ResourceAttributes.Resource == appsPluralDep
		},
	}
	v := &PodPoolCustomValidator{Client: probe.client(), RESTMapper: crdMapper()}

	pool := poolWithTemplate(templateForKind(crdGroup+"/"+crdVersion, kindDeployment))

	_, err := v.ValidateCreate(sarAdmissionCtx(), pool)
	if err == nil {
		t.Fatal("a grant on deployments must not authorize creating deploys.example.com")
	}

	if !strings.Contains(err.Error(), wantNotAuthorized) {
		t.Errorf("rejection = %v, want it to name the authorization failure", err)
	}
}

// A SubjectAccessReview that cannot be issued is a cluster problem, not
// something the user put in spec.workloadTemplate. Both still deny; only the
// diagnosis differs, and nothing else asserts which one we chose.
//
// The distinction is only visible before ToAggregate flattens the ErrorList,
// so this calls the guard directly.
func TestBrokenAuthorizationCheckIsInternalNotForbidden(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{
		Client: (&sarProbe{err: errors.New("API server unreachable")}).client(),
	}

	gvk := schema.GroupVersionKind{Group: groupApps, Version: "v1", Kind: kindDeployment}

	fieldErr := v.checkWorkloadAuthorization(sarAdmissionCtx(), sarPool(), gvk, "")
	if fieldErr == nil {
		t.Fatal("a failed authorization check must still deny")
	}

	if fieldErr.Type != field.ErrorTypeInternal {
		t.Errorf("error type is %v, want %v: an unreachable authorizer is not the "+
			"user's field being invalid", fieldErr.Type, field.ErrorTypeInternal)
	}
}

// The denial itself stays Forbidden. That one really is about the requester.
func TestAuthorizationDenialStaysForbidden(t *testing.T) {
	t.Parallel()

	v := &PodPoolCustomValidator{Client: (&sarProbe{allowed: false}).client()}

	gvk := schema.GroupVersionKind{Group: groupApps, Version: "v1", Kind: kindDeployment}

	fieldErr := v.checkWorkloadAuthorization(sarAdmissionCtx(), sarPool(), gvk, "")
	if fieldErr == nil {
		t.Fatal("a denied review must reject")
	}

	if fieldErr.Type != field.ErrorTypeForbidden {
		t.Errorf("error type is %v, want %v", fieldErr.Type, field.ErrorTypeForbidden)
	}
}
