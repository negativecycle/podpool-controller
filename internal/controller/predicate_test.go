package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

func pool(gen int64, annotations map[string]string, labels map[string]string) *podpoolsv1alpha1.PodPool {
	return &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-pool",
			Namespace:   "default",
			Generation:  gen,
			Annotations: annotations,
			Labels:      labels,
		},
	}
}

func TestPoolPredicate_Update(t *testing.T) {
	p := poolPredicate()

	ann := func(kv ...string) map[string]string {
		m := make(map[string]string, len(kv)/2)
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}

		return m
	}
	lbl := ann

	tests := []struct {
		name string
		old  *podpoolsv1alpha1.PodPool
		new  *podpoolsv1alpha1.PodPool
		want bool
	}{
		{
			name: "status-only change filters out",
			old:  pool(1, nil, nil),
			new:  pool(1, nil, nil),
			want: false,
		},
		{
			name: "identical objects (resync) filters out",
			old:  pool(1, ann("k1", "v1"), lbl("k2", "v2")),
			new:  pool(1, ann("k1", "v1"), lbl("k2", "v2")),
			want: false,
		},
		{
			name: "generation bumped passes",
			old:  pool(1, nil, nil),
			new:  pool(2, nil, nil),
			want: true,
		},
		{
			name: "annotation added passes",
			old:  pool(1, nil, nil),
			new:  pool(1, ann("new-ann", "v"), nil),
			want: true,
		},
		{
			name: "annotation changed passes",
			old:  pool(1, ann("mut-ann", "before"), nil),
			new:  pool(1, ann("mut-ann", "after"), nil),
			want: true,
		},
		{
			name: "annotation removed passes",
			old:  pool(1, ann("del-ann", "v"), nil),
			new:  pool(1, nil, nil),
			want: true,
		},
		{
			name: "label added passes",
			old:  pool(1, nil, nil),
			new:  pool(1, nil, lbl("new-lbl", "v")),
			want: true,
		},
		{
			name: "label changed passes",
			old:  pool(1, nil, lbl("mut-lbl", "before")),
			new:  pool(1, nil, lbl("mut-lbl", "after")),
			want: true,
		},
		{
			name: "label removed passes",
			old:  pool(1, nil, lbl("del-lbl", "v")),
			new:  pool(1, nil, nil),
			want: true,
		},
		{
			name: "annotation unchanged with status change filters out",
			old:  pool(1, ann("stable", "v"), nil),
			new:  pool(1, ann("stable", "v"), nil),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.Update(event.UpdateEvent{
				ObjectOld: tt.old,
				ObjectNew: tt.new,
			})
			if got != tt.want {
				t.Errorf("Update() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPoolPredicate_CreateDeleteGeneric(t *testing.T) {
	p := poolPredicate()
	obj := pool(1, nil, nil)

	if !p.Create(event.CreateEvent{Object: obj}) {
		t.Error("Create() should return true")
	}

	if !p.Delete(event.DeleteEvent{Object: obj}) {
		t.Error("Delete() should return true")
	}

	if !p.Generic(event.GenericEvent{Object: obj}) {
		t.Error("Generic() should return true")
	}
}
