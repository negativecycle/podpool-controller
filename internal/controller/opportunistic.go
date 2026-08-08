package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

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

// maxUnschedulableProbe caps how many Pending pods a single capacity probe
// will page in. A group short by more than this is already telling us
// everything we need to know.
const maxUnschedulableProbe int32 = 256

// opportunisticObservation is one group's child read plus, when it is short of
// target, the scheduler's verdict on the shortfall.
//
// found and foreign are both false only for a group with no child at all, which
// is the cold start. Every other state has to be distinguishable from it,
// because phase 3 answers "never sized" by offering the whole remainder.
type opportunisticObservation struct {
	// found means a child this pool owns was read and the counts below are
	// real. It does not mean "no error": a read that failed is reported as an
	// error instead, never as an observation.
	found bool

	// foreign means an object exists at the child's name under another owner.
	// Distinct from found=false: there is no capacity here to offer, but there
	// is also nothing to grow into, so this group must not be read as new.
	foreign bool

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

// observeOpportunistic reads each opportunistic group's child and, only when
// the group is short of what was asked, the scheduler's verdict on its pods.
func (r *PodPoolReconciler) observeOpportunistic(
	ctx context.Context, pool *podpoolsv1alpha1.PodPool, gvk schema.GroupVersionKind,
) (map[string]opportunisticObservation, error) {
	log := logf.FromContext(ctx)

	var out map[string]opportunisticObservation

	for _, g := range pool.Spec.Groups {
		if !workload.IsOpportunistic(g.Scaling) {
			continue
		}

		// Stop at the first failed read. A partial capacity map is worse than
		// none: phase 3 subtracts what it gives from what later groups receive,
		// so one unreadable child produces wrong targets for groups that were
		// read perfectly well.
		obs, err := r.childCounts(ctx, pool, gvk, g.Name)
		if err != nil {
			return nil, fmt.Errorf("reading group %s: %w", g.Name, err)
		}

		if obs.found && obs.ready < obs.asked {
			n, err := r.countUnschedulable(ctx, pool, g.Name,
				min(obs.asked-obs.ready, maxUnschedulableProbe))
			if err != nil {
				log.Error(err, "Counting unschedulable pods", "group", g.Name)
			} else {
				obs.unschedulable = n
			}
		}

		if out == nil {
			out = make(map[string]opportunisticObservation, len(pool.Spec.Groups))
		}

		out[g.Name] = obs
	}

	return out, nil
}

// countUnschedulable asks how many of a group's pods the scheduler refused.
//
// Namespace, labels and phase are all applied server-side, so the API server
// only sends back pods that already match. The condition check has to be done
// here rather than in the query (status.conditions[].reason is not an indexable
// field and the API server rejects it outright), which is exactly why the three
// filters above matter: they decide how much arrives to loop over.
func (r *PodPoolReconciler) countUnschedulable(
	ctx context.Context, pool *podpoolsv1alpha1.PodPool, groupName string, limit int32,
) (int32, error) {
	if r.APIReader == nil {
		return 0, errors.New("no APIReader configured")
	}

	var pods corev1.PodList

	err := r.APIReader.List(ctx, &pods,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{workload.LabelPool: pool.Name, workload.LabelGroup: groupName},
		client.MatchingFields{"status.phase": string(corev1.PodPending)},
		client.Limit(int64(limit)),
	)
	if err != nil {
		return 0, err
	}

	var n int32

	for i := range pods.Items {
		for _, c := range pods.Items[i].Status.Conditions {
			if c.Type == corev1.PodScheduled &&
				c.Status == corev1.ConditionFalse &&
				c.Reason == corev1.PodReasonUnschedulable {
				n++

				break
			}
		}
	}

	return n, nil
}

// capacityFrom turns observations into the capacity map the distribution uses.
//
// A group with no child yet is left out of the map entirely rather than
// entered as zero. Absence means "never sized" and phase 3 answers it with the
// whole remainder; zero means "sized, and nothing fits". Collapsing the two
// would turn every cold start into a group that never grows.
//
// The other subtlety is the outstanding probe: until it is Ready it must not
// count as capacity, even though it is also not (yet) refused. Counting an
// unjudged probe would raise this group's target and shrink the next group's
// before anything was proven: the speculative burst-pod kill this design
// exists to prevent.
func (r *PodPoolReconciler) capacityFrom(
	pool *podpoolsv1alpha1.PodPool, observed map[string]opportunisticObservation,
) map[string]int32 {
	var out map[string]int32

	for name, obs := range observed {
		if obs.foreign {
			// Someone else's object sits at this child's name. The group will
			// be refused by reconcileWorkload, so it has no capacity, but it
			// must be present in the map: absence would hand it the remainder
			// and shrink every group after it for replicas it cannot place.
			if out == nil {
				out = make(map[string]int32, len(observed))
			}

			out[name] = 0

			continue
		}

		if !obs.found {
			continue // no child yet → absent from the map → cold start
		}

		capacity := obs.asked - obs.unschedulable
		if r.probeOutstanding(pool, name) && obs.ready < obs.asked && obs.unschedulable == 0 {
			capacity = obs.asked - 1
		}

		if capacity < 0 {
			capacity = 0
		}

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

	// obs.found is the guard, not a formality. An observation that was never
	// read is the zero value, and the first case below is then 0 >= 0: the
	// controller would record that the scheduler accepted a replica nobody
	// looked at, and bias the next heartbeat toward growth on the strength of
	// it. Read failures no longer reach here, but a foreign-owned child does.
	if st.outstanding && obs.found {
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

		if !obs.found {
			return probeDecision{target: target}
		}

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
//
// The gate is deliberately against the child's .spec.replicas (what we last
// wrote), never .status.replicas. During a scale-up the ReplicaSet lags, so
// status.replicas still reports the old count while readyReplicas has caught up
// to it; read that way, every scale-up looks like a successful probe and the
// group grows by one replica every reconcile, without end.
//
// The four states it can find are materially different and only one of them,
// genuine absence, is the cold start that phase 3 answers with the whole
// remainder. A read that failed is returned as an error so the caller can
// abandon the pass rather than size the pool from data it does not have; an
// object under another owner is reported as such, because it offers no capacity
// but is not an invitation to grow either.
func (r *PodPoolReconciler) childCounts(
	ctx context.Context, pool *podpoolsv1alpha1.PodPool, gvk schema.GroupVersionKind, groupName string,
) (opportunisticObservation, error) {
	child := &unstructured.Unstructured{}
	child.SetGroupVersionKind(gvk)

	key := types.NamespacedName{
		Name:      workload.ChildName(pool.Name, groupName),
		Namespace: pool.Namespace,
	}

	err := r.Get(ctx, key, child)
	if apierrors.IsNotFound(err) {
		// Absence is the one answer that licenses growth, so confirm it
		// uncached before believing it. This is the read-path counterpart of
		// reconcileWorkload's confirm on the create path.
		uncached := &unstructured.Unstructured{}
		uncached.SetGroupVersionKind(gvk)

		uerr := r.APIReader.Get(ctx, key, uncached)

		switch {
		case apierrors.IsNotFound(uerr):
			return opportunisticObservation{}, nil // genuinely absent: the cold start
		case uerr != nil:
			return opportunisticObservation{}, uerr
		}

		child = uncached
	} else if err != nil {
		return opportunisticObservation{}, err
	}

	if !isControlledBy(child, pool) {
		return opportunisticObservation{foreign: true}, nil
	}

	specReplicas, _ := workload.ReadInt32(child, "spec", "replicas")
	readyReplicas, _ := workload.ReadInt32(child, "status", "readyReplicas")

	return opportunisticObservation{found: true, asked: specReplicas, ready: readyReplicas}, nil
}
