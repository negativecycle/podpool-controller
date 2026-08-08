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

package controller

// The manager's RBAC, pinned in two layers that fail in opposite directions:
//
//   - the manifest tests below catch the role getting WIDER than reviewed
//   - the envtest specs catch it getting NARROWER than the controller needs

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// ---------------------------------------------------------------------------
// constants for goconst
// ---------------------------------------------------------------------------

const (
	rbacGroupPodpools   = "podpools.dev"
	rbacResPodpools     = "podpools"
	rbacResDeployments  = "deployments"
	rbacResStatefulsets = "statefulsets"
	rbacResEvents       = "events"
	rbacResPods         = "pods"
	rbacGroupEvents     = "events.k8s.io"

	rbacSubStatus     = "status"
	rbacSubFinalizers = "finalizers"
	rbacSubScale      = "scale"

	verbGet    = "get"
	verbList   = "list"
	verbWatch  = "watch"
	verbCreate = "create"
	verbPatch  = "patch"
	verbUpdate = "update"
	verbDelete = "delete"
)

// ---------------------------------------------------------------------------
// the operation inventory
// ---------------------------------------------------------------------------

// rbacOp is one (group, resource, subresource, verb) the API server is asked
// about. The inventory below is derived from every client call the controller
// makes, built by grepping for them.
type rbacOp struct {
	group, resource, subresource, verb string
}

func (o rbacOp) String() string {
	res := o.resource
	if o.subresource != "" {
		res += "/" + o.subresource
	}

	g := o.group
	if g == "" {
		g = "core"
	}

	return fmt.Sprintf("%s %s.%s", o.verb, res, g)
}

// opsTheControllerPerforms must all be allowed. Trimming past this point is
// what turns a tidy-up into a production 403.
var opsTheControllerPerforms = []rbacOp{
	{rbacGroupPodpools, rbacResPodpools, "", verbGet},
	{rbacGroupPodpools, rbacResPodpools, "", verbList},
	{rbacGroupPodpools, rbacResPodpools, "", verbWatch},
	{rbacGroupPodpools, rbacResPodpools, rbacSubStatus, verbPatch},
	// OwnerReferencesPermissionEnforcement requires this for blockOwnerDeletion.
	{rbacGroupPodpools, rbacResPodpools, rbacSubFinalizers, verbUpdate},
	// Child reads, SSA apply (create when absent, patch when present),
	// orphan cleanup, and dynamic informers.
	{testAppsGroup, rbacResDeployments, "", verbGet},
	{testAppsGroup, rbacResDeployments, "", verbList},
	{testAppsGroup, rbacResDeployments, "", verbWatch},
	{testAppsGroup, rbacResDeployments, "", verbCreate},
	{testAppsGroup, rbacResDeployments, "", verbPatch},
	{testAppsGroup, rbacResDeployments, "", verbDelete},
	{testAppsGroup, rbacResStatefulsets, "", verbGet},
	{testAppsGroup, rbacResStatefulsets, "", verbList},
	{testAppsGroup, rbacResStatefulsets, "", verbWatch},
	{testAppsGroup, rbacResStatefulsets, "", verbCreate},
	{testAppsGroup, rbacResStatefulsets, "", verbPatch},
	{testAppsGroup, rbacResStatefulsets, "", verbDelete},
	// countUnschedulable, via APIReader.
	{"", rbacResPods, "", verbList},
	// The events sink, plus the legacy broadcaster.
	{"", rbacResEvents, "", verbCreate},
	{"", rbacResEvents, "", verbPatch},
	{rbacGroupEvents, rbacResEvents, "", verbCreate},
	{rbacGroupEvents, rbacResEvents, "", verbPatch},
	{rbacGroupEvents, rbacResEvents, "", verbUpdate},
}

// opsTheControllerNeverPerforms must all be denied. Every one of them was
// granted by the scaffold markers this commit trims.
var opsTheControllerNeverPerforms = []rbacOp{
	{rbacGroupPodpools, rbacResPodpools, "", verbCreate},
	{rbacGroupPodpools, rbacResPodpools, "", verbUpdate},
	{rbacGroupPodpools, rbacResPodpools, "", verbPatch},
	{rbacGroupPodpools, rbacResPodpools, "", verbDelete},
	{rbacGroupPodpools, rbacResPodpools, rbacSubScale, verbGet},
	{rbacGroupPodpools, rbacResPodpools, rbacSubScale, verbUpdate},
	{rbacGroupPodpools, rbacResPodpools, rbacSubScale, verbPatch},
	{rbacGroupPodpools, rbacResPodpools, rbacSubStatus, verbUpdate},
	{testAppsGroup, rbacResDeployments, "", verbUpdate},
	{testAppsGroup, rbacResStatefulsets, "", verbUpdate},
	{"", rbacResPods, "", verbGet},
}

// ---------------------------------------------------------------------------
// manifest assertions — no cluster needed
// ---------------------------------------------------------------------------

func loadClusterRole(t *testing.T, path string) *rbacv1.ClusterRole {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", path))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var cr rbacv1.ClusterRole
	if err := yaml.Unmarshal(raw, &cr); err != nil {
		t.Fatalf("unmarshalling %s: %v", path, err)
	}

	return &cr
}

// flattenRules turns a ClusterRole's grouped rules into "group/resource" ->
// sorted verbs, so an assertion does not depend on how controller-gen chose to
// collapse resources into rules.
func flattenRules(cr *rbacv1.ClusterRole) map[string][]string {
	out := map[string][]string{}

	for _, r := range cr.Rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				key := g + "/" + res
				out[key] = append(out[key], r.Verbs...)
			}
		}
	}

	for k := range out {
		slices.Sort(out[k])
	}

	return out
}

// TestManagerRoleGrantsOnlyWhatTheProjectUses is the golden assertion. It
// deliberately compares the COMPLETE rule set rather than checking for
// substrings: the point is to fail when a rule is added, which a substring
// check cannot see.
//
// Not redundant with a generated-output drift check. That proves role.yaml
// matches the markers; this proves the markers match a reviewed decision. A
// marker widened and regenerated passes the drift check and fails here.
//
// "Project", not "controller": manager-role carries the admission webhook's
// SubjectAccessReview grant too, because the role it used to inherit that
// from is one kustomization.yaml invites operators to delete. See
// opsTheWebhookPerforms in rbac_sar_test.go.
func TestManagerRoleGrantsOnlyWhatTheProjectUses(t *testing.T) {
	want := map[string][]string{
		rbacGroupPodpools + "/" + rbacResPodpools:                           {verbGet, verbList, verbWatch},
		rbacGroupPodpools + "/" + rbacResPodpools + "/" + rbacSubStatus:     {verbGet, verbPatch},
		rbacGroupPodpools + "/" + rbacResPodpools + "/" + rbacSubFinalizers: {verbUpdate},
		testAppsGroup + "/" + rbacResDeployments:                            {verbCreate, verbDelete, verbGet, verbList, verbPatch, verbWatch},
		testAppsGroup + "/" + rbacResStatefulsets:                           {verbCreate, verbDelete, verbGet, verbList, verbPatch, verbWatch},
		"/" + rbacResPods:                     {verbList},
		"/" + rbacResEvents:                   {verbCreate, verbPatch},
		rbacGroupEvents + "/" + rbacResEvents: {verbCreate, verbPatch, verbUpdate},

		// The admission webhook's, not the controller's. See
		// opsTheWebhookPerforms in rbac_sar_test.go.
		rbacGroupAuthz + "/" + rbacResSAR: {verbCreate},
	}

	got := flattenRules(loadClusterRole(t, "role.yaml"))

	for key, wantVerbs := range want {
		gotVerbs, present := got[key]
		if !present {
			t.Errorf("%s: rule missing entirely", key)

			continue
		}

		if strings.Join(gotVerbs, ",") != strings.Join(wantVerbs, ",") {
			t.Errorf("%s: verbs are %v, want %v", key, gotVerbs, wantVerbs)
		}
	}

	for key := range got {
		if _, expected := want[key]; !expected {
			t.Errorf("%s: rule present but nothing in this project uses it.\n"+
				"Either drop the marker that generated it, or add it to want with a "+
				"comment naming the call site. Note the user may be the admission "+
				"webhook rather than the controller.", key)
		}
	}
}

// ---------------------------------------------------------------------------
// sufficiency and excess, against a real authorizer
// ---------------------------------------------------------------------------

const rbacTestSA = "podpool-rbac-probe"

var _ = Describe("the manager's own RBAC", func() {
	var (
		impersonated client.Client
		ns           string
	)

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-rbac-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name

		raw, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
		Expect(err).NotTo(HaveOccurred())

		var shipped rbacv1.ClusterRole
		Expect(yaml.Unmarshal(raw, &shipped)).To(Succeed())

		role := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "rbac-probe-"},
			Rules:      shipped.Rules,
		}
		Expect(k8sClient.Create(ctx, role)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, role) })

		sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: rbacTestSA, Namespace: ns}}
		Expect(k8sClient.Create(ctx, sa)).To(Succeed())

		// Bind to the concrete ClusterRole, not aggregate-manager-role.
		// envtest runs no kube-controller-manager, so aggregation never fires.
		binding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "rbac-probe-"},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
			Subjects: []rbacv1.Subject{{
				Kind: rbacv1.ServiceAccountKind, Name: rbacTestSA, Namespace: ns,
			}},
		}
		Expect(k8sClient.Create(ctx, binding)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, binding) })

		impCfg := rest.CopyConfig(cfg)
		impCfg.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", ns, rbacTestSA),
			Groups: []string{
				"system:serviceaccounts",
				"system:serviceaccounts:" + ns,
				"system:authenticated",
			},
		}
		impersonated, err = client.New(impCfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
	})

	allowed := func(op rbacOp) bool {
		GinkgoHelper()

		review := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace:   ns,
					Group:       op.group,
					Resource:    op.resource,
					Subresource: op.subresource,
					Verb:        op.verb,
				},
			},
		}
		Expect(impersonated.Create(ctx, review)).To(Succeed(),
			"could not create a SelfSubjectAccessReview: check the impersonation groups")

		return review.Status.Allowed
	}

	It("permits every operation the controller performs", func() {
		var denied []string

		for _, op := range opsTheControllerPerforms {
			if !allowed(op) {
				denied = append(denied, op.String())
			}
		}

		Expect(denied).To(BeEmpty(), "the role does not cover what the controller does")
	})

	It("denies every operation the controller never performs", func() {
		var granted []string

		for _, op := range opsTheControllerNeverPerforms {
			if allowed(op) {
				granted = append(granted, op.String())
			}
		}

		Expect(granted).To(BeEmpty(), "granted but never used")
	})

	It("denies an operation nobody grants", func() {
		Expect(allowed(rbacOp{"", "secrets", "", verbGet})).To(BeFalse())
	})
})
