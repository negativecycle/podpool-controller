/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	kjson "sigs.k8s.io/json"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

var dnsLabelRegexp = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)

var childScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)

	return s
}()

// SetupPodPoolWebhookWithManager registers the defaulting and validating webhooks for PodPool.
func SetupPodPoolWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &podpoolsv1alpha1.PodPool{}).
		WithValidator(&PodPoolCustomValidator{}).
		WithDefaulter(&PodPoolCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-podpools-dev-v1alpha1-podpool,mutating=true,failurePolicy=fail,sideEffects=None,groups=podpools.dev,resources=podpools,verbs=create;update,versions=v1alpha1,name=mpodpool-v1alpha1.kb.io,admissionReviewVersions=v1

// PodPoolCustomDefaulter sets defaults on PodPool resources during admission.
type PodPoolCustomDefaulter struct{}

func (d *PodPoolCustomDefaulter) Default(ctx context.Context, obj *podpoolsv1alpha1.PodPool) error {
	logf.FromContext(ctx).Info("Defaulting for PodPool", "name", obj.GetName())

	for i := range obj.Spec.Groups {
		s := &obj.Spec.Groups[i].Scaling
		if s.Min == nil && s.Max == nil {
			zero := int32(0)
			s.Min = &zero
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-podpools-dev-v1alpha1-podpool,mutating=false,failurePolicy=fail,sideEffects=None,groups=podpools.dev,resources=podpools,verbs=create;update,versions=v1alpha1,name=vpodpool-v1alpha1.kb.io,admissionReviewVersions=v1

// PodPoolCustomValidator validates PodPool resources during admission.
type PodPoolCustomValidator struct{}

func validateWorkloadTemplate(fp *field.Path, raw []byte) field.ErrorList {
	var allErrs field.ErrorList

	if len(raw) == 0 {
		allErrs = append(allErrs, field.Required(fp, "workloadTemplate is required"))

		return allErrs
	}

	_, err := workload.ExtractGVK(raw)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(fp, string(raw), fmt.Sprintf("must have valid apiVersion and kind: %v", err)))

		return allErrs
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		allErrs = append(allErrs, field.Invalid(fp, string(raw), "must be valid JSON"))

		return allErrs
	}

	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		allErrs = append(allErrs, field.Required(fp.Child("spec"), "workloadTemplate must have a spec"))

		return allErrs
	}

	if _, ok := spec["template"]; !ok {
		allErrs = append(allErrs, field.Required(fp.Child("spec", "template"), "workloadTemplate must have .spec.template"))
	}

	return allErrs
}

// validateOpportunistic enforces the two rules that span groups rather than
// living inside one.
func validateOpportunistic(groupsPath *field.Path, groups []podpoolsv1alpha1.GroupSpec) field.ErrorList {
	var allErrs field.ErrorList

	var anyOpportunistic bool

	for _, g := range groups {
		if g.Scaling.Opportunistic != nil && *g.Scaling.Opportunistic {
			anyOpportunistic = true
		}
	}

	if !anyOpportunistic {
		return nil
	}

	for i, g := range groups {
		if g.Scaling.Opportunistic == nil || !*g.Scaling.Opportunistic {
			continue
		}
		// Replicas this group cannot place have to land somewhere. Without a
		// later group they would simply go missing, and the pool would run
		// below spec.replicas with no indication why.
		if i == len(groups)-1 {
			allErrs = append(allErrs, field.Invalid(
				groupsPath.Index(i).Child("scaling", "opportunistic"), true,
				"an opportunistic group must be followed by a group that can absorb what it cannot place"))
		}
		// Overflow lands on the first unbounded group in LIST order. One
		// placed before this group intercepts the displaced replicas and (as
		// it shares no capacity discovery) pins them Pending on a tier that
		// is already full. Found empirically: an uncapped base swallowed the
		// whole pool while burst sat at zero.
		//
		// workload.IsBounded rather than a local presence check: this guard is
		// only as good as its agreement with the distributor, and a local copy
		// that read a malformed target as a cap let exactly the case it was
		// written for through (#71).
		for j := range i {
			if !workload.IsBounded(groups[j].Scaling) {
				allErrs = append(allErrs, field.Invalid(
					groupsPath.Index(j).Child("scaling"), groups[j].Name,
					"a group without max or target placed before an opportunistic group would absorb its displaced replicas; cap it or move it after"))
			}
		}
	}

	return allErrs
}

// validateOverflowSink rejects a pool with more than one unbounded group.
//
// Phase 4 of the distributor iterates groups in list order. The first unbounded
// group absorbs the entire remainder; any later unbounded group receives zero
// overflow at every scale and every replica count. Its min still works (phase 1),
// but the unbounded characteristic is provably dead weight — the user asked for
// an overflow sink that can never overflow into.
//
// Uses workload.IsBounded so the predicate agrees with the distributor (#71).
func validateOverflowSink(groupsPath *field.Path, groups []podpoolsv1alpha1.GroupSpec) field.ErrorList {
	var allErrs field.ErrorList

	firstUnbounded := -1

	for i, g := range groups {
		if workload.IsBounded(g.Scaling) {
			continue
		}

		if firstUnbounded >= 0 {
			allErrs = append(allErrs, field.Invalid(
				groupsPath.Index(i).Child("scaling"), g.Name,
				fmt.Sprintf("at most one group may be unbounded (no max, target, or opportunistic); "+
					"group %q at index %d is already the overflow sink — cap this group with max or target, or remove it",
					groups[firstUnbounded].Name, firstUnbounded)))
		} else {
			firstUnbounded = i
		}
	}

	return allErrs
}

func validatePodPoolSpec(pp *podpoolsv1alpha1.PodPool) field.ErrorList {
	var allErrs field.ErrorList

	specPath := field.NewPath("spec")
	groupsPath := specPath.Child("groups")

	allErrs = append(allErrs, validateWorkloadTemplate(specPath.Child("workloadTemplate"), pp.Spec.WorkloadTemplate.Raw)...)

	if len(pp.Spec.Groups) == 0 {
		allErrs = append(allErrs, field.Required(groupsPath, "at least one group is required"))

		return allErrs
	}

	names := make(map[string]bool)

	for i, g := range pp.Spec.Groups {
		gp := groupsPath.Index(i)

		if g.Name == "" {
			allErrs = append(allErrs, field.Required(gp.Child("name"), "group name is required"))
		} else if len(g.Name) < 2 || !dnsLabelRegexp.MatchString(g.Name) {
			allErrs = append(allErrs, field.Invalid(gp.Child("name"), g.Name, "must be a valid DNS label (lowercase alphanumeric and hyphens, at least 2 characters)"))
		}

		// Unreachable in a real cluster: +listType=map,listMapKey=name rejects
		// duplicates at the schema layer. Kept for direct-call unit tests and
		// clusters running a stale CRD.
		if names[g.Name] {
			allErrs = append(allErrs, field.Duplicate(gp.Child("name"), g.Name))
		}

		names[g.Name] = true

		allErrs = append(allErrs, validateScaling(gp.Child("scaling"), &g.Scaling)...)
	}

	allErrs = append(allErrs, validateOverflowSink(groupsPath, pp.Spec.Groups)...)
	allErrs = append(allErrs, validateOpportunistic(groupsPath, pp.Spec.Groups)...)

	return allErrs
}

const fmtUnset = "unset"

func fmtI32(p *int32) string {
	if p == nil {
		return fmtUnset
	}

	return strconv.FormatInt(int64(*p), 10)
}

func fmtBool(p *bool) string {
	if p == nil {
		return fmtUnset
	}

	return strconv.FormatBool(*p)
}

func fmtTarget(t *intstr.IntOrString) string {
	if t == nil {
		return fmtUnset
	}

	return t.String()
}

func validateScaling(fp *field.Path, s *podpoolsv1alpha1.ScalingConstraints) field.ErrorList {
	var allErrs field.ErrorList

	hasOpportunistic := s.Opportunistic != nil && *s.Opportunistic

	// Duplicates the XValidation rule on .opportunistic (podpool_types.go):
	// "(!has(self.opportunistic) || !self.opportunistic) || (!has(self.max) &&
	// !has(self.target))". Unreachable through admission in a real cluster: the
	// schema rejects it first. Kept for direct-call unit tests and clusters
	// running a stale CRD, like the duplicate-name check in validatePodPoolSpec.
	if hasOpportunistic && (s.Max != nil || s.Target != nil) {
		allErrs = append(allErrs, field.Invalid(fp, fmt.Sprintf("min=%s max=%s target=%s opportunistic=%s",
			fmtI32(s.Min), fmtI32(s.Max), fmtTarget(s.Target), fmtBool(s.Opportunistic)),
			"opportunistic is itself the ceiling; it cannot be combined with max or target"))
	}

	// Duplicates the XValidation rule "!has(self.min) || !has(self.max) ||
	// self.min <= self.max" (podpool_types.go). Same unreachability and same
	// reason to keep it as the opportunistic check above.
	if s.Min != nil && s.Max != nil && *s.Min > *s.Max {
		allErrs = append(allErrs, field.Invalid(fp, fmt.Sprintf("min=%s max=%s",
			fmtI32(s.Min), fmtI32(s.Max)),
			"min must not exceed max"))
	}

	return allErrs
}

// warnOnFullyCappedPool returns a warning when no group can absorb overflow.
//
// Of the three legal scaling shapes only (min) alone is unbounded, so a pool
// where every group carries a max or a target has a hard ceiling on what it
// can place. Whenever that ceiling falls below spec.replicas the difference is
// left unplaced and reported in status.unplacedReplicas. Deliberate, but
// surprising if you have not met it before.
//
// A warning rather than a rejection: the configuration is legitimate, and
// rejecting it would break pools that are already running.
//
// Shares workload.IsBounded with the distributor, so a group whose target is
// present but unreadable counts as capped here too. It is: the distributor
// binds it at zero. The presence check this replaced said otherwise and left
// the warning silent about a pool that would run below spec.replicas (#71).
func warnOnFullyCappedPool(pp *podpoolsv1alpha1.PodPool) admission.Warnings {
	for _, g := range pp.Spec.Groups {
		if !workload.IsBounded(g.Scaling) {
			return nil
		}
	}

	if len(pp.Spec.Groups) == 0 {
		return nil
	}

	return admission.Warnings{
		"every group sets max or target, so no group can absorb overflow: " +
			"replicas beyond the combined ceiling will be left unplaced and reported " +
			"in status.unplacedReplicas. Remove target from one group to make it the overflow bucket.",
	}
}

// warnOnUnreadableTarget flags a target the distributor cannot parse.
//
// A warning and not an error, deliberately. The CRD's CEL rule already rejects
// these on create, so anything reaching here is a stored object: one admitted
// before the rule existed, or written against a stale CRD. Validation ratcheting
// keeps such an object alive — an update that leaves `scaling` untouched never
// re-runs the rule, so the pool goes on scaling indefinitely — while any edit
// that does touch `scaling` is rejected. The pool is scalable but not editable.
//
// Rejecting here would close the one operation still available, on an object
// whose operator may need to scale it down precisely because it is overspending.
// Repair stays possible because fixing target is itself a `scaling`
// edit, and CEL admits an edit that makes the object valid.
//
// The wording deliberately echoes the CEL rule's message: an operator who meets
// both should not have to work out that they are the same complaint.
func warnOnUnreadableTarget(pp *podpoolsv1alpha1.PodPool) admission.Warnings {
	// Kept identical, en-dash included, to the XValidation message on
	// ScalingConstraints.Target in api/v1alpha1/podpool_types.go. A CEL rule
	// message cannot reference a Go constant, so this is two copies of one
	// sentence; grep the phrase to find both.
	const grammar = `is not a percentage string like "30%" (1%–100%)`

	var warnings admission.Warnings

	for _, g := range pp.Spec.Groups {
		if g.Scaling.Target == nil {
			continue
		}

		if _, ok := workload.TargetPercent(g.Scaling.Target); ok {
			continue
		}

		warnings = append(warnings, fmt.Sprintf(
			"group %q: target %q %s, so the group is capped at 0 and will not "+
				"grow beyond its min. Set a valid target or remove the field",
			g.Name, fmtTarget(g.Scaling.Target), grammar))
	}

	return warnings
}

type renderedChildResult struct {
	globalErrs field.ErrorList
	groupErrs  map[string]field.ErrorList
	warnings   admission.Warnings
}

func (r renderedChildResult) allErrors() field.ErrorList {
	all := append(field.ErrorList{}, r.globalErrs...)
	for _, errs := range r.groupErrs {
		all = append(all, errs...)
	}

	return all
}

func checkControllerOwnedPaths(fp *field.Path, overrides []byte) field.ErrorList {
	var allErrs field.ErrorList

	var obj map[string]any
	if err := json.Unmarshal(overrides, &obj); err != nil {
		return nil
	}

	if spec, ok := obj["spec"].(map[string]any); ok {
		if sel, ok := spec["selector"].(map[string]any); ok {
			if _, ok := sel["matchLabels"]; ok {
				allErrs = append(allErrs, field.Forbidden(
					fp.Child("spec", "selector", "matchLabels"),
					"overriding spec.selector.matchLabels has no effect: the controller overwrites it"))
			}
		}

		if _, ok := spec["replicas"]; ok {
			allErrs = append(allErrs, field.Forbidden(
				fp.Child("spec", "replicas"),
				"overriding spec.replicas has no effect: the controller sets it from the distribution"))
		}
	}

	if md, ok := obj["metadata"].(map[string]any); ok {
		if _, ok := md["name"]; ok {
			allErrs = append(allErrs, field.Forbidden(
				fp.Child("metadata", "name"),
				"overriding metadata.name has no effect: the controller names children <pool>-<group>"))
		}

		if _, ok := md["ownerReferences"]; ok {
			allErrs = append(allErrs, field.Forbidden(
				fp.Child("metadata", "ownerReferences"),
				"overriding ownerReferences has no effect: the controller sets the owner reference"))
		}

		if labels, ok := md["labels"].(map[string]any); ok {
			for k := range labels {
				if strings.HasPrefix(k, "podpools.dev/") {
					allErrs = append(allErrs, field.Forbidden(
						fp.Child("metadata", "labels").Key(k),
						"overriding podpools.dev/* labels has no effect: the controller manages them"))
				}
			}
		}
	}

	return allErrs
}

func validateRenderedChildren(pp *podpoolsv1alpha1.PodPool) renderedChildResult {
	result := renderedChildResult{groupErrs: make(map[string]field.ErrorList)}

	gvk, err := workload.ExtractGVK(pp.Spec.WorkloadTemplate.Raw)
	if err != nil {
		return result
	}

	if gvk.Group == podpoolsv1alpha1.SchemeGroupVersion.Group && gvk.Kind == workload.KindPodPool {
		result.globalErrs = append(result.globalErrs, field.Forbidden(
			field.NewPath("spec", "workloadTemplate"),
			"a PodPool cannot use another PodPool as its workload template"))

		return result
	}

	// Unreachable for malformed JSON: ExtractGVK unmarshals the same bytes above.
	tmpl, parseErr := workload.ParseTemplate(pp.Spec.WorkloadTemplate.Raw)
	if parseErr != nil {
		return result
	}

	_, schemeCheckErr := childScheme.New(gvk)
	knownGVK := schemeCheckErr == nil

	dist := workload.ComputeGroupTargets(pp.Spec.Replicas, pp.Spec.Groups, nil)
	groupsPath := field.NewPath("spec", "groups")

	for i, g := range pp.Spec.Groups {
		gp := groupsPath.Index(i)

		var groupErrs field.ErrorList

		if g.Overrides != nil && len(g.Overrides.Raw) > 0 {
			groupErrs = append(groupErrs, checkControllerOwnedPaths(gp.Child("overrides"), g.Overrides.Raw)...)
		}

		replicas := int32(0)
		if i < len(dist.Targets) {
			replicas = dist.Targets[i]
		}

		child, renderErr := workload.BuildChildWorkload(tmpl, g, pp, replicas)
		if renderErr != nil {
			groupErrs = append(groupErrs, field.Invalid(gp, g.Name,
				fmt.Sprintf("cannot render child workload: %v", renderErr)))
			if len(groupErrs) > 0 {
				result.groupErrs[g.Name] = groupErrs
			}

			continue
		}

		if knownGVK {
			typed, _ := childScheme.New(gvk)
			childJSON, _ := json.Marshal(child.Object) //nolint:errchkjson // unstructured objects always marshal cleanly

			strictErrs, decodeErr := kjson.UnmarshalStrict(childJSON, typed)
			if decodeErr != nil {
				groupErrs = append(groupErrs, field.Invalid(gp, g.Name,
					fmt.Sprintf("rendered child has type errors: %v", decodeErr)))
			}

			for _, se := range strictErrs {
				result.warnings = append(result.warnings,
					fmt.Sprintf("group %q: %v", g.Name, se))
			}
		}

		if len(groupErrs) > 0 {
			result.groupErrs[g.Name] = groupErrs
		}
	}

	return result
}

func downgradePreExisting(oldResult, newResult renderedChildResult) (field.ErrorList, admission.Warnings) {
	var (
		errs     field.ErrorList
		warnings admission.Warnings
	)

	oldGlobalSet := make(map[string]bool)
	for _, e := range oldResult.globalErrs {
		oldGlobalSet[e.Field+"|"+e.Detail] = true
	}

	for _, e := range newResult.globalErrs {
		if oldGlobalSet[e.Field+"|"+e.Detail] {
			warnings = append(warnings, "pre-existing: "+e.Detail)
		} else {
			errs = append(errs, e)
		}
	}

	for groupName, newGroupErrs := range newResult.groupErrs {
		oldGroupErrs := oldResult.groupErrs[groupName]
		if len(oldGroupErrs) == 0 {
			errs = append(errs, newGroupErrs...)

			continue
		}

		oldDetailSet := make(map[string]bool)
		for _, e := range oldGroupErrs {
			oldDetailSet[e.Type.String()+"|"+e.Detail] = true
		}

		for _, e := range newGroupErrs {
			if oldDetailSet[e.Type.String()+"|"+e.Detail] {
				warnings = append(warnings, fmt.Sprintf(
					"pre-existing issue in group %q: %s", groupName, e.Detail))
			} else {
				errs = append(errs, e)
			}
		}
	}

	return errs, warnings
}

func warnOnGroupRemoval(oldPP, newPP *podpoolsv1alpha1.PodPool) admission.Warnings {
	newNames := make(map[string]bool)
	for _, g := range newPP.Spec.Groups {
		newNames[g.Name] = true
	}

	var warnings admission.Warnings

	for _, g := range oldPP.Spec.Groups {
		if !newNames[g.Name] {
			warnings = append(warnings, fmt.Sprintf(
				"group %q was removed: its child workload %s will be deleted and its replicas will move to other groups",
				g.Name, workload.ChildName(oldPP.Name, g.Name)))
		}
	}

	return warnings
}

const maxPoolNameLen = 63

func validatePoolNameCreate(pp *podpoolsv1alpha1.PodPool) *field.Error {
	name := pp.Name
	if len(name) <= maxPoolNameLen {
		return nil
	}

	detail := fmt.Sprintf("must be at most %d characters (it becomes the %s label value, which is bounded by the Kubernetes label value limit)",
		maxPoolNameLen, workload.LabelPool)
	if pp.GenerateName != "" {
		detail = fmt.Sprintf("generated name %q is %d characters; %s", name, len(name), detail)
	}

	return field.Invalid(field.NewPath("metadata", "name"), name, detail)
}

func warnPoolNameUpdate(pp *podpoolsv1alpha1.PodPool) admission.Warnings {
	if len(pp.Name) <= maxPoolNameLen {
		return nil
	}

	return admission.Warnings{
		fmt.Sprintf("pool name %q is %d characters, exceeding the %d-character label value limit; it was admitted before this check existed and cannot be renamed. Consider recreating with a shorter name",
			pp.Name, len(pp.Name), maxPoolNameLen),
	}
}

func (v *PodPoolCustomValidator) ValidateCreate(ctx context.Context, obj *podpoolsv1alpha1.PodPool) (admission.Warnings, error) {
	logf.FromContext(ctx).Info("Validation for PodPool upon creation", "name", obj.GetName())

	allErrs := validatePodPoolSpec(obj)

	if nameErr := validatePoolNameCreate(obj); nameErr != nil {
		allErrs = append(allErrs, nameErr)
	}

	rcResult := validateRenderedChildren(obj)
	allErrs = append(allErrs, rcResult.allErrors()...)

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}

	warnings := rcResult.warnings
	warnings = append(warnings, warnOnFullyCappedPool(obj)...)
	warnings = append(warnings, warnOnUnreadableTarget(obj)...)

	return warnings, nil
}

func (v *PodPoolCustomValidator) ValidateUpdate(ctx context.Context, oldObj *podpoolsv1alpha1.PodPool, newObj *podpoolsv1alpha1.PodPool) (admission.Warnings, error) {
	logf.FromContext(ctx).Info("Validation for PodPool upon update", "name", newObj.GetName())

	allErrs := validatePodPoolSpec(newObj)

	// The workload kind is immutable, and this is the only place that can say
	// so: a single object is never invalid for it, so no per-field rule and no
	// CEL expression over `self` can see the violation. Changing the kind
	// orphans every child of the old kind, which the controller's sweep does
	// handle — but it does so by deleting running workloads, and an operator
	// who edited one line of a template deserves to be told rather than
	// obeyed.
	oldGVK, oldErr := workload.ExtractGVK(oldObj.Spec.WorkloadTemplate.Raw)

	newGVK, newErr := workload.ExtractGVK(newObj.Spec.WorkloadTemplate.Raw)
	if oldErr == nil && newErr == nil && oldGVK != newGVK {
		allErrs = append(allErrs, field.Invalid(
			field.NewPath("spec", "workloadTemplate"),
			newGVK.String(),
			fmt.Sprintf("workload GVK is immutable (was %s)", oldGVK.String()),
		))
	}

	newRCResult := validateRenderedChildren(newObj)

	var renderWarnings admission.Warnings

	// A stricter rule must not brick a stored object. Compare the new pool's
	// render errors against the same pool's before this edit: a violation it
	// already had becomes a warning, one this edit introduces stays an error.
	// Without it, shipping any new render check makes every existing pool that
	// trips it unpatchable — including by the patch that would fix it.
	if newRenderErrs := newRCResult.allErrors(); len(newRenderErrs) > 0 {
		oldRCResult := validateRenderedChildren(oldObj)
		remaining, downgraded := downgradePreExisting(oldRCResult, newRCResult)
		allErrs = append(allErrs, remaining...)
		renderWarnings = append(renderWarnings, downgraded...)
	}

	renderWarnings = append(renderWarnings, newRCResult.warnings...)

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}

	warnings := renderWarnings
	warnings = append(warnings, warnOnGroupRemoval(oldObj, newObj)...)
	warnings = append(warnings, warnOnFullyCappedPool(newObj)...)
	warnings = append(warnings, warnOnUnreadableTarget(newObj)...)
	warnings = append(warnings, warnPoolNameUpdate(newObj)...)

	return warnings, nil
}

// ValidateDelete is registered because the framework requires the interface to
// be complete, not because deleting a pool needs a verdict: the controller's
// own cleanup runs on the NotFound pass, and refusing a delete would leave an
// operator no way to remove a broken pool.
func (v *PodPoolCustomValidator) ValidateDelete(_ context.Context, _ *podpoolsv1alpha1.PodPool) (admission.Warnings, error) {
	return nil, nil
}
