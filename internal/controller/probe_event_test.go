package controller

import (
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clocktesting "k8s.io/utils/clock/testing"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// The probe is the one thing this controller does that costs a scheduling
// attempt, so its event has to say "I asked a question", not "a question is
// outstanding". decideProbe returns target+1 from two structurally different
// places -- opening a probe, and re-asserting one that has not been answered --
// and an event gated on "the target went up" cannot tell them apart. It would
// fire once per probeVerdictRequeue, forever.
//
// The second half matters as much as the first. A probe can stay outstanding
// indefinitely: countUnschedulable only counts pods that exist and carry
// PodScheduled=False/Unschedulable, so a pod blocked by a ResourceQuota and
// never created never resolves either way. Announcing only on issuance without
// also bounding the wait would trade a noisy bug for a silent one.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// probeEventLog is a minimal events.EventRecorder that keeps structured
// records.
//
// k8s.io/client-go/tools/events ships a FakeRecorder, but it delivers into an
// unbuffered-by-default channel and drops the action field. A channel would
// block once its buffer filled, which is precisely the failure mode under test
// here: an unbounded emitter would hang the test rather than fail it.
type probeEventLog struct {
	mu      sync.Mutex
	entries []probeEventEntry
}

type probeEventEntry struct {
	eventType, reason, action, note string
}

func (l *probeEventLog) Eventf(
	_ runtime.Object, _ runtime.Object,
	eventtype, reason, action, note string, args ...any,
) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, probeEventEntry{
		eventType: eventtype,
		reason:    reason,
		action:    action,
		note:      note,
	})
	_ = args
}

// count returns how many events carry exactly this reason. Exact match, not a
// prefix: "CapacityProbe" is a prefix of "CapacityProbeTimeout".
func (l *probeEventLog) count(reason string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	var n int

	for _, e := range l.entries {
		if e.reason == reason {
			n++
		}
	}

	return n
}

func (l *probeEventLog) summary() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]string, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, e.eventType+"/"+e.reason)
	}

	return "[" + strings.Join(out, " ") + "]"
}

const (
	reasonCapacityProbe        = "CapacityProbe"
	reasonCapacityProbeTimeout = "CapacityProbeTimeout"
)

func probeEventReconciler(now time.Time) (*PodPoolReconciler, *probeEventLog) {
	log := &probeEventLog{}

	return &PodPoolReconciler{
		Clock:    clocktesting.NewFakePassiveClock(now),
		Recorder: log,
	}, log
}

// settledObs is a group sitting exactly at its target with everything ready:
// the only state from which a probe may be issued.
func settledObs(at int32) opportunisticObservation {
	return opportunisticObservation{found: true, asked: at, ready: at}
}

// pendingObs is the probe written but unanswered: one more asked than ready,
// and no pod the scheduler has refused.
func pendingObs(target int32) opportunisticObservation {
	return opportunisticObservation{found: true, asked: target + 1, ready: target}
}

// probePass runs one applyProbes for a single-group pool.
func probePass(
	r *PodPoolReconciler, pool *podpoolsv1alpha1.PodPool,
	base int32, obs opportunisticObservation, now time.Time,
) (int32, bool) {
	targets, pending := r.applyProbes(pool, []int32{base},
		map[string]opportunisticObservation{testGroupScav: obs}, now)

	return targets[0], pending
}

// ---------------------------------------------------------------------------
// piece A: the event is an action, not a state
// ---------------------------------------------------------------------------

// TestProbeEventFiresOnceWhileVerdictPending is #78 itself.
func TestProbeEventFiresOnceWhileVerdictPending(t *testing.T) {
	const base int32 = 4

	now := probeTestBase
	pool := probePool(60)
	r, log := probeEventReconciler(now)

	if got, pending := probePass(r, pool, base, settledObs(base), now); got != base+1 || !pending {
		t.Fatalf("issuing pass: target=%d pending=%v, want %d true", got, pending, base+1)
	}

	// Three more passes at the 15s verdict cadence with the answer still absent.
	for i := 1; i <= 3; i++ {
		at := now.Add(time.Duration(i) * probeVerdictRequeue)

		got, pending := probePass(r, pool, base, pendingObs(base), at)
		if got != base+1 || !pending {
			t.Fatalf("pass %d: target=%d pending=%v, want %d true (the probe is still held)",
				i, got, pending, base+1)
		}
	}

	if n := log.count(reasonCapacityProbe); n != 1 {
		t.Errorf("%s emitted %d times across one issuance plus three waiting passes, want 1; events %s.\n"+
			"decideProbe returns target+1 both when it issues a probe and when it is merely "+
			"still waiting, and applyProbes cannot tell those apart. An event announces that "+
			"something happened; \"still outstanding\" is a state, not an occurrence.",
			reasonCapacityProbe, n, log.summary())
	}
}

// TestProbeEventFiresAgainForTheNextProbe is the guard against over-correcting
// #78. Suppressing the repeat must not suppress the *next real probe*: a
// successful probe deliberately does not reset the backoff, so a group
// discovering free capacity walks up one replica per reconcile and each step is
// a genuine new action.
func TestProbeEventFiresAgainForTheNextProbe(t *testing.T) {
	const base int32 = 4

	now := probeTestBase
	pool := probePool(60)
	r, log := probeEventReconciler(now)

	probePass(r, pool, base, settledObs(base), now)

	// The probe landed: the child reports base+1 ready, and the distribution has
	// absorbed the new capacity, so the next pass probes from the higher base.
	got, _ := probePass(r, pool, base+1, settledObs(base+1), now.Add(probeVerdictRequeue))
	if got != base+2 {
		t.Fatalf("walk-up: target=%d, want %d; success must re-probe immediately", got, base+2)
	}

	if n := log.count(reasonCapacityProbe); n != 2 {
		t.Errorf("%s emitted %d times for two distinct probes, want 2; events %s.\n"+
			"Each issuance is a real action and must still be announced.",
			reasonCapacityProbe, n, log.summary())
	}
}

// ---------------------------------------------------------------------------
// decision-level tests
// ---------------------------------------------------------------------------

// TestProbeDecisionDistinguishesIssuanceFromWaiting pins the field semantics
// directly: issuance is the only pass that announces, and every outstanding
// pass keeps asking for the short requeue.
func TestProbeDecisionDistinguishesIssuanceFromWaiting(t *testing.T) {
	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	pool := probePool(60)

	d := r.decideProbe(pool, testGroupScav, 4, settledObs(4), probeTestBase)
	if !d.issued || !d.awaitVerdict || d.target != 5 {
		t.Fatalf("issuing pass: %+v, want issued+awaitVerdict, target 5", d)
	}

	d = r.decideProbe(pool, testGroupScav, 4, pendingObs(4), probeTestBase.Add(probeVerdictRequeue))
	if d.issued || !d.awaitVerdict || d.target != 5 {
		t.Fatalf("waiting pass: %+v, want awaitVerdict only, target 5", d)
	}

	if d.abandoned {
		t.Fatal("nothing has timed out yet")
	}
}

// TestProbeDecisionTimesOutAnUnansweredProbe is the reason the bound exists.
// Without it the group sits one replica below its capacity, spinning on a 15s
// requeue, with nothing in status or events to say why.
func TestProbeDecisionTimesOutAnUnansweredProbe(t *testing.T) {
	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	pool := probePool(60)

	r.decideProbe(pool, testGroupScav, 4, settledObs(4), probeTestBase)

	// Just inside the bound: still waiting, nothing announced.
	d := r.decideProbe(pool, testGroupScav, 4, pendingObs(4), probeTestBase.Add(probeVerdictTimeout-time.Second))
	if d.abandoned || !d.awaitVerdict {
		t.Fatalf("inside the bound: %+v, want a plain wait", d)
	}

	// Past it: withdrawn, and the withdrawal starts the heartbeat backoff so
	// the next attempt is not immediate.
	d = r.decideProbe(pool, testGroupScav, 4, pendingObs(4), probeTestBase.Add(probeVerdictTimeout+time.Second))
	if !d.abandoned || d.target != 4 || d.awaitVerdict {
		t.Fatalf("past the bound: %+v, want abandoned at 4 with no further wait", d)
	}

	if r.probeOutstanding(pool, testGroupScav) {
		t.Error("an abandoned probe must not stay on the books")
	}
}

// TestProbeDecisionUnreadableChildHoldsWithoutSpin pins the third return
// shape (§0.1): outstanding probe, child unreadable. The +1 is withdrawn, no
// short requeue, nothing announced — and the timeout still bounds it.
func TestProbeDecisionUnreadableChildHoldsWithoutSpin(t *testing.T) {
	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	pool := probePool(60)

	r.decideProbe(pool, testGroupScav, 4, settledObs(4), probeTestBase)

	unread := opportunisticObservation{}

	d := r.decideProbe(pool, testGroupScav, 4, unread, probeTestBase.Add(time.Second))
	if d.target != 4 || d.issued || d.awaitVerdict || d.abandoned {
		t.Fatalf("unreadable child: %+v, want bare hold at 4", d)
	}

	if !r.probeOutstanding(pool, testGroupScav) {
		t.Fatal("the probe record must survive an unreadable pass")
	}

	d = r.decideProbe(pool, testGroupScav, 4, unread, probeTestBase.Add(probeVerdictTimeout+time.Second))
	if !d.abandoned || d.target != 4 {
		t.Fatalf("past timeout while unreadable: %+v, want abandoned at 4", d)
	}
}

// TestProbeDecisionVerdictBeatsTimeout: a verdict arriving on the same pass
// the deadline expires resolves normally; abandoned stays false.
func TestProbeDecisionVerdictBeatsTimeout(t *testing.T) {
	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}
	pool := probePool(60)

	r.decideProbe(pool, testGroupScav, 4, settledObs(4), probeTestBase)

	succeeded := opportunisticObservation{found: true, asked: 5, ready: 5}

	d := r.decideProbe(pool, testGroupScav, 5, succeeded, probeTestBase.Add(probeVerdictTimeout))
	if d.abandoned {
		t.Fatalf("verdict and deadline on the same pass: %+v; the answer is real, use it", d)
	}
}

// ---------------------------------------------------------------------------
// piece B: an unanswered probe must not wait forever
// ---------------------------------------------------------------------------

// runUntilVerdictTimeout issues a probe and then holds the verdict for well
// past any plausible bound, returning the final target and pending flag.
func runUntilVerdictTimeout(
	r *PodPoolReconciler, pool *podpoolsv1alpha1.PodPool, base int32, now time.Time,
) (int32, bool) {
	probePass(r, pool, base, settledObs(base), now)

	var (
		target  = base + 1
		pending = true
	)

	// 5 minutes at the 15s cadence: 20 passes, far beyond any scheduling cycle.
	for i := 1; i <= 20; i++ {
		target, pending = probePass(r, pool, base, pendingObs(base),
			now.Add(time.Duration(i)*probeVerdictRequeue))
	}

	return target, pending
}

// TestStuckProbeIsAbandonedAfterTimeout covers the state the plan's section 2
// argues makes piece A unsafe on its own: a probe whose pod is never created
// (quota, admission) or never scheduled (scheduler down) resolves neither way,
// so the pool spins at 15s indefinitely and the group is pinned one replica
// below capacity.
func TestStuckProbeIsAbandonedAfterTimeout(t *testing.T) {
	const base int32 = 4

	now := probeTestBase
	pool := probePool(60)
	r, _ := probeEventReconciler(now)

	target, pending := runUntilVerdictTimeout(r, pool, base, now)

	if target != base {
		t.Errorf("target is still %d after five minutes with no verdict, want %d withdrawn.\n"+
			"Nothing bounds an outstanding probe, so the extra replica is held and the "+
			"group stays one below its real capacity for as long as the pool exists.",
			target, base)
	}

	if pending {
		t.Error("the pool is still requeuing every 15s for a verdict that will never arrive")
	}
}

// TestAbandonedProbeEmitsWarning is the other half: withdrawing the probe
// silently would leave an operator with no signal at all, which is strictly
// worse than today's noise. A probe that cannot be answered usually means quota
// or scheduler trouble, which is the operator's problem to see.
func TestAbandonedProbeEmitsWarning(t *testing.T) {
	const base int32 = 4

	now := probeTestBase
	pool := probePool(60)
	r, log := probeEventReconciler(now)

	runUntilVerdictTimeout(r, pool, base, now)

	if n := log.count(reasonCapacityProbeTimeout); n != 1 {
		t.Errorf("%s emitted %d times, want 1; events %s.\n"+
			"Gating the Normal event on issuance removes the only current sign that a "+
			"probe is stuck. Something has to take its place.",
			reasonCapacityProbeTimeout, n, log.summary())
	}

	// The abandoning pass must not also count as a new issuance: settled requires
	// obs.asked == target, and asked is target+1 throughout an outstanding probe,
	// so the fall-through cannot re-issue. Pin it, because a future edit to that
	// branch is exactly how both could fire and the pool would emit a
	// contradictory pair.
	if n := log.count(reasonCapacityProbe); n != 1 {
		t.Errorf("%s emitted %d times, want 1 (the original issuance only); events %s",
			reasonCapacityProbe, n, log.summary())
	}
}
