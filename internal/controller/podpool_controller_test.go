/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

var _ = Describe("PodPool Controller", func() {
	Context("with Deployment workloadTemplate", func() {
		const poolName = "test-pool"

		var ns string

		BeforeEach(func() {
			nsObj := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "test-deploy-"},
			}
			Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
			ns = nsObj.Name
		})

		createPool := func() {
			minThree := int32(3)
			minZero := int32(0)
			pool := &podpoolsv1alpha1.PodPool{
				ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: ns},
				Spec: podpoolsv1alpha1.PodPoolSpec{
					Replicas:         5,
					WorkloadTemplate: workloadTemplateWithSelector("ctrl-app"),
					Groups: []podpoolsv1alpha1.GroupSpec{
						// base is capped by its 70% target, so spot is the
						// unbounded group that absorbs the remainder.
						{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minThree, Target: pctTarget(70)}},
						{Name: testGroupSpot, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minZero}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())
		}

		getDeployment := func(group string) func() (*appsv1.Deployment, error) {
			return func() (*appsv1.Deployment, error) {
				var dep appsv1.Deployment

				err := k8sClient.Get(ctx, types.NamespacedName{
					Name: poolName + "-" + group, Namespace: ns,
				}, &dep)

				return &dep, err
			}
		}

		It("creates one child per group with the distributed replica counts", func() {
			createPool()

			// base takes floor(5 x 0.70) = 3; spot absorbs the remaining 2.
			Eventually(func(g Gomega) {
				dep, err := getDeployment(testGroupBase)()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(*dep.Spec.Replicas).To(Equal(int32(3)))
			}).Should(Succeed())

			Eventually(func(g Gomega) {
				dep, err := getDeployment(testGroupSpot)()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
			}).Should(Succeed())
		})

		It("resizes children when spec.replicas changes", func() {
			createPool()

			Eventually(func(g Gomega) {
				_, err := getDeployment(testGroupSpot)()
				g.Expect(err).NotTo(HaveOccurred())
			}).Should(Succeed())

			var pool podpoolsv1alpha1.PodPool
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName, Namespace: ns}, &pool)).To(Succeed())
			pool.Spec.Replicas = 10
			Expect(k8sClient.Update(ctx, &pool)).To(Succeed())

			// At 10: base grows to its 70% target (7) and spot keeps the rest.
			Eventually(func(g Gomega) {
				base, err := getDeployment(testGroupBase)()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(*base.Spec.Replicas).To(Equal(int32(7)))

				spot, err := getDeployment(testGroupSpot)()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(*spot.Spec.Replicas).To(Equal(int32(3)))
			}).Should(Succeed())
		})

		It("sets a controller owner reference on every child", func() {
			createPool()

			Eventually(func(g Gomega) {
				dep, err := getDeployment(testGroupBase)()
				g.Expect(err).NotTo(HaveOccurred())

				ref := metav1.GetControllerOf(dep)
				g.Expect(ref).NotTo(BeNil())
				g.Expect(ref.Kind).To(Equal(workload.KindPodPool))
				g.Expect(ref.Name).To(Equal(poolName))
				g.Expect(ref.BlockOwnerDeletion).To(HaveValue(BeTrue()))
			}).Should(Succeed())
		})
	})

	// The same loop drives CRD workload kinds it has no compiled types for.
	// The minimal Rollout and CloneSet CRDs in testdata/crds are schemaless
	// where it matters, so they also need no selector.
	Context("with CRD workloadTemplates", func() {
		var ns string

		BeforeEach(func() {
			nsObj := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "test-crd-"},
			}
			Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
			ns = nsObj.Name
		})

		crdChild := func(gvk schema.GroupVersionKind, name string) func() (*unstructured.Unstructured, error) {
			return func() (*unstructured.Unstructured, error) {
				u := &unstructured.Unstructured{}
				u.SetGroupVersionKind(gvk)
				err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, u)

				return u, err
			}
		}

		It("creates an Argo Rollout child", func() {
			minTwo := int32(2)
			pool := &podpoolsv1alpha1.PodPool{
				ObjectMeta: metav1.ObjectMeta{Name: "rollout-pool", Namespace: ns},
				Spec: podpoolsv1alpha1.PodPoolSpec{
					Replicas:         2,
					WorkloadTemplate: workloadTemplateJSON("argoproj.io/v1alpha1", "Rollout", testContainer, testImageNginx),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())

			get := crdChild(schema.GroupVersionKind{
				Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout",
			}, "rollout-pool-"+testGroupBase)

			Eventually(func(g Gomega) {
				u, err := get()
				g.Expect(err).NotTo(HaveOccurred())

				replicas, found, err := unstructured.NestedInt64(u.Object, "spec", "replicas")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())
				g.Expect(replicas).To(Equal(int64(2)))
			}).Should(Succeed())
		})

		It("creates a Kruise CloneSet child", func() {
			minTwo := int32(2)
			pool := &podpoolsv1alpha1.PodPool{
				ObjectMeta: metav1.ObjectMeta{Name: "cloneset-pool", Namespace: ns},
				Spec: podpoolsv1alpha1.PodPoolSpec{
					Replicas:         2,
					WorkloadTemplate: workloadTemplateJSON("apps.kruise.io/v1alpha1", "CloneSet", testContainer, testImageNginx),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())

			get := crdChild(schema.GroupVersionKind{
				Group: "apps.kruise.io", Version: "v1alpha1", Kind: "CloneSet",
			}, "cloneset-pool-"+testGroupBase)

			Eventually(func(g Gomega) {
				u, err := get()
				g.Expect(err).NotTo(HaveOccurred())

				replicas, found, err := unstructured.NestedInt64(u.Object, "spec", "replicas")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(found).To(BeTrue())
				g.Expect(replicas).To(Equal(int64(2)))
			}).Should(Succeed())
		})
	})
})
