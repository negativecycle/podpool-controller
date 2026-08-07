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
// It grows as the controller learns to read more; for now the only fact worth
// carrying is whether this pass created the object.
type childObservation struct {
	created bool
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
	gvk, err := workload.ExtractGVK(pool.Spec.WorkloadTemplate.Raw)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("extracting workload GVK: %w", err)
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

	reconciledGroups := make(map[string]bool, len(pool.Spec.Groups))

	for i, group := range pool.Spec.Groups {
		if _, err := r.reconcileGroup(ctx, &pool, tmpl, group, result.Targets[i]); err != nil {
			errs = append(errs, fmt.Errorf("group %s: %w", group.Name, err))

			continue
		}

		reconciledGroups[group.Name] = true
	}

	if sweepErrs := r.sweepAllOrphans(ctx, &pool, gvk, reconciledGroups); len(sweepErrs) > 0 {
		errs = append(errs, sweepErrs...)
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
	group podpoolsv1alpha1.GroupSpec,
	target int32,
) (childObservation, error) {
	desired, err := workload.BuildChildWorkload(tmpl, group, pool, target)
	if err != nil {
		return childObservation{}, fmt.Errorf("building workload: %w", err)
	}

	obs, err := r.reconcileWorkload(ctx, pool, desired)
	if err != nil {
		return childObservation{}, fmt.Errorf("reconciling workload: %w", err)
	}

	return obs, nil
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

		return childObservation{}, nil
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

	return childObservation{}, nil
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

		groupLabel := item.GetLabels()[workload.LabelGroup]

		log.Info("Deleting orphaned workload", "workload", klog.KObj(item),
			"reason", fmt.Sprintf(reasonFmt, groupLabel))

		if err := r.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	return nil
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
