package workload

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const helloStr = "hello"

func childObj(gen, obsGen int64, conditions []any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{fieldGeneration: gen},
	}}

	st := map[string]any{}
	if obsGen >= 0 {
		st["observedGeneration"] = obsGen
	}

	if conditions != nil {
		st["conditions"] = conditions
	}

	obj.Object[fieldStatus] = st

	return obj
}

func cond(typ, st, reason, message string) map[string]any {
	return map[string]any{
		"type":      typ,
		fieldStatus: st,
		"reason":    reason,
		"message":   message,
	}
}

func TestChildDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		child       *unstructured.Unstructured
		wantReason  string
		wantMessage string
		wantOK      bool
	}{
		{
			name:   "no status at all",
			child:  &unstructured.Unstructured{Object: map[string]any{}},
			wantOK: false,
		},
		{
			name:   "status but no conditions",
			child:  childObj(1, 1, nil),
			wantOK: false,
		},
		{
			name:   "empty conditions list",
			child:  childObj(1, 1, []any{}),
			wantOK: false,
		},
		{
			name: "generation not yet observed — suppress",
			child: childObj(3, 2, []any{
				cond("Available", condStatusFalse, "MinimumReplicasUnavailable", "not enough"),
			}),
			wantOK: false,
		},
		{
			name: "generation missing observedGeneration — assume current",
			child: childObj(2, -1, []any{
				cond("Available", condStatusFalse, "MinRepl", "msg"),
			}),
			wantReason:  "MinRepl",
			wantMessage: "msg",
			wantOK:      true,
		},
		{
			name: "ReplicaFailure True wins over Available False",
			child: childObj(2, 2, []any{
				cond("Available", condStatusFalse, "MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
				cond("ReplicaFailure", "True", "FailedCreate", `pods "x" is forbidden: exceeded quota`),
			}),
			wantReason:  "FailedCreate",
			wantMessage: `pods "x" is forbidden: exceeded quota`,
			wantOK:      true,
		},
		{
			name: "Available False with no ReplicaFailure",
			child: childObj(1, 1, []any{
				cond("Available", condStatusFalse, "MinimumReplicasUnavailable", "Deployment does not have minimum availability."),
				cond("Progressing", "True", "ReplicaSetUpdated", "updated"),
			}),
			wantReason:  "MinimumReplicasUnavailable",
			wantMessage: "Deployment does not have minimum availability.",
			wantOK:      true,
		},
		{
			name: "Ready False",
			child: childObj(1, 1, []any{
				cond("Ready", condStatusFalse, "NotReady", "waiting on readiness"),
			}),
			wantReason:  "NotReady",
			wantMessage: "waiting on readiness",
			wantOK:      true,
		},
		{
			name: "Ready Unknown",
			child: childObj(1, 1, []any{
				cond("Ready", "Unknown", "Probing", "health check pending"),
			}),
			wantReason:  "Probing",
			wantMessage: "health check pending",
			wantOK:      true,
		},
		{
			name: "Available Unknown",
			child: childObj(1, 1, []any{
				cond("Available", "Unknown", "Checking", "still checking"),
			}),
			wantReason:  "Checking",
			wantMessage: "still checking",
			wantOK:      true,
		},
		{
			name: "all conditions healthy — nothing to surface",
			child: childObj(1, 1, []any{
				cond("Available", "True", "MinimumReplicasAvailable", "Deployment has minimum availability."),
				cond("Progressing", condStatusFalse, "NewReplicaSetAvailable", "all done"),
			}),
			wantOK: false,
		},
		{
			name: "Progressing False is not surfaced",
			child: childObj(1, 1, []any{
				cond("Progressing", condStatusFalse, "NewReplicaSetAvailable", "done"),
			}),
			wantOK: false,
		},
		{
			name: "condition with empty reason and message is skipped",
			child: childObj(1, 1, []any{
				cond("Available", condStatusFalse, "", ""),
			}),
			wantOK: false,
		},
		{
			name: "reason only, no message",
			child: childObj(1, 1, []any{
				cond("Available", condStatusFalse, "SomeReason", ""),
			}),
			wantReason: "SomeReason",
			wantOK:     true,
		},
		{
			name: "conditions is a string not a list",
			child: func() *unstructured.Unstructured {
				obj := &unstructured.Unstructured{Object: map[string]any{
					"metadata": map[string]any{fieldGeneration: int64(1)},
					fieldStatus: map[string]any{
						"observedGeneration": int64(1),
						"conditions":         "not a list",
					},
				}}

				return obj
			}(),
			wantOK: false,
		},
		{
			name:   "condition element is a number not a map",
			child:  childObj(1, 1, []any{42}),
			wantOK: false,
		},
		{
			name: "condition type is a bool not a string",
			child: childObj(1, 1, []any{
				map[string]any{"type": true, "status": condStatusFalse, "reason": "X", "message": "Y"},
			}),
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reason, message, ok := ChildDetail(tt.child)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}

			if !tt.wantOK {
				return
			}

			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}

			if message != tt.wantMessage {
				t.Errorf("message = %q, want %q", message, tt.wantMessage)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string", helloStr, 10, helloStr},
		{"exact length", helloStr, 5, helloStr},
		{"truncate ascii", "hello world", 5, helloStr},
		{"truncate multi-byte", "héllo wörld", 5, "héllo"},
		{"zero max", "hello", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := TruncateRunes(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}

	t.Run("long message is bounded", func(t *testing.T) {
		t.Parallel()

		long := strings.Repeat("a", 1000)

		got := TruncateRunes(long, 512)
		if len(got) != 512 {
			t.Errorf("len = %d, want 512", len(got))
		}
	})
}
