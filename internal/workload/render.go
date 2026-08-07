package workload

import (
	"encoding/json"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// KindPodPool is the owner kind every child's controller reference names.
const KindPodPool = "PodPool"

// ChildName is the one definition of a child workload's name. The rule is
// derived again by anyone who needs to find a child from its pool and group,
// so it gets one home before a second derivation can exist.
func ChildName(poolName, groupName string) string {
	return poolName + "-" + groupName
}

func ExtractGVK(raw []byte) (schema.GroupVersionKind, error) {
	var partial struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &partial); err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("unmarshalling GVK: %w", err)
	}

	if partial.APIVersion == "" || partial.Kind == "" {
		return schema.GroupVersionKind{}, errors.New("workloadTemplate must have apiVersion and kind")
	}

	gv, err := schema.ParseGroupVersion(partial.APIVersion)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}

	return gv.WithKind(partial.Kind), nil
}

func ParseTemplate(raw []byte) (map[string]any, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("unmarshalling workloadTemplate: %w", err)
	}

	return obj, nil
}

func BuildChildWorkload(
	tmpl map[string]any,
	group podpoolsv1alpha1.GroupSpec,
	pool *podpoolsv1alpha1.PodPool,
	replicas int32,
) (*unstructured.Unstructured, error) {
	obj := runtime.DeepCopyJSON(tmpl)

	child := &unstructured.Unstructured{Object: obj}

	// obj is a fresh DeepCopyJSON and nothing else holds a reference, so
	// NestedFieldNoCopy is safe.
	templateRaw, found, err := unstructured.NestedFieldNoCopy(child.Object, "spec", "template")
	if err != nil || !found {
		return nil, errors.New("workload has no .spec.template")
	}

	if _, ok := templateRaw.(map[string]any); !ok {
		return nil, errors.New("workload has no .spec.template")
	}

	if err := unstructured.SetNestedField(child.Object, int64(replicas), "spec", "replicas"); err != nil {
		return nil, err
	}

	child.SetName(ChildName(pool.Name, group.Name))
	child.SetNamespace(pool.Namespace)

	child.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion:         podpoolsv1alpha1.SchemeGroupVersion.String(),
		Kind:               KindPodPool,
		Name:               pool.Name,
		UID:                pool.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}})

	return child, nil
}
