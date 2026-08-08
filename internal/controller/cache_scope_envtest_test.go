package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

const testStranger = "stranger"

func managedByLabel() map[string]string {
	return map[string]string{workload.LabelManagedBy: workload.ManagerName}
}

// ---------------------------------------------------------------------------
// #8 — DefaultLabelSelector
// ---------------------------------------------------------------------------

var _ = Describe("#8 cache DefaultLabelSelector", func() {
	var ns string

	managedBySelector := func() labels.Selector {
		return labels.SelectorFromSet(labels.Set{workload.LabelManagedBy: workload.ManagerName})
	}

	BeforeEach(func() { ns = newTestNamespace("test-scoped-cache-") })

	It("caches labelled children and hides unlabelled ones", func() {
		makeDeployment(ns, "managed", managedByLabel())
		makeDeployment(ns, testStranger, nil)

		cl := startCache(cache.Options{DefaultLabelSelector: managedBySelector()})

		By("the labelled child is visible")
		Eventually(func(g Gomega) {
			g.Expect(cl.Get(ctx, types.NamespacedName{Name: "managed", Namespace: ns}, unstructuredDeployment())).To(Succeed())
		}).Should(Succeed())

		By("the unlabelled object reads as NotFound even though it exists")

		err := cl.Get(ctx, types.NamespacedName{Name: testStranger, Namespace: ns}, unstructuredDeployment())
		Expect(apierrors.IsNotFound(err)).To(BeTrue(),
			"a label-scoped cache reports absent, not filtered — this is the #8 hazard")

		By("the API server still has it")
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: testStranger, Namespace: ns}, &appsv1.Deployment{})).To(Succeed())

		By("List returns only the labelled child")

		ul := &unstructured.UnstructuredList{}
		ul.SetAPIVersion(testAppsV1)
		ul.SetKind(testDepKind + "List")
		Expect(cl.List(ctx, ul, client.InNamespace(ns))).To(Succeed())
		Expect(ul.Items).To(HaveLen(1))
		Expect(ul.Items[0].GetName()).To(Equal("managed"))
	})

	Context("opting the PodPool type out of the default selector", func() {
		makePool := func(name string) *podpoolsv1alpha1.PodPool {
			GinkgoHelper()

			minOne := int32(1)
			pool := &podpoolsv1alpha1.PodPool{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: podpoolsv1alpha1.PodPoolSpec{
					Replicas:         1,
					WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minOne}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())

			return pool
		}

		It("an empty ByObject entry does NOT opt out: nil Label cascades to the default", func() {
			pool := makePool("cascade-pool")

			cl := startCache(cache.Options{
				DefaultLabelSelector: managedBySelector(),
				ByObject: map[client.Object]cache.ByObject{
					&podpoolsv1alpha1.PodPool{}: {},
				},
			})

			err := cl.Get(ctx, client.ObjectKeyFromObject(pool), &podpoolsv1alpha1.PodPool{})
			Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"cache.defaultConfig copies DefaultLabelSelector into any ByObject entry whose "+
					"Label is nil, so an empty ByObject{} silently hides every PodPool")
		})

		It("Label: labels.Everything() does opt out and keeps PodPools visible", func() {
			pool := makePool("everything-pool")

			cl := startCache(cache.Options{
				DefaultLabelSelector: managedBySelector(),
				ByObject: map[client.Object]cache.ByObject{
					&podpoolsv1alpha1.PodPool{}: {Label: labels.Everything()},
				},
			})

			Eventually(func(g Gomega) {
				g.Expect(cl.Get(ctx, client.ObjectKeyFromObject(pool), &podpoolsv1alpha1.PodPool{})).To(Succeed())
			}).Should(Succeed(), "a non-nil selector stops the cascade")
		})

		It("the default selector still scopes child workloads when PodPool is opted out", func() {
			makeDeployment(ns, "managed", managedByLabel())
			makeDeployment(ns, testStranger, nil)

			cl := startCache(cache.Options{
				DefaultLabelSelector: managedBySelector(),
				ByObject: map[client.Object]cache.ByObject{
					&podpoolsv1alpha1.PodPool{}: {Label: labels.Everything()},
				},
			})

			Eventually(func(g Gomega) {
				g.Expect(cl.Get(ctx, types.NamespacedName{Name: "managed", Namespace: ns}, unstructuredDeployment())).To(Succeed())
			}).Should(Succeed())

			err := cl.Get(ctx, types.NamespacedName{Name: testStranger, Namespace: ns}, unstructuredDeployment())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})
})

// ---------------------------------------------------------------------------
// #8 hazard — the ownership check against a label-scoped cache (#55)
// ---------------------------------------------------------------------------

var _ = Describe("#8 ownership check under a label-scoped cache (#55)", func() {
	var ns string

	BeforeEach(func() { ns = newTestNamespace("test-scoped-owner-") })

	newPool := func(name string) *podpoolsv1alpha1.PodPool {
		return &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("pool-uid-" + name)},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         1,
				WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
				Groups:           []podpoolsv1alpha1.GroupSpec{{Name: testGroupBase}},
			},
		}
	}

	scopedReconciler := func() *PodPoolReconciler {
		GinkgoHelper()

		cl := startCache(cache.Options{
			DefaultLabelSelector: labels.SelectorFromSet(labels.Set{workload.LabelManagedBy: workload.ManagerName}),
			ByObject: map[client.Object]cache.ByObject{
				&podpoolsv1alpha1.PodPool{}: {Label: labels.Everything()},
			},
		})

		return &PodPoolReconciler{
			Client:    cl,
			Scheme:    scheme.Scheme,
			APIReader: k8sClient,
		}
	}

	renderedSelector := func(poolName string) map[string]string {
		return map[string]string{workload.LabelPool: poolName, workload.LabelGroup: testGroupBase}
	}

	It("refuses to adopt an unlabelled workload that the scoped cache cannot see", func() {
		const poolName = "adopt-pool"

		childName := poolName + "-" + testGroupBase

		By("a stranger already owns the name, with no podpool labels and no owner")

		stranger := makeDeploymentWithPodLabels(ns, childName, nil, renderedSelector(poolName))
		Expect(stranger.GetOwnerReferences()).To(BeEmpty())

		pool := newPool(poolName)
		tmpl, parseErr := workload.ParseTemplate(pool.Spec.WorkloadTemplate.Raw)
		Expect(parseErr).NotTo(HaveOccurred())

		desired, err := workload.BuildChildWorkload(
			tmpl, pool.Spec.Groups[0], pool, 1)
		Expect(err).NotTo(HaveOccurred())

		r := scopedReconciler()

		By("the scoped cache reports the stranger as absent")

		cacheErr := r.Get(ctx, types.NamespacedName{Name: childName, Namespace: ns}, unstructuredDeployment())
		Expect(apierrors.IsNotFound(cacheErr)).To(BeTrue())

		By("reconcileWorkload must still refuse it")

		_, err = r.reconcileWorkload(ctx, pool, desired)
		Expect(err).To(HaveOccurred(),
			"the cache miss must not be taken as proof of absence")
		Expect(err).To(BeAssignableToTypeOf(&workloadNotOwnedError{}))

		By("the stranger is untouched")

		var after appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: childName, Namespace: ns}, &after)).To(Succeed())
		Expect(after.GetOwnerReferences()).To(BeEmpty(), "ownership was stolen")
		Expect(after.GetLabels()).NotTo(HaveKey(workload.LabelPool), "the pool stamped its labels on a stranger")
	})

	It("still adopts a workload the pool already owns but which lost its labels", func() {
		const poolName = "relabel-pool"

		childName := poolName + "-" + testGroupBase

		pool := newPool(poolName)

		By("an owned child exists without the managed-by label")

		orphanedLabels := map[string]string{workload.LabelPool: poolName, workload.LabelGroup: testGroupBase}
		child := makeDeploymentWithPodLabels(ns, childName, orphanedLabels, renderedSelector(poolName))
		child.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: podpoolsv1alpha1.SchemeGroupVersion.String(),
			Kind:       workload.KindPodPool,
			Name:       pool.Name,
			UID:        pool.UID,
			Controller: ptr.To(true),
		}}
		Expect(k8sClient.Update(ctx, child)).To(Succeed())

		tmpl, parseErr := workload.ParseTemplate(pool.Spec.WorkloadTemplate.Raw)
		Expect(parseErr).NotTo(HaveOccurred())

		desired, err := workload.BuildChildWorkload(
			tmpl, pool.Spec.Groups[0], pool, 1)
		Expect(err).NotTo(HaveOccurred())

		r := scopedReconciler()

		_, err = r.reconcileWorkload(ctx, pool, desired)
		Expect(err).NotTo(HaveOccurred(), "an owned child must be re-adopted, not rejected")

		By("the apply restores the managed-by label so the cache can see it again")

		var after appsv1.Deployment
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: childName, Namespace: ns}, &after)).To(Succeed())
		Expect(after.GetLabels()).To(HaveKeyWithValue(workload.LabelManagedBy, workload.ManagerName))
	})
})
