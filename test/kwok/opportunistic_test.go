//go:build kwok

package kwok

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// TestOpportunisticConvergence is the system-level proof for opportunistic
// sizing, against the real kube-scheduler. It exists because the first version
// of the probe passed every unit and envtest layer while being unusable: it
// probed on every reconcile and funded each probe by killing a burst pod. Only
// a real scheduler judging real pods exposes that class of bug.
//
// Arithmetic, pinned to the fixture (2 on-demand + 2 spot nodes, 4 CPU each):
//
//	blockers   2 × 2000m on-demand   → 2000m free per on-demand node
//	base       2 × 1000m on-demand   → 2000m free remains in total
//	scavenger  1000m pods            → true capacity = 2, however base spreads
//	replicas 10                      → expect base 2 / scav 2 / burst 6
//
// base carries a target so it is capped: an uncapped base would be the first
// unbounded group and phase 4 would spill scavenger's displaced replicas into
// it — a full tier — instead of into burst. The first run of this test made
// exactly that mistake and base swallowed the pool.
//
// After the blockers are removed, 6000m frees up: the walk-up should reclaim
// to scav 6 / burst 2, ending on a refusal at the 7th replica.
func TestOpportunisticConvergence(t *testing.T) {
	const (
		poolName = "kwok-opportunistic"
		ns       = "default"
		hb       = 2 * time.Second
	)

	ctx := context.Background()

	cleanupPodPool(t, poolName)
	defer cleanupPodPool(t, poolName)

	blocker := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "od-blocker", Namespace: ns}}

	_ = k8sClient.Delete(ctx, blocker)
	defer func() { _ = k8sClient.Delete(ctx, blocker) }()

	// The capacity arithmetic below is exact, so pods still terminating from
	// earlier tests in this suite would silently skew it: cleanupPodPool waits
	// for the pool object, but children and pods are garbage-collected in the
	// background. Start from a provably empty namespace.
	waitForNoPods(t)

	// Fill most of each on-demand node. Two 2000m pods cannot share a node
	// (4000m allocatable), so they spread one per node deterministically.
	blocker = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "od-blocker", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](2),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "od-blocker"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "od-blocker"}},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{"capacity-type": "on-demand"},
					Containers: []corev1.Container{{
						Name: "c", Image: "nginx",
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2000m"),
						}},
					}},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, blocker); err != nil {
		t.Fatalf("creating blocker: %v", err)
	}

	waitForDeploymentReady(t, "od-blocker", 2)

	hbSec := int32(hb.Seconds())

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: ns},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:                      10,
			WorkloadTemplate:              cpuWorkloadTemplate("1000m"),
			OpportunisticHeartbeatSeconds: &hbSec,
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      "base",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2), Target: pctTarget(20)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, ""),
				},
				{
					Name:      "scav",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: ptr.To(true)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, ""),
				},
				{
					Name:      "burst",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "spot"}, ""),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("creating pool: %v", err)
	}

	t.Run("settles at real capacity", func(t *testing.T) {
		waitForChildSpec(t, poolName+"-base", 2)
		waitForChildSpec(t, poolName+"-scav", 2)
		waitForChildSpec(t, poolName+"-burst", 6)
	})

	// The anti-flap regression. The original bug probed on every reconcile and
	// paid for each probe out of burst, so burst churned a pod per cycle. Watch
	// burst through two full probe cycles (issue → refusal ≈ verdict requeue +
	// heartbeat): its spec must never move and none of its pods may be
	// replaced. The probe itself is visible on the scavenger child as 2→3→2 —
	// that is the design working, not flap.
	t.Run("burst is untouched by failing probes", func(t *testing.T) {
		before := burstPodUIDs(t, poolName)

		// The window must contain at least one probe or it proves nothing —
		// a dead probe machinery would also leave burst untouched. The probe
		// is visible as the scavenger child briefly asking for one more.
		//
		// Deliberate invariant-window loop, not a poll: every tick asserts
		// burst never moved, and success is running out the clock — the
		// inverse of what wait.PollUntilContextTimeout expresses.
		probeSeen := false

		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if got := childSpec(t, poolName+"-burst"); got != 6 {
				t.Fatalf("burst spec moved to %d during a probe cycle — the probe is being funded from the budget", got)
			}

			if childSpec(t, poolName+"-scav") == 3 {
				probeSeen = true
			}

			time.Sleep(250 * time.Millisecond)
		}

		if !probeSeen {
			t.Fatal("no probe observed in the window: burst being untouched proved nothing — is the probe machinery running?")
		}

		after := burstPodUIDs(t, poolName)
		for uid := range before {
			if !after[uid] {
				t.Fatalf("a burst pod was replaced during failing probes — speculative kill")
			}
		}
	})

	t.Run("walk-up reclaims freed capacity", func(t *testing.T) {
		if err := k8sClient.Delete(ctx, blocker); err != nil {
			t.Fatalf("deleting blocker: %v", err)
		}
		// 6000m freed → scav walks 2→6, burst shrinks 6→2, one PROVEN replica
		// at a time, ending on a refusal at the 7th.
		waitForChildSpec(t, poolName+"-scav", 6)
		waitForChildSpec(t, poolName+"-burst", 2)
		waitForChildSpec(t, poolName+"-base", 2)
	})
}

// TestPreemptionMigration is the end-to-end proof that preemption, migration,
// and reclaim work as a cycle against the real kube-scheduler with real
// PriorityClasses.
//
// The design claims preemption produces the same signal as "never fit" — an
// evicted scavenger pod's replacement goes Pending/Unschedulable, and the
// controller treats it identically to capacity exhaustion. This test verifies
// that claim with actual scheduler-driven preemption rather than simulated
// capacity exhaustion.
//
// Arithmetic, pinned to the fixture (2 on-demand + 2 spot nodes, 4 CPU each):
//
//	Phase 1 (settle):
//	  base       2 × 1000m on-demand  (priority 1000)
//	  scavenger  1000m on-demand      (priority -100)   → capacity = 6
//	  replicas 10                     → expect base 2 / scav 6 / burst 2
//
//	Phase 2 (preempt):
//	  preemptor  2 × 2000m on-demand  (priority 1000)
//	  each node: base(1000m) + preemptor(2000m) = 3000m, 1000m free → 1 scav
//	  → expect base 2 / scav 2 / burst 6
//
//	Phase 3 (reclaim):
//	  preemptor deleted → 4000m freed, probe walks scav back up
//	  → expect base 2 / scav 6 / burst 2
func TestPreemptionMigration(t *testing.T) {
	const (
		poolName = "kwok-preemption"
		ns       = "default"
		hb       = 2 * time.Second
	)

	ctx := context.Background()

	highPC := &schedulingv1.PriorityClass{
		ObjectMeta:       metav1.ObjectMeta{Name: "production-high"},
		Value:            1000,
		GlobalDefault:    false,
		PreemptionPolicy: ptr.To(corev1.PreemptLowerPriority),
	}
	scavPC := &schedulingv1.PriorityClass{
		ObjectMeta:       metav1.ObjectMeta{Name: "scavenger-priority"},
		Value:            -100,
		GlobalDefault:    false,
		PreemptionPolicy: ptr.To(corev1.PreemptNever),
	}

	_ = topologyClient.Delete(ctx, highPC)

	_ = topologyClient.Delete(ctx, scavPC)

	if err := topologyClient.Create(ctx, highPC); err != nil {
		t.Fatalf("creating production-high PriorityClass: %v", err)
	}
	defer func() { _ = topologyClient.Delete(ctx, highPC) }()

	if err := topologyClient.Create(ctx, scavPC); err != nil {
		t.Fatalf("creating scavenger-priority PriorityClass: %v", err)
	}
	defer func() { _ = topologyClient.Delete(ctx, scavPC) }()

	cleanupPodPool(t, poolName)
	defer cleanupPodPool(t, poolName)

	preemptor := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "preemptor", Namespace: ns}}

	_ = k8sClient.Delete(ctx, preemptor)
	defer func() { _ = k8sClient.Delete(ctx, preemptor) }()

	waitForNoPods(t)

	hbSec := int32(hb.Seconds())

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: ns},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:                      10,
			WorkloadTemplate:              cpuWorkloadTemplate("1000m"),
			OpportunisticHeartbeatSeconds: &hbSec,
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      "base",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2), Target: pctTarget(20)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, "production-high"),
				},
				{
					Name:      "scav",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: ptr.To(true)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}, "scavenger-priority"),
				},
				{
					Name:      "burst",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "spot"}, ""),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("creating pool: %v", err)
	}

	t.Run("settles with full capacity", func(t *testing.T) {
		waitForChildSpec(t, poolName+"-base", 2)
		waitForChildSpec(t, poolName+"-scav", 6)
		waitForChildSpec(t, poolName+"-burst", 2)
	})

	t.Run("preemption migrates to burst", func(t *testing.T) {
		preemptor = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "preemptor", Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](2),
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "preemptor"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "preemptor"}},
					Spec: corev1.PodSpec{
						PriorityClassName: "production-high",
						NodeSelector:      map[string]string{"capacity-type": "on-demand"},
						Containers: []corev1.Container{{
							Name: "c", Image: "nginx",
							Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("2000m"),
							}},
						}},
					},
				},
			},
		}
		if err := k8sClient.Create(ctx, preemptor); err != nil {
			t.Fatalf("creating preemptor: %v", err)
		}

		waitForDeploymentReady(t, "preemptor", 2)

		// After preemption: each on-demand node has base(1000m) +
		// preemptor(2000m) = 3000m, leaving 1000m → 1 scav pod per node.
		waitForChildSpec(t, poolName+"-scav", 2)
		waitForChildSpec(t, poolName+"-burst", 6)
		waitForChildSpec(t, poolName+"-base", 2)
	})

	t.Run("reclaims after preemptor leaves", func(t *testing.T) {
		if err := k8sClient.Delete(ctx, preemptor); err != nil {
			t.Fatalf("deleting preemptor: %v", err)
		}
		// 4000m freed → probe walks scav 2→6, burst shrinks 6→2.
		waitForChildSpec(t, poolName+"-scav", 6)
		waitForChildSpec(t, poolName+"-burst", 2)
		waitForChildSpec(t, poolName+"-base", 2)
	})
}

func cpuWorkloadTemplate(cpu string) runtime.RawExtension {
	tmpl := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name":  "app",
						"image": "nginx",
						"resources": map[string]any{
							"requests": map[string]any{"cpu": cpu},
						},
					}},
				},
			},
		},
	}
	raw, _ := json.Marshal(tmpl)

	return runtime.RawExtension{Raw: raw}
}

func childSpec(t *testing.T, name string) int32 {
	t.Helper()

	dep := &appsv1.Deployment{}
	if err := k8sClient.Get(context.Background(),
		types.NamespacedName{Name: name, Namespace: testNamespace}, dep); err != nil {
		t.Fatalf("getting %s: %v", name, err)
	}

	if dep.Spec.Replicas == nil {
		return 0
	}

	return *dep.Spec.Replicas
}

func waitForChildSpec(t *testing.T, name string, want int32) {
	t.Helper()

	var last int32 = -1

	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		dep := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: testNamespace}, dep); err == nil && dep.Spec.Replicas != nil {
			last = *dep.Spec.Replicas
		}

		return last == want, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for %s spec.replicas=%d, last saw %d", name, want, last)
	}
}

func waitForDeploymentReady(t *testing.T, name string, want int32) {
	t.Helper()

	var last int32 = -1

	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		dep := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: testNamespace}, dep); err == nil {
			last = dep.Status.ReadyReplicas
		}

		return last == want, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for %s to have %d ready, last saw %d", name, want, last)
	}
}

func burstPodUIDs(t *testing.T, poolName string) map[types.UID]bool {
	t.Helper()

	pods := &corev1.PodList{}
	if err := k8sClient.List(context.Background(), pods); err != nil {
		t.Fatalf("listing pods: %v", err)
	}

	out := map[types.UID]bool{}

	for i := range pods.Items {
		l := pods.Items[i].Labels
		if l["podpools.dev/pool"] == poolName && l["podpools.dev/group"] == "burst" {
			out[pods.Items[i].UID] = true
		}
	}

	return out
}

// waitForNoPods blocks until the test namespace has no pods at all, so a
// test whose arithmetic depends on node headroom cannot inherit terminating
// pods from an earlier test.
func waitForNoPods(t *testing.T) {
	t.Helper()

	remaining := -1

	// pollTimeout, not a shorter budget of its own. Deleting a pool removes
	// the object once its children are gone, but the ReplicaSets and Pods
	// beneath them are collected asynchronously afterwards, so this waits on
	// a chain the previous test's cleanup never claimed to have finished. A
	// loaded runner needs the full budget; halving it here was an outlier
	// with no reason behind it, and it failed on the first machine that was
	// not the author's.
	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		pods := &corev1.PodList{}
		if err := k8sClient.List(ctx, pods); err == nil {
			remaining = len(pods.Items)
		}

		return remaining == 0, nil
	})
	if err != nil {
		t.Fatalf("namespace not quiescent: %d pods still present at test start", remaining)
	}
}
