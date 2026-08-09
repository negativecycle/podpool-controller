package controller

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

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
