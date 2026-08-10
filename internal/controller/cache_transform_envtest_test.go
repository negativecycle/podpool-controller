package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

const testFieldManager = "kubectl"

// startCache brings up a cache with the given options against the suite's
// envtest API server and returns a client that reads through it.
func startCache(opts cache.Options) client.Client {
	GinkgoHelper()

	opts.Scheme = scheme.Scheme
	c, err := cache.New(cfg, opts)
	Expect(err).NotTo(HaveOccurred())

	cctx, ccancel := context.WithCancel(ctx)
	DeferCleanup(ccancel)

	go func() {
		defer GinkgoRecover()

		_ = c.Start(cctx)
	}()

	Expect(c.WaitForCacheSync(cctx)).To(BeTrue())

	cl, err := client.New(cfg, client.Options{
		Scheme: scheme.Scheme,
		Cache:  &client.CacheOptions{Reader: c, Unstructured: true},
	})
	Expect(err).NotTo(HaveOccurred())

	return cl
}

func newTestNamespace(prefix string) string {
	GinkgoHelper()

	nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix}}
	Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())

	return nsObj.Name
}

func makeDeployment(ns, name string, objLabels map[string]string) {
	GinkgoHelper()
	makeDeploymentWithPodLabels(ns, name, objLabels, map[string]string{"app": name})
}

func makeDeploymentWithPodLabels(ns, name string, objLabels, podLabels map[string]string) *appsv1.Deployment {
	GinkgoHelper()

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    objLabels,
			Annotations: map[string]string{
				corev1.LastAppliedConfigAnnotation: `{"apiVersion":"apps/v1","kind":"Deployment","spec":{"replicas":1}}`,
				"keep.me/annotation":               "kept",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: podLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: testContainer, Image: testImageNginx}},
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, dep)).To(Succeed())

	return dep
}

func unstructuredDeployment() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(testAppsV1)
	u.SetKind(testDepKind)

	return u
}

var _ = Describe("#32 cache DefaultTransform", func() {
	Context("the transform function in isolation", func() {
		It("strips managedFields and the last-applied-configuration annotation", func() {
			u := unstructuredDeployment()
			u.SetName("x")
			u.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: testFieldManager, Operation: metav1.ManagedFieldsOperationApply}})
			u.SetAnnotations(map[string]string{
				corev1.LastAppliedConfigAnnotation: "{}",
				"keep.me/annotation":               "kept",
			})

			out, err := TransformStripCacheWeight()(u)
			Expect(err).NotTo(HaveOccurred())

			got := out.(*unstructured.Unstructured)
			Expect(got.GetManagedFields()).To(BeNil())
			Expect(got.GetAnnotations()).NotTo(HaveKey(corev1.LastAppliedConfigAnnotation))
			Expect(got.GetAnnotations()).To(HaveKeyWithValue("keep.me/annotation", "kept"))
		})

		It("preserves the fields the controller actually reads", func() {
			u := unstructuredDeployment()
			u.SetName("x")
			u.SetLabels(map[string]string{workload.LabelPool: "p", workload.LabelGroup: "g", workload.LabelManagedBy: workload.ManagerName})
			u.SetOwnerReferences([]metav1.OwnerReference{{
				APIVersion: podpoolsv1alpha1.SchemeGroupVersion.String(),
				Kind:       workload.KindPodPool, Name: "p", UID: types.UID("u"),
			}})
			Expect(unstructured.SetNestedField(u.Object, int64(3), "status", "readyReplicas")).To(Succeed())
			u.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: testFieldManager}})

			out, err := TransformStripCacheWeight()(u)
			Expect(err).NotTo(HaveOccurred())

			got := out.(*unstructured.Unstructured)
			Expect(got.GetLabels()).To(HaveKeyWithValue(workload.LabelGroup, "g"))
			Expect(got.GetOwnerReferences()).To(HaveLen(1))
			ready, found := workload.ReadInt32(got, "status", "readyReplicas")
			Expect(found).To(BeTrue())
			Expect(ready).To(BeEquivalentTo(3))
		})

		It("is idempotent and safe on objects with neither field", func() {
			u := unstructuredDeployment()
			u.SetName("x")

			first, err := TransformStripCacheWeight()(u)
			Expect(err).NotTo(HaveOccurred())
			second, err := TransformStripCacheWeight()(first)
			Expect(err).NotTo(HaveOccurred())

			got := second.(*unstructured.Unstructured)
			Expect(got.GetManagedFields()).To(BeNil())
			Expect(got.GetAnnotations()).To(BeEmpty())
		})

		It("passes non-object entries through without error", func() {
			out, err := TransformStripCacheWeight()("not-an-object")
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal("not-an-object"))
		})
	})

	Context("wired into a real cache", func() {
		var ns string

		BeforeEach(func() { ns = newTestNamespace("test-transform-") })

		It("baseline: without a transform the cache retains managedFields", func() {
			makeDeployment(ns, "baseline", nil)

			cl := startCache(cache.Options{})

			u := unstructuredDeployment()

			Eventually(func(g Gomega) {
				g.Expect(cl.Get(ctx, types.NamespacedName{Name: "baseline", Namespace: ns}, u)).To(Succeed())
			}).Should(Succeed())

			Expect(u.GetManagedFields()).NotTo(BeEmpty(),
				"the API server stamps managedFields; an unconfigured cache keeps them")
			Expect(u.GetAnnotations()).To(HaveKey(corev1.LastAppliedConfigAnnotation))
		})

		It("strips both fields from objects read back through the cache", func() {
			makeDeployment(ns, "stripped", nil)

			cl := startCache(cache.Options{DefaultTransform: TransformStripCacheWeight()})
			u := unstructuredDeployment()

			Eventually(func(g Gomega) {
				g.Expect(cl.Get(ctx, types.NamespacedName{Name: "stripped", Namespace: ns}, u)).To(Succeed())
			}).Should(Succeed())

			Expect(u.GetManagedFields()).To(BeEmpty())
			Expect(u.GetAnnotations()).NotTo(HaveKey(corev1.LastAppliedConfigAnnotation))
			Expect(u.GetAnnotations()).To(HaveKeyWithValue("keep.me/annotation", "kept"),
				"unrelated annotations must survive")
		})

		It("applies to dynamically-created unstructured informers (the ensureWatch path)", func() {
			makeDeployment(ns, "dynamic", nil)

			cl := startCache(cache.Options{DefaultTransform: TransformStripCacheWeight()})

			ul := &unstructured.UnstructuredList{}
			ul.SetAPIVersion(testAppsV1)
			ul.SetKind(testDepKind + "List")
			Eventually(func(g Gomega) {
				g.Expect(cl.List(ctx, ul, client.InNamespace(ns))).To(Succeed())
				g.Expect(ul.Items).To(HaveLen(1))
			}).Should(Succeed())

			Expect(ul.Items[0].GetManagedFields()).To(BeEmpty())
			Expect(ul.Items[0].GetAnnotations()).NotTo(HaveKey(corev1.LastAppliedConfigAnnotation))
		})
	})
})
