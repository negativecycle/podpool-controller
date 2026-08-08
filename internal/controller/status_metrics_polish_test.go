package controller

import (
	"fmt"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// The tests here read the gauges back out of the registry the manager serves,
// which is the only vantage point from which "would a Prometheus scrape see
// this?" is actually answerable.

// ownedChild is a child Deployment this pool controls, with its counts already
// published. The controller ownerReference has to carry the pool's real UID or
// isControlledBy rejects it and the group fails instead of reporting.
//
// Status is set on the object directly: the fake client registers a status
// subresource for PodPool only, so a Deployment's status round-trips through
// WithObjects intact. reconcileWorkload reads the counts off this object before
// it applies, so the apply that follows cannot disturb them.
func ownedChild(pool *podpoolsv1alpha1.PodPool, group string, replicas, ready int32) *appsv1.Deployment {
	labels := map[string]string{workload.LabelPool: pool.Name, workload.LabelGroup: group}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", pool.Name, group),
			Namespace: pool.Namespace,
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: podpoolsv1alpha1.GroupVersion.String(),
				Kind:       workload.KindPodPool,
				Name:       pool.Name,
				UID:        pool.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: testContainer, Image: testImageNginx}}},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      replicas,
			ReadyReplicas: ready,
		},
	}
}

const conditionMetricName = "podpool_status_condition"

// conditionSeries returns the condition gauge's series for one pool, keyed
// "<type>/<status>". Gathering by family name rather than by Go symbol asks the
// question a scrape asks: it is the exported name that consumers write alerting
// rules against, and renaming the Go variable must not silently change it.
func conditionSeries(t *testing.T, namespace, name string) map[string]float64 {
	t.Helper()

	families, err := crmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	out := map[string]float64{}

	for _, fam := range families {
		if fam.GetName() != conditionMetricName {
			continue
		}

		for _, m := range fam.GetMetric() {
			labels := labelMap(m)
			if labels[labelNamespace] != namespace || labels[labelName] != name {
				continue
			}

			out[labels[labelType]+"/"+labels[labelStatus]] = m.GetGauge().GetValue()
		}
	}

	return out
}

func labelMap(m *dto.Metric) map[string]string {
	out := make(map[string]string, len(m.GetLabel()))
	for _, lp := range m.GetLabel() {
		out[lp.GetName()] = lp.GetValue()
	}

	return out
}

// reconcileForMetrics reconciles a pool under identifiers no other fixture
// produces. The gauges are package-level and shared with the manager the
// envtest suite runs, so tests must not collide on labels.
func reconcileForMetrics(t *testing.T, namespace, name string, mutate func(*podpoolsv1alpha1.PodPool)) *podpoolsv1alpha1.PodPool {
	t.Helper()

	pool := fakeTestPool()
	pool.Namespace = namespace

	pool.Name = name
	if mutate != nil {
		mutate(pool)
	}

	r, cl := newFakeReconciler(t, nil, pool)
	reconcilePool(t, r, pool)

	return getPool(t, cl, pool)
}

// The whole point of the gauge: "alert when a pool has been unavailable for ten
// minutes" should be a rule over metrics this controller already publishes, not
// a kube-state-metrics CustomResourceStateMetrics config that mirrors our
// status schema in someone else's repository.
func TestConditionMetricExportsEveryPublishedCondition(t *testing.T) {
	const ns, name = "cond-metric-export", "pool-export"

	t.Cleanup(func() { deletePoolMetrics(ns, name) })

	pool := reconcileForMetrics(t, ns, name, nil)
	series := conditionSeries(t, ns, name)

	if len(series) == 0 {
		t.Fatalf("no %s series for %s/%s; conditions are invisible to Prometheus",
			conditionMetricName, ns, name)
	}

	for _, c := range pool.Status.Conditions {
		key := c.Type + "/" + strings.ToLower(string(c.Status))

		got, ok := series[key]
		if !ok {
			t.Errorf("condition %s=%s has no %s series", c.Type, c.Status, conditionMetricName)

			continue
		}

		if got != 1 {
			t.Errorf("%s{type=%q,status=%q} = %v, want 1 for the current status",
				conditionMetricName, c.Type, c.Status, got)
		}
	}
}

// Why the convention writes a series for every status rather than only the
// current one.
//
// Write only the current status and a True->False flip leaves the True series
// reading 1 forever, so `podpool_status_condition == 1` matches both and every
// alert built on it fires on stale data.
func TestConditionMetricClearsPreviousStatusOnFlip(t *testing.T) {
	const ns, name = "cond-metric-flip", "pool-flip"

	t.Cleanup(func() { deletePoolMetrics(ns, name) })

	// Ready=True: scaled to zero is the cheapest way there.
	reconcileForMetrics(t, ns, name, func(p *podpoolsv1alpha1.PodPool) {
		p.Spec.Replicas = 0
	})

	before := conditionSeries(t, ns, name)
	if before[ConditionReady+"/true"] != 1 {
		t.Fatalf("%s{type=Ready,status=true} = %v, want 1 before the flip",
			conditionMetricName, before[ConditionReady+"/true"])
	}

	// Ready=False: replicas requested, none ready.
	reconcileForMetrics(t, ns, name, func(p *podpoolsv1alpha1.PodPool) {
		p.Spec.Replicas = 3
	})

	after := conditionSeries(t, ns, name)
	if got := after[ConditionReady+"/false"]; got != 1 {
		t.Errorf("%s{type=Ready,status=false} = %v, want 1 after the flip", conditionMetricName, got)
	}

	if got := after[ConditionReady+"/true"]; got != 0 {
		t.Errorf("%s{type=Ready,status=true} = %v, want 0 after the flip — "+
			"a stale 1 makes every `== 1` alert match two contradictory series",
			conditionMetricName, got)
	}
}

// Registry-driven on purpose: this test enumerates nothing itself, so it covers
// gauge number ten the day it is added. Every other test in the package names
// the gauges it checks, one way or another, and would keep passing while a new
// one leaked.
func TestEveryPodpoolSeriesIsRemovedOnPoolDelete(t *testing.T) {
	const ns, name = "registry-sweep-ns", "registry-sweep-pool"

	reconcileForMetrics(t, ns, name, nil)

	before := podpoolSeriesFor(t, ns, name)
	if len(before) == 0 {
		t.Fatal("expected the reconcile to publish at least one podpool_ series")
	}

	t.Logf("populated %d series across %d families", countSeries(before), len(before))

	deletePoolMetrics(ns, name)

	if after := podpoolSeriesFor(t, ns, name); len(after) != 0 {
		for family, n := range after {
			t.Errorf("%s: %d series still carry namespace=%q name=%q after deletePoolMetrics",
				family, n, ns, name)
		}
	}
}

// A condition type that leaves status must lose its series, not merely stop
// being updated. recordConditionMetrics writes every type it *sees*, so a type
// that disappears is simply never visited again and its last values stand.
func TestRetiredConditionMetricIsRemoved(t *testing.T) {
	const ns, name = "cond-metric-retire", "pool-retire"

	t.Cleanup(func() { deletePoolMetrics(ns, name) })

	pool := fakeTestPool()
	pool.Namespace = ns
	pool.Name = name
	meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
		Type:   "AncientCondition",
		Status: metav1.ConditionTrue,
		Reason: "LeftOverFromAnOlderVersion",
	})

	r, cl := newFakeReconciler(t, nil, pool)

	// Pass one: the type is not retired yet, so it is carried and exported
	// like any other condition on the object.
	reconcilePool(t, r, pool)

	if got := conditionSeries(t, ns, name)["AncientCondition/true"]; got != 1 {
		t.Fatalf("%s{type=AncientCondition,status=true} = %v, want 1 before retirement",
			conditionMetricName, got)
	}

	// Pass two: retired. setConditions prunes it from status, and the metric
	// has to follow it out.
	withRetiredType(t, "AncientCondition")
	reconcilePool(t, r, getPool(t, cl, pool))

	for _, status := range conditionStatuses {
		key := "AncientCondition/" + status
		if got, ok := conditionSeries(t, ns, name)[key]; ok {
			t.Errorf("%s{type=AncientCondition,status=%s} = %v still present; a retired condition's "+
				"metric froze instead of going away", conditionMetricName, status, got)
		}
	}
}

// podpoolSeriesFor counts, per metric family, the series carrying this pool's
// namespace and name.
func podpoolSeriesFor(t *testing.T, namespace, name string) map[string]int {
	t.Helper()

	families, err := crmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	out := map[string]int{}

	for _, fam := range families {
		if !strings.HasPrefix(fam.GetName(), "podpool_") {
			continue
		}

		for _, m := range fam.GetMetric() {
			labels := labelMap(m)
			if labels[labelNamespace] == namespace && labels[labelName] == name {
				out[fam.GetName()]++
			}
		}
	}

	return out
}

// groupSeriesFor counts, per metric family, the series carrying this pool's
// namespace, name and the given group.
//
// Deliberately gathered from the registry rather than from groupGauges. A test
// that iterates the same list the code iterates cannot see a gauge dropped from
// that list: the assertion shrinks with the implementation and stays green.
// That is the very failure this milestone's slice discipline is meant to
// prevent, so the check for it has to come from somewhere else.
func groupSeriesFor(t *testing.T, namespace, name, group string) map[string]int {
	t.Helper()

	families, err := crmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	out := map[string]int{}

	for _, fam := range families {
		if !strings.HasPrefix(fam.GetName(), "podpool_group_") {
			continue
		}

		for _, m := range fam.GetMetric() {
			labels := labelMap(m)
			if labels[labelNamespace] == namespace && labels[labelName] == name && labels[labelGroup] == group {
				out[fam.GetName()]++
			}
		}
	}

	return out
}

func countSeries(m map[string]int) int {
	total := 0
	for _, n := range m {
		total += n
	}

	return total
}
