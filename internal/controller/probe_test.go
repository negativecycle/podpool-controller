package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

var probeTestBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// The probe lifecycle, tested without a cluster. Every case here pins one edge
// of a state machine whose whole job is to be careful about time, so the clock
// is injected and nothing sleeps.

func probePool() *podpoolsv1alpha1.PodPool {
	return &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: "probe-pool", Namespace: "default"},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Groups: []podpoolsv1alpha1.GroupSpec{
				{Name: testGroupScav, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Opportunistic: opportunistic()}},
			},
		},
	}
}

func TestDecideProbeLifecycle(t *testing.T) {
	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	pool := probePool()
	now := probeTestBase

	settled := opportunisticObservation{found: true, asked: 4, ready: 4}

	// 1. Settled, no history → probe issues: target+1, check back soon.
	d := r.decideProbe(pool, testGroupScav, 4, settled, now)
	if d.target != 5 || !d.awaitVerdict || !d.issued {
		t.Fatalf("first probe: %+v, want target=5 issued awaitVerdict", d)
	}

	// 2. Verdict not in yet → the probe is held, not re-issued. Still target+1,
	//    but issued is false: nothing new happened, so nothing may be announced.
	unjudged := opportunisticObservation{found: true, asked: 5, ready: 4}

	d = r.decideProbe(pool, testGroupScav, 4, unjudged, now.Add(time.Second))
	if d.target != 5 || !d.awaitVerdict || d.issued {
		t.Fatalf("held probe: %+v, want target=5 awaitVerdict, issued=false", d)
	}

	// 3. Refused → withdrawn to the distribution's target, and the refusal
	//    starts the heartbeat clock.
	refused := opportunisticObservation{found: true, asked: 5, ready: 4, unschedulable: 1}

	d = r.decideProbe(pool, testGroupScav, 4, refused, now.Add(2*time.Second))
	if d.target != 4 || d.awaitVerdict {
		t.Fatalf("refused probe: %+v, want target=4 awaitVerdict=false", d)
	}

	// 4. Settled again immediately after a refusal → NO new probe. Without the
	//    backoff every settled reconcile would probe, and a group with no room
	//    left would ask, be refused, and ask again forever.
	d = r.decideProbe(pool, testGroupScav, 4, settled, now.Add(3*time.Second))
	if d.target != 4 || d.awaitVerdict {
		t.Fatalf("within backoff: %+v, want target=4 awaitVerdict=false — probe must wait out the heartbeat", d)
	}

	// 5. Heartbeat elapsed → probe again. Measured from the refusal at +2s,
	//    not from the start: the backoff clock starts when the answer was no.
	afterHeartbeat := now.Add(2*time.Second + defaultOpportunisticHeartbeat)

	d = r.decideProbe(pool, testGroupScav, 4, settled, afterHeartbeat)
	if d.target != 5 {
		t.Fatalf("after heartbeat: target=%d, want 5", d.target)
	}

	// 6. This probe succeeds: the child reports 5 ready. Capacity has been
	//    absorbed upstream (the distribution now says 5), and the very same
	//    call may probe again — success must not wait out a heartbeat, or a
	//    freed node would take one heartbeat per replica to reclaim.
	succeeded := opportunisticObservation{found: true, asked: 5, ready: 5}

	d = r.decideProbe(pool, testGroupScav, 5, succeeded, afterHeartbeat.Add(time.Second))
	if d.target != 6 || !d.awaitVerdict {
		t.Fatalf("walk-up: %+v, want target=6 awaitVerdict=true — success re-probes immediately", d)
	}
}

func TestDecideProbeRequiresSettledState(t *testing.T) {
	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	pool := probePool()
	now := probeTestBase

	cases := []struct {
		name   string
		target int32
		obs    opportunisticObservation
	}{
		// A group mid-change must not be probed: the answer would be
		// unattributable to the probe.
		{"no child yet", 4, opportunisticObservation{}},
		{"child not yet at the distribution's target", 4, opportunisticObservation{found: true, asked: 3, ready: 3}},
		{"replicas still starting", 4, opportunisticObservation{found: true, asked: 4, ready: 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := r.decideProbe(pool, testGroupScav, c.target, c.obs, now)
			if d.target != c.target || d.awaitVerdict {
				t.Errorf("target=%d awaitVerdict=%v, want %d false", d.target, d.awaitVerdict, c.target)
			}
		})
	}
}

// An unjudged probe must not count as capacity. If it did, the distribution
// would hand its replica to this group and take one from the next — the
// speculative burst-pod kill the design exists to prevent.
func TestCapacityFromHoldsUnjudgedProbe(t *testing.T) {
	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	pool := probePool()

	// Arrange an outstanding probe.
	settled := opportunisticObservation{found: true, asked: 4, ready: 4}
	if d := r.decideProbe(pool, testGroupScav, 4, settled, probeTestBase); d.target != 5 {
		t.Fatalf("setup: expected a probe to issue")
	}

	obs := map[string]opportunisticObservation{
		testGroupScav: {found: true, asked: 5, ready: 4}, // probe pending, unjudged
	}

	capacity := r.capacityFrom(pool, obs)
	if capacity[testGroupScav] != 4 {
		t.Fatalf("capacity=%d, want 4 — an unjudged probe is not capacity", capacity[testGroupScav])
	}

	// Once ready, it is.
	obs[testGroupScav] = opportunisticObservation{found: true, asked: 5, ready: 5}

	capacity = r.capacityFrom(pool, obs)
	if capacity[testGroupScav] != 5 {
		t.Fatalf("capacity=%d, want 5 — a running probe is proven capacity", capacity[testGroupScav])
	}
}

// The probe rides outside the distribution: while one is outstanding, every
// other group's target is exactly what it would be with no probe at all.
func TestProbeDoesNotFundItselfFromOtherGroups(t *testing.T) {
	groups := threeTierSpec()
	capacity := map[string]int32{testGroupScav: 30}

	base := workload.ComputeGroupTargets(100, groups, capacity)

	// The distribution knows nothing about probes, so this is structural —
	// but pin it, because a design that funded the probe from the budget
	// would change exactly this number.
	if base.Targets[2] != 35 {
		t.Fatalf("burst=%d, want 35", base.Targets[2])
	}

	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	pool := probePool()
	settled := opportunisticObservation{found: true, asked: 30, ready: 30}
	scavDecision := r.decideProbe(pool, testGroupScav, base.Targets[1], settled, probeTestBase)

	if scavDecision.target != 31 {
		t.Fatalf("scavenger probe target=%d, want 31", scavDecision.target)
	}
	// Re-running the distribution with the same capacity proves burst is
	// untouched while the probe is in flight: 35 + 31 + 35 = total + 1, and
	// the +1 is the probe, not burst's replica.
	again := workload.ComputeGroupTargets(100, groups, capacity)
	if again.Targets[2] != 35 {
		t.Fatalf("burst=%d after probe issued, want 35 — the probe must not be funded from the budget", again.Targets[2])
	}
}

func TestForgetProbesDropsOnlyThatPool(t *testing.T) {
	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	a := probePool()
	b := probePool()
	b.Name = "other-pool"

	settled := opportunisticObservation{found: true, asked: 1, ready: 1}
	r.decideProbe(a, testGroupScav, 1, settled, probeTestBase)
	r.decideProbe(b, testGroupScav, 1, settled, probeTestBase)

	r.forgetProbes(a.Namespace, a.Name)

	if r.probeOutstanding(a, testGroupScav) {
		t.Error("pool a's probe record survived deletion")
	}

	if !r.probeOutstanding(b, testGroupScav) {
		t.Error("pool b's probe record was collateral damage")
	}
}
