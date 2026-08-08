package controller

import (
	"context"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// defaultOpportunisticHeartbeat is how long a refused probe waits before
// asking again. Free capacity appears without any event this controller
// watches, so the only way to notice it is to look, and looking costs a
// scheduling attempt.
const defaultOpportunisticHeartbeat = 5 * time.Minute

// probeVerdictRequeue is how soon to look again while a probe pod is Pending
// but the scheduler has not yet said Unschedulable. Long enough for a
// scheduling cycle, short enough that a refusal is acted on promptly.
const probeVerdictRequeue = 15 * time.Second

// opportunisticObservation is one group's child read: what we last asked of it
// and what it achieved.
type opportunisticObservation struct {
	// found means a child was read and the counts below are real.
	found bool

	asked         int32 // what we last wrote
	ready         int32 // the child's status.readyReplicas
	unschedulable int32 // pods the scheduler refused; only counted when short
}

// probeState is the controller's memory of one opportunistic group's probe.
//
// It is in memory and nowhere else, which is a decision rather than an
// omission. Writing it to status would put a controller-internal question
// ("am I currently asking?") into an object other actors read and HPAs write
// to, and every restart would then have to reconcile a stored answer against
// a cluster that moved on without it. Forgetting instead costs one early
// probe and the withdrawal of at most one Pending pod, both harmless, and the
// next heartbeat re-derives everything from the cluster.
type probeState struct {
	// outstanding means the child was written target+1 and the scheduler's
	// answer has not been read yet.
	outstanding bool

	// lastFailed is when a probe was last refused. Growth waits out the
	// heartbeat from here; a *successful* probe deliberately does not touch
	// it, which is what makes the walk-up immediate.
	lastFailed time.Time
}

// probeDecision is decideProbe's verdict.
//
// issued and awaitVerdict are distinct, and the distinction is the reason this
// is a struct rather than an (int32, bool). Both an opening probe and every
// pass that re-asserts an outstanding one return target+1, so a bare pair
// cannot tell "I just asked" from "I am still waiting". A caller that wants to
// announce the question exactly once has nothing to key on, and announces it
// once per requeue instead, for as long as the answer takes.
type probeDecision struct {
	target       int32
	issued       bool // a new probe started this pass
	awaitVerdict bool // a probe is outstanding; look again soon
}

// observeOpportunistic reads each opportunistic group's child.
func (r *PodPoolReconciler) observeOpportunistic(
	ctx context.Context, pool *podpoolsv1alpha1.PodPool, gvk schema.GroupVersionKind,
) map[string]opportunisticObservation {
	var out map[string]opportunisticObservation

	for _, g := range pool.Spec.Groups {
		if !workload.IsOpportunistic(g.Scaling) {
			continue
		}

		if out == nil {
			out = make(map[string]opportunisticObservation, len(pool.Spec.Groups))
		}

		out[g.Name] = r.childCounts(ctx, pool, gvk, g.Name)
	}

	return out
}

// capacityFrom turns observations into the capacity map the distribution uses.
//
// A group with no child yet is left out of the map entirely rather than
// entered as zero. Absence means "never sized" and phase 3 answers it with the
// whole remainder; zero means "sized, and nothing fits". Collapsing the two
// would turn every cold start into a group that never grows.
func capacityFrom(observed map[string]opportunisticObservation) map[string]int32 {
	var out map[string]int32

	for name, obs := range observed {
		if !obs.found {
			continue // no child yet → absent from the map → cold start
		}

		capacity := max(obs.asked-obs.unschedulable, 0)

		if out == nil {
			out = make(map[string]int32, len(observed))
		}

		out[name] = capacity
	}

	return out
}

// decideProbe resolves an outstanding probe and decides whether to issue a new
// one, returning the group's final target: the distribution's answer plus at
// most one probe replica, added OUTSIDE the total so no other group pays for
// an unproven question.
func (r *PodPoolReconciler) decideProbe(
	pool *podpoolsv1alpha1.PodPool,
	groupName string,
	target int32,
	obs opportunisticObservation,
	now time.Time,
) probeDecision {
	key := probeKey(pool, groupName)

	r.probeMu.Lock()
	defer r.probeMu.Unlock()

	if r.probes == nil {
		r.probes = make(map[string]probeState)
	}

	st := r.probes[key]

	if st.outstanding {
		switch {
		case obs.ready >= obs.asked:
			st.outstanding = false
		case obs.unschedulable > 0:
			st.outstanding = false
			st.lastFailed = now
		}
	}

	if st.outstanding {
		r.probes[key] = st

		return probeDecision{target: target + 1, awaitVerdict: true}
	}

	// Only a group sitting exactly where it was told to sit may be probed. Ask
	// while it is still moving and the answer is unattributable: a replica that
	// fails to schedule might be the probe, or might be one of the ones already
	// in flight.
	settled := obs.found && obs.asked == target && obs.ready >= obs.asked
	if settled && now.Sub(st.lastFailed) >= defaultOpportunisticHeartbeat {
		st.outstanding = true
		r.probes[key] = st

		return probeDecision{target: target + 1, issued: true, awaitVerdict: true}
	}

	r.probes[key] = st

	return probeDecision{target: target}
}

// probeOutstanding reports whether a probe is awaiting a verdict, without
// deciding anything.
func (r *PodPoolReconciler) probeOutstanding(pool *podpoolsv1alpha1.PodPool, groupName string) bool {
	r.probeMu.Lock()
	defer r.probeMu.Unlock()

	return r.probes[probeKey(pool, groupName)].outstanding
}

// forgetProbes drops a deleted pool's probe records so the map cannot grow
// without bound across pool churn. The out-of-range gate is keyed the same way
// and goes with it, for the same reason.
func (r *PodPoolReconciler) forgetProbes(namespace, name string) {
	prefix := namespace + "/" + name + "/"

	r.probeMu.Lock()

	for k := range r.probes {
		if strings.HasPrefix(k, prefix) {
			delete(r.probes, k)
		}
	}
	r.probeMu.Unlock()

	r.outOfRangeMu.Lock()
	defer r.outOfRangeMu.Unlock()

	for k := range r.outOfRangeEmitted {
		if strings.HasPrefix(k, prefix) {
			delete(r.outOfRangeEmitted, k)
		}
	}
}

func probeKey(pool *podpoolsv1alpha1.PodPool, groupName string) string {
	return pool.Namespace + "/" + pool.Name + "/" + groupName
}

// childCounts reads what we last asked of a group's child and what it achieved.
//
// The comparison between the two is the gate on everything expensive that
// follows: a group standing at its target has nothing to explain, and a group
// short of it does. A converged pool therefore issues no extra reads at all.
func (r *PodPoolReconciler) childCounts(
	ctx context.Context, pool *podpoolsv1alpha1.PodPool, gvk schema.GroupVersionKind, groupName string,
) opportunisticObservation {
	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(gvk)

	key := types.NamespacedName{
		Name:      workload.ChildName(pool.Name, groupName),
		Namespace: pool.Namespace,
	}
	if err := r.Get(ctx, key, child); err != nil {
		return opportunisticObservation{}
	}

	replicas, _ := workload.ReadInt32(child, "status", "replicas")
	ready, _ := workload.ReadInt32(child, "status", "readyReplicas")

	return opportunisticObservation{found: true, asked: replicas, ready: ready}
}
