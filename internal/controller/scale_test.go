package controller

// The scale subresource is declared entirely by kubebuilder markers, with no
// Go behind it: a typo in any of the three paths compiles, generates, and
// deploys, then fails only when somebody points an autoscaler at the CRD.
// These specs cover what envtest can see.

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

var _ = Describe("The scale subresource", func() {
	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-scale-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name
	})

	It("serves the pool's selector through /scale", func() {
		minTwo := int32(2)
		pool := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: "scale-pool", Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         2,
				WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())

		Eventually(func(g Gomega) {
			var scale autoscalingv1.Scale
			g.Expect(k8sClient.SubResource("scale").Get(ctx, pool, &scale)).To(Succeed())
			g.Expect(scale.Spec.Replicas).To(Equal(int32(2)))
			g.Expect(scale.Status.Selector).To(Equal(workload.LabelPool + "=scale-pool"))
		}).Should(Succeed())
	})

	It("exposes the selector even when the pool cannot be reconciled", func() {
		// The selectorpath reads status.selector, and an HPA reads /scale on
		// its own schedule with no idea whether the pool is healthy. A pool
		// whose template cannot even be parsed must still serve a selector:
		// the field is derived from the pool name alone, so there is no
		// reason for any early exit to leave it empty.
		pool := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: "broken-pool", Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         1,
				WorkloadTemplate: runtime.RawExtension{Raw: []byte(`{"spec": {}}`)},
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())

		Eventually(func(g Gomega) {
			var scale autoscalingv1.Scale
			g.Expect(k8sClient.SubResource("scale").Get(ctx, pool, &scale)).To(Succeed())
			g.Expect(scale.Status.Selector).To(Equal(workload.LabelPool + "=broken-pool"))
		}).Should(Succeed())
	})
})
