package workload

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

const (
	fieldGeneration   = "generation"
	condStatusFalse   = "False"
	condStatusUnknown = "Unknown"
)

// GenerationCurrent reports whether a child's controller has observed its
// current spec. Both fields absent (a fresh object, or a type that does not
// publish observedGeneration) is treated as current: absence is not evidence
// of staleness.
func GenerationCurrent(child *unstructured.Unstructured) bool {
	gen, genFound, _ := unstructured.NestedInt64(child.Object, "metadata", fieldGeneration)

	obsGen, obsGenFound, _ := unstructured.NestedInt64(child.Object, "status", "observedGeneration")
	if !genFound || !obsGenFound {
		return true
	}

	return obsGen >= gen
}

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

	if group.Overrides != nil && group.Overrides.Raw != nil {
		var patch map[string]any
		if err := json.Unmarshal(group.Overrides.Raw, &patch); err != nil {
			return nil, fmt.Errorf("unmarshalling overrides for group %s: %w", group.Name, err)
		}

		obj = MergeMaps(obj, patch)
	}

	stripPastedMetadata(obj)

	child := &unstructured.Unstructured{Object: obj}

	// obj is a fresh DeepCopyJSON and nothing else holds a reference, so
	// NestedFieldNoCopy is safe.
	templateRaw, found, err := unstructured.NestedFieldNoCopy(child.Object, "spec", "template")
	if err != nil || !found {
		return nil, errors.New("workload has no .spec.template")
	}

	templateMap, ok := templateRaw.(map[string]any)
	if !ok {
		return nil, errors.New("workload has no .spec.template")
	}

	controllerLabels := map[string]string{
		LabelPool:      pool.Name,
		LabelGroup:     group.Name,
		LabelManagedBy: ManagerName,
	}

	mdRaw, _ := templateMap["metadata"].(map[string]any)
	if mdRaw == nil {
		mdRaw = make(map[string]any)
		templateMap["metadata"] = mdRaw
	}

	labelsRaw, _ := mdRaw["labels"].(map[string]any)
	if labelsRaw == nil {
		labelsRaw = make(map[string]any)
		mdRaw["labels"] = labelsRaw
	}

	for k, v := range controllerLabels {
		labelsRaw[k] = v
	}

	matchLabels := map[string]any{
		LabelPool:  pool.Name,
		LabelGroup: group.Name,
	}
	if err := unstructured.SetNestedMap(child.Object, matchLabels, "spec", "selector", "matchLabels"); err != nil {
		return nil, err
	}

	if err := unstructured.SetNestedField(child.Object, int64(replicas), "spec", "replicas"); err != nil {
		return nil, err
	}

	child.SetName(ChildName(pool.Name, group.Name))
	child.SetNamespace(pool.Namespace)

	childLabels := child.GetLabels()
	if childLabels == nil {
		childLabels = make(map[string]string)
	}

	maps.Copy(childLabels, controllerLabels)
	child.SetLabels(childLabels)

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

// stripPastedMetadata removes the fields a template picks up when it is
// copied from a live object, which is what `kubectl get -o yaml` hands you.
// A template carrying metadata.uid fails apply with a uid mismatch, so the
// child is never created, and being a 409 it retries forever.
func stripPastedMetadata(obj map[string]any) {
	delete(obj, "status")

	md, ok := obj["metadata"].(map[string]any)
	if !ok {
		return
	}

	for _, key := range []string{
		"uid", "resourceVersion", fieldGeneration, "generateName",
		"creationTimestamp", "deletionTimestamp",
		"finalizers", "managedFields", "selfLink",
		"ownerReferences",
	} {
		delete(md, key)
	}

	annotations, ok := md["annotations"].(map[string]any)
	if ok {
		delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")

		if len(annotations) == 0 {
			delete(md, "annotations")
		}
	}
}

// ReadInt32 reads an integer status field from a child workload, returning
// ok=false when the field is absent or unreadable.
//
// Absent is also the ordinary state of a healthy child: these fields are
// omitempty on the built-in types, so a count of zero and a field the kind
// never publishes are the same wire state. Treat ok=false as "zero for now",
// never as "this kind does not publish the field"; only elapsed time can
// separate those readings.
func ReadInt32(obj *unstructured.Unstructured, fields ...string) (int32, bool) {
	v, found, err := unstructured.NestedInt64(obj.Object, fields...)
	if err != nil || !found {
		return 0, false
	}

	return int32(v), true //nolint:gosec // counts are small in practice; revisited when a hostile child is considered
}

// MergeMaps deep-merges patch into base following RFC 7386 semantics: maps
// merge recursively, a null deletes the key it targets, and anything else
// replaces. Exported because admission will validate overrides with the same
// merge; two implementations of "what does this override do" would drift.
//
// Every level the patch touches gets a fresh map, so the base is never
// mutated: callers can merge one shared template many times.
func MergeMaps(base, patch map[string]any) map[string]any {
	result := make(map[string]any, len(base))
	maps.Copy(result, base)

	for k, v := range patch {
		if v == nil {
			delete(result, k)

			continue
		}

		baseMap, baseIsMap := result[k].(map[string]any)

		patchMap, patchIsMap := v.(map[string]any)
		if baseIsMap && patchIsMap {
			result[k] = MergeMaps(baseMap, patchMap)
		} else {
			result[k] = v
		}
	}

	return result
}

// ChildDetail extracts a best-effort explanation from a child workload's
// conditions. Returns ok=false when the child publishes nothing usable,
// which is the common case, not an error.
//
// Rules mirror sigs.k8s.io/cli-utils/pkg/kstatus without the dependency.
// Generation is checked first: if the child has not observed its own current
// spec, its conditions describe the previous one and are suppressed.
func ChildDetail(child *unstructured.Unstructured) (reason, message string, ok bool) {
	if !GenerationCurrent(child) {
		return "", "", false
	}

	raw, found, _ := unstructured.NestedFieldNoCopy(child.Object, "status", "conditions")
	if !found {
		return "", "", false
	}

	conds, isList := raw.([]any)
	if !isList || len(conds) == 0 {
		return "", "", false
	}

	type probe struct {
		condType  string
		badStatus string // the status value that indicates a problem
	}
	// Ordered most specific first. ReplicaFailure (negative polarity: True
	// is the problem) carries the API error verbatim. Ready and Available
	// are positive polarity: False or Unknown is the problem.
	probes := []probe{
		{"ReplicaFailure", "True"},
		{"Ready", condStatusFalse},
		{"Ready", condStatusUnknown},
		{"Available", condStatusFalse},
		{"Available", condStatusUnknown},
	}

	for _, p := range probes {
		for _, item := range conds {
			c, isMap := item.(map[string]any)
			if !isMap {
				continue
			}

			cType, _ := c["type"].(string)

			cStatus, _ := c["status"].(string)
			if cType != p.condType || cStatus != p.badStatus {
				continue
			}

			r, _ := c["reason"].(string)

			m, _ := c["message"].(string)
			if r == "" && m == "" {
				continue
			}

			return r, m, true
		}
	}

	return "", "", false
}

// TruncateRunes truncates s to at most maxLen runes, on a rune boundary.
func TruncateRunes(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}

	n := 0
	for i := range s {
		if n >= maxLen {
			return s[:i]
		}

		n++
	}

	return s
}
