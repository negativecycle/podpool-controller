package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// Runs against a real API server because the fake client cannot hold an object
// in terminating: the race under test is the controller recreating a child
// between the deletionTimestamp landing and the object going away.
var _ = Describe("Terminating pool", func() {
	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-deletion-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name
	})

	It("does not recreate a child deleted while the pool is terminating", func() {
		const poolName = "terminating-pool"

		poolKey := types.NamespacedName{Name: poolName, Namespace: ns}

		minTwo := int32(2)
		pool := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      poolName,
				Namespace: ns,
				// Stands in for any third-party finalizer; what matters is the
				// pool sits in terminating instead of vanishing.
				Finalizers: []string{"test.podpools.dev/hold"},
			},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         2,
				WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())

		// The finalizer must come off even when the assertion fails, or the
		// namespace wedges in terminating.
		DeferCleanup(func() {
			Eventually(func() error {
				var p podpoolsv1alpha1.PodPool
				if err := k8sClient.Get(ctx, poolKey, &p); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}

					return err
				}

				p.Finalizers = nil

				return k8sClient.Update(ctx, &p)
			}).Should(Succeed())
		})

		childKey := types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: ns}

		var child appsv1.Deployment

		Eventually(func() error {
			return k8sClient.Get(ctx, childKey, &child)
		}).Should(Succeed())

		Expect(k8sClient.Delete(ctx, pool)).To(Succeed())

		// envtest runs no garbage collector, so deleting the child by hand
		// stands in for the foreground GC's cascade.
		Expect(k8sClient.Delete(ctx, &child)).To(Succeed())

		// The pool-delete event alone enqueues a pass; without the guard the
		// recreate lands in well under a second, so 5s is generous without
		// being slow.
		Consistently(func() error {
			return k8sClient.Get(ctx, childKey, &appsv1.Deployment{})
		}, 5*time.Second, 250*time.Millisecond).ShouldNot(Succeed(),
			"child must not be recreated while the pool is terminating")
	})
})
