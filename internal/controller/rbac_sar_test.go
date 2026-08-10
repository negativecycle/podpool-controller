package controller

// The admission guard creates a SubjectAccessReview and fail-closes if it
// cannot. In the history this tutorial is based on, the manager's permission
// to create one came only from config/rbac/metrics_auth_role.yaml — a role
// config/rbac/kustomization.yaml documents as safe to comment out when metrics
// protection is unwanted. Follow that instruction and every PodPool create and
// update starts failing admission, cluster-wide, with failurePolicy=fail
// leaving no partial path. On this branch the grant was declared beside the
// guard from the start; these tests are what keep that from regressing.
//
// These assertions live beside the controller's RBAC goldens rather than in
// internal/webhook because loadClusterRole and the rbac* constants are here and
// the manifests they read are a single shared surface. The permission under
// test is the webhook's, not the controller's.

import (
	"slices"
	"testing"
)

const (
	rbacGroupAuthz = "authorization.k8s.io"
	rbacResSAR     = "subjectaccessreviews"

	rbacGroupAuthn = "authentication.k8s.io"
	rbacResTokens  = "tokenreviews"
)

// opsTheWebhookPerforms mirrors opsTheControllerPerforms for the admission
// webhook. The webhook makes exactly two outbound calls: RESTMapper.RESTMapping
// (discovery, not gated by a project role) and Client.Create of a
// SubjectAccessReview. Only the second needs a grant.
//
// Kept as an inventory rather than a single inline assertion so that a future
// permission is added here and immediately checked against every role that is
// always applied.
var opsTheWebhookPerforms = []rbacOp{
	{group: rbacGroupAuthz, resource: rbacResSAR, verb: verbCreate},
}

// alwaysAppliedRoles are the ClusterRoles a default `kustomize build config/default`
// installs and that kustomization.yaml does NOT invite the operator to remove.
// A permission the webhook needs must be granted by one of these.
//
// metrics_auth_role.yaml is deliberately absent: kustomization.yaml says
// "Comment the following permissions if you want to disable this protection"
// directly above it, so nothing load-bearing may depend on it alone.
var alwaysAppliedRoles = []string{"role.yaml"}

// TestWebhookPermissionsSurviveRemovingOptionalRoles is the independence
// requirement stated as an assertion. It passes from birth here, because the
// SAR marker sits beside checkWorkloadAuthorization; it exists so that moving
// or "deduplicating" that grant into an optional role fails loudly instead of
// during the next metrics-hardening cleanup.
func TestWebhookPermissionsSurviveRemovingOptionalRoles(t *testing.T) {
	granted := map[string][]string{}

	for _, file := range alwaysAppliedRoles {
		for key, verbs := range flattenRules(loadClusterRole(t, file)) {
			granted[key] = append(granted[key], verbs...)
		}
	}

	for _, op := range opsTheWebhookPerforms {
		key := op.group + "/" + op.resource

		if !slices.Contains(granted[key], op.verb) {
			t.Errorf("%s: not granted by any always-applied role (have %v for %s).\n"+
				"The webhook needs this permission on every install. Granting it only in "+
				"metrics_auth_role.yaml means an operator who follows kustomization.yaml's "+
				"instruction to comment that role out makes every PodPool write fail admission.",
				op, granted[key], key)
		}
	}
}

// TestMetricsAuthRoleStaysSelfSufficient guards the tempting simplification.
// With the grant declared in role.yaml, the copy in metrics_auth_role.yaml
// looks redundant — but relocating rather than duplicating would leave
// metrics_auth_role.yaml no longer expressing what metrics protection actually
// requires, because the metrics authz filter issues SubjectAccessReviews too.
//
// The right shape is to ADD the grant to the manager role, never to relocate
// it. RBAC is additive and both roles bind the same ServiceAccount, so the
// duplicate costs nothing.
func TestMetricsAuthRoleStaysSelfSufficient(t *testing.T) {
	rules := flattenRules(loadClusterRole(t, "metrics_auth_role.yaml"))

	for _, want := range []struct{ key, verb string }{
		{rbacGroupAuthn + "/" + rbacResTokens, verbCreate},
		{rbacGroupAuthz + "/" + rbacResSAR, verbCreate},
	} {
		if !slices.Contains(rules[want.key], want.verb) {
			t.Errorf("metrics_auth_role.yaml no longer grants %s on %s (have %v).\n"+
				"Metrics authn/authz needs both TokenReviews and SubjectAccessReviews. "+
				"#65 is fixed by adding the SAR grant to the manager role, not by moving "+
				"it out of here.", want.verb, want.key, rules[want.key])
		}
	}
}

// TestManagerRoleSARGrantIsDeclaredNotInherited pins the specific rule so a
// failure names the file to change. TestWebhookPermissionsSurviveRemovingOptionalRoles
// covers the same ground generically; this one exists because a generic failure
// message is a poor place to learn that the fix is a kubebuilder marker.
func TestManagerRoleSARGrantIsDeclaredNotInherited(t *testing.T) {
	rules := flattenRules(loadClusterRole(t, "role.yaml"))

	key := rbacGroupAuthz + "/" + rbacResSAR
	if !slices.Contains(rules[key], verbCreate) {
		t.Fatalf("role.yaml does not grant create on %s (have %v).\n"+
			"Add this marker beside checkWorkloadAuthorization in "+
			"internal/webhook/v1alpha1/podpool_webhook.go and run `make manifests`:\n"+
			"  // +kubebuilder:rbac:groups=%s,resources=%s,verbs=create",
			key, rules[key], rbacGroupAuthz, rbacResSAR)
	}
}
