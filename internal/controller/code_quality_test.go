package controller

import (
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// testGroupScavShort keeps the child's name inside the DNS budget in tests that
// build one by hand.
const testGroupScavShort = "scav"

// ---------------------------------------------------------------------------
// The GroupsReady switch: the user-visible message contract
// ---------------------------------------------------------------------------

// setConditions is this codebase's one writer of conditions, which makes its
// switch the whole user-visible contract in one place -- and a refactor that
// touches the switch can break a message without breaking a reason. In the
// history this tutorial is based on, the invalid-template path once passed a
// []string{"*"} sentinel as the failed-group list, and the "*" reached the
// condition message verbatim (#42). These pin the contract so no arm can
// regress to naming a placeholder instead of the problem.

func TestGroupsReadyMessageDoesNotLeakAPlaceholder(t *testing.T) {
	pool := &podpoolsv1alpha1.PodPool{}
	pool.Generation = 1

	// Exactly the call the invalid-template exit makes.
	setConditions(pool, conditionInputs{
		desired:     3,
		poolInvalid: true,
	})

	got := conditionByType(pool, ConditionGroupsReady)
	if got == nil {
		t.Fatal("GroupsReady was not set")
	}

	if got.Status != metav1.ConditionFalse {
		t.Errorf("Status = %s, want False", got.Status)
	}
	// The reason is the stable part of the contract and must not move.
	if got.Reason != ReasonGroupSpecInvalid {
		t.Errorf("Reason = %q, want %q — consumers key on this",
			got.Reason, ReasonGroupSpecInvalid)
	}

	if strings.Contains(got.Message, "*") {
		t.Errorf("message leaks a placeholder to the user: %q", got.Message)
	}

	if !strings.Contains(strings.ToLower(got.Message), "workloadtemplate") {
		t.Errorf("message should name the actual problem, got %q", got.Message)
	}
}

// Real per-group terminal failures must keep reaching their own branch with
// poolInvalid sitting ahead of them in the switch.
func TestGroupsReadyStillReportsRealTerminalGroups(t *testing.T) {
	pool := &podpoolsv1alpha1.PodPool{}
	pool.Generation = 1

	setConditions(pool, conditionInputs{
		desired:        3,
		failedGroups:   []string{"a", "b"},
		terminalGroups: []string{"a", "b"},
	})

	got := conditionByType(pool, ConditionGroupsReady)
	if got == nil {
		t.Fatal("GroupsReady was not set")
	}

	if got.Reason != ReasonGroupSpecInvalid {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonGroupSpecInvalid)
	}

	for _, name := range []string{"a", "b"} {
		if !strings.Contains(got.Message, name) {
			t.Errorf("message should name group %q, got %q", name, got.Message)
		}
	}
}

// The non-terminal branch must stay reachable too: one retryable failure
// among the terminal ones means the pool may still resolve itself.
func TestGroupsReadyReportsPartialFailureSeparately(t *testing.T) {
	pool := &podpoolsv1alpha1.PodPool{}
	pool.Generation = 1

	// Two failed, one terminal — len differs, so this is the retryable branch.
	setConditions(pool, conditionInputs{
		desired:        3,
		failedGroups:   []string{"a", "b"},
		terminalGroups: []string{"a"},
	})

	got := conditionByType(pool, ConditionGroupsReady)
	if got == nil {
		t.Fatal("GroupsReady was not set")
	}

	if got.Reason != ReasonGroupReconcileFailed {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonGroupReconcileFailed)
	}
}

// ---------------------------------------------------------------------------
// #35 — the clock
// ---------------------------------------------------------------------------

// The field has existed since the progress deadline was born: every deadline
// test in this package already swaps in a fake. These two pin the seam itself,
// so the field cannot narrow to a concrete type — the fake and the real clock
// are the two implementations that must keep fitting.

func TestClockFieldExistsAndAcceptsFake(t *testing.T) {
	fake := clocktesting.NewFakePassiveClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	r := &PodPoolReconciler{Clock: fake}

	got := r.Clock.Now()
	if !got.Equal(fake.Now()) {
		t.Errorf("Clock.Now() = %v, want %v", got, fake.Now())
	}
}

func TestClockFieldAcceptsRealClock(t *testing.T) {
	r := &PodPoolReconciler{Clock: clock.RealClock{}}
	if r.Clock == nil {
		t.Fatal("Clock is nil after setting to RealClock")
	}
}

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

// ---------------------------------------------------------------------------
// The message contract, end to end
// ---------------------------------------------------------------------------

// The controller suite runs no webhook, which is the bypass this needs: a pool
// whose workloadTemplate has no apiVersion/kind reaches the controller and
// fails ExtractGVK.
var _ = Describe("GroupsReady message for an uninterpretable pool", func() {
	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-poolinvalid-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name
	})

	It("names the template rather than a placeholder group", func() {
		pool := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-template", Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas: 1,
				// Parses as JSON, carries no apiVersion/kind — ExtractGVK fails.
				WorkloadTemplate: runtime.RawExtension{Raw: []byte(`{"spec":{}}`)},
				Groups:           []podpoolsv1alpha1.GroupSpec{{Name: testGroupBase}},
			},
		}
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())

		var got metav1.Condition

		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx,
				types.NamespacedName{Name: "bad-template", Namespace: ns}, &p)).To(Succeed())
			c := conditionByType(&p, ConditionGroupsReady)
			g.Expect(c).NotTo(BeNil())
			g.Expect(c.Reason).To(Equal(ReasonGroupSpecInvalid))
			got = *c
		}).Should(Succeed())

		Expect(got.Message).NotTo(ContainSubstring("*"),
			"a placeholder reaches the user verbatim")
		Expect(strings.ToLower(got.Message)).To(ContainSubstring("workloadtemplate"),
			"the message should name what is actually wrong")
	})
})
