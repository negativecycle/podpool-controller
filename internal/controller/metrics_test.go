package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The gauges under test are package-level and shared with the manager running
// in the envtest suite, so every test here uses pool identifiers no fixture
// would produce. A test that borrowed a real pool's namespace and name would
// pass or fail depending on what the envtest manager had reconciled a moment
// earlier.

type namedCollector struct {
	name string
	c    prometheus.Collector
}

// allPoolMetrics enumerates every gauge deletePoolMetrics is responsible for by
// reading the same lists deletePoolMetrics reads, not a fourth hand-maintained
// copy. Gauge number ten is covered here the day it joins a list, and a gauge
// that joins neither list fails the registry sweep instead.
var allPoolMetrics = func() []namedCollector {
	seen := map[*prometheus.GaugeVec]bool{}

	var out []namedCollector

	for _, g := range poolExactGauges {
		if !seen[g] {
			out = append(out, namedCollector{name: gaugeFQName(g), c: g})
			seen[g] = true
		}
	}

	for _, g := range poolPartialGauges {
		if !seen[g] {
			out = append(out, namedCollector{name: gaugeFQName(g), c: g})
			seen[g] = true
		}
	}

	return out
}()

func gaugeFQName(g *prometheus.GaugeVec) string {
	ch := make(chan *prometheus.Desc, 1)
	g.Describe(ch)

	return (<-ch).String()
}

func testConditions() []metav1.Condition {
	return []metav1.Condition{
		{Type: ConditionAvailable, Status: metav1.ConditionTrue, Reason: ReasonMinimumReplicasAvailable},
		{Type: ConditionProgressing, Status: metav1.ConditionFalse, Reason: ReasonAllReplicasReady},
		{Type: ConditionTargetDegraded, Status: metav1.ConditionFalse, Reason: ReasonTargetsSatisfied},
		{Type: ConditionGroupsReady, Status: metav1.ConditionTrue, Reason: ReasonAllGroupsReconciled},
		{Type: ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonPoolReady},
	}
}

// seriesFor counts the series in c carrying the given namespace and name
// labels. It must not create series, which rules out vec.WithLabelValues as an
// existence check: asking that way makes the answer true.
func seriesFor(c prometheus.Collector, namespace, name string) int {
	ch := make(chan prometheus.Metric, 64)
	go func() {
		c.Collect(ch)
		close(ch)
	}()

	count := 0

	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			continue
		}

		var gotNamespace, gotName string

		for _, lp := range pb.GetLabel() {
			switch lp.GetName() {
			case labelNamespace:
				gotNamespace = lp.GetValue()
			case labelName:
				gotName = lp.GetValue()
			}
		}

		if gotNamespace == namespace && gotName == name {
			count++
		}
	}

	return count
}

func TestDeletePoolMetricsRemovesAllPoolSeries(t *testing.T) {
	const ns, name = "metrics-unit-all", "pool-all"

	recordPoolMetrics(ns, name, 5, 5, 4, 0, []groupMetric{
		{name: testGroupBase, replicas: 3, ready: 3, sharePercent: 60, lastProgressTime: 1000},
	}, testConditions())

	for _, g := range poolExactGauges {
		if got := seriesFor(g, ns, name); got == 0 {
			t.Fatalf("poolExactGauge: expected at least one series before delete, got 0")
		}
	}

	deletePoolMetrics(ns, name)

	for _, g := range allPoolMetrics {
		if got := seriesFor(g.c, ns, name); got != 0 {
			t.Errorf("%s: expected 0 series after delete, got %d", g.name, got)
		}
	}
}

// A pool that drops a group is still there, so nothing deletes it wholesale.
// The removed group's gauges have to be retired individually or they freeze at
// the last values the group ever reported.
func TestDeleteStaleGroupMetrics(t *testing.T) {
	const ns, name = "metrics-unit-stale", "pool-stale"

	groups := []groupMetric{
		{name: testGroupBase, replicas: 3, ready: 3, sharePercent: 50, lastProgressTime: 1000},
		{name: "removed", replicas: 2, ready: 2, sharePercent: 50, lastProgressTime: 1000},
	}
	recordPoolMetrics(ns, name, 5, 5, 5, 0, groups, testConditions())

	for _, g := range groupGauges {
		if got := seriesFor(g, ns, name); got != 2 {
			t.Fatalf("groupGauge: expected 2 series before removal, got %d", got)
		}
	}

	deleteStaleGroupMetrics(ns, name, []string{testGroupBase}, []string{testGroupBase, "removed"})

	for _, g := range groupGauges {
		if got := seriesFor(g, ns, name); got != 1 {
			t.Errorf("groupGauge: expected 1 series after removal, got %d", got)
		}
	}

	deletePoolMetrics(ns, name)
}

// The group gauges carry a label deletePoolMetrics is not given, so they need
// the partial match rather than the exact one. Getting this wrong leaves one
// series per group of every pool ever deleted.
func TestDeletePoolMetricsRemovesAllGroupSeries(t *testing.T) {
	const ns, name = "metrics-unit-groups", "pool-groups"

	groups := []groupMetric{
		{name: testGroupBase, replicas: 3, ready: 3, sharePercent: 30, lastProgressTime: 1000},
		{name: testGroupScav, replicas: 3, ready: 2, sharePercent: 30, lastProgressTime: 1000},
		{name: testGroupSpot, replicas: 4, ready: 4, sharePercent: 40, lastProgressTime: 1000},
	}
	recordPoolMetrics(ns, name, 10, 10, 9, 0, groups, testConditions())

	for _, g := range groupGauges {
		if got := seriesFor(g, ns, name); got != len(groups) {
			t.Fatalf("groupGauge: expected %d series before delete, got %d", len(groups), got)
		}
	}

	deletePoolMetrics(ns, name)

	for _, g := range groupGauges {
		if got := seriesFor(g, ns, name); got != 0 {
			t.Errorf("groupGauge: expected 0 series after delete, got %d", got)
		}
	}
}

// End to end through Reconcile, which is the half the three tests above cannot
// see: they call the recorder directly, so deleting its call site in Reconcile
// leaves every one of them green. This pins that a reconciled pool has series
// at all, and that the pass which finds the pool gone takes them away.
func TestReconcilePublishesAndRetiresPoolSeries(t *testing.T) {
	pool := fakeTestPool()
	pool.Namespace = "metrics-reconcile"
	pool.Name = "pool-reconcile"

	r, cl := newFakeReconciler(t, nil, pool)

	reconcilePool(t, r, pool)

	if got := seriesFor(specReplicas, pool.Namespace, pool.Name); got != 1 {
		t.Fatalf("after reconcile: got %d spec_replicas series, want 1", got)
	}

	for _, g := range groupGauges {
		if got := seriesFor(g, pool.Namespace, pool.Name); got == 0 {
			t.Errorf("after reconcile: group gauge has no series for a pool with %d groups", len(pool.Spec.Groups))
		}
	}

	if err := cl.Delete(t.Context(), pool); err != nil {
		t.Fatalf("deleting pool: %v", err)
	}

	// The pass that finds the pool gone is the only chance to clean up: no
	// later event will ever mention this name again.
	reconcilePool(t, r, pool)

	for _, g := range allPoolMetrics {
		if got := seriesFor(g.c, pool.Namespace, pool.Name); got != 0 {
			t.Errorf("after the NotFound pass: %s kept %d series, want 0 — a deleted pool's gauges alert forever",
				g.name, got)
		}
	}
}

// The wiring half of deleteStaleGroupMetrics. The helper has its own test
// above, which stays green when Reconcile stops calling it — and a pool that
// drops a group is far more common than a pool that is deleted, so this is the
// leak that actually accumulates.
func TestReconcileRetiresADroppedGroupsSeries(t *testing.T) {
	pool := fakeTestPool()
	pool.Namespace = "metrics-dropgroup"
	pool.Name = "pool-dropgroup"

	r, cl := newFakeReconciler(t, nil, pool)
	t.Cleanup(func() { deletePoolMetrics(pool.Namespace, pool.Name) })

	reconcilePool(t, r, pool)

	if got := seriesFor(groupReplicas, pool.Namespace, pool.Name); got != 2 {
		t.Fatalf("after reconcile: got %d group_replicas series, want 2", got)
	}

	live := getPool(t, cl, pool)
	dropped := live.Spec.Groups[1].Name
	live.Spec.Groups = live.Spec.Groups[:1]

	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("dropping a group: %v", err)
	}

	reconcilePool(t, r, live)

	// Counted from the registry, not from groupGauges: a test that iterates the
	// implementation's own list cannot notice a gauge dropped from it.
	if left := groupSeriesFor(t, pool.Namespace, pool.Name, dropped); len(left) != 0 {
		for family, n := range left {
			t.Errorf("%s: %d series still carry group=%q after it left the spec — "+
				"the removed group's gauges are frozen at its last values", family, n, dropped)
		}
	}

	if kept := groupSeriesFor(t, pool.Namespace, pool.Name, testGroupBase); len(kept) == 0 {
		t.Error("the surviving group lost its series too; the stale sweep deleted more than the difference")
	}
}

// The exits that compute nothing are the ones that matter. A pool whose
// template stops parsing returns before any aggregate exists, so a recording
// call placed at the bottom of Reconcile never runs and the gauges keep
// reporting the pool's last healthy numbers. Alerting keyed on the metric never
// fires; alerting keyed on the object cannot see the metric.
func TestEarlyExitStillPublishesMetrics(t *testing.T) {
	pool := fakeTestPool()
	pool.Namespace = "metrics-earlyexit"
	pool.Name = "pool-earlyexit"

	r, cl := newFakeReconciler(t, nil, pool)
	t.Cleanup(func() { deletePoolMetrics(pool.Namespace, pool.Name) })

	// A healthy pass first, so there are values to go stale.
	reconcilePool(t, r, pool)

	if got := conditionSeries(t, pool.Namespace, pool.Name)[ConditionGroupsReady+"/true"]; got != 1 {
		t.Fatalf("precondition: GroupsReady/true = %v, want 1", got)
	}

	live := getPool(t, cl, pool)
	live.Spec.WorkloadTemplate.Raw = []byte(`{"not":"valid"}`)

	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("breaking the template: %v", err)
	}

	// This pass returns from the GVK check, long before any aggregate is
	// computed.
	reconcilePool(t, r, live)

	series := conditionSeries(t, pool.Namespace, pool.Name)
	if got := series[ConditionGroupsReady+"/false"]; got != 1 {
		t.Errorf("GroupsReady/false = %v after the early exit, want 1 -- the gauges are "+
			"frozen at the pool's last healthy values", got)
	}

	if got := series[ConditionGroupsReady+"/true"]; got != 0 {
		t.Errorf("GroupsReady/true = %v after the early exit, want 0", got)
	}
}

// A group standing at its target has no last-progress timestamp, and the gauge
// must have no series rather than a series reading zero: zero is a real unix
// timestamp, and "seconds since last progress" against it is 56 years.
func TestLastProgressSeriesIsDeletedAtTarget(t *testing.T) {
	const ns, name = "metrics-unit-lpt", "pool-lpt"

	recordPoolMetrics(ns, name, 3, 3, 3, 0, []groupMetric{
		{name: testGroupBase, replicas: 3, ready: 1, sharePercent: 100, lastProgressTime: 1000},
	}, testConditions())

	if got := seriesFor(groupLastProgress, ns, name); got != 1 {
		t.Fatalf("expected 1 last-progress series while short of target, got %d", got)
	}

	recordPoolMetrics(ns, name, 3, 3, 3, 0, []groupMetric{
		{name: testGroupBase, replicas: 3, ready: 3, sharePercent: 100, lastProgressTime: 0},
	}, testConditions())

	if got := seriesFor(groupLastProgress, ns, name); got != 0 {
		t.Errorf("expected the last-progress series to be removed at target, got %d", got)
	}

	deletePoolMetrics(ns, name)
}
