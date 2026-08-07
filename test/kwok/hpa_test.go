//go:build kwok

package kwok

import (
	"context"
	"fmt"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// A PodPool declares a /scale subresource, which is what lets an
// HorizontalPodAutoscaler treat it as a scale target. Nothing in this project
// implements that — it is three JSON paths in a kubebuilder marker — so the
// only way to know it works is to point a real HPA at a real PodPool and watch.
//
// envtest cannot do this: it runs kube-apiserver but no kube-controller-manager,
// so there is no HPA controller. A kwok cluster runs the full control plane,
// which is why this lives here.
//
// There is no metrics-server, so metric-driven scaling cannot be exercised.
// That is fine — metric computation is Kubernetes' code, not ours. What is ours
// is whether the HPA can read our scale subresource and whether a write to it
// redistributes correctly, and the min/max bounds reach both: the HPA applies
// them before it ever consults a metric.

func hpaCleanup(t *testing.T, name string) {
	t.Helper()

	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
	}
	_ = k8sClient.Delete(context.Background(), hpa)
}

// waitForPoolReplicas polls spec.replicas, which is what the HPA writes.
func waitForPoolReplicas(t *testing.T, name string, want int32) {
	t.Helper()

	var last int32 = -1

	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		pool := &podpoolsv1alpha1.PodPool{}
		if err := k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: testNamespace}, pool); err == nil {
			last = pool.Spec.Replicas
		}

		return last == want, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for %s spec.replicas=%d, last saw %d", name, want, last)
	}
}

func waitForHPACondition(t *testing.T, name string, condType autoscalingv2.HorizontalPodAutoscalerConditionType) autoscalingv2.HorizontalPodAutoscalerCondition {
	t.Helper()

	var found autoscalingv2.HorizontalPodAutoscalerCondition

	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		hpa := &autoscalingv2.HorizontalPodAutoscaler{}
		if err := k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: testNamespace}, hpa); err == nil {
			for _, c := range hpa.Status.Conditions {
				if c.Type == condType {
					found = c

					return true, nil
				}
			}
		}

		return false, nil
	})
	if err != nil {
		t.Fatalf("timed out waiting for HPA condition %s on %s", condType, name)
	}

	return found
}

func TestHPAScalesPodPool(t *testing.T) {
	const name = "kwok-hpa"

	cleanupPodPool(t, name)

	hpaCleanup(t, name)
	defer cleanupPodPool(t, name)
	defer hpaCleanup(t, name)

	ctx := context.Background()

	pool := &podpoolsv1alpha1.PodPool{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: podpoolsv1alpha1.PodPoolSpec{
			Replicas:         6,
			WorkloadTemplate: makeWorkloadTemplate("nginx:latest"),
			Groups: []podpoolsv1alpha1.GroupSpec{
				{
					Name:      "on-demand",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](2)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "on-demand"}),
				},
				{
					Name:      "spot",
					Scaling:   podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0), Target: pctTarget(70)},
					Overrides: schedulingOverride(map[string]string{"capacity-type": "spot"}),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pool); err != nil {
		t.Fatalf("creating pool: %v", err)
	}

	waitForPoolReplicas(t, name, 6)

	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: podpoolsv1alpha1.SchemeGroupVersion.String(),
				Kind:       "PodPool",
				Name:       name,
			},
			MinReplicas: ptr.To[int32](3),
			MaxReplicas: 30,
		},
	}
	if err := k8sClient.Create(ctx, hpa); err != nil {
		t.Fatalf("creating HPA: %v", err)
	}

	// SucceededGetScale is the whole point: the HPA controller resolved a CRD
	// it knows nothing about, through the scale subresource, and read it. A
	// typo in any of the three marker paths surfaces here.
	t.Run("HPA can read the scale subresource", func(t *testing.T) {
		cond := waitForHPACondition(t, name, autoscalingv2.AbleToScale)
		if cond.Status != "True" {
			t.Fatalf("AbleToScale=%s (%s): %s", cond.Status, cond.Reason, cond.Message)
		}

		t.Logf("AbleToScale=True reason=%s", cond.Reason)
	})

	// Below minReplicas and above maxReplicas are both applied before any
	// metric is consulted, so they exercise the HPA's write path without a
	// metrics server.
	t.Run("HPA raises a pool below minReplicas", func(t *testing.T) {
		scalePool(t, name, 1)
		waitForPoolReplicas(t, name, 3)
		assertGroupReplicas(t, name, map[string]int32{"on-demand": 2, "spot": 1})
	})

	t.Run("HPA lowers a pool above maxReplicas", func(t *testing.T) {
		scalePool(t, name, 40)
		waitForPoolReplicas(t, name, 30)
		// spot is capped at 70% of 30 = 21; the remaining 9 fall to the
		// overflow bucket, which is the first group.
		assertGroupReplicas(t, name, map[string]int32{"on-demand": 9, "spot": 21})
	})
}

// scalePool writes spec.replicas directly, standing in for whatever pushed the
// pool out of the HPA's range.
func scalePool(t *testing.T, name string, replicas int32) {
	t.Helper()

	err := pollFor(30*time.Second, func(ctx context.Context) (bool, error) {
		pool := &podpoolsv1alpha1.PodPool{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, pool); err != nil {
			return false, fmt.Errorf("getting pool: %w", err)
		}

		pool.Spec.Replicas = replicas

		// Conflict: the HPA may be writing at the same moment. Re-read and retry.
		return k8sClient.Update(ctx, pool) == nil, nil
	})
	if err != nil {
		t.Fatalf("scaling %s to %d: %v", name, replicas, err)
	}
}

func assertGroupReplicas(t *testing.T, name string, want map[string]int32) {
	t.Helper()

	var last map[string]int32

	err := pollFor(pollTimeout, func(ctx context.Context) (bool, error) {
		pool := &podpoolsv1alpha1.PodPool{}
		if err := k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: testNamespace}, pool); err != nil {
			return false, nil //nolint:nilerr // transient read failure: keep polling
		}

		got := map[string]int32{}
		for _, g := range pool.Status.Groups {
			got[g.Name] = g.Replicas
		}

		last = got

		all := len(got) == len(want)
		for k, v := range want {
			if got[k] != v {
				all = false
			}
		}

		return all, nil
	})
	if err != nil {
		t.Fatalf("group replicas: got %v, want %v", last, want)
	}
}
