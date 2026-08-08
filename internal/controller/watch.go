package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// errWatchSyncPending means the informer exists and is still filling its
// initial cache. The first reconcile for any GVK always sees this, because
// GetInformer is deliberately asked not to block, so it is a normal startup
// state rather than a failure.
//
// It stops being normal after watchSyncGrace. A GVK whose CRD is absent
// presents identically at any single instant: GetInformer succeeds, the
// informer is created, and its ListWatch fails silently against an
// unregistered resource, so HasSynced stays false forever. Only elapsed time
// separates the two, which is why this cannot be decided by inspection.
var errWatchSyncPending = errors.New("informer sync pending")

// watchSyncGrace is how long an informer may remain unsynced before the wait
// is reported as a failure. Long enough for an initial LIST of a large
// collection, short enough that a missing CRD is surfaced promptly.
const watchSyncGrace = 30 * time.Second

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
// The invariant is one rule rather than a sequence of states: Watch once per
// informer instance, verify liveness every pass. Steady state costs a map
// lookup and two interface calls.
//
// Reconcile runs concurrently, so the map needs the mutex. Two pools created
// at once with the same kind would otherwise race, and a concurrent map
// write in Go does not merely lose an update: it panics and takes the
// process down.
func (r *PodPoolReconciler) ensureWatch(ctx context.Context, gvk schema.GroupVersionKind) error {
	// Unit tests construct the reconciler without a manager. No controller
	// means no watches to add, which is fine: they drive Reconcile directly.
	if r.ctrl == nil {
		return nil
	}

	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)

	// GetInformer, with the unstructured object. Never GetInformerForKind,
	// which takes a GVK and builds the object itself by calling scheme.New:
	//
	//   - For a CRD kind the scheme does not know, that call fails outright,
	//     and CRD workloads are the entire reason this controller resolves
	//     kinds at runtime.
	//   - For a kind the scheme does know, it succeeds and is worse. The cache
	//     keys informers by object *type* as well as GVK, so a typed object
	//     lands in the structured map while source.Kind below registers our
	//     handler on the unstructured one. Same kind, two informers, two
	//     watches, and every check we make from here on would be interrogating
	//     the one with no handler attached.
	//
	// It is also the only place a kind the API server does not serve can be
	// detected synchronously: the informer is constructed inline, so a missing
	// CRD surfaces as an error here rather than as silence.
	//
	// Fetched before anything is decided, and asked not to block: the
	// reconcile context has no deadline, so a blocking wait would park a
	// worker indefinitely on a kind that may never sync.
	inf, err := r.Cache.GetInformer(ctx, u, cache.BlockUntilSynced(false))
	if err != nil {
		return fmt.Errorf("getting informer for %s: %w", gvk, err)
	}

	// A stopped informer delivers nothing and never restarts. Drop it and
	// forget the registration; the next pass builds a fresh one and watches
	// that.
	if inf.IsStopped() {
		_ = r.Cache.RemoveInformer(ctx, u)

		r.watchMu.Lock()
		r.initWatchMapsLocked()
		delete(r.watchStates, gvk)
		r.watchMu.Unlock()

		return fmt.Errorf("informer for %s stopped; rebuilding", gvk)
	}

	// Watch once per informer instance. Identity is the whole test: the same
	// instance already carries our handler and must not get a second one,
	// while a different instance carries none at all.
	r.watchMu.Lock()
	r.initWatchMapsLocked()

	if r.watchStates[gvk] != inf {
		if err := r.ctrl.Watch(
			source.Kind(r.Cache, u,
				handler.TypedEnqueueRequestForOwner[*unstructured.Unstructured](r.Scheme, r.RESTMapper, &podpoolsv1alpha1.PodPool{}),
			),
		); err != nil {
			r.watchMu.Unlock()

			return fmt.Errorf("setting up watch for %s: %w", gvk, err)
		}

		r.watchStates[gvk] = inf
	}
	r.watchMu.Unlock()

	if !inf.HasSynced() {
		now := r.Clock.Now()

		r.watchMu.Lock()
		r.initWatchMapsLocked()

		since, seen := r.watchPendingSince[gvk]
		if !seen {
			since = now
			r.watchPendingSince[gvk] = since
		}
		r.watchMu.Unlock()

		if now.Sub(since) < watchSyncGrace {
			return fmt.Errorf("%w for %s", errWatchSyncPending, gvk)
		}

		return fmt.Errorf("informer for %s has not synced after %s", gvk, watchSyncGrace)
	}

	// Synced. Forget both pieces of failure bookkeeping so a kind that breaks
	// again later is treated as a fresh problem rather than a remembered one.
	r.watchMu.Lock()
	delete(r.watchPendingSince, gvk)
	delete(r.watchFailureEmitted, gvk)
	r.watchMu.Unlock()

	return nil
}

// initWatchMapsLocked makes the watch-machinery maps safe to write. Caller
// must hold watchMu. SetupWithManager also builds them eagerly; this exists
// because nothing forces every construction path through SetupWithManager,
// and unit tests deliberately do not go through it. A nil-map write in Go is
// a panic, not a lost update.
func (r *PodPoolReconciler) initWatchMapsLocked() {
	if r.watchStates == nil {
		r.watchStates = make(map[schema.GroupVersionKind]cache.Informer)
	}

	if r.watchFailureEmitted == nil {
		r.watchFailureEmitted = make(map[schema.GroupVersionKind]bool)
	}

	if r.watchPendingSince == nil {
		r.watchPendingSince = make(map[schema.GroupVersionKind]time.Time)
	}
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
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.MaxConcurrentReconciles,
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[ctrl.Request](
				r.RateLimiterBaseDelay,
				r.RateLimiterMaxDelay,
			),
		}).
		Build(r)
	if err != nil {
		return err
	}

	r.ctrl = c
	r.RESTMapper = mgr.GetRESTMapper()

	r.Cache = mgr.GetCache()
	if r.Clock == nil {
		r.Clock = clock.RealClock{}
	}

	r.watchStates = make(map[schema.GroupVersionKind]cache.Informer)
	r.watchFailureEmitted = make(map[schema.GroupVersionKind]bool)
	r.watchPendingSince = make(map[schema.GroupVersionKind]time.Time)

	return nil
}
