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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// PodPoolReconciler reconciles a PodPool object.
type PodPoolReconciler struct {
	client.Client

	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=podpools.dev,resources=podpools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=podpools.dev,resources=podpools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=podpools.dev,resources=podpools/finalizers,verbs=update

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

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&podpoolsv1alpha1.PodPool{}).
		Named("podpool").
		Complete(r)
}
