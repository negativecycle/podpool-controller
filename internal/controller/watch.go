package controller

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// ensureWatch starts watching a workload kind the first time a pool asks for
// it.
//
// Most controllers declare their watches up front with
// Owns(&appsv1.Deployment{}) and are done. This one cannot: the kinds it
// manages come out of a user-supplied workloadTemplate at runtime, and some
// of them are CRDs that may not have existed when the process started. So the
// watch is registered on first sight instead, and the registered set is
// remembered so it happens once.
//
// The watch enqueues the *owner*, not the child. A Deployment going unready
// has to wake the PodPool that owns it, because the PodPool is the only
// object this controller knows how to reconcile.
//
// The child watch carries no predicates, deliberately, unlike the primary
// watch's poolPredicate: child watches exist to deliver status changes
// (readyReplicas), and GenerationChangedPredicate would drop those and
// freeze every ready count.
//
// Reconcile runs concurrently, so the map needs the mutex. Two pools created
// at once with the same kind would otherwise race, and a concurrent map
// write in Go does not merely lose an update: it panics and takes the
// process down.
func (r *PodPoolReconciler) ensureWatch(gvk schema.GroupVersionKind) error {
	// Unit tests construct the reconciler without a manager. No controller
	// means no watches to add, which is fine: they drive Reconcile directly.
	if r.ctrl == nil {
		return nil
	}

	r.watchMu.Lock()
	defer r.watchMu.Unlock()

	if r.watchedGVKs[gvk] {
		return nil
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)

	if err := r.ctrl.Watch(
		source.Kind(r.Cache, u,
			handler.TypedEnqueueRequestForOwner[*unstructured.Unstructured](r.Scheme, r.RESTMapper, &podpoolsv1alpha1.PodPool{}),
		),
	); err != nil {
		return fmt.Errorf("setting up watch for %s: %w", gvk, err)
	}

	r.watchedGVKs[gvk] = true

	return nil
}

// poolPredicate drops PodPool updates that changed neither spec, annotations,
// nor labels: exactly the shape of this controller's own status writes.
// Safe because this controller is the sole writer of PodPool.status, and a
// status write moves neither the generation nor the metadata this filter
// watches.
func poolPredicate() predicate.Predicate {
	return predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.AnnotationChangedPredicate{},
		predicate.LabelChangedPredicate{},
	)
}

// SetupWithManager sets up the controller with the Manager.
//
// Build rather than Complete: Complete throws away the controller handle,
// and ensureWatch needs it to attach watches later.
func (r *PodPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&podpoolsv1alpha1.PodPool{}, builder.WithPredicates(poolPredicate())).
		Named("podpool").
		Build(r)
	if err != nil {
		return err
	}

	r.ctrl = c
	r.RESTMapper = mgr.GetRESTMapper()
	r.Cache = mgr.GetCache()
	r.watchedGVKs = make(map[schema.GroupVersionKind]bool)

	return nil
}
