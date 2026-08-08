package controller

import (
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	toolscache "k8s.io/client-go/tools/cache"
)

// TransformStripCacheWeight returns the cache transform installed as the
// manager's DefaultTransform. It drops managedFields and the
// last-applied-configuration annotation from every cached object — the two
// heaviest fields the controller never reads — and touches only the
// informer's copy; the object in etcd keeps both.
func TransformStripCacheWeight() toolscache.TransformFunc {
	return func(in any) (any, error) {
		obj, err := apimeta.Accessor(in)
		if err != nil {
			return in, nil //nolint:nilerr // non-meta cache objects pass through unchanged
		}

		if obj.GetManagedFields() != nil {
			obj.SetManagedFields(nil)
		}

		if a := obj.GetAnnotations(); a != nil {
			if _, ok := a[corev1.LastAppliedConfigAnnotation]; ok {
				delete(a, corev1.LastAppliedConfigAnnotation)
				obj.SetAnnotations(a)
			}
		}

		return in, nil
	}
}
