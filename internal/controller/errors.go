package controller

import (
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// terminalError marks a failure that cannot succeed until the PodPool spec
// changes. Retrying is not just wasted work: it hides which pools are waiting
// on a human behind pools that are merely slow.
type terminalError struct{ err error }

func (e *terminalError) Error() string { return e.err.Error() }
func (e *terminalError) Unwrap() error { return e.err }

func terminal(err error) error { return &terminalError{err: err} }

// isTerminal uses errors.As rather than a type assertion because every error
// leaving reconcileGroup is wrapped at least once on its way up. A classifier
// that only sees the outermost error is a classifier that never fires.
func isTerminal(err error) bool {
	var t *terminalError

	return errors.As(err, &t)
}

// isTerminalAPIError decides which API rejections cannot be fixed by waiting.
//
// IsForbidden is deliberately excluded. A 403 is usually RBAC, which an admin
// fixes without touching the spec, and nothing here watches ClusterRoles to
// notice that it happened -- so treating it as terminal would leave the pool
// wedged after the fix.
//
// The InternalError arm is the interesting one. SSA rejects a template whose
// fields do not exist in the target's schema -- typos, wrong types -- as a
// 500, not a 4xx, with an empty reason. That is the single most common thing a
// user gets wrong, and the obvious IsInvalid check misses all of it.
//
// Message-matching is deliberate and load-bearing. Other 500s MUST stay
// retryable: a child type's own admission webhook being down is also a 500 and
// heals on its own. And if the wording ever changes upstream this falls back to
// retryable, which is the safe direction: a wedged pool that keeps trying is
// recoverable, a pool that gave up on a transient fault is not.
func isTerminalAPIError(err error) bool {
	if apierrors.IsInvalid(err) || apierrors.IsRequestEntityTooLargeError(err) {
		return true
	}

	return apierrors.IsInternalError(err) &&
		strings.Contains(err.Error(), "failed to create typed patch object")
}

type workloadNotOwnedError struct {
	kind, name string
	owner      *metav1.OwnerReference
}

func (e *workloadNotOwnedError) Error() string {
	if e.owner == nil {
		return fmt.Sprintf("%s %s exists and has no controller owner", e.kind, e.name)
	}

	return fmt.Sprintf("%s %s is controlled by %s/%s", e.kind, e.name, e.owner.Kind, e.owner.Name)
}

func isControlledBy(obj metav1.Object, pool *podpoolsv1alpha1.PodPool) bool {
	if pool.UID == "" {
		return false
	}

	owner := metav1.GetControllerOf(obj)

	return owner != nil && owner.UID == pool.UID
}
