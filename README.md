# PodPool

A Kubernetes controller that distributes pod replicas across groups with
different infrastructure, scaling constraints, and scheduling behaviour —
from a single resource.

## Why

A Deployment maps one pod template to one set of replicas. Splitting a
workload across on-demand and spot nodes, or across zones with different
capacity profiles, means maintaining separate Deployments with separate
scaling policies and no shared replica count. PodPool replaces that fan-out
with a single object whose groups share a total and fill in priority order:
earlier groups are satisfied first, opportunistic groups take whatever the
scheduler will accept, and the remainder overflows to the next group.

The controller creates one child workload (Deployment, StatefulSet, or any
CRD with a pod template at `.spec.template`) per group, applies
group-specific overrides with server-side apply, and reports aggregated
status back through the PodPool's conditions and scale subresource — so an
HPA can target the pool directly.

## Example

```yaml
apiVersion: podpools.dev/v1alpha1
kind: PodPool
metadata:
  name: web
spec:
  replicas: 10
  workloadTemplate:
    apiVersion: apps/v1
    kind: Deployment
    spec:
      template:
        spec:
          containers:
          - name: app
            image: nginx:1.27
  groups:
  - name: on-demand
    scaling:
      min: 3
    overrides:
      spec:
        template:
          spec:
            nodeSelector:
              capacity-type: on-demand
  - name: spot
    scaling:
      min: 0
      opportunistic: true
    overrides:
      spec:
        template:
          spec:
            nodeSelector:
              capacity-type: spot
            tolerations:
            - key: capacity-type
              value: spot
  - name: overflow
    scaling:
      min: 0
```

This creates three Deployments — `web-on-demand`, `web-spot`, and
`web-overflow` — sharing 10 replicas. The on-demand group always gets at
least 3. The spot group takes as many as the scheduler will place on spot
nodes. Whatever remains lands in the overflow group.

## Install

Apply the installer from the latest release:

```bash
kubectl apply -f https://github.com/negativecycle/podpool-controller/releases/latest/download/install.yaml
```

Or pin to a specific version:

```bash
kubectl apply -f https://github.com/negativecycle/podpool-controller/releases/download/v0.1.0/install.yaml
```

The installer is rendered against the image digest, not the tag, so what
you deploy is exactly what was signed.

### Verify the image

Every release image is signed with [cosign](https://github.com/sigstore/cosign)
using keyless OIDC. To verify:

```bash
cosign verify ghcr.io/negativecycle/podpool-controller:<version> \
  --certificate-identity-regexp '^https://github.com/negativecycle/podpool-controller/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Uninstall

```bash
kubectl delete -f https://github.com/negativecycle/podpool-controller/releases/latest/download/install.yaml
```

## Annotations

Set on the PodPool itself, not on children.

| Annotation | Effect |
|------------|--------|
| `podpools.dev/paused` | The controller stops reconciling the pool. Children are left exactly as they are, and `Ready` reports the pause as its reason. Remove the annotation to resume. |

The **value** is honoured, not just the annotation's presence:

| Value | Paused? |
|-------|---------|
| annotation absent | no |
| `true`, `True`, `1`, `t` | yes |
| `false`, `False`, `0`, `f` | **no** |
| `""` or anything else | yes |

## Status

**v1alpha1** — the API is under active development and may change between
minor releases.

## AI disclosure

This project was developed with the assistance of Claude, Anthropic's AI
assistant. Claude contributed to code generation, test writing, CI/CD
pipeline design, and documentation across the project's development.

## License

Apache License 2.0. See individual file headers.
