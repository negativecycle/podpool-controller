package controller

import (
	"testing"
	"time"

	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// testGroupScavShort keeps the child's name inside the DNS budget in tests that
// build one by hand.
const testGroupScavShort = "scav"

func TestDecideProbeHeartbeatArithmeticIsTimeControlled(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	pool := &podpoolsv1alpha1.PodPool{}
	pool.Name = "p"
	pool.Namespace = "ns"
	pool.Spec.Groups = []podpoolsv1alpha1.GroupSpec{{
		Name:    testGroupScavShort,
		Scaling: podpoolsv1alpha1.ScalingConstraints{Opportunistic: ptr.To(true)},
	}}

	// Child is at target and healthy: the state in which a growth probe is
	// considered at all.
	settled := opportunisticObservation{found: true, asked: 4, ready: 4}

	newReconciler := func() *PodPoolReconciler {
		return &PodPoolReconciler{probes: map[string]probeState{}}
	}

	// A probe refused at `base` must not be retried before the heartbeat.
	r := newReconciler()
	r.probes[probeKey(pool, testGroupScavShort)] = probeState{lastFailed: base}

	justBefore := base.Add(defaultOpportunisticHeartbeat - time.Second)
	if d := r.decideProbe(pool, testGroupScavShort, 4, settled, justBefore); d.target != 4 {
		t.Errorf("probe retried %v after failure (before the %v heartbeat): target = %d, want 4",
			defaultOpportunisticHeartbeat-time.Second, defaultOpportunisticHeartbeat, d.target)
	}

	// Once the heartbeat has elapsed, growth is allowed again.
	r = newReconciler()
	r.probes[probeKey(pool, testGroupScavShort)] = probeState{lastFailed: base}

	justAfter := base.Add(defaultOpportunisticHeartbeat + time.Second)
	if d := r.decideProbe(pool, testGroupScavShort, 4, settled, justAfter); d.target != 5 {
		t.Errorf("probe withheld %v after failure (past the %v heartbeat): target = %d, want 5",
			defaultOpportunisticHeartbeat+time.Second, defaultOpportunisticHeartbeat, d.target)
	}
}

// ---------------------------------------------------------------------------
// #37 — defaulting
// ---------------------------------------------------------------------------

func poolWithGroups(opportunistic bool, heartbeatSeconds *int32) *podpoolsv1alpha1.PodPool {
	g := podpoolsv1alpha1.GroupSpec{Name: testGroupBase}
	if opportunistic {
		g.Scaling.Opportunistic = ptr.To(true)
	}

	return &podpoolsv1alpha1.PodPool{
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:                      3,
			Groups:                        []podpoolsv1alpha1.GroupSpec{g},
			OpportunisticHeartbeatSeconds: heartbeatSeconds,
		},
	}
}

// GREEN today — and it is the assertion that makes the schema default in #37
// safe to add.
//
// opportunisticHeartbeat() returns 0 for a pool with no opportunistic group,
// and requeueAfter branches on that zero to pick the 10m floor. Once a schema
// default populates the field on *every* pool, only the ordering inside
// opportunisticHeartbeat() — the !any check coming first — keeps idle pools on
// the floor. Hoisting the field check above it would silently halve every idle
// pool's requeue interval, and this test is what would catch that.
func TestRequeueAfterKeepsTheFloorWhenNoGroupIsOpportunistic(t *testing.T) {
	// The field is populated, exactly as a schema default would leave it.
	pool := poolWithGroups(false, ptr.To(int32(300)))

	if h := opportunisticHeartbeat(pool); h != 0 {
		t.Fatalf("opportunisticHeartbeat = %v, want 0 for a pool with no opportunistic group", h)
	}

	got := requeueAfter(pool)
	// wait.Jitter(d, 0.1) yields [d, 1.1d).
	if got < reconcileFloor || got >= time.Duration(float64(reconcileFloor)*1.1) {
		t.Errorf("requeueAfter = %v, want the %v floor (jittered up to 10%%); "+
			"a populated heartbeat must not pull a non-opportunistic pool off the floor",
			got, reconcileFloor)
	}
}

// GREEN today — guards the in-code fallback that a schema default makes *look*
// dead. It is not dead: objects stored before the default existed keep a nil
// field, and structs built in tests never pass through admission.
func TestOpportunisticHeartbeatFallsBackWhenFieldIsNil(t *testing.T) {
	pool := poolWithGroups(true, nil)

	if got := opportunisticHeartbeat(pool); got != defaultOpportunisticHeartbeat {
		t.Errorf("opportunisticHeartbeat = %v, want the %v fallback; "+
			"the nil check is still load-bearing after a schema default is added",
			got, defaultOpportunisticHeartbeat)
	}
}

// GREEN today — an explicit value still wins over the default.
func TestOpportunisticHeartbeatHonoursAnExplicitValue(t *testing.T) {
	pool := poolWithGroups(true, ptr.To(int32(90)))

	if got := opportunisticHeartbeat(pool); got != 90*time.Second {
		t.Errorf("opportunisticHeartbeat = %v, want 90s", got)
	}
}
