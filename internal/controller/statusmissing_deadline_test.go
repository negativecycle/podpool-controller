package controller

// The gate the last commit shipped is wrong on a real API server, and these
// tests are the inversion. Four of the five fail against it; only
// TestStatusMissingEmitsOncePerStall was already green.
//
// The harness stands in for the workload controller the fake client does not
// run: a Get interceptor rewrites the child's status on every read, and nil
// fields are omitted from the object entirely, which is what omitempty does on
// a real API server -- see status_wire_state_test.go, which measures it.

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/events"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var statusMissingTestBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// injectedChildStatus is what the interceptor writes over the child's status.
// A nil field is removed from the object, not set to zero: absent and zero
// are the same wire state for omitempty status ints, and these tests exist
// because the controller once read absence as "unsupported".
type injectedChildStatus struct {
	replicas, ready, updated *int64
}

// installChildStatus wraps the reconciler's client so every read of the named
// child carries exactly the status *st describes at call time. Install once;
// mutate *st between reconciles.
func installChildStatus(t *testing.T, r *PodPoolReconciler, childName string, st *injectedChildStatus) {
	t.Helper()

	base, ok := r.Client.(client.WithWatch)
	if !ok {
		t.Fatalf("fake client %T does not implement client.WithWatch", r.Client)
	}

	r.Client = interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}

			u, isU := obj.(*unstructured.Unstructured)
			if !isU || u.GetName() != childName {
				return nil
			}

			unstructured.RemoveNestedField(u.Object, "status")

			for field, v := range map[string]*int64{
				"replicas":        st.replicas,
				"readyReplicas":   st.ready,
				"updatedReplicas": st.updated,
			} {
				if v != nil {
					_ = unstructured.SetNestedField(u.Object, *v, "status", field)
				}
			}

			return nil
		},
	})
}

// newStatusMissingHarness builds a single-group pool over a fake clock with
// the status injector installed. The returned *injectedChildStatus starts
// all-nil (a child with no status at all).
func newStatusMissingHarness(t *testing.T) (*PodPoolReconciler, *events.FakeRecorder, *clocktesting.FakePassiveClock, *injectedChildStatus) {
	t.Helper()

	pool := singleGroupPool()
	rec := events.NewFakeRecorder(64)
	r, _ := newFakeReconciler(t, nil, pool)
	r.Recorder = rec

	fake := clocktesting.NewFakePassiveClock(statusMissingTestBase)
	r.Clock = fake

	st := &injectedChildStatus{}
	installChildStatus(t, r, pool.Name+"-"+testGroupBase, st)

	return r, rec, fake, st
}

// The first reconcile of a healthy pool creates the child, which has no
// status at all, while target > 0. On a real API server a child with zero
// ready pods stores no readyReplicas key either, so this state is
// indistinguishable from a healthy rollout that is seconds old. Silence is
// the only correct output.
func TestNoStatusMissingOnFreshPool(t *testing.T) {
	r, rec, _, _ := newStatusMissingHarness(t)

	reconcilePool(t, r, singleGroupPool())

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, "StatusMissing"); n != 0 {
		t.Errorf("got %d StatusMissing events on the first reconcile of a healthy pool, want 0; events: %v", n, evts)
	}
}

// A normal rollout, pass by pass, mirroring what a real API server stores. No
// pass may warn: the first row is what the previous gate got wrong, and the
// last pins that a kind which has published readiness once is proven, so a
// later collapse to zero (stored as absent) is not "unsupported".
func TestNoStatusMissingDuringHealthyRollout(t *testing.T) {
	r, rec, fake, st := newStatusMissingHarness(t)
	pool := singleGroupPool()

	reconcilePool(t, r, pool)

	steps := []struct {
		name string
		at   time.Duration
		st   injectedChildStatus
	}{
		{"replicas up, none ready yet", 30 * time.Second,
			injectedChildStatus{replicas: ptr.To(int64(3)), updated: ptr.To(int64(3))}},
		{"some ready", 60 * time.Second,
			injectedChildStatus{replicas: ptr.To(int64(3)), ready: ptr.To(int64(2)), updated: ptr.To(int64(3))}},
		{"ready fell back to zero", 90 * time.Second,
			injectedChildStatus{replicas: ptr.To(int64(3)), updated: ptr.To(int64(3))}},
	}

	for _, step := range steps {
		*st = step.st
		fake.SetTime(statusMissingTestBase.Add(step.at))
		reconcilePool(t, r, pool)

		evts := drainEvents(rec.Events)
		if n := countEventsByReason(evts, "StatusMissing"); n != 0 {
			t.Errorf("%s: got %d StatusMissing events, want 0; events: %v", step.name, n, evts)
		}
	}
}

// The true positive: a kind that never publishes readiness, run past the
// default 600s progress deadline with replicas up the whole time. The
// diagnostic must arrive exactly once, in the same pass as the deadline
// event it explains.
func TestStatusMissingFiresAtTheDeadline(t *testing.T) {
	r, rec, fake, st := newStatusMissingHarness(t)
	pool := singleGroupPool()

	reconcilePool(t, r, pool)
	drainEvents(rec.Events)

	*st = injectedChildStatus{replicas: ptr.To(int64(3)), updated: ptr.To(int64(3))}

	fake.SetTime(statusMissingTestBase.Add(601 * time.Second))
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, "StatusMissing"); n != 1 {
		t.Errorf("got %d StatusMissing events at the deadline, want 1; events: %v", n, evts)
	}

	if n := countEventsByReason(evts, ReasonProgressDeadlineExceeded); n != 1 {
		t.Errorf("got %d %s events at the deadline, want 1; the two explain each other and must arrive together; events: %v",
			n, ReasonProgressDeadlineExceeded, evts)
	}
}

// A child that never scaled up at all is a different fault, and this warning
// must not claim it. status.replicas is 0, so the child has no replica that
// could be ready and its silence about readiness says nothing about whether the
// kind publishes it. The stall is real and ProgressDeadlineExceeded reports it;
// the diagnosis "this kind does not publish readiness" would be a guess, and a
// wrong one for every workload controller that is merely wedged.
func TestNoStatusMissingWhenTheChildHasNoReplicas(t *testing.T) {
	r, rec, fake, st := newStatusMissingHarness(t)
	pool := singleGroupPool()

	reconcilePool(t, r, pool)
	drainEvents(rec.Events)

	// The child's own controller is stuck: it publishes status, but has
	// brought up nothing.
	*st = injectedChildStatus{replicas: ptr.To(int64(0))}

	fake.SetTime(statusMissingTestBase.Add(601 * time.Second))
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, ReasonProgressDeadlineExceeded); n != 1 {
		t.Errorf("got %d %s events, want 1 (the pool is genuinely stalled); events: %v",
			n, ReasonProgressDeadlineExceeded, evts)
	}

	if n := countEventsByReason(evts, "StatusMissing"); n != 0 {
		t.Errorf("got %d StatusMissing events for a child with zero replicas, want 0 — "+
			"a child with no replicas has nothing to report readiness about; events: %v", n, evts)
	}
}

// A kind that has published readiness is proven to publish it. When its
// readiness later collapses to zero (stored as absent) and the pool stalls,
// the deadline event fires but StatusMissing must not: the correct diagnosis
// is "nothing is ready", which ProgressDeadlineExceeded already says.
func TestNoStatusMissingForAProvenKind(t *testing.T) {
	r, rec, fake, st := newStatusMissingHarness(t)
	pool := singleGroupPool()

	reconcilePool(t, r, pool)

	*st = injectedChildStatus{replicas: ptr.To(int64(3)), ready: ptr.To(int64(2)), updated: ptr.To(int64(3))}

	fake.SetTime(statusMissingTestBase.Add(60 * time.Second))
	reconcilePool(t, r, pool)

	*st = injectedChildStatus{replicas: ptr.To(int64(3)), updated: ptr.To(int64(3))}

	fake.SetTime(statusMissingTestBase.Add(60*time.Second + 601*time.Second))
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, ReasonProgressDeadlineExceeded); n != 1 {
		t.Errorf("got %d %s events, want 1 (the pool genuinely stalled); events: %v",
			n, ReasonProgressDeadlineExceeded, evts)
	}

	if n := countEventsByReason(evts, "StatusMissing"); n != 0 {
		t.Errorf("got %d StatusMissing events for a kind that has published readiness, want 0; events: %v", n, evts)
	}
}

// Continuously stalled is not repeatedly news. The warning rides the same
// transition edge as the deadline event: one emission when the pool enters
// stalled, silence while it stays there.
func TestStatusMissingEmitsOncePerStall(t *testing.T) {
	r, rec, fake, st := newStatusMissingHarness(t)
	pool := singleGroupPool()

	reconcilePool(t, r, pool)

	*st = injectedChildStatus{replicas: ptr.To(int64(3)), updated: ptr.To(int64(3))}

	fake.SetTime(statusMissingTestBase.Add(601 * time.Second))
	reconcilePool(t, r, pool)
	drainEvents(rec.Events)

	fake.SetTime(statusMissingTestBase.Add(1200 * time.Second))
	reconcilePool(t, r, pool)

	evts := drainEvents(rec.Events)
	if n := countEventsByReason(evts, "StatusMissing"); n != 0 {
		t.Errorf("got %d StatusMissing events while continuously stalled, want 0 (emit on the transition only); events: %v", n, evts)
	}
}
