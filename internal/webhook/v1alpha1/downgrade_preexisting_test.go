package v1alpha1

import (
	"fmt"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// downgradePreExisting decides whether an update to a pool whose stored spec
// already violated a rendered-child rule is rejected or waved through with a
// warning. The two halves deliberately match on different keys — global
// errors on Field|Detail (Type ignored), group errors on Type|Detail (Field
// ignored) — and these tables pin both keys so a drive-by unification cannot
// change the ratcheting behavior silently.

const (
	warnPreExistingNested = "pre-existing: nested PodPool"
	warnPreExistingRender = `pre-existing issue in group "base": cannot render`
)

func errKeys(errs field.ErrorList) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, fmt.Sprintf("%s|%s|%s", e.Type, e.Field, e.Detail))
	}

	slices.Sort(out)

	return out
}

func sortedWarnings(in admission.Warnings) []string {
	out := append([]string(nil), in...)
	slices.Sort(out)

	return out
}

func TestDowngradePreExistingGlobalErrs(t *testing.T) {
	t.Parallel()

	tmplPath := field.NewPath("spec", "workloadTemplate")

	tests := []struct {
		name         string
		old          field.ErrorList
		new          field.ErrorList
		wantErrs     field.ErrorList
		wantWarnings admission.Warnings
	}{
		{
			name:         "matched field and detail downgrades to a warning",
			old:          field.ErrorList{field.Forbidden(tmplPath, "nested PodPool")},
			new:          field.ErrorList{field.Forbidden(tmplPath, "nested PodPool")},
			wantWarnings: admission.Warnings{warnPreExistingNested},
		},
		{
			name:     "new detail on an old field stays an error",
			old:      field.ErrorList{field.Forbidden(tmplPath, "nested PodPool")},
			new:      field.ErrorList{field.Forbidden(tmplPath, "template is not an object")},
			wantErrs: field.ErrorList{field.Forbidden(tmplPath, "template is not an object")},
		},
		{
			name:     "same detail on a different field stays an error",
			old:      field.ErrorList{field.Forbidden(tmplPath, "nested PodPool")},
			new:      field.ErrorList{field.Forbidden(field.NewPath("spec", "groups"), "nested PodPool")},
			wantErrs: field.ErrorList{field.Forbidden(field.NewPath("spec", "groups"), "nested PodPool")},
		},
		{
			// The global key is Field|Detail: a matching pair downgrades even
			// when the error Type changed between old and new.
			name:         "type is not part of the global match key",
			old:          field.ErrorList{field.Invalid(tmplPath, "v", "nested PodPool")},
			new:          field.ErrorList{field.Forbidden(tmplPath, "nested PodPool")},
			wantWarnings: admission.Warnings{warnPreExistingNested},
		},
		{
			name: "matched and new errors split into one warning and one error",
			old:  field.ErrorList{field.Forbidden(tmplPath, "nested PodPool")},
			new: field.ErrorList{
				field.Forbidden(tmplPath, "nested PodPool"),
				field.Forbidden(tmplPath, "template is not an object"),
			},
			wantErrs:     field.ErrorList{field.Forbidden(tmplPath, "template is not an object")},
			wantWarnings: admission.Warnings{warnPreExistingNested},
		},
		{
			name: "old errors with a clean update produce nothing",
			old:  field.ErrorList{field.Forbidden(tmplPath, "nested PodPool")},
		},
		{
			name: "both sides clean produce nothing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs, warnings := downgradePreExisting(
				renderedChildResult{globalErrs: tt.old},
				renderedChildResult{globalErrs: tt.new},
			)

			if got, want := errKeys(errs), errKeys(tt.wantErrs); !slices.Equal(got, want) {
				t.Errorf("errors = %v, want %v", got, want)
			}

			if got, want := sortedWarnings(warnings), sortedWarnings(tt.wantWarnings); !slices.Equal(got, want) {
				t.Errorf("warnings = %v, want %v", got, want)
			}
		})
	}
}

func TestDowngradePreExistingGroupErrs(t *testing.T) {
	t.Parallel()

	oldPath := field.NewPath("spec", "groups").Index(0)
	newPath := field.NewPath("spec", "groups").Index(2)

	tests := []struct {
		name         string
		old          map[string]field.ErrorList
		new          map[string]field.ErrorList
		wantErrs     field.ErrorList
		wantWarnings admission.Warnings
	}{
		{
			name:         "matched type and detail downgrades to a warning",
			old:          map[string]field.ErrorList{testGroupBase: {field.Invalid(oldPath, "v", "cannot render")}},
			new:          map[string]field.ErrorList{testGroupBase: {field.Invalid(oldPath, "v", "cannot render")}},
			wantWarnings: admission.Warnings{warnPreExistingRender},
		},
		{
			name:     "no old errors for the group escalates everything",
			old:      map[string]field.ErrorList{},
			new:      map[string]field.ErrorList{testGroupBase: {field.Invalid(oldPath, "v", "cannot render")}},
			wantErrs: field.ErrorList{field.Invalid(oldPath, "v", "cannot render")},
		},
		{
			name:     "old errors in a different group do not shield this one",
			old:      map[string]field.ErrorList{testGroupBurst: {field.Invalid(oldPath, "v", "cannot render")}},
			new:      map[string]field.ErrorList{testGroupBase: {field.Invalid(oldPath, "v", "cannot render")}},
			wantErrs: field.ErrorList{field.Invalid(oldPath, "v", "cannot render")},
		},
		{
			name:     "same detail with a different type stays an error",
			old:      map[string]field.ErrorList{testGroupBase: {field.Forbidden(oldPath, "cannot render")}},
			new:      map[string]field.ErrorList{testGroupBase: {field.Invalid(oldPath, "v", "cannot render")}},
			wantErrs: field.ErrorList{field.Invalid(oldPath, "v", "cannot render")},
		},
		{
			// The group key is Type|Detail: the group moving to another index
			// (a different Field path) must not defeat the downgrade, or
			// reordering spec.groups would reject previously accepted specs.
			name:         "field is not part of the group match key",
			old:          map[string]field.ErrorList{testGroupBase: {field.Invalid(oldPath, "v", "cannot render")}},
			new:          map[string]field.ErrorList{testGroupBase: {field.Invalid(newPath, "v", "cannot render")}},
			wantWarnings: admission.Warnings{warnPreExistingRender},
		},
		{
			name: "matched and new errors in one group split",
			old:  map[string]field.ErrorList{testGroupBase: {field.Invalid(oldPath, "v", "cannot render")}},
			new: map[string]field.ErrorList{testGroupBase: {
				field.Invalid(oldPath, "v", "cannot render"),
				field.Invalid(oldPath, "v", "type errors"),
			}},
			wantErrs:     field.ErrorList{field.Invalid(oldPath, "v", "type errors")},
			wantWarnings: admission.Warnings{warnPreExistingRender},
		},
		{
			name: "old errors with a clean update produce nothing",
			old:  map[string]field.ErrorList{testGroupBase: {field.Invalid(oldPath, "v", "cannot render")}},
			new:  map[string]field.ErrorList{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs, warnings := downgradePreExisting(
				renderedChildResult{groupErrs: tt.old},
				renderedChildResult{groupErrs: tt.new},
			)

			if got, want := errKeys(errs), errKeys(tt.wantErrs); !slices.Equal(got, want) {
				t.Errorf("errors = %v, want %v", got, want)
			}

			if got, want := sortedWarnings(warnings), sortedWarnings(tt.wantWarnings); !slices.Equal(got, want) {
				t.Errorf("warnings = %v, want %v", got, want)
			}
		})
	}
}
