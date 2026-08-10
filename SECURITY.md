# Security Policy

## Supported versions

PodPool is pre-release. The API is `v1alpha1` and no versions have been tagged,
so there is no supported-version matrix yet. Fixes land on the default branch
only. Expect breaking changes to the API without a conversion path until the
group is promoted past `v1alpha1`.

## Reporting a vulnerability

Report suspected vulnerabilities privately through GitHub's private
vulnerability reporting: open the repository's **Security** tab and choose
**Report a vulnerability**. Please do not open a public issue for a suspected
vulnerability.

> This requires private vulnerability reporting to be enabled on the
> repository (Settings → Code security). Until it is, there is no private
> disclosure channel.

Because this is a personal pre-release project, no response-time commitment is
offered.

## Known limitations

These are accepted, documented gaps rather than oversights. Each is deferred
deliberately; none should be assumed fixed.

### Metrics endpoint TLS verification is disabled

The `ServiceMonitor` in `config/prometheus/monitor.yaml` sets
`insecureSkipVerify: true`.

It is **not** applied by the default overlay: `- ../prometheus` is commented
out in `config/default/kustomization.yaml`, since applying a Prometheus Operator
CRD breaks `make deploy` on clusters without that operator. So this gap only
applies once you opt in. The reasoning below is unchanged for anyone who does.

The metrics endpoint serves HTTPS with authn/authz enabled, but no certificate
is supplied to the manager, so controller-runtime generates a self-signed,
ephemeral certificate at startup, regenerated on every restart, with no stable
CA to validate against. Verification is therefore disabled on the scrape side.

The consequence worth understanding: the `ServiceMonitor` authenticates scrapes
with a ServiceAccount bearer token
(`bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token`). With
verification disabled, Prometheus presents that token to an endpoint whose
identity it has not verified. An in-cluster attacker able to intercept traffic
to the metrics Service could capture a token bound to
`podpools-metrics-reader`. Exposure of the metrics themselves (replica counts,
group names, namespaces) is the lesser concern.

This matches the upstream kubebuilder default and is common across the
ecosystem, which does not make it correct.

**Resolution path.** Issue a metrics serving certificate from cert-manager,
make its CA available in the namespace where Prometheus runs, and set
`insecureSkipVerify: false`. The scaffolding is present but not applied:

- `config/certmanager/certificate-metrics.yaml` (commented out in that
  directory's `kustomization.yaml`)
- `config/default/cert_metrics_manager_patch.yaml` and the
  `[METRICS-WITH-CERTS]` blocks in `config/default/kustomization.yaml`
- `config/prometheus/monitor_tls_patch.yaml`

Note that a `ServiceMonitor`'s `tlsConfig` secret references resolve in the
namespace where **Prometheus** runs, not where the ServiceMonitor lives. The
current `selfSigned` Issuer also makes each certificate its own CA, so the
trust anchor rotates with the leaf; a long-lived root CA is a prerequisite for
distributing it sustainably.

### NetworkPolicy is not applied

`config/network-policy/` contains policies for the metrics and webhook
endpoints, but the directory is commented out of
`config/default/kustomization.yaml`. Both endpoints therefore accept
connections from any pod in the cluster. Authn/authz still applies to
`/metrics`; the policies would add defence in depth.

### Base images are not pinned by digest

`Dockerfile` references `golang:1.26` and `gcr.io/distroless/static:nonroot` by
tag. Both are mutable, so builds are not reproducible and a silently updated or
compromised upstream would be pulled without notice.

## Permission model

Two aspects are security-relevant by design:

**Cluster-wide workload access.** The controller holds create, delete, get,
list, patch, and watch on `apps/deployments` and `apps/statefulsets` across all
namespaces, because a PodPool in any namespace creates child workloads there.

**Aggregated ClusterRole.** `config/rbac/aggregate_manager_role.yaml` grants
the controller the union of every `ClusterRole` labelled
`podpools.dev/aggregate-to-manager: "true"`. This is intentional: it is how
users grant access to non-default workload types such as Argo Rollouts without
forking the manifests. But it means anyone able to create a labelled
`ClusterRole` can widen the controller's permissions. This is the same
mechanism Kubernetes uses for its own `admin`/`edit`/`view` roles, and it
should be treated with the same care. The effective permissions of the manager
are `manager-role ∪ every labelled ClusterRole`, so `config/rbac/role.yaml`
describes what the project ships, not what a given cluster grants.

**Privilege equivalence and enforcement.** PodPool write access is workload
write access. The controller renders an arbitrary pod spec from
`spec.workloadTemplate` (any image, any `serviceAccountName` in the namespace,
any `securityContext` that namespace's admission will admit) and applies it
under the manager's credentials. This is not a flaw; it is the same bargain as
granting Deployment create. The difference is that `podpool-editor-role` does not
look like Deployment create, so it is easy to hand out without realising.

The validating webhook enforces this at admission: it performs a
`SubjectAccessReview` checking whether the requesting user has `create`
permission on the target workload type (e.g. `deployments.apps`) in the
PodPool's namespace. A user with `create podpools` but not `create
statefulsets` is rejected when targeting StatefulSets. The controller then
reconciles under its own credentials.

The check runs at PodPool creation, and again on update whenever the stored
template's workload type cannot be read or has changed: the create-time
decision only carries forward while the type provably has not. An update that
leaves the workload type alone is not re-authorized, so the common case costs
no extra round-trip.

That "cannot be read" clause is load-bearing rather than defensive. The GVK
immutability check needs both the stored and the new template to parse, so a
stored template missing its type meta disabled it, and until then nothing else
on the update path asked the authorization question at all. A principal with
only `update podpools` could point such a pool at any workload kind the manager
reconciles.

Within the authorised workload types, the pod spec contents are never checked
against the user's permissions. The child workload is created by the manager,
and the pods by kube-controller-manager, so neither step consults the requester
beyond the initial admission check.

Blast radius: a PodPool creates children in its own namespace only, so a
namespaced editor grant confers workload-create in that namespace. The
manager's role is cluster-wide, so this holds in every namespace someone is
granted the role.

Late revocation: if a user's workload-create permission is revoked after the
PodPool exists, the pool continues to reconcile. This matches Kubernetes
behaviour for Deployments. Delete the PodPool to stop it.
