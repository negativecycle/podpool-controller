/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// actionReconcileGroup is the `action` field on every group-scoped event: what
// the controller was doing, as opposed to the reason it is telling you.
const actionReconcileGroup = "ReconcileGroup"

// childObservation is what one pass learned about a group's child workload.
type childObservation struct {
	replicas, ready, updated int32
	created                  bool
	// readyFound means the readyReplicas key was present on the wire. The
	// built-in types omit it when the count is zero, so false means "zero
	// or unpublished"; only elapsed time can separate those two readings.
	readyFound bool
	// outOfRange means at least one count the child published could not be
	// represented and was clamped. The numbers above are safe to use; this
	// says they are not what the child claimed.
	outOfRange bool
	child      *unstructured.Unstructured
}

func observeChild(child *unstructured.Unstructured) childObservation {
	var obs childObservation

	obs.child = child
	obs.readInto(child)

	return obs
}

func (o *childObservation) readInto(child *unstructured.Unstructured) {
	var replicasClamped, readyClamped, updatedClamped bool

	o.replicas, _, replicasClamped = workload.ReadInt32Checked(child, "status", "replicas")
	o.ready, o.readyFound, readyClamped = workload.ReadInt32Checked(child, "status", "readyReplicas")
	o.updated, _, updatedClamped = workload.ReadInt32Checked(child, "status", "updatedReplicas")

	o.outOfRange = replicasClamped || readyClamped || updatedClamped
}

// groupReconcileResult pairs the status row a group publishes with what this
// pass observed of its child.
type groupReconcileResult struct {
	status podpoolsv1alpha1.GroupStatus
	obs    childObservation
}

// PodPoolReconciler reconciles a PodPool object.
type PodPoolReconciler struct {
	client.Client

	Scheme     *runtime.Scheme
	RESTMapper meta.RESTMapper
	Cache      cache.Cache

	// Recorder publishes Events. Events mark transitions; conditions carry
	// state. The two are not alternatives: a condition answers "what is true
	// now?" and is overwritten every pass, while an event answers "what
	// happened, and when?" and is retained for its TTL. An operator running
	// kubectl describe is asking the second question, and until now this
	// controller could only answer the first.
	Recorder events.EventRecorder

	// Clock is injected rather than read from the wall, because deadline
	// behaviour is untestable against time.Now: a test cannot wait ten
	// minutes to see a stall fire.
	Clock clock.PassiveClock

	// APIReader bypasses the cache for reads that must not lag it: before the
	// create path force-applies over an object the cache says is absent, the
	// absence is confirmed against the API server itself.
	APIReader client.Reader

	// Watches are registered lazily, from Reconcile, which runs on several
	// goroutines at once. The mutex guards the map; the controller handle is
	// what watches get attached to after Build.
	// watchStates maps each GVK to the informer instance our event handler
	// was registered on, which is what makes "is this watch still live?"
	// answerable rather than assumed.
	ctrl                controller.Controller
	watchStates         map[schema.GroupVersionKind]cache.Informer
	watchFailureEmitted map[schema.GroupVersionKind]bool
	// watchPendingSince is when each GVK's informer was first seen unsynced,
	// which is the only thing that separates a cache still filling from one
	// that never will.
	watchPendingSince map[schema.GroupVersionKind]time.Time
	// statusMissingEmitted records, per workload GVK, that the child status
	// contract has already been complained about.
	//
	// A second dedup mechanism, deliberately, and the shape of the question is
	// what picks it. The group-event gate compares against a reason this
	// controller persisted in status, so it needs no memory and survives a
	// restart. Nothing about "this kind does not publish readyReplicas" is
	// written anywhere, and it is a property of the *kind* rather than of any
	// pool, so there is nothing to diff against and per-pool dedup would repeat
	// the same complaint once per pool. A process-lifetime map keyed by GVK is
	// the honest answer to a question with no persisted state behind it.
	//
	// The cost is that a restart re-reports once. That is the right side to err
	// on: the alternative is persisting a controller-internal observation into
	// a user-visible object.
	statusMissingEmitted map[schema.GroupVersionKind]bool
	watchMu              sync.Mutex

	// Probe bookkeeping for opportunistic groups. In-memory by design (see
	// probeState) and guarded because Reconcile runs concurrently across
	// pools.
	probes  map[string]probeState
	probeMu sync.Mutex

	// Which groups have already been reported as publishing a count we could
	// not represent. Keyed per group, not per GVK: a child reporting nonsense
	// is a property of that object, not of its kind, so gating on the kind
	// would silence every pool after the first. In-memory, so a restart
	// re-reports once, which is the right side to err on.
	outOfRangeEmitted map[string]bool
	outOfRangeMu      sync.Mutex
}

// +kubebuilder:rbac:groups=podpools.dev,resources=podpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=podpools.dev,resources=podpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=podpools.dev,resources=podpools/finalizers,verbs=update
// Server-side apply against an absent object checks create then patch; both are
// required or the first reconcile of every child fails.
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=create;delete;get;list;patch;watch
// Pods are listed (never watched) for opportunistic sizing: the scheduler's
// verdict on a handful of pods, not a permanent cache.
// +kubebuilder:rbac:groups="",resources=pods,verbs=list

// The recorder writes through events.k8s.io; the legacy core-group rule is
// still needed because the client falls back to it against older servers.
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch;update

// Reconcile moves the cluster toward the pool's desired state. Everything
// starts from a fresh read of the pool: the request carries only a name, and
// the object may have changed, or vanished, since the event that queued it.
func (r *PodPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	var pool podpoolsv1alpha1.PodPool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		// NotFound is not an error: the pool was deleted between the event
		// and this pass, and there is nothing to do. Returning the error
		// instead would requeue a name that will never resolve again.
		if apierrors.IsNotFound(err) {
			deletePoolMetrics(req.Namespace, req.Name)
			r.forgetProbes(req.Namespace, req.Name)

			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	// A terminating pool gets no writes: recreating a child the GC just
	// deleted turns foreground deletion into a fight the GC cannot win once
	// children carry blockOwnerDeletion, and any write to a dying object is
	// noise. Cleanup happens on the NotFound pass once the object is gone.
	if !pool.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Everything below mutates pool.Status in place; this writes it back once,
	// on every exit path, and only when it actually differs from what we read.
	before := pool.DeepCopy()
	defer func() {
		if err := r.patchStatus(ctx, before, &pool); err != nil {
			reterr = kerrors.NewAggregate([]error{reterr, err})
		}
	}()

	// Above every early return, deliberately. The scale subresource's
	// selectorpath points here, so a pool created with an unparseable
	// template would otherwise expose an empty selector to every HPA reading
	// /scale for as long as it stayed in that state. It is derived from the
	// pool name alone, so no early path lacks anything it needs.
	pool.Status.Selector = labels.Set{workload.LabelPool: pool.Name}.String()

	// Asked before any expensive work: a template that is not even
	// addressable (no apiVersion or kind) cannot be rendered for any group.
	// The error is the pool's own, not any group's, and no retry fixes a
	// spec: report it through conditions and stop rather than backing off
	// forever on an error only an edit can resolve.
	gvk, err := workload.ExtractGVK(pool.Spec.WorkloadTemplate.Raw)
	if err != nil {
		setConditions(&pool, conditionInputs{
			desired:     pool.Spec.Replicas,
			poolInvalid: true,
		})

		if conditionTupleChanged(before.Status.Conditions, pool.Status.Conditions, ConditionGroupsReady) {
			r.event(&pool, corev1.EventTypeWarning, ReasonWorkloadTemplateInvalid, "ParseWorkloadTemplate",
				"workloadTemplate has an invalid GVK: %v", err)
		}

		return ctrl.Result{RequeueAfter: requeueAfter(&pool)}, nil
	}

	// Parse once per reconcile; BuildChildWorkload deep-copies per group.
	// Unreachable for malformed JSON: ExtractGVK unmarshals the same bytes
	// above.
	tmpl, err := workload.ParseTemplate(pool.Spec.WorkloadTemplate.Raw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing workload template: %w", err)
	}

	// Registered before any child is written, so the first child of a kind is
	// born observed: a child changing is the event a pool most needs to hear,
	// and the pool watch alone cannot deliver it.
	if res, handled, err := r.setUpWatch(ctx, &pool, gvk); handled {
		return res, err
	}

	// A failed read leaves the pool's capacity unknown, and the targets derived
	// from it are written by SSA in this same pass. Return the error and let the
	// workqueue retry: writing nothing is always recoverable, writing a guess is
	// not.
	observed, err := r.observeOpportunistic(ctx, &pool, gvk)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("observing opportunistic capacity: %w", err)
	}

	result := workload.ComputeGroupTargets(pool.Spec.Replicas, pool.Spec.Groups, r.capacityFrom(&pool, observed))

	now := r.Clock.Now()

	// The probe rides on top of the distribution rather than inside it, so the
	// extra replica is funded by nobody: the pool briefly runs one over
	// spec.replicas and the surplus is the question itself.
	finalTargets, probePending := r.applyProbes(&pool, result.Targets, observed, now)

	// Groups are reconciled independently: one that cannot be built or
	// applied must not stop the others. Failures are collected and returned
	// together at the end, each wrapped with %w so a later classifier can
	// still reach the original error through errors.As.
	var errs []error

	var (
		failedGroups, notOwnedGroups []string
		pendingGroupEvents           []pendingEvent
	)

	// The deep copy taken at the top is the status this pass read, before
	// anything below overwrites it. Failed groups carry their previous row
	// forward from here.
	prevGroups := before.Status.Groups

	reconciledGroups := make(map[string]bool, len(pool.Spec.Groups))
	childByGroup := make(map[string]*unstructured.Unstructured, len(pool.Spec.Groups))
	groupStatuses := make([]podpoolsv1alpha1.GroupStatus, 0, len(pool.Spec.Groups))

	for i, group := range pool.Spec.Groups {
		grResult, err := r.reconcileGroup(ctx, &pool, tmpl, gvk, group, finalTargets[i])
		if err != nil {
			errs = append(errs, fmt.Errorf("group %s: %w", group.Name, err))
			failedGroups = append(failedGroups, group.Name)

			pe := classifyGroupError(group.Name, err)

			// Reading the classifier's verdict back rather than running a
			// second errors.As: one place decides the class, so the condition
			// and the event can never disagree about what happened.
			if pe.reason == ReasonWorkloadNotOwned {
				notOwnedGroups = append(notOwnedGroups, group.Name)
			}

			pendingGroupEvents = append(pendingGroupEvents, pe)

			// Carry the last observed counts and workloadRef forward. Dropping
			// the group would report its replicas as lost while the child is
			// still running them, and losing the workloadRef would blind the
			// stale-kind sweep exactly when a broken replacement makes it
			// matter.
			if previous := findGroupStatus(prevGroups, group.Name); previous != nil {
				carried := *previous
				carried.Ready = false
				carried.Reason = pe.reason
				carried.Message = ""
				groupStatuses = append(groupStatuses, carried)
			} else {
				groupStatuses = append(groupStatuses, podpoolsv1alpha1.GroupStatus{
					Name:           group.Name,
					Ready:          false,
					Reason:         pe.reason,
					TargetReplicas: finalTargets[i],
				})
			}

			continue
		}

		grResult.status.TargetReplicas = finalTargets[i]

		reconciledGroups[group.Name] = true

		if grResult.obs.child != nil {
			childByGroup[group.Name] = grResult.obs.child
		}

		groupStatuses = append(groupStatuses, grResult.status)
	}

	if sweepErrs := r.sweepAllOrphans(ctx, before, &pool, gvk, reconciledGroups); len(sweepErrs) > 0 {
		errs = append(errs, sweepErrs...)
	}

	// Stamp progress timestamps on freshly reconciled groups only. Failed
	// groups keep their previous status, including timestamps, from the
	// carry-forward above: a group whose applies are failing is not
	// progressing, and a generation bump should not restart its clock.
	genChanged := before.Status.ObservedGeneration != pool.Generation

	for i := range groupStatuses {
		gs := &groupStatuses[i]
		if !reconciledGroups[gs.Name] {
			continue
		}

		stampGroupProgress(gs, findGroupStatus(before.Status.Groups, gs.Name), genChanged, now)
	}

	stalledGroups := evaluateStalled(&pool, groupStatuses, now)

	stalledSet := make(map[string]bool, len(stalledGroups))
	for _, name := range stalledGroups {
		stalledSet[name] = true
	}

	assignGroupReasons(groupStatuses, stalledSet, childByGroup)

	// Sum in int64, then narrow once. Every group count is already bounded to
	// int32 individually, but two groups each reporting a large valid count
	// can sum past MaxInt32, and pool.Status.Replicas is this CRD's own scale
	// statusReplicasPath, which the API server rejects when negative. 32
	// groups (the schema cap) at MaxInt32 is 6.9e10, so the wide accumulator
	// provably cannot overflow.
	var sumReplicas, sumReady, sumUpdated int64

	for _, gs := range groupStatuses {
		sumReplicas += int64(gs.Replicas)
		sumReady += int64(gs.ReadyReplicas)
		sumUpdated += int64(gs.UpdatedReplicas)
	}

	totalReplicas := clampInt32(sumReplicas)

	for i := range groupStatuses {
		groupStatuses[i].SharePercent = shareOfTotal(groupStatuses[i].Replicas, totalReplicas)
	}

	pool.Status.Replicas = totalReplicas
	pool.Status.ReadyReplicas = clampInt32(sumReady)
	pool.Status.UpdatedReplicas = clampInt32(sumUpdated)
	pool.Status.UnplacedReplicas = result.Unplaced
	pool.Status.GroupCount = int32(len(pool.Spec.Groups)) //nolint:gosec // spec.groups carries MaxItems=32, so len() fits int32
	pool.Status.Groups = groupStatuses

	// Emit a warning on the transition into stalled, not on every pass. With a
	// requeue floor the pool wakes on its own for as long as it stays wedged,
	// so an ungated warning is an unbounded stream against an object nobody is
	// touching.
	prevProgressing := meta.FindStatusCondition(before.Status.Conditions, ConditionProgressing)

	wasStalled := prevProgressing != nil && prevProgressing.Reason == ReasonProgressDeadlineExceeded
	if len(stalledGroups) > 0 && !wasStalled {
		r.event(&pool, corev1.EventTypeWarning, ReasonProgressDeadlineExceeded, "ProgressDeadline",
			"Group(s) %s exceeded progress deadline", formatGroupNames(stalledGroups))
	}

	setConditions(&pool, conditionInputs{
		targetDegraded: result.TargetDegraded,
		unplaced:       result.Unplaced,
		ready:          pool.Status.ReadyReplicas,
		desired:        pool.Spec.Replicas,
		failedGroups:   failedGroups,
		stalledGroups:  stalledGroups,
		notOwnedGroups: notOwnedGroups,
	})

	// Per group, not per pool. `before` is the deep copy from the top of
	// Reconcile; pool.Status.Groups already holds this pass's reasons and would
	// compare every group against itself.
	for _, pe := range pendingGroupEvents {
		if groupEventChanged(before.Status.Groups, pe.group, pe.reason) {
			r.event(&pool, pe.eventType, pe.reason, pe.action, "%s", pe.note)
		}
	}

	deleteStaleGroupMetrics(pool.Namespace, pool.Name,
		groupNames(groupStatuses), groupNames(before.Status.Groups))
	deleteStaleConditionMetrics(pool.Namespace, pool.Name,
		pool.Status.Conditions, before.Status.Conditions)

	metricGroups := make([]groupMetric, len(groupStatuses))

	for i, gs := range groupStatuses {
		var lpt float64
		if gs.LastProgressTime != nil {
			lpt = float64(gs.LastProgressTime.Unix())
		}

		metricGroups[i] = groupMetric{
			name:             gs.Name,
			replicas:         gs.Replicas,
			ready:            gs.ReadyReplicas,
			sharePercent:     gs.SharePercent,
			lastProgressTime: lpt,
		}
	}

	recordPoolMetrics(pool.Namespace, pool.Name,
		pool.Spec.Replicas,
		pool.Status.Replicas, pool.Status.ReadyReplicas, pool.Status.UnplacedReplicas,
		metricGroups, pool.Status.Conditions)

	if err := kerrors.NewAggregate(errs); err != nil {
		return ctrl.Result{}, err
	}

	// An outstanding probe is the one thing that cannot wait for the ordinary
	// requeue: the pool is holding a replica the scheduler has not ruled on,
	// and every other group is sized as though it does not exist.
	if probePending {
		return ctrl.Result{RequeueAfter: probeVerdictRequeue}, nil
	}

	// A deadline needs something to wake the pool, and a wedged pool is
	// precisely the one that goes silent: ready < desired is byte-identical
	// for a rollout four seconds old and a pool stuck forever, so only a
	// requeue can turn elapsed time into a verdict.
	return ctrl.Result{RequeueAfter: deadlineAwareRequeue(&pool, groupStatuses, now)}, nil
}

// stampGroupProgress applies the progress timestamp rules.
//
// Stamp when: shortfall first appears, shortfall decreases (progress),
// target increases (new work assigned), or generation changes (fresh
// rollout). Clear when shortfall reaches zero. Do NOT stamp when
// shortfall is unchanged or when it grows because ready fell. That is
// a regression, and the deadline should run from the original shortfall.
func stampGroupProgress(gs *podpoolsv1alpha1.GroupStatus, prev *podpoolsv1alpha1.GroupStatus, genChanged bool, now time.Time) {
	shortfall := max(int32(0), gs.TargetReplicas-gs.ReadyReplicas)

	if shortfall == 0 {
		gs.LastProgressTime = nil

		return
	}

	stamp := &metav1.Time{Time: now}

	if prev == nil {
		gs.LastProgressTime = stamp

		return
	}

	prevShortfall := max(int32(0), prev.TargetReplicas-prev.ReadyReplicas)

	switch {
	case genChanged:
		gs.LastProgressTime = stamp
	case prevShortfall == 0:
		// Shortfall first appeared.
		gs.LastProgressTime = stamp
	case shortfall < prevShortfall:
		// Real progress.
		gs.LastProgressTime = stamp
	case gs.TargetReplicas > prev.TargetReplicas:
		// Target rose (capacity shift between groups, not a generation
		// change): new work assigned, fresh deadline.
		gs.LastProgressTime = stamp
	default:
		// Shortfall unchanged or grew because ready fell. Keep the
		// existing timestamp so the deadline runs from the original
		// shortfall, not from the regression.
		gs.LastProgressTime = prev.LastProgressTime
	}
}

// patchStatus writes the pass's status exactly once, and only when it differs
// from what the pass read. Writing unconditionally has two costs the naive
// version paid: an identical write still round-trips to the API server on
// every pass, and the controller's own write event wakes it again, a quiet
// self-sustaining loop. NotFound is tolerated because a pool deleted mid-pass
// has nothing left to report to.
func (r *PodPoolReconciler) patchStatus(ctx context.Context, before, after *podpoolsv1alpha1.PodPool) error {
	if apiequality.Semantic.DeepEqual(before.Status, after.Status) {
		return nil
	}

	err := r.Status().Patch(ctx, after, client.MergeFrom(before))
	if apierrors.IsNotFound(err) {
		return nil
	}

	return err
}

func (r *PodPoolReconciler) reconcileGroup(
	ctx context.Context,
	pool *podpoolsv1alpha1.PodPool,
	tmpl map[string]any,
	gvk schema.GroupVersionKind,
	group podpoolsv1alpha1.GroupSpec,
	target int32,
) (groupReconcileResult, error) {
	desired, err := workload.BuildChildWorkload(tmpl, group, pool, target)
	if err != nil {
		return groupReconcileResult{}, fmt.Errorf("building workload: %w", err)
	}

	obs, err := r.reconcileWorkload(ctx, pool, desired)
	if err != nil {
		return groupReconcileResult{}, fmt.Errorf("reconciling workload: %w", err)
	}

	if obs.created {
		r.event(pool, corev1.EventTypeNormal, "ChildCreated", "CreateWorkload", "Created %s %s", gvk.Kind, desired.GetName())
	}

	// The counts have already been clamped into something the API can store,
	// so the pool is safe. Say so anyway: otherwise the operator sees a group
	// pinned at an odd number with nothing explaining that its child is
	// publishing figures we could not represent.
	r.reportOutOfRange(pool, group.Name, gvk, desired.GetName(), obs.outOfRange)

	// readyReplicas is omitempty on every built-in workload type, so an absent
	// key means "zero, or never published" and the pool cannot tell which. It
	// reports 0 ready either way, which for a kind that simply does not publish
	// readiness is permanently wrong and invisible. Gated on the pool having
	// asked for replicas at all, because a group told to run none has nothing
	// to be ready.
	r.reportStatusMissing(pool, group.Name, gvk, desired.GetName(), obs.readyFound, target)

	return groupReconcileResult{
		status: podpoolsv1alpha1.GroupStatus{
			Name:            group.Name,
			Replicas:        obs.replicas,
			ReadyReplicas:   obs.ready,
			UpdatedReplicas: obs.updated,
			WorkloadRef: &podpoolsv1alpha1.WorkloadReference{
				APIVersion: gvk.GroupVersion().String(),
				Kind:       gvk.Kind,
				Name:       workload.ChildName(pool.Name, group.Name),
			},
		},
		obs: obs,
	}, nil
}

func findGroupStatus(statuses []podpoolsv1alpha1.GroupStatus, name string) *podpoolsv1alpha1.GroupStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return &statuses[i]
		}
	}

	return nil
}

// assignGroupReasons fills Ready/Reason/Message on every group status that
// was reconciled successfully. Failed groups already have their reason from
// the error path.
func assignGroupReasons(
	statuses []podpoolsv1alpha1.GroupStatus,
	stalledGroups map[string]bool,
	childByGroup map[string]*unstructured.Unstructured,
) {
	for i := range statuses {
		gs := &statuses[i]
		if gs.Reason != "" {
			continue
		}

		child := childByGroup[gs.Name]

		var childReason, childMessage string
		if child != nil {
			childReason, childMessage, _ = workload.ChildDetail(child)
		}

		switch {
		case stalledGroups[gs.Name]:
			gs.Ready = false
			gs.Reason = ReasonProgressDeadlineExceeded
			gs.Message = formatChildMessage(child, childReason, childMessage)

		case child != nil && !workload.GenerationCurrent(child):
			gs.Ready = false
			gs.Reason = ReasonWorkloadUpdating
			gs.Message = ""

		case gs.ReadyReplicas < gs.TargetReplicas:
			gs.Ready = false
			gs.Reason = ReasonReplicasUpdating
			gs.Message = formatChildMessage(child, childReason, childMessage)

		case gs.TargetReplicas == 0:
			gs.Ready = true
			gs.Reason = ReasonScaledToZero

		default:
			gs.Ready = true
			gs.Reason = ReasonAllReplicasReady
		}
	}
}

const maxGroupMessageLen = 512

// formatChildMessage projects a child's own explanation into the group's
// message, prefixed with what the child is and bounded in runes rather than
// bytes: truncating mid-rune would put invalid UTF-8 in a status field.
func formatChildMessage(child *unstructured.Unstructured, reason, message string) string {
	if reason == "" && message == "" {
		return ""
	}

	var prefix string
	if child != nil {
		prefix = fmt.Sprintf("%s %s: ", child.GetKind(), child.GetName())
	}

	var detail string

	switch {
	case reason != "" && message != "":
		detail = reason + ": " + message
	case reason != "":
		detail = reason
	default:
		detail = message
	}

	return workload.TruncateRunes(prefix+detail, maxGroupMessageLen)
}

func (r *PodPoolReconciler) reconcileWorkload(
	ctx context.Context,
	pool *podpoolsv1alpha1.PodPool,
	desired *unstructured.Unstructured,
) (childObservation, error) {
	key := types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())

	err := r.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		// The cache may lag the API server, so an unowned object at the same
		// name can read as absent here and be force-applied over. Confirm
		// absence with an uncached read before the first apply; a read
		// failure fails closed, because assuming absence is exactly the bug.
		uncached := &unstructured.Unstructured{}
		uncached.SetGroupVersionKind(desired.GroupVersionKind())

		if uerr := r.APIReader.Get(ctx, key, uncached); uerr == nil {
			if !isControlledBy(uncached, pool) {
				return childObservation{}, &workloadNotOwnedError{
					kind:  uncached.GetKind(),
					name:  uncached.GetName(),
					owner: metav1.GetControllerOf(uncached),
				}
			}

			existing = uncached
		} else if !apierrors.IsNotFound(uerr) {
			return childObservation{}, uerr
		}

		if err := r.applyChild(ctx, desired); err != nil {
			return childObservation{}, err
		}

		if existing.GetName() == "" {
			return childObservation{created: true}, nil
		}

		return observeChild(existing), nil
	}

	if err != nil {
		return childObservation{}, err
	}

	if !isControlledBy(existing, pool) {
		return childObservation{}, &workloadNotOwnedError{
			kind:  existing.GetKind(),
			name:  existing.GetName(),
			owner: metav1.GetControllerOf(existing),
		}
	}

	if err := r.applyChild(ctx, desired); err != nil {
		return childObservation{}, err
	}

	return observeChild(existing), nil
}

// sweepAllOrphans sweeps orphaned children across the current and any stale
// GVKs. For the current GVK an orphan is a child whose group left the spec.
// For a stale GVK (after a kind change in the template), a child is orphaned
// once its replacement has been reconciled: gating on reconciledGroups
// preserves running capacity until the new child exists, so a failed
// replacement never costs the capacity it was meant to replace.
//
// The stale GVKs come from the workloadRef each group's status records, which
// is the only place the old kind survives once the spec has moved on. Status
// starts recording it later in this milestone; until then the stale set is
// empty and only the current-GVK sweep runs.
//
// Both predicates key on the child's name, never on its group label. The name
// is derived from the spec and immutable for the object's lifetime, whereas the
// label is user-writable and read here through a cache that may not yet have
// seen this reconcile repair it. Deciding a delete on the label lets a stale
// read destroy a healthy group; deciding on the name means a stale read can at
// worst defer a real orphan to the next pass.
func (r *PodPoolReconciler) sweepAllOrphans(
	ctx context.Context,
	before *podpoolsv1alpha1.PodPool,
	pool *podpoolsv1alpha1.PodPool,
	gvk schema.GroupVersionKind,
	reconciledGroups map[string]bool,
) []error {
	activeChildren := make(map[string]bool, len(pool.Spec.Groups))
	for _, g := range pool.Spec.Groups {
		activeChildren[workload.ChildName(pool.Name, g.Name)] = true
	}

	reconciledChildren := make(map[string]bool, len(reconciledGroups))
	for g := range reconciledGroups {
		reconciledChildren[workload.ChildName(pool.Name, g)] = true
	}

	var errs []error
	if err := r.sweepOrphans(ctx, r.Client, pool, gvk,
		func(name string) bool { return activeChildren[name] },
		"group %s removed from spec",
	); err != nil {
		errs = append(errs, fmt.Errorf("deleting orphaned workloads: %w", err))
	}

	// A stale-GVK child shares its replacement's name, so activeChildren alone
	// says "keep" for both. The reconciled term is what distinguishes them;
	// without it the entire pre-change workload set leaks.
	for _, staleGVK := range staleWorkloadGVKs(before.Status.Groups, gvk) {
		if err := r.sweepOrphans(ctx, r.APIReader, pool, staleGVK,
			func(name string) bool { return activeChildren[name] && !reconciledChildren[name] },
			"group %s: workload kind changed",
		); err != nil {
			errs = append(errs, fmt.Errorf("deleting stale %s orphans: %w", staleGVK.Kind, err))
		}
	}

	return errs
}

// sweepOrphans deletes children of the given GVK whose name the keep
// predicate rejects. The reader is either the cache (for the current GVK,
// whose informer exists anyway) or r.APIReader (for a stale GVK, to avoid
// constructing a permanent informer for a one-shot cleanup).
//
// Children are found by the controller's own labels, and a labelled workload
// this pool does not control is skipped, never deleted: the labels are the
// search, ownership is the authority.
func (r *PodPoolReconciler) sweepOrphans(
	ctx context.Context,
	reader client.Reader,
	pool *podpoolsv1alpha1.PodPool,
	gvk schema.GroupVersionKind,
	keep func(childName string) bool,
	reasonFmt string,
) error {
	log := logf.FromContext(ctx)

	listGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind + "List",
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(listGVK)

	if err := reader.List(ctx, list,
		client.InNamespace(pool.Namespace),
		client.MatchingLabels{
			workload.LabelPool:      pool.Name,
			workload.LabelManagedBy: workload.ManagerName,
		},
	); err != nil {
		return err
	}

	for i := range list.Items {
		item := &list.Items[i]
		if !isControlledBy(item, pool) {
			log.Info("Skipping labelled workload not controlled by this pool",
				"workload", klog.KObj(item))

			continue
		}

		// The label reports; the name decides.
		if keep(item.GetName()) {
			continue
		}

		fresh, err := r.confirmOrphan(ctx, pool, item, gvk, keep)
		if err != nil {
			return err
		}

		if fresh == nil {
			continue
		}

		groupLabel := fresh.GetLabels()[workload.LabelGroup]

		log.Info("Deleting orphaned workload", "workload", klog.KObj(item),
			"reason", fmt.Sprintf(reasonFmt, groupLabel))

		// Bind the delete to the object we confirmed. Without it, a name freed
		// and reused between the confirm and the delete takes the newcomer.
		// UID and not ResourceVersion: identity is what needs guarding here,
		// and an RV precondition would 409 on any unrelated concurrent write to
		// a legitimately orphaned object.
		uid := fresh.GetUID()

		if err := r.Delete(ctx, item, client.Preconditions{UID: &uid}); err != nil {
			if !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
				return err
			}

			// The object moved under us. Nothing was deleted, so nothing is
			// reported; the next reconcile decides against whatever is there
			// now. Returning this would back the whole pool off for a benign
			// race.
			log.V(4).Info("Orphan changed under the sweep, leaving it to the next pass",
				"workload", klog.KObj(item), "err", err)

			continue
		}

		// A deletion is the one thing here that cannot be undone by the next
		// pass, so it gets both a log line and an event: the log for whoever is
		// reading the manager's output, the event for whoever is looking at the
		// pool and wondering where their workload went.
		r.event(pool, corev1.EventTypeNormal, "OrphanDeleted", "DeleteWorkload",
			"Deleted orphaned %s %s ("+reasonFmt+")", gvk.Kind, item.GetName(), groupLabel)
	}

	return nil
}

// confirmOrphan re-reads a delete candidate straight from the API server and
// re-runs both tests against the result. A nil object means leave it alone:
// already gone, no longer ours, or no longer an orphan.
//
// Keying the sweep on the name settles the keep predicate, but the ownership
// check still ran on a cached copy. A user who adopts a genuinely orphaned
// child by removing our controller reference would have their object deleted
// off a cache that still showed it. The create path already confirms absence
// with an uncached read before its first apply; this is the counterpart the
// delete path never had.
//
// It costs nothing in steady state: the read happens only for objects already
// selected for deletion, which is empty unless a group was actually removed.
func (r *PodPoolReconciler) confirmOrphan(
	ctx context.Context,
	pool *podpoolsv1alpha1.PodPool,
	item *unstructured.Unstructured,
	gvk schema.GroupVersionKind,
	keep func(childName string) bool,
) (*unstructured.Unstructured, error) {
	fresh := &unstructured.Unstructured{}
	fresh.SetGroupVersionKind(gvk)

	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(item), fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	if !isControlledBy(fresh, pool) || keep(fresh.GetName()) {
		return nil, nil
	}

	return fresh, nil
}

// staleWorkloadGVKs returns the distinct GVKs recorded in the previous
// status that differ from the current template GVK. After a template kind
// change, these are the kinds whose children the sweep must also visit.
func staleWorkloadGVKs(prev []podpoolsv1alpha1.GroupStatus, current schema.GroupVersionKind) []schema.GroupVersionKind {
	seen := make(map[schema.GroupVersionKind]bool)

	var result []schema.GroupVersionKind

	for _, gs := range prev {
		if gs.WorkloadRef == nil {
			continue
		}

		gv, err := schema.ParseGroupVersion(gs.WorkloadRef.APIVersion)
		if err != nil {
			continue
		}

		stale := schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: gs.WorkloadRef.Kind}
		if stale == current || seen[stale] {
			continue
		}

		seen[stale] = true
		result = append(result, stale)
	}

	return result
}

type pendingEvent struct {
	// group names the group this event belongs to, so the flush can ask that
	// group's own history whether the event is news. A plain string rather than
	// a pointer into pool.Spec.Groups: the struct stays comparable and nothing
	// retains a reference to a slice element the loop reuses.
	group                           string
	eventType, reason, action, note string
}

// groupEventChanged reports whether a group's failure class differs from what
// it last published.
//
// Group events are gated per group rather than on the pool-level GroupsReady
// tuple. That tuple summarises every group at once, so a group that changes
// failure class while the summary stays put is silenced: a group already
// failing retryably that starts hitting an ownership conflict keeps the same
// status, the same reason and the same failing-group list, and the refusal to
// touch another controller's object is never announced. Deriving a per-item
// signal from an aggregate is the general shape of that bug.
//
// The comparison is against the reason already persisted in
// status.groups[].reason, so nothing new has to be tracked and the gate
// survives a manager restart. It must be the reason and not the message: the
// per-group message is cleared on the failure path and the event note carries
// raw error text, which varies between passes for one underlying condition.
// Comparing either would reintroduce the spam the gate exists to prevent.
//
// The anti-spam property is preserved. An unchanging failure finds prev.Reason
// equal to this pass's reason on every subsequent pass and emits once.
//
// prev must come from `before`, the deep copy taken at the top of Reconcile.
// Passing this pass's own status compares each reason against itself, silences
// every group event, and does so in a way no "emits exactly one" test would
// catch, because zero also satisfies "not more than one".
func groupEventChanged(before []podpoolsv1alpha1.GroupStatus, groupName, reason string) bool {
	prev := findGroupStatus(before, groupName)

	return prev == nil || prev.Reason != reason
}

// conditionTupleChanged reports whether the (status, reason, message) of a
// named condition changed between the previous and current status. A nil
// previous condition is treated as changed: the first-ever reconcile of a
// broken pool emits.
//
// This governs the pool-level events, which for now is the bad-GVK warning:
// the pool's own template being unreadable really is a property of the pool as
// a whole. Group events used to be gated on it too and are not any more,
// because one pool-level signal cannot answer a question about one group.
//
// Reading a whole tuple rather than diffing group names couples this to message
// determinism: if messages are later truncated to a column budget, gating
// degrades toward fewer emissions and never toward spam. That bias is
// deliberate, and it is also why the group gate now reads only reasons, which
// are compile-time constants and cannot be truncated at all.
func conditionTupleChanged(prev []metav1.Condition, cur []metav1.Condition, condType string) bool {
	p := meta.FindStatusCondition(prev, condType)

	c := meta.FindStatusCondition(cur, condType)
	if p == nil || c == nil {
		return p != c
	}

	return p.Status != c.Status || p.Reason != c.Reason || p.Message != c.Message
}

// classifyGroupError decides what a failed group's failure *is*, once, so the
// condition reason and the event can never disagree about it.
func classifyGroupError(groupName string, err error) pendingEvent {
	var notOwned *workloadNotOwnedError
	if errors.As(err, &notOwned) {
		return pendingEvent{
			groupName,
			corev1.EventTypeWarning, ReasonWorkloadNotOwned, actionReconcileGroup,
			fmt.Sprintf("Refusing to manage group %s: %v", groupName, err),
		}
	}

	return pendingEvent{
		groupName,
		corev1.EventTypeWarning, ReasonGroupReconcileFailed, actionReconcileGroup,
		fmt.Sprintf("Failed to reconcile group %s: %v", groupName, err),
	}
}

// event is the one place that touches the Recorder, so a nil one (every
// fake-client test, and any construction that forgets to wire it) degrades to
// silence rather than a panic in the middle of a reconcile.
func (r *PodPoolReconciler) event(pool *podpoolsv1alpha1.PodPool, eventType, reason, action, noteFmt string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(pool, nil, eventType, reason, action, noteFmt, args...)
	}
}

// reportStatusMissing emits one Warning per workload kind per process when a
// child publishes no status.readyReplicas.
//
// Per GVK and not per pool: whether a kind publishes readiness is a fact about
// the kind, so the second pool running the same CRD adds no information. That
// is the opposite call from reportOutOfRange, where a child reporting nonsense
// is a fact about that one object and gating per kind would silence every pool
// after the first. The two maps look alike and are keyed differently on
// purpose.
func (r *PodPoolReconciler) reportStatusMissing(
	pool *podpoolsv1alpha1.PodPool,
	groupName string,
	gvk schema.GroupVersionKind,
	childName string,
	readyFound bool,
	target int32,
) {
	if readyFound || target <= 0 {
		return
	}

	r.watchMu.Lock()

	emitted := r.statusMissingEmitted[gvk]
	if !emitted {
		if r.statusMissingEmitted == nil {
			r.statusMissingEmitted = make(map[schema.GroupVersionKind]bool)
		}

		r.statusMissingEmitted[gvk] = true
	}

	r.watchMu.Unlock()

	if !emitted {
		r.event(pool, corev1.EventTypeWarning, "StatusMissing", "ChildStatus",
			"%s %s does not publish status.readyReplicas; the pool cannot track readiness for group %s",
			gvk.Kind, childName, groupName)
	}
}

// reportOutOfRange emits one Warning the first time a group's child publishes
// a count that had to be clamped, and re-arms once the child recovers. The
// stored values are already clamped and safe; this exists so the clamp does not
// launder the corruption into silence.
//
// Gating on the transition rather than on every pass matters here more than
// most: a child stuck publishing a bad number is reconciled on every heartbeat
// and every child event, so an ungated warning is an unbounded event stream
// against a pool that is otherwise working.
func (r *PodPoolReconciler) reportOutOfRange(
	pool *podpoolsv1alpha1.PodPool,
	groupName string,
	gvk schema.GroupVersionKind,
	childName string,
	outOfRange bool,
) {
	key := probeKey(pool, groupName)

	// The lock covers the map and nothing else. Emitting under it would hold a
	// process-wide mutex across a broadcast to every event sink, which is the
	// kind of lock scope that only hurts when the cluster is already unwell.
	r.outOfRangeMu.Lock()

	already := r.outOfRangeEmitted[key]

	switch {
	case !outOfRange:
		delete(r.outOfRangeEmitted, key)
	case !already:
		if r.outOfRangeEmitted == nil {
			r.outOfRangeEmitted = make(map[string]bool)
		}

		r.outOfRangeEmitted[key] = true
	}

	r.outOfRangeMu.Unlock()

	if outOfRange && !already {
		r.event(pool, corev1.EventTypeWarning, "ChildStatusOutOfRange", "ChildStatus",
			"%s %s published a replica count outside the representable range; "+
				"group %s is using a clamped value and its reported counts are not the child's",
			gvk.Kind, childName, groupName)
	}
}

// setUpWatch establishes the child watch and turns its outcome into an exit.
// handled is false only when the watch is ready and Reconcile should carry on.
//
// Separate from ensureWatch because the two answer different questions: that
// one reports what the informer is doing, this one decides what the pool
// should do about it.
func (r *PodPoolReconciler) setUpWatch(
	ctx context.Context, pool *podpoolsv1alpha1.PodPool, gvk schema.GroupVersionKind,
) (ctrl.Result, bool, error) {
	err := r.ensureWatch(ctx, gvk)
	if err == nil {
		return ctrl.Result{}, false, nil
	}

	// A cache still filling is not a fault. No warning and no error: come back
	// when it is warm. Returning the error would put the pool into exponential
	// backoff waiting for something that takes milliseconds, and complain
	// about it on every manager start.
	if errors.Is(err, errWatchSyncPending) {
		logf.FromContext(ctx).V(4).Info("Informer still syncing, retrying shortly", "gvk", gvk)

		return ctrl.Result{RequeueAfter: watchSyncRequeue}, true, nil
	}

	// Without this the pool keeps reporting whatever the last pass wrote,
	// which for a pool that was healthy until its CRD was uninstalled is
	// Ready. Nothing in the object mentions the watch, and the only other
	// signal is deduped to one line per GVK per process.
	setConditions(pool, conditionInputs{watchFailed: true})
	r.handleWatchFailure(pool, gvk, err)

	return ctrl.Result{}, true, fmt.Errorf("setting up watch for %s: %w", gvk, err)
}

// handleWatchFailure reports a watch that will not come up, once per GVK per
// process. Deduped because the pool retries on backoff for as long as the
// kind stays unservable, and a line per retry buries the one that mattered.
// The record is cleared when the informer finally syncs, so a kind that
// breaks again later is reported again.
func (r *PodPoolReconciler) handleWatchFailure(pool *podpoolsv1alpha1.PodPool, gvk schema.GroupVersionKind, err error) {
	r.watchMu.Lock()
	r.initWatchMapsLocked()

	emitted := r.watchFailureEmitted[gvk]
	if !emitted {
		r.watchFailureEmitted[gvk] = true
	}
	r.watchMu.Unlock()

	if !emitted {
		r.event(pool, corev1.EventTypeWarning, ReasonWatchSetupFailed, "SetupWatch",
			"Failed to set up watch for %s: %v", gvk, err)
	}
}

// applyProbes layers the +1 probe on top of the distribution for any
// opportunistic groups that are due a heartbeat.
func (r *PodPoolReconciler) applyProbes(
	pool *podpoolsv1alpha1.PodPool,
	targets []int32,
	observed map[string]opportunisticObservation,
	now time.Time,
) ([]int32, bool) {
	finalTargets := make([]int32, len(targets))
	copy(finalTargets, targets)

	var probePending bool

	for i, group := range pool.Spec.Groups {
		if !workload.IsOpportunistic(group.Scaling) {
			continue
		}

		d := r.decideProbe(pool, group.Name, targets[i], observed[group.Name], now) //nolint:gosec // targets is len(pool.Spec.Groups)
		if d.issued {
			r.event(pool, corev1.EventTypeNormal, "CapacityProbe", "ProbeCapacity",
				"Probing group %s for one replica beyond its observed capacity", group.Name)
		}

		if d.abandoned {
			r.event(pool, corev1.EventTypeWarning, "CapacityProbeTimeout", "ProbeCapacity",
				"Probe for group %s got no scheduler verdict within %s; treating it as refused",
				group.Name, probeVerdictTimeout)
		}

		finalTargets[i] = d.target
		probePending = probePending || d.awaitVerdict
	}

	return finalTargets, probePending
}

// applyChild writes the rendered child with server-side apply.
//
// ForceOwnership is deliberate. A conflict means another manager has taken a
// field the pool renders, and the pool is the authority on those.
func (r *PodPoolReconciler) applyChild(ctx context.Context, desired *unstructured.Unstructured) error {
	desired.SetResourceVersion("")

	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(desired),
		client.FieldOwner(workload.ManagerName),
		client.ForceOwnership,
	)
}
