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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// childObservation is what one pass learned about a group's child workload.
type childObservation struct {
	replicas, ready, updated int32
	created                  bool
	// readyFound means the readyReplicas key was present on the wire. The
	// built-in types omit it when the count is zero, so false means "zero
	// or unpublished"; only elapsed time can separate those two readings.
	readyFound bool
	child      *unstructured.Unstructured
}

func observeChild(child *unstructured.Unstructured) childObservation {
	var obs childObservation

	obs.child = child
	obs.replicas, _ = workload.ReadInt32(child, "status", "replicas")
	obs.ready, obs.readyFound = workload.ReadInt32(child, "status", "readyReplicas")
	obs.updated, _ = workload.ReadInt32(child, "status", "updatedReplicas")

	return obs
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

	Scheme *runtime.Scheme

	// APIReader bypasses the cache for reads that must not lag it: before the
	// create path force-applies over an object the cache says is absent, the
	// absence is confirmed against the API server itself.
	APIReader client.Reader
}

// +kubebuilder:rbac:groups=podpools.dev,resources=podpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=podpools.dev,resources=podpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=podpools.dev,resources=podpools/finalizers,verbs=update
// Server-side apply against an absent object checks create then patch; both are
// required or the first reconcile of every child fails.
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;delete;get;list;patch;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=create;delete;get;list;patch;watch

// Reconcile moves the cluster toward the pool's desired state. Everything
// starts from a fresh read of the pool: the request carries only a name, and
// the object may have changed, or vanished, since the event that queued it.
func (r *PodPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var pool podpoolsv1alpha1.PodPool
	if err := r.Get(ctx, req.NamespacedName, &pool); err != nil {
		// NotFound is not an error: the pool was deleted between the event
		// and this pass, and there is nothing to do. Returning the error
		// instead would requeue a name that will never resolve again.
		if apierrors.IsNotFound(err) {
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

		if err := r.Status().Patch(ctx, &pool, client.Merge); err != nil {
			return ctrl.Result{}, fmt.Errorf("writing status: %w", err)
		}

		return ctrl.Result{}, nil
	}

	// Parse once per reconcile; BuildChildWorkload deep-copies per group.
	// Unreachable for malformed JSON: ExtractGVK unmarshals the same bytes
	// above.
	tmpl, err := workload.ParseTemplate(pool.Spec.WorkloadTemplate.Raw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing workload template: %w", err)
	}

	result := workload.ComputeGroupTargets(pool.Spec.Replicas, pool.Spec.Groups)

	// Groups are reconciled independently: one that cannot be built or
	// applied must not stop the others. Failures are collected and returned
	// together at the end, each wrapped with %w so a later classifier can
	// still reach the original error through errors.As.
	var errs []error

	var failedGroups, notOwnedGroups []string

	// The status this pass read, before anything below overwrites it. Failed
	// groups carry their previous row forward from here.
	prevGroups := pool.Status.Groups

	reconciledGroups := make(map[string]bool, len(pool.Spec.Groups))
	childByGroup := make(map[string]*unstructured.Unstructured, len(pool.Spec.Groups))
	groupStatuses := make([]podpoolsv1alpha1.GroupStatus, 0, len(pool.Spec.Groups))

	for i, group := range pool.Spec.Groups {
		grResult, err := r.reconcileGroup(ctx, &pool, tmpl, gvk, group, result.Targets[i])
		if err != nil {
			errs = append(errs, fmt.Errorf("group %s: %w", group.Name, err))
			failedGroups = append(failedGroups, group.Name)

			reason := ReasonGroupReconcileFailed

			var notOwned *workloadNotOwnedError
			if errors.As(err, &notOwned) {
				notOwnedGroups = append(notOwnedGroups, group.Name)
				reason = ReasonWorkloadNotOwned
			}

			// Carry the last observed counts and workloadRef forward. Dropping
			// the group would report its replicas as lost while the child is
			// still running them, and losing the workloadRef would blind the
			// stale-kind sweep exactly when a broken replacement makes it
			// matter.
			if previous := findGroupStatus(prevGroups, group.Name); previous != nil {
				carried := *previous
				carried.Ready = false
				carried.Reason = reason
				carried.Message = ""
				groupStatuses = append(groupStatuses, carried)
			} else {
				groupStatuses = append(groupStatuses, podpoolsv1alpha1.GroupStatus{
					Name:           group.Name,
					Ready:          false,
					Reason:         reason,
					TargetReplicas: result.Targets[i],
				})
			}

			continue
		}

		grResult.status.TargetReplicas = result.Targets[i]

		reconciledGroups[group.Name] = true

		if grResult.obs.child != nil {
			childByGroup[group.Name] = grResult.obs.child
		}

		groupStatuses = append(groupStatuses, grResult.status)
	}

	if sweepErrs := r.sweepAllOrphans(ctx, &pool, gvk, reconciledGroups); len(sweepErrs) > 0 {
		errs = append(errs, sweepErrs...)
	}

	assignGroupReasons(groupStatuses, nil, childByGroup)

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

	setConditions(&pool, conditionInputs{
		targetDegraded: result.TargetDegraded,
		unplaced:       result.Unplaced,
		ready:          pool.Status.ReadyReplicas,
		desired:        pool.Spec.Replicas,
		failedGroups:   failedGroups,
		notOwnedGroups: notOwnedGroups,
	})

	// A merge patch rather than an update: an update carries the object's
	// resourceVersion and conflicts whenever anything else touched the pool
	// mid-pass, including this controller's own previous write, and every
	// conflict is a full retry of the pass. Status is this controller's to
	// state, so last-writer-wins is correct here. The remaining sharp edges
	// of this write are a dedicated commit later in the milestone.
	if err := r.Status().Patch(ctx, &pool, client.Merge); err != nil {
		errs = append(errs, fmt.Errorf("writing status: %w", err))
	}

	return ctrl.Result{}, kerrors.NewAggregate(errs)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&podpoolsv1alpha1.PodPool{}).
		Named("podpool").
		Complete(r)
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
	for _, staleGVK := range staleWorkloadGVKs(pool.Status.Groups, gvk) {
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
