package controller

import (
	"fmt"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
