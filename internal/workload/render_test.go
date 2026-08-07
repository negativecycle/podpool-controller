package workload

import (
	"testing"
)

const (
	testGroupBurst = "burst"
)

func TestChildName(t *testing.T) {
	t.Parallel()

	if got := ChildName("my-pool", testGroupBurst); got != "my-pool-burst" {
		t.Errorf("ChildName = %q, want %q", got, "my-pool-burst")
	}
}

func TestExtractGVK(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"deployment", `{"apiVersion":"apps/v1","kind":"Deployment"}`, "apps/v1, Kind=Deployment", false},
		{"core group", `{"apiVersion":"v1","kind":"Pod"}`, "/v1, Kind=Pod", false},
		{"crd group", `{"apiVersion":"argoproj.io/v1alpha1","kind":"Rollout"}`, "argoproj.io/v1alpha1, Kind=Rollout", false},
		{"missing apiVersion", `{"kind":"Deployment"}`, "", true},
		{"missing kind", `{"apiVersion":"apps/v1"}`, "", true},
		{"malformed json", `{`, "", true},
		{"unparseable group version", `{"apiVersion":"a/b/c","kind":"X"}`, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gvk, err := ExtractGVK([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractGVK(%q) succeeded with %v, want error", tt.raw, gvk)
				}

				return
			}

			if err != nil {
				t.Fatalf("ExtractGVK(%q): %v", tt.raw, err)
			}

			if got := gvk.String(); got != tt.want {
				t.Errorf("ExtractGVK(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
