package controller

// The four mutexes, driven concurrently.
//
// PodPoolReconciler carries four maps that outlive a single pass -- the watch
// registry, the readiness latch, the probe records, and the out-of-range gate
// -- and Reconcile reaches all four from as many goroutines as
// --max-concurrent-reconciles allows. Every one of them is guarded, and until
// now nothing drove two goroutines at the same map: the suite runs under -race,
// but a detector only reports what a test actually executes, so the guards were
// asserted by comment alone.
//
// What the concurrency actually is, and it decides the shape of every test
// here: controller-runtime never runs two reconciles for the same object key at
// once, so no two workers ever contend for a single pool's entry. They contend
// for the *map*, each holding a different pool. That is why nothing below races
// two goroutines over one key -- it would be testing an interleaving the
// workqueue cannot produce -- and why each test instead points raceWidth
// distinct pools at one shared reconciler.
//
// The failure the lock prevents is therefore a lost or corrupted neighbouring
// entry, not a mangled one of your own. Both halves matter: an unguarded
// concurrent map write takes the process down and is impossible to miss, while
// a lost update is silent and costs one pool the probe record or the warning
// gate that another pool's pass overwrote.
//
// These do not use t.Parallel. The package is serial on purpose (see
// harness_test.go); the concurrency under test is inside each test, between
// goroutines this file starts and joins.

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// raceWidth is how many goroutines each test points at one piece of shared
// state. Production allows five concurrent reconciles by default; this is
// deliberately wider, because the window a lost update needs is short and a
// test that opens it once may never observe it.
const raceWidth = 16

// startTogether runs fn once per worker, released from a common gate so the
// goroutines arrive at the shared state at once rather than in the order they
// were spawned. Without the gate the first goroutine has usually finished
// before the last is scheduled, and every interleaving under test is missed.
func startTogether(fn func(worker int)) {
	var wg sync.WaitGroup

	gate := make(chan struct{})

	for i := range raceWidth {
		wg.Go(func() {
			<-gate
			fn(i)
		})
	}

	close(gate)
	wg.Wait()
}

// nthProbePool is one worker's pool: the shared fixture under a distinct name,
// so raceWidth of them key distinct entries in the same map. The trailing
// slash probeKey appends is what keeps pool-1 from prefix-matching pool-11.
func nthProbePool(i int) *podpoolsv1alpha1.PodPool {
	pool := probePool(60)
	pool.Name = "probe-pool-" + strconv.Itoa(i)

	return pool
}

// ---------------------------------------------------------------------------
// readyPublishedMu: the readiness latch
// ---------------------------------------------------------------------------

// Each worker reconciles a pool whose template names a different workload kind,
// which is the realistic fan-out: the latch is keyed by GVK, not by pool.
//
// The map is built lazily inside the lock, and that is the part that cannot
// survive being unguarded: two goroutines finding it nil both allocate, one
// allocation wins, and the other goroutine's latch is dropped. A dropped latch
// is not cosmetic -- readinessProven gates the StatusMissing diagnostic, so
// losing it re-arms a warning about a kind that has already proven it publishes
// readiness.
func TestMarkReadinessPublishedUnderConcurrentReconciles(t *testing.T) {
	r := &PodPoolReconciler{}

	gvks := make([]schema.GroupVersionKind, 0, raceWidth)
	for i := range raceWidth {
		gvks = append(gvks, schema.GroupVersionKind{
			Group:   testAppsGroup,
			Version: "v1",
			Kind:    testDepKind + strconv.Itoa(i),
		})
	}

	startTogether(func(worker int) {
		r.markReadinessPublished(gvks[worker])
		r.readinessProven(gvks[worker])
	})

	for _, gvk := range gvks {
		if !r.readinessProven(gvk) {
			t.Errorf("readinessProven(%s) = false, want true: a latch was lost", gvk.Kind)
		}
	}
}

// ---------------------------------------------------------------------------
// probeMu: the opportunistic probe records
// ---------------------------------------------------------------------------

// A probe is the pool deliberately running one replica over spec.replicas to
// ask the scheduler whether it fits, and decideProbe is a read-modify-write
// over that record: read outstanding, decide, write it back. raceWidth pools
// each reach a settled group at the same instant, so every worker performs that
// sequence against the shared map simultaneously.
//
// Every pool is entitled to exactly one probe here, and all of them must keep
// it. A record dropped by a neighbour's write does not fail loudly: the group
// silently re-probes on the next pass and the walk-up it was meant to drive
// never converges.
func TestDecideProbeUnderConcurrentReconciles(t *testing.T) {
	const target int32 = 4

	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}

	pools := make([]*podpoolsv1alpha1.PodPool, 0, raceWidth)
	for i := range raceWidth {
		pools = append(pools, nthProbePool(i))
	}

	var issued atomic.Int32

	startTogether(func(worker int) {
		// Settled at target with no history: the one state from which an
		// issuance is licensed, so every worker arrives entitled to ask.
		d := r.decideProbe(pools[worker], testGroupScav, target, settledObs(target), probeTestBase)
		if d.issued {
			issued.Add(1)
		}
	})

	if got := issued.Load(); got != raceWidth {
		t.Errorf("probes issued = %d, want %d: a pool was denied its probe", got, raceWidth)
	}

	for i, pool := range pools {
		if !r.probeOutstanding(pool, testGroupScav) {
			t.Errorf("pool %d: probeOutstanding = false: its record was overwritten by a neighbour", i)
		}
	}
}

// forgetProbes walks and deletes from both probe maps while other workers are
// reading and writing them. Deleting during iteration is safe in Go on its own;
// doing it concurrently with another goroutine's write is not.
//
// The invariant is the prefix scoping, under contention: a pool being cleaned
// up must take its own records and nobody else's. Worker 0 is the pass that
// found its pool deleted; every other worker is mid-reconcile on a live one.
func TestForgetProbesRacesDecideProbe(t *testing.T) {
	const target int32 = 4

	r := &PodPoolReconciler{Clock: clocktesting.NewFakePassiveClock(probeTestBase)}

	pools := make([]*podpoolsv1alpha1.PodPool, 0, raceWidth)
	for i := range raceWidth {
		pools = append(pools, nthProbePool(i))
	}

	// Seed every pool's record first, so the sweep below has neighbours to
	// damage. Serial: this is setup, not the thing under test.
	for _, pool := range pools {
		r.decideProbe(pool, testGroupScav, target, settledObs(target), probeTestBase)
	}

	startTogether(func(worker int) {
		if worker == 0 {
			r.forgetProbes(pools[0].Namespace, pools[0].Name)

			return
		}

		r.probeOutstanding(pools[worker], testGroupScav)
	})

	if r.probeOutstanding(pools[0], testGroupScav) {
		t.Error("pool 0: probeOutstanding = true after forgetProbes: the record outlived the pool")
	}

	for i, pool := range pools[1:] {
		if !r.probeOutstanding(pool, testGroupScav) {
			t.Errorf("pool %d: lost its probe record to a neighbour's forgetProbes", i+1)
		}
	}
}

// ---------------------------------------------------------------------------
// outOfRangeMu: the one-warning-per-group gate
// ---------------------------------------------------------------------------

// The gate exists because a child stuck publishing a bad count is reconciled on
// every heartbeat and every child event, so an ungated warning is an unbounded
// stream. Read-modify-write again: read already, set it, emit only if it was
// unset.
//
// Two rounds, because the two halves fail differently. The first proves each
// pool announces its own clamp exactly once with raceWidth workers writing the
// same map. The second proves the gate then holds: a pool whose entry was lost
// to a neighbour re-announces, which is the unbounded stream the gate exists to
// prevent, and it would show up nowhere else.
func TestReportOutOfRangeUnderConcurrentReconciles(t *testing.T) {
	r, log := probeEventReconciler(probeTestBase)
	gvk := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

	pools := make([]*podpoolsv1alpha1.PodPool, 0, raceWidth)
	for i := range raceWidth {
		pools = append(pools, nthProbePool(i))
	}

	startTogether(func(worker int) {
		r.reportOutOfRange(pools[worker], testGroupScav, gvk, "child", true)
	})

	if got := log.count("ChildStatusOutOfRange"); got != raceWidth {
		t.Errorf("first round events = %d, want %d: %s", got, raceWidth, log.summary())
	}

	startTogether(func(worker int) {
		r.reportOutOfRange(pools[worker], testGroupScav, gvk, "child", true)
	})

	if got := log.count("ChildStatusOutOfRange"); got != raceWidth {
		t.Errorf("after the second round events = %d, want %d: a gate was lost and the pool re-announced: %s",
			got, raceWidth, log.summary())
	}

	// The gate re-arms on recovery, and that clear is written under the same
	// lock. Racing the clears against a neighbour's re-reports proves the
	// delete and the insert cannot interleave into a gate stuck armed.
	startTogether(func(worker int) {
		r.reportOutOfRange(pools[worker], testGroupScav, gvk, "child", false)
	})

	startTogether(func(worker int) {
		r.reportOutOfRange(pools[worker], testGroupScav, gvk, "child", true)
	})

	if got := log.count("ChildStatusOutOfRange"); got != 2*raceWidth {
		t.Errorf("after re-arming events = %d, want %d: the gate never cleared: %s",
			got, 2*raceWidth, log.summary())
	}
}

// ---------------------------------------------------------------------------
// watchMu: the runtime watch registry
// ---------------------------------------------------------------------------

// This one needs a real informer, so it runs in the envtest tier alongside the
// other ensureWatch specs: the property is informer identity, which no fake
// reproduces.
//
// It is also the one place where workers legitimately converge on a single key.
// Distinct pools whose templates name the *same* workload kind are reconciled
// concurrently -- the ordinary case on a manager start, not an exotic one --
// and all of them find that GVK unrecorded and reach the registration.
//
// Attaching a second handler to one informer is permanent, because nothing
// removes it. Every child event from then on is delivered twice, and the pool
// reconciles twice for every change for the life of the process.
var _ = Describe("ensureWatch under concurrent reconciles", func() {
	gvk := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

	It("should register the watch exactly once when workers race for the same GVK", func() {
		counter := &watchCounter{}
		r := &PodPoolReconciler{
			ctrl:        counter,
			Cache:       reconciler.Cache,
			Scheme:      reconciler.Scheme,
			RESTMapper:  reconciler.RESTMapper,
			Clock:       clock.RealClock{},
			watchStates: make(map[schema.GroupVersionKind]cache.Informer),
		}

		// The error is discarded on purpose. A cache still filling returns
		// errWatchSyncPending, which is a normal startup state and says nothing
		// about how many handlers were attached -- and the registration happens
		// before the sync check, so the count is meaningful either way.
		startTogether(func(int) {
			_ = r.ensureWatch(ctx, gvk)
		})

		Expect(counter.calls.Load()).To(Equal(int32(1)),
			"a duplicate handler cannot be removed: every child event is delivered twice for the life of the process")
		Expect(r.watchStates).To(HaveKey(gvk))
	})
})
