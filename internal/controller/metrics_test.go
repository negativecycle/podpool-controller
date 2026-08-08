package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// The gauges under test are package-level and shared with the manager running
// in the envtest suite, so every test here uses pool identifiers no fixture
// would produce. A test that borrowed a real pool's namespace and name would
// pass or fail depending on what the envtest manager had reconciled a moment
// earlier.

// poolGauges and groupGauges enumerate what deletePoolMetrics is responsible
// for, by hand, because that is what the delete helper does too. Both lists are
// maintained by remembering, which is the property the next commit removes.
func poolGaugesUnderTest() []*prometheus.GaugeVec {
	return []*prometheus.GaugeVec{specReplicas, statusReplicas, statusReadyReplicas, unplacedReplicas}
}

func groupGaugesUnderTest() []*prometheus.GaugeVec {
	return []*prometheus.GaugeVec{groupReplicas, groupReadyReplicas, groupSharePercent, groupLastProgress}
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
	})

	for _, g := range poolGaugesUnderTest() {
		if got := seriesFor(g, ns, name); got == 0 {
			t.Fatalf("pool gauge: expected at least one series before delete, got 0")
		}
	}

	deletePoolMetrics(ns, name)

	for _, g := range poolGaugesUnderTest() {
		if got := seriesFor(g, ns, name); got != 0 {
			t.Errorf("pool gauge: expected 0 series after delete, got %d", got)
		}
	}

	for _, g := range groupGaugesUnderTest() {
		if got := seriesFor(g, ns, name); got != 0 {
			t.Errorf("group gauge: expected 0 series after delete, got %d", got)
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
	recordPoolMetrics(ns, name, 5, 5, 5, 0, groups)

	for _, g := range groupGaugesUnderTest() {
		if got := seriesFor(g, ns, name); got != 2 {
			t.Fatalf("group gauge: expected 2 series before removal, got %d", got)
		}
	}

	deleteStaleGroupMetrics(ns, name, []string{testGroupBase}, []string{testGroupBase, "removed"})

	for _, g := range groupGaugesUnderTest() {
		if got := seriesFor(g, ns, name); got != 1 {
			t.Errorf("group gauge: expected 1 series after removal, got %d", got)
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
	recordPoolMetrics(ns, name, 10, 10, 9, 0, groups)

	for _, g := range groupGaugesUnderTest() {
		if got := seriesFor(g, ns, name); got != len(groups) {
			t.Fatalf("group gauge: expected %d series before delete, got %d", len(groups), got)
		}
	}

	deletePoolMetrics(ns, name)

	for _, g := range groupGaugesUnderTest() {
		if got := seriesFor(g, ns, name); got != 0 {
			t.Errorf("group gauge: expected 0 series after delete, got %d", got)
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

	for _, g := range groupGaugesUnderTest() {
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

	for _, g := range append(poolGaugesUnderTest(), groupGaugesUnderTest()...) {
		if got := seriesFor(g, pool.Namespace, pool.Name); got != 0 {
			t.Errorf("after the NotFound pass: got %d series, want 0 — a deleted pool's gauges alert forever", got)
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
	live.Spec.Groups = live.Spec.Groups[:1]

	if err := cl.Update(t.Context(), live); err != nil {
		t.Fatalf("dropping a group: %v", err)
	}

	reconcilePool(t, r, live)

	for _, g := range groupGaugesUnderTest() {
		if got := seriesFor(g, pool.Namespace, pool.Name); got != 1 {
			t.Errorf("after dropping a group: got %d series, want 1 — the removed group's gauges are frozen at its last values", got)
		}
	}
}

// A group standing at its target has no last-progress timestamp, and the gauge
// must have no series rather than a series reading zero: zero is a real unix
// timestamp, and "seconds since last progress" against it is 56 years.
func TestLastProgressSeriesIsDeletedAtTarget(t *testing.T) {
	const ns, name = "metrics-unit-lpt", "pool-lpt"

	recordPoolMetrics(ns, name, 3, 3, 3, 0, []groupMetric{
		{name: testGroupBase, replicas: 3, ready: 1, sharePercent: 100, lastProgressTime: 1000},
	})

	if got := seriesFor(groupLastProgress, ns, name); got != 1 {
		t.Fatalf("expected 1 last-progress series while short of target, got %d", got)
	}

	recordPoolMetrics(ns, name, 3, 3, 3, 0, []groupMetric{
		{name: testGroupBase, replicas: 3, ready: 3, sharePercent: 100, lastProgressTime: 0},
	})

	if got := seriesFor(groupLastProgress, ns, name); got != 0 {
		t.Errorf("expected the last-progress series to be removed at target, got %d", got)
	}

	deletePoolMetrics(ns, name)
}
