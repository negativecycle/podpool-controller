package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// The unit specs prove ensureWatch re-registers on a replaced informer. This
// one proves the consequence the pool actually cares about: that child status
// still reaches the pool afterwards. A watch can be re-registered correctly
// and still deliver nothing if it went onto the wrong object.
var _ = Describe("Watch liveness verification", func() {
	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-watch-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name
	})

	It("should recover from a removed informer and resume observing child status", func() {
		const poolName = "blind-pool"

		poolKey := types.NamespacedName{Name: poolName, Namespace: ns}
		childKey := types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: ns}

		minTwo := int32(2)
		pool := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         2,
				WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())

		By("waiting for child Deployment and a settled status")
		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, poolKey, &p)).To(Succeed())
			g.Expect(p.Status.Groups).To(HaveLen(1))
		}).Should(Succeed())

		By("removing the Deployment informer to simulate death and replacement")

		depU := &unstructured.Unstructured{}
		depU.SetGroupVersionKind(schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind})
		Expect(reconciler.Cache.RemoveInformer(ctx, depU)).To(Succeed())

		// The pool has no live child watch at this instant, so nothing would
		// wake it on its own. The nudge stands in for whatever does in
		// production, and the wait matters: everything after this point has to
		// happen on the far side of the pass that re-establishes the watch, or
		// the pool could observe the child by chance rather than by
		// subscription.
		By("nudging the pool and waiting for the pass that re-establishes the watch")

		countBefore := reconcileCount()

		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, poolKey, &p)).To(Succeed())

			if p.Annotations == nil {
				p.Annotations = map[string]string{}
			}

			p.Annotations["test.podpools.dev/nudge"] = "recover"
			g.Expect(k8sClient.Update(ctx, &p)).To(Succeed())
		}).Should(Succeed())

		Eventually(reconcileCount).Should(BeNumerically(">", countBefore),
			"the nudge never produced a reconcile")

		By("simulating the child becoming ready, with nothing nudging the pool")
		Eventually(func(g Gomega) {
			var dep appsv1.Deployment
			g.Expect(k8sClient.Get(ctx, childKey, &dep)).To(Succeed())
			dep.Status.Replicas = 2
			dep.Status.ReadyReplicas = 2
			dep.Status.UpdatedReplicas = 2
			g.Expect(k8sClient.Status().Update(ctx, &dep)).To(Succeed())
		}).Should(Succeed())

		// Only the child watch can deliver this. The pool was not touched
		// again, and its own requeue is minutes away.
		By("verifying the pool status reflects the child's ready replicas")
		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, poolKey, &p)).To(Succeed())
			g.Expect(p.Status.ReadyReplicas).To(Equal(int32(2)),
				fmt.Sprintf("pool status: %+v", p.Status))
		}).Should(Succeed())
	})
})
