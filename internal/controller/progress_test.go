package controller

import (
	"math"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

func TestStampGroupProgress(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-5 * time.Minute)
	stamp := &metav1.Time{Time: earlier}

	tests := []struct {
		name       string
		gs         podpoolsv1alpha1.GroupStatus
		prev       *podpoolsv1alpha1.GroupStatus
		genChanged bool
		wantStamp  bool // true = now, false = prev's value (or nil)
		wantNil    bool // true = cleared
	}{
		{
			name:      "shortfall first appears, no prev",
			gs:        podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 0},
			prev:      nil,
			wantStamp: true,
		},
		{
			name: "shortfall first appears, was at target",
			gs:   podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 0},
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 3, ReadyReplicas: 3,
			},
			wantStamp: true,
		},
		{
			name: "progress — shortfall decreases",
			gs:   podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 2},
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 3, ReadyReplicas: 0,
				LastProgressTime: stamp,
			},
			wantStamp: true,
		},
		{
			name: "shortfall unchanged — clock runs",
			gs:   podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 1},
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 3, ReadyReplicas: 1,
				LastProgressTime: stamp,
			},
			wantStamp: false,
		},
		{
			name: "shortfall cleared — at target",
			gs:   podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 3},
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 3, ReadyReplicas: 1,
				LastProgressTime: stamp,
			},
			wantNil: true,
		},
		{
			name: "regression — ready fell, shortfall grew, must not restamp",
			gs:   podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 0},
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 3, ReadyReplicas: 2,
				LastProgressTime: stamp,
			},
			wantStamp: false,
		},
		{
			name:       "generation changed — fresh rollout, same target",
			gs:         podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 1},
			genChanged: true,
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 3, ReadyReplicas: 1,
				LastProgressTime: stamp,
			},
			wantStamp: true,
		},
		{
			name: "target increased — new work assigned",
			gs:   podpoolsv1alpha1.GroupStatus{TargetReplicas: 5, ReadyReplicas: 1},
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 3, ReadyReplicas: 1,
				LastProgressTime: stamp,
			},
			wantStamp: true,
		},
		{
			name: "scale-down overshoot — ready exceeds target",
			gs:   podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 5},
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 5, ReadyReplicas: 5,
			},
			wantNil: true,
		},
		{
			name: "upgrade path — prev has zero target (field absent before this version)",
			gs:   podpoolsv1alpha1.GroupStatus{TargetReplicas: 3, ReadyReplicas: 1},
			prev: &podpoolsv1alpha1.GroupStatus{
				TargetReplicas: 0, ReadyReplicas: 1,
			},
			wantStamp: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stampGroupProgress(&tt.gs, tt.prev, tt.genChanged, now)

			switch {
			case tt.wantNil:
				if tt.gs.LastProgressTime != nil {
					t.Errorf("want nil LastProgressTime, got %v", tt.gs.LastProgressTime.Time)
				}
			case tt.wantStamp:
				if tt.gs.LastProgressTime == nil {
					t.Fatal("want LastProgressTime stamped to now, got nil")
				}

				if !tt.gs.LastProgressTime.Time.Equal(now) {
					t.Errorf("want stamp at %v, got %v", now, tt.gs.LastProgressTime.Time)
				}
			default:
				if tt.prev != nil && tt.prev.LastProgressTime != nil {
					if tt.gs.LastProgressTime == nil {
						t.Fatal("want LastProgressTime carried forward, got nil")
					}

					if !tt.gs.LastProgressTime.Time.Equal(tt.prev.LastProgressTime.Time) {
						t.Errorf("want stamp carried forward at %v, got %v",
							tt.prev.LastProgressTime.Time, tt.gs.LastProgressTime.Time)
					}
				}
			}
		})
	}
}

func TestSetConditionsProgressDeadlineExceeded(t *testing.T) {
	t.Run("stalled groups override ReplicasUpdating", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{}
		pool.Generation = 1
		setConditions(pool, conditionInputs{ready: 5, desired: 10, stalledGroups: []string{testGroupBase}})

		got := conditionByType(pool, ConditionProgressing)
		if got == nil {
			t.Fatal("Progressing was not set")
		}

		if got.Status != metav1.ConditionFalse {
			t.Errorf("status = %s, want False", got.Status)
		}

		if got.Reason != ReasonProgressDeadlineExceeded {
			t.Errorf("reason = %s, want %s", got.Reason, ReasonProgressDeadlineExceeded)
		}
	})

	t.Run("unplaced outranks stalled", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{}
		pool.Generation = 1
		setConditions(pool, conditionInputs{unplaced: 3, ready: 5, desired: 10, stalledGroups: []string{testGroupBase}})

		got := conditionByType(pool, ConditionProgressing)
		if got.Reason != ReasonCeilingsBelowDesired {
			t.Errorf("reason = %s, want %s (unplaced outranks stalled)", got.Reason, ReasonCeilingsBelowDesired)
		}
	})

	t.Run("stalled when ready equals desired", func(t *testing.T) {
		// A group can be stalled while the pool's total is met, because
		// redistribution can leave one group short while another runs over.
		pool := &podpoolsv1alpha1.PodPool{}
		pool.Generation = 1
		setConditions(pool, conditionInputs{ready: 10, desired: 10, stalledGroups: []string{testGroupBase}})

		got := conditionByType(pool, ConditionProgressing)
		if got.Reason != ReasonProgressDeadlineExceeded {
			t.Errorf("reason = %s, want %s", got.Reason, ReasonProgressDeadlineExceeded)
		}
	})

	t.Run("no stalled groups, before deadline", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{}
		pool.Generation = 1
		setConditions(pool, conditionInputs{ready: 5, desired: 10})

		got := conditionByType(pool, ConditionProgressing)
		if got.Reason != ReasonReplicasUpdating {
			t.Errorf("reason = %s, want %s", got.Reason, ReasonReplicasUpdating)
		}
	})

	t.Run("message names stalled groups", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{}
		pool.Generation = 1
		setConditions(pool, conditionInputs{ready: 5, desired: 10, stalledGroups: []string{"alpha", "bravo"}})

		got := conditionByType(pool, ConditionProgressing)
		if got.Message != "Group(s) alpha, bravo exceeded progress deadline" {
			t.Errorf("message = %q", got.Message)
		}
	})
}

func TestFormatGroupNames(t *testing.T) {
	tests := []struct {
		groups []string
		want   string
	}{
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a, b"},
		{[]string{"a", "b", "c"}, "a, b +1 more"},
		{[]string{"a", "b", "c", "d", "e"}, "a, b +3 more"},
	}
	for _, tt := range tests {
		if got := formatGroupNames(tt.groups); got != tt.want {
			t.Errorf("formatGroupNames(%v) = %q, want %q", tt.groups, got, tt.want)
		}
	}
}

func TestRequeueAfter(t *testing.T) {
	t.Run("floor with jitter", func(t *testing.T) {
		d := requeueAfter()
		if d == 0 {
			t.Fatal("a pool must not get zero requeue")
		}
		// Jittered around reconcileFloor ± 10%.
		if d < reconcileFloor*90/100 || d > reconcileFloor*110/100+time.Second {
			t.Errorf("requeue = %v, want within 10%% of %v", d, reconcileFloor)
		}
	})
}

func TestProgressDeadline(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{}
		if got := progressDeadline(pool); got != 600*time.Second {
			t.Errorf("default deadline = %v, want 600s", got)
		}
	})

	t.Run("custom", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{
			Spec: podpoolsv1alpha1.PodPoolSpec{
				ProgressDeadlineSeconds: ptr.To(int32(120)),
			},
		}
		if got := progressDeadline(pool); got != 120*time.Second {
			t.Errorf("custom deadline = %v, want 120s", got)
		}
	})

	t.Run("MaxInt32 disabled", func(t *testing.T) {
		pool := &podpoolsv1alpha1.PodPool{
			Spec: podpoolsv1alpha1.PodPoolSpec{
				ProgressDeadlineSeconds: ptr.To(int32(math.MaxInt32)),
			},
		}
		if hasProgressDeadline(pool) {
			t.Error("MaxInt32 should disable the deadline")
		}
	})
}
