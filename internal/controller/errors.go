package controller

import (
	"errors"
	"fmt"

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
