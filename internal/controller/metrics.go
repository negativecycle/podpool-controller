package controller

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// Label names are constants because they are used three times each: to declare
// a gauge's label set, to write a sample, and to delete a series. A typo in the
// third one is silent — DeletePartialMatch on a label nobody has matches
// nothing and returns no error.
const (
	labelNamespace = "namespace"
	labelName      = "name"
	labelGroup     = "group"
	labelType      = "type"
	labelStatus    = "status"
)

// Cardinality is the whole design constraint. Every series is keyed by
// (namespace, name) and optionally group, all three of which are bounded by the
// API: names are DNS labels, and spec.groups carries MaxItems=32. Nothing here
// is labelled with anything a user can vary freely — no reason strings, no
// error text, no child names — because a metric label is a permanent commitment
// to a time series per distinct value.
var (
	poolLabels  = []string{labelNamespace, labelName}
	groupLabels = []string{labelNamespace, labelName, labelGroup}
	condLabels  = []string{labelNamespace, labelName, labelType, labelStatus}

	specReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_spec_replicas",
		Help: "Desired number of replicas for a PodPool.",
	}, poolLabels)

	statusReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_status_replicas",
		Help: "Current number of replicas reported by child workloads.",
	}, poolLabels)

	statusReadyReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_status_ready_replicas",
		Help: "Current number of ready replicas reported by child workloads.",
	}, poolLabels)

	groupReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_group_replicas",
		Help: "Current number of replicas for a specific group.",
	}, groupLabels)

	groupReadyReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_group_ready_replicas",
		Help: "Current number of ready replicas for a specific group.",
	}, groupLabels)

	groupSharePercent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_group_share_percent",
		Help: "Percentage of observed replicas in a specific group. Integer truncation means the sum across groups is at most 100 and may be short by up to len(groups)-1.",
	}, groupLabels)

	groupLastProgress = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_group_last_progress_timestamp_seconds",
		Help: "Unix timestamp of the last time this group made progress toward its target.",
	}, groupLabels)

	unplacedReplicas = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_unplaced_replicas",
		Help: "Replicas no group could accept without exceeding a max or target ceiling.",
	}, poolLabels)

	statusCondition = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "podpool_status_condition",
		Help: "Current status of a PodPool condition. 1 for the active status, 0 for the others. One series per (type, status) pair; all three statuses are written every pass to prevent stale series.",
	}, condLabels)
)

// Registering with controller-runtime's registry rather than a new one puts
// these on the same /metrics endpoint the manager already serves, beside the
// workqueue and client-go series an operator reads them next to.
func init() {
	metrics.Registry.MustRegister(
		specReplicas,
		statusReplicas,
		statusReadyReplicas,
		groupReplicas,
		groupReadyReplicas,
		groupSharePercent,
		groupLastProgress,
		unplacedReplicas,
		statusCondition,
	)
}

// conditionStatuses is the closed set metav1.ConditionStatus can take, lowered
// for use as a label value. Every one of them is written on every pass; see
// recordConditionMetrics.
var conditionStatuses = []string{"true", "false", "unknown"}

// groupMetric is one group's row, projected out of its GroupStatus. The gauges
// take primitives rather than the API type so that recording can be tested
// without building a pool.
type groupMetric struct {
	name             string
	replicas         int32
	ready            int32
	sharePercent     int32
	lastProgressTime float64 // unix seconds; 0 means at target
}

func recordPoolMetrics(
	namespace, name string,
	spec, replicas, ready, unplaced int32,
	groups []groupMetric,
	conditions []metav1.Condition,
) {
	specReplicas.WithLabelValues(namespace, name).Set(float64(spec))
	statusReplicas.WithLabelValues(namespace, name).Set(float64(replicas))
	statusReadyReplicas.WithLabelValues(namespace, name).Set(float64(ready))
	unplacedReplicas.WithLabelValues(namespace, name).Set(float64(unplaced))

	for _, g := range groups {
		groupReplicas.WithLabelValues(namespace, name, g.name).Set(float64(g.replicas))
		groupReadyReplicas.WithLabelValues(namespace, name, g.name).Set(float64(g.ready))
		groupSharePercent.WithLabelValues(namespace, name, g.name).Set(float64(g.sharePercent))

		// Deleted rather than zeroed when the group is at target. Zero is a
		// real unix timestamp (1970), so a dashboard plotting "time since last
		// progress" against it reads 56 years of stall on a healthy group.
		if g.lastProgressTime > 0 {
			groupLastProgress.WithLabelValues(namespace, name, g.name).Set(g.lastProgressTime)
		} else {
			groupLastProgress.DeleteLabelValues(namespace, name, g.name)
		}
	}

	recordConditionMetrics(namespace, name, conditions)
}

// recordConditionMetrics exports the conditions as a gauge per (type, status)
// pair, 1 for the status the condition currently holds and 0 for the others.
//
// A condition is already a time series; the only question is who turns it into
// one. Leaving it in status means every consumer that wants "unavailable for
// ten minutes" writes a kube-state-metrics CustomResourceStateMetrics config
// against this CRD's status shape, which is a copy of our schema in someone
// else's repository, kept up to date by nobody.
//
// Writing all three statuses rather than only the current one is the part that
// is easy to get wrong. Write only the active one and a True->False flip leaves
// the True series reading 1 for the lifetime of the process, so the obvious
// alerting expression `podpool_status_condition == 1` matches two contradictory
// series and fires on the stale one.
func recordConditionMetrics(namespace, name string, conditions []metav1.Condition) {
	for _, c := range conditions {
		current := strings.ToLower(string(c.Status))

		for _, s := range conditionStatuses {
			v := float64(0)
			if s == current {
				v = 1
			}

			statusCondition.WithLabelValues(namespace, name, c.Type, s).Set(v)
		}
	}
}

// deletePoolMetrics drops every series belonging to a pool that no longer
// exists. Without it the gauges keep reporting a deleted pool's last known
// values for the lifetime of the process, and an alert keyed on
// podpool_status_ready_replicas < podpool_spec_replicas fires forever against
// an object nobody can fix.
func deletePoolMetrics(namespace, name string) {
	specReplicas.DeleteLabelValues(namespace, name)
	statusReplicas.DeleteLabelValues(namespace, name)
	statusReadyReplicas.DeleteLabelValues(namespace, name)
	unplacedReplicas.DeleteLabelValues(namespace, name)

	// The group gauges carry a third label, so DeleteLabelValues cannot reach
	// them without knowing every group name the pool ever had — which is
	// exactly what a deleted object no longer tells us.
	poolMatch := prometheus.Labels{labelNamespace: namespace, labelName: name}
	groupReplicas.DeletePartialMatch(poolMatch)
	groupReadyReplicas.DeletePartialMatch(poolMatch)
	groupSharePercent.DeletePartialMatch(poolMatch)
	groupLastProgress.DeletePartialMatch(poolMatch)

	// Two extra labels, so it needs the partial match too, for the same reason
	// the group gauges do: nothing here knows which condition types the pool
	// was carrying when it went away.
	statusCondition.DeletePartialMatch(poolMatch)
}

// deleteStaleGroupMetrics retires the series of groups that have left the spec.
//
// A pool that drops a group keeps existing, so deletePoolMetrics never runs and
// the removed group's gauges freeze at their last values. A set that shrinks
// needs the difference deleting, not merely skipping.
func deleteStaleGroupMetrics(namespace, name string, current, previous []string) {
	active := make(map[string]bool, len(current))
	for _, n := range current {
		active[n] = true
	}

	for _, n := range previous {
		if !active[n] {
			groupReplicas.DeleteLabelValues(namespace, name, n)
			groupReadyReplicas.DeleteLabelValues(namespace, name, n)
			groupSharePercent.DeleteLabelValues(namespace, name, n)
			groupLastProgress.DeleteLabelValues(namespace, name, n)
		}
	}
}

func groupNames(groups []podpoolsv1alpha1.GroupStatus) []string {
	out := make([]string, len(groups))
	for i := range groups {
		out[i] = groups[i].Name
	}

	return out
}
