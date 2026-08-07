package controller

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

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
func (r *PodPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&podpoolsv1alpha1.PodPool{}, builder.WithPredicates(poolPredicate())).
		Named("podpool").
		Complete(r)
}
