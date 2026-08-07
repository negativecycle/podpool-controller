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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// managerName is this controller's field-manager identity for server-side
// apply. Moves beside the label scheme once one exists.
const managerName = "podpool-controller"

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
}

// +kubebuilder:rbac:groups=podpools.dev,resources=podpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=podpools.dev,resources=podpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=podpools.dev,resources=podpools/finalizers,verbs=update
// Server-side apply against an absent object checks create then patch; both are
// required or the first reconcile of every child fails.
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=create;get;list;patch;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=create;get;list;patch;watch

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
	if _, err := workload.ExtractGVK(pool.Spec.WorkloadTemplate.Raw); err != nil {
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

	for i, group := range pool.Spec.Groups {
		if _, err := r.reconcileGroup(ctx, &pool, tmpl, group, result.Targets[i]); err != nil {
			return ctrl.Result{}, fmt.Errorf("group %s: %w", group.Name, err)
		}
	}

	return ctrl.Result{}, nil
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

	obs, err := r.reconcileWorkload(ctx, desired)
	if err != nil {
		return childObservation{}, fmt.Errorf("reconciling workload: %w", err)
	}

	return obs, nil
}

func (r *PodPoolReconciler) reconcileWorkload(
	ctx context.Context,
	desired *unstructured.Unstructured,
) (childObservation, error) {
	key := types.NamespacedName{Name: desired.GetName(), Namespace: desired.GetNamespace()}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())

	err := r.Get(ctx, key, existing)
	if apierrors.IsNotFound(err) {
		if err := r.applyChild(ctx, desired); err != nil {
			return childObservation{}, err
		}

		return childObservation{created: true}, nil
	}

	if err != nil {
		return childObservation{}, err
	}

	if err := r.applyChild(ctx, desired); err != nil {
		return childObservation{}, err
	}

	return childObservation{}, nil
}

// applyChild writes the rendered child with server-side apply.
//
// ForceOwnership is deliberate. A conflict means another manager has taken a
// field the pool renders, and the pool is the authority on those.
func (r *PodPoolReconciler) applyChild(ctx context.Context, desired *unstructured.Unstructured) error {
	desired.SetResourceVersion("")

	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(desired),
		client.FieldOwner(managerName),
		client.ForceOwnership,
	)
}
