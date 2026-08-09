package controller

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

func statefulSetTemplateJSON(containerName string) runtime.RawExtension {
	tmpl := map[string]any{
		fieldAPIVersion: testAppsV1,
		fieldKind:       testStsKind,
		fieldSpec: map[string]any{
			"serviceName": "headless",
			fieldTemplate: map[string]any{
				fieldSpec: map[string]any{
					fieldContainers: []any{
						map[string]any{
							fieldName:  containerName,
							fieldImage: testImageNginx,
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(tmpl)

	return runtime.RawExtension{Raw: raw}
}

// The controller suite runs no webhook, which is the bypass path these tests
// exercise: a GVK change that reaches the controller without admission
// blocking it.
var _ = Describe("GVK swap orphan sweep", func() {
	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-sweep-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name
	})

	It("should delete the old Deployment when the template swaps to StatefulSet", func() {
		const poolName = "swap-pool"

		poolKey := types.NamespacedName{Name: poolName, Namespace: ns}
		depKey := types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: ns}
		stsKey := types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: ns}

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

		By("waiting for the Deployment child to exist")
		Eventually(func() error {
			return k8sClient.Get(ctx, depKey, &appsv1.Deployment{})
		}).Should(Succeed())

		By("waiting for status to record the Deployment workloadRef")
		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, poolKey, &p)).To(Succeed())
			g.Expect(p.Status.Groups).To(HaveLen(1))
			g.Expect(p.Status.Groups[0].WorkloadRef).NotTo(BeNil())
			g.Expect(p.Status.Groups[0].WorkloadRef.Kind).To(Equal(testDepKind))
		}).Should(Succeed())

		By("swapping the template to StatefulSet")
		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, poolKey, &p)).To(Succeed())
			p.Spec.WorkloadTemplate = statefulSetTemplateJSON(testContainer)
			g.Expect(k8sClient.Update(ctx, &p)).To(Succeed())
		}).Should(Succeed())

		By("waiting for the StatefulSet to appear")
		Eventually(func() error {
			return k8sClient.Get(ctx, stsKey, &appsv1.StatefulSet{})
		}).Should(Succeed())

		By("waiting for the old Deployment to be swept")
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, depKey, &appsv1.Deployment{}))
		}).Should(BeTrue())
	})

	It("should preserve the old Deployment while the StatefulSet replacement fails", func() {
		const poolName = "fail-swap-pool"

		poolKey := types.NamespacedName{Name: poolName, Namespace: ns}
		depKey := types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: ns}
		stsKey := types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: ns}

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

		By("waiting for the Deployment child and a stable status")
		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, poolKey, &p)).To(Succeed())
			g.Expect(p.Status.Groups).To(HaveLen(1))
			g.Expect(p.Status.Groups[0].WorkloadRef).NotTo(BeNil())
			g.Expect(p.Status.Groups[0].WorkloadRef.Kind).To(Equal(testDepKind))
		}).Should(Succeed())

		By("swapping to a StatefulSet with an invalid podManagementPolicy")

		invalidStsTemplate := func() runtime.RawExtension {
			tmpl := map[string]any{
				fieldAPIVersion: testAppsV1,
				fieldKind:       "StatefulSet",
				fieldSpec: map[string]any{
					"serviceName":         "headless",
					"podManagementPolicy": "Bogus",
					fieldTemplate: map[string]any{
						fieldSpec: map[string]any{
							fieldContainers: []any{
								map[string]any{
									fieldName:  testContainer,
									fieldImage: testImageNginx,
								},
							},
						},
					},
				},
			}
			raw, _ := json.Marshal(tmpl)

			return runtime.RawExtension{Raw: raw}
		}

		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, poolKey, &p)).To(Succeed())
			p.Spec.WorkloadTemplate = invalidStsTemplate()
			g.Expect(k8sClient.Update(ctx, &p)).To(Succeed())
		}).Should(Succeed())

		By("verifying the old Deployment survives while the replacement fails")
		Consistently(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, depKey, &appsv1.Deployment{})).To(Succeed())
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, stsKey, &appsv1.StatefulSet{}))).To(BeTrue())
		}, 3*time.Second, 250*time.Millisecond).Should(Succeed())

		By("fixing the template to a valid StatefulSet")
		Eventually(func(g Gomega) {
			var p podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, poolKey, &p)).To(Succeed())
			p.Spec.WorkloadTemplate = statefulSetTemplateJSON(testContainer)
			g.Expect(k8sClient.Update(ctx, &p)).To(Succeed())
		}).Should(Succeed())

		By("waiting for the StatefulSet to appear and the Deployment to be swept")
		Eventually(func() error {
			return k8sClient.Get(ctx, stsKey, &appsv1.StatefulSet{})
		}).Should(Succeed())

		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, depKey, &appsv1.Deployment{}))
		}).Should(BeTrue())
	})
})
