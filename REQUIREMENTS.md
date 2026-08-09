# PodPool CRD — Requirements Spec

## Overview

A PodPool is a Kubernetes Custom Resource that manages **multiple groups of pods** distributed across different node types (e.g., on-demand and spot). It provides a single resource to define a workload that spans infrastructure tiers with configurable scaling policies.

The controller creates and owns child workload resources (Deployments or StatefulSets) per group. It is a "controller of controllers" — it sets replica counts on child workloads via their `/scale` subresource and lets Kubernetes handle pod lifecycle, scheduling, and rollouts.

## API Group

`podpools.dev/v1alpha1`

## CRD Spec

### Top-Level Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `replicas` | int32 | Yes | Total desired replica count across all groups |
| `template` | PodTemplateSpec | Yes | Base pod template shared by all groups |
| `groups` | []GroupSpec | Yes (min 1) | Ordered list of pod groups |

### GroupSpec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique name within the PodPool (used in child resource naming) |
| `workload` | WorkloadSpec | Yes | What kind of child resource to create |
| `scaling` | ScalingConstraints | Yes | Absolute + ratio constraints for this group |
| `priorityClassName` | string | No | Kubernetes PriorityClass name for this group's pods |
| `nodeSelector` | map[string]string | No | Node selector for this group's pods |
| `tolerations` | []Toleration | No | Tolerations for this group's pods |
| `affinity` | Affinity | No | Affinity rules for this group's pods |
| `overrides` | PodTemplateOverride | No | Strategic merge patch applied on top of `spec.template` |

### WorkloadSpec

```yaml
workload:
  apiVersion: apps/v1
  kind: Deployment    # or StatefulSet
```

v1 supports `apps/v1 Deployment` and `apps/v1 StatefulSet` only. The webhook rejects other kinds. Support for arbitrary `/scale`-compatible CRDs is deferred to v2.

The controller creates a child resource of this kind, named `{podpool-name}-{group-name}`, with the merged pod template and the replica count assigned by the scaling constraints.

The controller manages the child resource's replica count via the `/scale` subresource.

### ScalingConstraints

All groups participate in the distribution of `spec.replicas`. They are ordered by list position — earlier groups have higher priority and are filled first. Each group uses **one absolute constraint** paired with **one optional ratio constraint**.

Three constraint combinations are available:

**min only:**

```yaml
scaling:
  min: 3              # cascade threshold — satisfy before filling later groups
```

The group receives at least `min` pods before subsequent groups get any. No cap — if other groups' ratio constraints leave surplus pods, this group absorbs them (see Overflow bucket below). This is the natural configuration for the first group.

**min + maxRatio:**

```yaml
scaling:
  min: 3              # cascade threshold
  maxRatio: 70        # best-effort ceiling — group should not exceed this % of total
```

Use when you want to **satisfy a cascade threshold** and **cap a group's share**. Typical for a burst/spot group.

At low scale, min dominates and maxRatio is violated (e.g., at total=3 with min=3, the group is 100% of the pool). As total grows, the ratio constraint takes effect.

**max + minRatio:**

```yaml
scaling:
  max: 5              # absolute ceiling — never more than this many pods
  minRatio: 30        # best-effort floor — group should be at least this % of total
```

Use when you want to **cap an expensive group** and **guarantee a minimum share**. Typical for a GPU or premium tier.

At high scale, max dominates and minRatio is violated (e.g., at total=20 with max=5, the group is 25% even if minRatio=30%). At low scale, the ratio constraint takes effect.

**Constraint precedence rule:**

> **Absolute constraints (min/max) are hard limits. Ratio constraints (minRatio/maxRatio) are best-effort. When they conflict, the absolute constraint always wins.**

Both combinations with ratios follow the same pattern: absolute constraints dominate at one end of the scale range, ratio constraints take effect at the other:

| Combination | Dominates at low scale | Dominates at high scale |
|-------------|----------------------|------------------------|
| min + maxRatio | min (ratio violated) | maxRatio (ratio enforced) |
| max + minRatio | minRatio (ratio enforced) | max (ratio violated) |

The controller sets a `RatioDegraded` condition on the PodPool status when an absolute constraint forces a ratio violation.

**Semantics of `min`:**

`min` is a **cascade threshold**, not a hard floor. It means "satisfy this group before pods flow to the next group." If `spec.replicas` (or the HPA-set value) is less than the sum of all mins, the controller respects the total — earlier groups are partially filled in list order, later groups get zero. This is a valid state, not an error. The HPA's replica count is always authoritative for the actual number of running pods.

### Scavenger pattern

A common three-group configuration uses priority and preemption to optimize both cost and node utilization. The scaling order uses free capacity before buying more:

1. **base** (on-demand, high priority) — guaranteed minimum, already paid for
2. **scavenger** (on-demand spare capacity, low priority) — fills gaps on base nodes, **free**
3. **burst** (spot, medium priority) — new instances when needed, cheap but not free

```yaml
spec:
  replicas: 10
  groups:
    - name: base
      scaling: { min: 3 }
      priorityClassName: production-high     # value: 1000
      nodeSelector: { capacity-type: on-demand }

    - name: scavenger
      scaling: { min: 0, maxRatio: 30 }
      priorityClassName: scavenger           # value: -100
      nodeSelector: { capacity-type: on-demand }
      overrides:
        spec:
          terminationGracePeriodSeconds: 5
          containers:
            - name: api
              resources:
                requests: { cpu: 50m }

    - name: burst
      scaling: { min: 0, maxRatio: 50 }
      priorityClassName: production-burst    # value: 500
      nodeSelector: { capacity-type: spot }
```

When another workload preempts scavenger pods from the on-demand nodes, those scavenger pods become Pending. The pool shows fewer `readyReplicas` but the replica count is unchanged.

#### Preemption flow

1. Other team's high-priority pod can't schedule — node is full
2. Scheduler preempts scavenger pods (priority -100 vs 1000)
3. Scavenger pods evicted, become Pending
4. Scheduler retries — if no on-demand room, they stay Pending or land on spot nodes
5. Scavenger `priorityClassName` value below `expendable-pods-priority-cutoff` (-10) prevents cluster autoscaler from provisioning new nodes just for scavenger pods

### PodTemplateOverride

Strategic merge patch applied to `spec.template` to produce the group's final pod template.

```yaml
overrides:
  spec:
    containers:
      - name: app
        resources:
          requests:
            cpu: 250m  # override base template's cpu request
```

### Labels

The controller applies the following labels to all child workloads and their pods:

| Label | Scope | Description |
|-------|-------|-------------|
| `podpools.dev/pool` | All pods in pool | Set to the PodPool name. Used by HPA for aggregated metrics. |
| `podpools.dev/group` | Group-specific | Set to the group name. Enables per-group Services and PDBs. |
| `podpools.dev/managed-by` | All pods in pool | Set to `podpool-controller`. |

The child Deployment/StatefulSet's `spec.selector.matchLabels` includes both `podpools.dev/pool` and `podpools.dev/group` so each child only manages its own pods.

User labels from `spec.template.metadata.labels` are merged into the pod template. If a user label conflicts with a `podpools.dev/*` label, the controller's label wins.

## /scale Subresource

The PodPool resource itself exposes the `/scale` subresource so that an HPA (or KEDA, or any external scaler) can target it:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: my-service-hpa
spec:
  scaleTargetRef:
    apiVersion: podpools.dev/v1alpha1
    kind: PodPool
    name: my-service
  minReplicas: 3
  maxReplicas: 60
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

When the HPA writes to `/scale`, it updates `spec.replicas` (following the standard Deployment convention). The controller redistributes across groups using each group's scaling constraints. The HPA's replica count is always authoritative.

## Reconciliation Logic

The controller uses **declarative reconciliation**. Each reconcile pass computes the complete desired state in one shot and diffs it against current state. It never adds pods incrementally.

### What triggers reconciliation

- PodPool spec changed (user edits replicas, groups, template)
- HPA writes to `/scale` subresource
- Child workload status changed (pods became ready, failed, etc.)
- Periodic resync (safety net)

### Reconcile steps

1. **Read desired state** from `spec.replicas`, groups, and scaling constraints.
2. **Compute per-group targets** — pure math, single pass, no Kubernetes API calls:
   - Distribute `spec.replicas` across groups using constraint formulas (see below).
   - All groups are computed together. There is no sequencing or one-at-a-time logic.
3. **Read current state** of each child workload (Deployment/StatefulSet).
4. **Apply changes** — for each group, in the same reconcile pass:
   - If child workload doesn't exist: create it with merged template and computed replicas.
   - If replicas differ: update via `/scale` subresource.
   - If template changed: update pod template (triggers rolling update). All groups roll out simultaneously in v1.
5. **Update PodPool status** — aggregate child workload states, set conditions.
6. **Set owner references** so child workloads are garbage-collected with the PodPool.

### Direct computation formulas

The step-by-step mental model (add one pod at a time) is useful for understanding the algorithm, but the controller computes all targets in one pass. These formulas produce the same result as the step-by-step trace for any total:

**Two groups: base (min) and burst (min + maxRatio):**

```
burstTarget = min(floor(total * burst.maxRatio / 100), total - base.min)
burstTarget = max(burstTarget, burst.min)
baseTarget  = total - burstTarget
```

**Two groups: base (max + minRatio) and burst (min + maxRatio):**

```
baseTarget  = min(base.max, max(ceil(total * base.minRatio / 100), ...))
burstTarget = total - baseTarget
burstTarget = max(burstTarget, burst.min)
```

**General case (N groups):**

1. Satisfy all cascade thresholds (min) in list order, constrained by total.
2. Distribute remaining pods, respecting maxRatio ceilings and max caps.
3. Clamp each group to its absolute constraints.
4. Distribute any remainder from clamping to the overflow bucket group (see below).

### Overflow bucket

The first group (highest priority) is implicitly the **overflow bucket**. It absorbs any pods that aren't claimed by other groups' ratio constraints.

A group with only `min` and no `maxRatio` does **not** greedily take pods. After its min is satisfied, the cascade moves on to subsequent groups. The group only grows beyond min when other groups can't absorb more (their maxRatios or maxes cap them), and the surplus flows back to the first group.

For example, with three groups where scav has `maxRatio: 30%` and burst has `maxRatio: 50%` (total capped ratio = 80%), 20% of pods are unaccounted for by explicit ratios. That 20% goes to the overflow bucket (base).

If you don't want the first group to absorb overflow, add a `maxRatio` to it. When all groups have maxRatios that sum to 100%, the distribution is fully specified with no implicit overflow.

### Controller responsibilities by layer

| Layer | Manages | Concern |
|-------|---------|---------|
| **PodPool controller** | Child workload replica counts | Distribution across groups |
| **Deployment/StatefulSet controller** | ReplicaSets, rolling updates | Pod lifecycle within a group |
| **ReplicaSet controller** | Pod creation/deletion | Matching desired pod count |
| **Scheduler** | Pod-to-Node assignment | nodeSelector, tolerations, affinity |

The PodPool controller never creates or deletes pods directly. It only sets replica counts on child workloads and lets Kubernetes handle everything below.

## Status

```yaml
status:
  replicas: 10
  readyReplicas: 8
  updatedReplicas: 10
  conditions:
    - type: Available
      status: "True"
      reason: MinimumReplicasAvailable
    - type: Progressing
      status: "True"
      reason: NewReplicaSetAvailable
    - type: RatioDegraded
      status: "False"
  groups:
    - name: base
      replicas: 4
      readyReplicas: 4
      activeRatio: 33
      workloadRef:
        apiVersion: apps/v1
        kind: Deployment
        name: my-service-base
    - name: scavenger
      replicas: 3
      readyReplicas: 2
      activeRatio: 25
      workloadRef:
        apiVersion: apps/v1
        kind: Deployment
        name: my-service-scavenger
    - name: burst
      replicas: 5
      readyReplicas: 4
      activeRatio: 42
      workloadRef:
        apiVersion: apps/v1
        kind: Deployment
        name: my-service-burst
```

### Conditions

| Condition | Meaning |
|-----------|---------|
| `Available` | All group cascade thresholds (mins) are satisfied and pods are ready |
| `Progressing` | A rollout or scale operation is in progress |
| `RatioDegraded` | An absolute constraint (min/max) is forcing a ratio violation |

## Validation (Webhook)

- At least one group required
- Group names must be unique within the PodPool
- Each group must use exactly one constraint combination: (min), (min + maxRatio), or (max + minRatio)
- Every group must have at least one pull constraint (min or minRatio)
- min >= 0, max >= 1, 0 < maxRatio <= 100, 0 < minRatio <= 100
- `workload` must be `apps/v1 Deployment` or `apps/v1 StatefulSet`
- Group names must be valid DNS subdomain components (used in child resource naming)
- If `priorityClassName` is set, the referenced PriorityClass must exist in the cluster

## Out of Scope (v1)

- Spot interruption handling / graceful drain
- Adopting existing workloads into groups
- Cross-cluster distribution
- Canary / blue-green rollout strategies (sequential rollout across groups)
- Per-group HPA (each group gets its own autoscaler)
- Cost-aware scheduling decisions
- Cluster-pressure-aware scheduling (see below)
- Arbitrary `/scale`-compatible CRDs as workload kinds
- `min` + `max` constraint combination (requires secondary overflow redistribution)

### v2: Cluster-Pressure-Aware Scheduling

When cluster resource utilization crosses a configurable threshold, the controller switches from soft to hard scheduling constraints on child workloads. This forces the Kubernetes scheduler to spread pods across nodes rather than packing them, which triggers the cluster autoscaler to provision new nodes for better balance.

**Mechanism:**
- Controller watches node-level resource utilization (via metrics-server API)
- Below threshold: child workloads use `preferredDuringSchedulingIgnoredDuringExecution` (soft rules — pods pack efficiently)
- Above threshold: controller mutates child workloads to use `requiredDuringSchedulingIgnoredDuringExecution` (hard rules — pods become unschedulable on crowded nodes)
- Cluster autoscaler sees pending pods and provisions new nodes
- Once utilization drops below threshold, controller relaxes back to soft rules

**Possible spec shape:**
```yaml
spec:
  clusterPressure:
    enabled: true
    threshold: 80              # % cluster resource usage
    metric: cpu                # cpu | memory | both
    enforcedConstraints:
      - topologySpreadConstraints
      - podAntiAffinity
```

**Why v2:** The v1 controller operates above the scheduler (sets replica counts, never touches nodes). This feature requires node metrics awareness, dynamic pod spec mutation, and careful interaction with the cluster autoscaler — a different class of complexity.

## Example: on-demand base + scavenger + spot burst

Three groups demonstrating the cost-optimal scaling order: guaranteed on-demand base, free spare capacity via scavenger, then spot for additional scale.

```yaml
apiVersion: podpools.dev/v1alpha1
kind: PodPool
metadata:
  name: api-server
spec:
  replicas: 10
  template:
    metadata:
      labels:
        app: api-server
    spec:
      containers:
        - name: api
          image: api-server:v2.1.0
          ports:
            - containerPort: 8080
          resources:
            requests:
              cpu: 500m
              memory: 256Mi
            limits:
              cpu: "1"
              memory: 512Mi
  groups:
    # Group 1: on-demand base (overflow bucket — absorbs unclaimed pods)
    - name: base
      workload:
        apiVersion: apps/v1
        kind: Deployment
      scaling:
        min: 3
      priorityClassName: production-high     # value: 1000
      nodeSelector:
        capacity-type: on-demand

    # Group 2: scavenger — fills spare capacity on on-demand nodes (free!)
    - name: scavenger
      workload:
        apiVersion: apps/v1
        kind: Deployment
      scaling:
        min: 0
        maxRatio: 30
      priorityClassName: scavenger           # value: -100 (below expendable cutoff)
      nodeSelector:
        capacity-type: on-demand
      overrides:
        spec:
          terminationGracePeriodSeconds: 5
          containers:
            - name: api
              resources:
                requests:
                  cpu: 50m                   # tiny requests to fit in gaps

    # Group 3: spot burst — cheap scaling when more capacity needed
    - name: burst
      workload:
        apiVersion: apps/v1
        kind: Deployment
      scaling:
        min: 0
        maxRatio: 50
      priorityClassName: production-burst    # value: 500
      nodeSelector:
        capacity-type: spot
      tolerations:
        - key: spot
          operator: Equal
          value: "true"
          effect: NoSchedule
      overrides:
        spec:
          containers:
            - name: api
              resources:
                requests:
                  cpu: 250m
```

### Scaling trace

Scav maxRatio=30%, burst maxRatio=50%. Combined capped ratio = 80%, so 20% is unclaimed and flows to base (overflow bucket).

Formula per group:
```
scavTarget  = min(floor(total * 0.30), ...)
burstTarget = min(floor(total * 0.50), ...)
baseTarget  = total - scavTarget - burstTarget   (absorbs overflow)
All targets clamped to [min, ...] per group.
```

| Total | Base | Scav | Burst | Base% | Scav% | Burst% | Notes |
|-------|------|------|-------|-------|-------|--------|-------|
| 1 | 1 | 0 | 0 | 100% | 0% | 0% | Filling base min |
| 3 | 3 | 0 | 0 | 100% | 0% | 0% | Base min satisfied |
| 4 | 3 | 1 | 0 | 75% | 25% | 0% | Scavenger fills on-demand gaps first |
| 5 | 3 | 1 | 1 | 60% | 20% | 20% | Burst starts on spot |
| 7 | 3 | 2 | 2 | 43% | 29% | 29% | Both scaling, base at min |
| 10 | 3 | 3 | 4 | 30% | 30% | 40% | Scav at cap, burst below cap |
| 12 | 4 | 3 | 5 | 33% | 25% | 42% | Scav capped, burst growing, base absorbs 1 |
| 15 | 4 | 4 | 7 | 27% | 27% | 47% | All three active |
| 20 | 4 | 6 | 10 | 20% | 30% | 50% | All groups at or near caps |
| 25 | 5 | 7 | 12 | 20% | 28% | 48% | Base absorbs overflow (20%) |
| 30 | 6 | 9 | 15 | 20% | 30% | 50% | Steady state: 20/30/50 split |

At low scale (total 1-3), `RatioDegraded` condition is set because base.min forces ratios above caps. As total grows, the ratios converge toward the 20/30/50 steady state with base absorbing the 20% not claimed by scav+burst.

### Scaling order rationale

1. **base** (on-demand, high priority) — guaranteed minimum, already paying for these nodes
2. **scavenger** (on-demand spare capacity, low priority) — fills gaps on base nodes at zero additional cost
3. **burst** (spot, medium priority) — new instances, cheap but not free

This order is cost-optimal: use free capacity before buying more. When other workloads need the on-demand nodes, scavenger pods get preempted (priority -100 vs 1000) and the pool's `readyReplicas` drops while the replica count stays the same.

### Direct computation example

When HPA scales from 5 to 20 in one shot:

```
scavTarget  = min(floor(20 * 0.30), ...) = 6
burstTarget = min(floor(20 * 0.50), ...) = 10
baseTarget  = 20 - 6 - 10 = 4

→ Set Deployment/api-server-base replicas = 4       (was 3)
→ Set Deployment/api-server-scavenger replicas = 6   (was 1)
→ Set Deployment/api-server-burst replicas = 10      (was 1)
```

No intermediate states. One reconcile pass. The Deployment controllers handle pod creation below.
