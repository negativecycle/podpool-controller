package workload

import (
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

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
