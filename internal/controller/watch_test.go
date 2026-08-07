package controller

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// watchCounter stands in for the controller handle and counts Watch calls,
// which is the quantity every assertion here is about. The other three
// methods exist to satisfy the interface and panic if anything reaches them.
type watchCounter struct {
	calls atomic.Int32
}

func (w *watchCounter) Watch(_ source.Source) error {
	w.calls.Add(1)

	return nil
}
func (w *watchCounter) Reconcile(context.Context, reconcile.Request) (reconcile.Result, error) {
	panic("not implemented")
}
func (w *watchCounter) Start(context.Context) error { panic("not implemented") }
func (w *watchCounter) GetLogger() logr.Logger      { return logr.Discard() }

// errorWatch fails every registration, so a caller that records the informer
// before checking the error is visible.
type errorWatch struct {
	err error
}

func (e *errorWatch) Watch(_ source.Source) error { return e.err }
func (e *errorWatch) Reconcile(context.Context, reconcile.Request) (reconcile.Result, error) {
	panic("not implemented")
}
func (e *errorWatch) Start(context.Context) error { panic("not implemented") }
func (e *errorWatch) GetLogger() logr.Logger      { return logr.Discard() }

// These borrow the envtest suite's cache so the informers are real: the
// property under test is informer identity, which no fake reproduces.
var _ = Describe("ensureWatch identity tracking", func() {
	gvk := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

	newTestReconciler := func(ctrl interface {
		Watch(src source.Source) error
		Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error)
		Start(ctx context.Context) error
		GetLogger() logr.Logger
	},
	) *PodPoolReconciler {
		return &PodPoolReconciler{
			ctrl:        ctrl,
			Cache:       reconciler.Cache,
			Scheme:      reconciler.Scheme,
			RESTMapper:  reconciler.RESTMapper,
			watchStates: make(map[schema.GroupVersionKind]cache.Informer),
		}
	}

	// The Eventually covers the first pass, which finds the informer it just
	// created still filling its cache.
	It("should call Watch exactly once for repeated ensureWatch on the same GVK", func() {
		counter := &watchCounter{}
		r := newTestReconciler(counter)

		Eventually(func() error {
			return r.ensureWatch(ctx, gvk)
		}).Should(Succeed())

		Expect(r.ensureWatch(ctx, gvk)).To(Succeed())
		Expect(r.ensureWatch(ctx, gvk)).To(Succeed())
		Expect(counter.calls.Load()).To(Equal(int32(1)),
			"a second Watch on the same informer adds a duplicate handler that never goes away")
	})

	// The case a phase enum cannot express. The GVK is unchanged and the
	// watch was genuinely registered, but on a different object: the new
	// informer carries no handler, so anything that treats registration as
	// done-forever leaves the pool deaf to every child of this kind.
	It("should re-Watch after RemoveInformer replaces the instance", func() {
		counter := &watchCounter{}
		r := newTestReconciler(counter)

		Eventually(func() error {
			return r.ensureWatch(ctx, gvk)
		}).Should(Succeed())
		Expect(counter.calls.Load()).To(Equal(int32(1)))

		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		Expect(r.Cache.RemoveInformer(ctx, u)).To(Succeed())

		// Nothing is cleared by hand. The recorded entry still names the GVK,
		// and only comparing against the instance can tell that the thing it
		// names is no longer the informer the cache would hand out.
		Eventually(func() error {
			return r.ensureWatch(ctx, gvk)
		}).Should(Succeed())
		Expect(counter.calls.Load()).To(Equal(int32(2)),
			"the replacement informer carries no handler, so it needs its own Watch")
	})

	It("should not record the informer when Watch returns an error", func() {
		errCtrl := &errorWatch{err: errors.New("simulated watch error")}
		r := newTestReconciler(errCtrl)

		err := r.ensureWatch(ctx, gvk)
		if err == nil {
			// The informer may already be synced from a prior spec, in which
			// case ensureWatch reaches the Watch call immediately. Either way
			// it must have failed.
			Fail("expected error from ensureWatch with a broken Watch")
		}

		Expect(r.watchStates).NotTo(HaveKey(gvk),
			"recording a failed registration means it is never retried")
	})
})
