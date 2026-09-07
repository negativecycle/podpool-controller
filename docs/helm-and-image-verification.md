# Helm install and image verification

The controller can be installed with Helm, and its image can be verified — both
by you before you trust it, and by the cluster at admission time.

## Install with Helm

The chart lives in-repo at [`dist/chart`](../dist/chart) and deploys the signed
release image (`ghcr.io/negativecycle/podpool-controller`) pinned to the chart's
`appVersion`.

```bash
helm install podpool-controller ./dist/chart --namespace podpool-system
```

Note: **do not pass `--create-namespace`.** By default the chart renders the
namespace itself so it can label it for Pod Security (below); Helm cannot label
a namespace it did not create. If you'd rather manage the namespace yourself,
set `podSecurityStandards.enabled=false` and then `--create-namespace` is fine.

Useful values (see [`dist/chart/values.yaml`](../dist/chart/values.yaml) for the
full set):

| Value | Default | Purpose |
|-------|---------|---------|
| `controllerManager.container.image.tag` | `""` → `appVersion` | Image tag to deploy |
| `controllerManager.container.image.digest` | `""` | Pin by digest (`sha256:…`); overrides the tag |
| `crd.enable` / `crd.keep` | `true` / `true` | Install the CRD, and keep it on uninstall |
| `metrics.enable` | `true` | Metrics service + RBAC |
| `podSecurityStandards.enabled` | `true` | Create + label the namespace to enforce a Pod Security Standard (below) |
| `policy.kyverno.enabled` | `false` | Install the admission verification policy (below) |
| `extraObjects` | `[]` | Inject arbitrary manifests (PDB, HPA, extra RBAC…) without forking |

Values are validated against [`values.schema.json`](../dist/chart/values.schema.json)
on install/upgrade, so a typo'd enum (e.g. `standard: restrictd`) or a bad type
fails up front with a clear message instead of rendering broken YAML.

## Pod Security Standard

The controller meets the **restricted** [Pod Security Standard](https://kubernetes.io/docs/concepts/security/pod-security-standards/)
— it runs non-root, read-only-root, no added capabilities, seccomp
`RuntimeDefault`. By default (`podSecurityStandards.enabled=true`) the chart
labels its namespace to **enforce** and **warn** at that level, so a workload
that regresses below restricted is rejected there:

```yaml
pod-security.kubernetes.io/enforce: restricted
pod-security.kubernetes.io/warn: restricted
```

Set `podSecurityStandards.standard=baseline` for the looser profile, or
`podSecurityStandards.enabled=false` to manage the namespace and its labels
yourself. The kustomize install (`install.yaml`) carries the same labels.

## High availability and scheduling

The controller is leader-elected, so extra replicas are warm standbys, not extra
throughput. For failover, raise the replica count:

```bash
helm upgrade podpool-controller ./dist/chart --reuse-values \
  --set controllerManager.replicas=2
```

By default the chart spreads replicas across failure domains:

- **one per node** — `kubernetes.io/hostname` with `DoNotSchedule`, so two
  replicas never share a machine. On a cluster with fewer nodes than replicas
  the extra replicas stay `Pending` (that is the point of a hard constraint);
  lower it or add nodes.
- **one per zone, best-effort** — `topology.kubernetes.io/zone` with
  `ScheduleAnyway`, so a single-zone cluster still schedules.

These are no-ops at `replicas: 1`. Override or drop them via
`controllerManager.topologySpreadConstraints` (set `[]` to remove). The chart
also exposes `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, and
`imagePullSecrets` on `controllerManager`.

## Verify the image yourself

Every released image is signed keyless with cosign and carries a signed SLSA
provenance attestation, both bound to this repository's release workflow. Verify
the signature:

```bash
cosign verify ghcr.io/negativecycle/podpool-controller:<version> \
  --certificate-identity-regexp '^https://github.com/negativecycle/podpool-controller/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Verify the provenance and SBOM attestations, signed by the release workflow:

```bash
gh attestation verify oci://ghcr.io/negativecycle/podpool-controller:<version> \
  --repo negativecycle/podpool-controller
```

## Enforce verification at admission (Kyverno)

For clusters that run [Kyverno](https://kyverno.io), the repository ships a
`ClusterPolicy` that refuses to admit a controller image which cannot prove the
same lineage — checked by the cluster, not by the workflow that built it.

It is **opt-in** and ships in **Audit** mode (reports violations, blocks
nothing). Two ways to install it:

**With the chart:**

```bash
helm upgrade podpool-controller ./dist/chart \
  --reuse-values \
  --set policy.kyverno.enabled=true \
  --set policy.kyverno.validationFailureAction=Audit
```

**Standalone (kustomize), independent of how the controller was installed:**

```bash
kubectl apply -k https://github.com/negativecycle/podpool-controller/config/kyverno
```

The policy requires the image to carry:

1. a cosign signature issued to `…/image.yml@refs/tags/v*` via GitHub OIDC, and
2. a SLSA provenance attestation whose build type and source repository match
   this project.

Once the Kyverno policy report shows no unexpected violations, graduate to
enforcement — set `validationFailureAction: Enforce` (chart value) or edit the
`ClusterPolicy`. In Enforce mode an image that fails verification is rejected.

> The verification the policy performs only succeeds against images produced by
> the signing release pipeline. Build images from that pipeline (tagged
> releases) before enabling Enforce.
