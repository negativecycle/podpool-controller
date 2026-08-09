package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

var _ = Describe("CRD schema validation", func() {
	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-schema-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name
	})

	basePool := func(name string) *podpoolsv1alpha1.PodPool {
		minTwo := int32(2)

		return &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         2,
				WorkloadTemplate: workloadTemplateWithSelector(labelKeyApp),
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
				},
			},
		}
	}

	It("rejects more than 32 groups", func() {
		pool := basePool("too-many-groups")

		pool.Spec.Groups = make([]podpoolsv1alpha1.GroupSpec, 33)
		for i := range pool.Spec.Groups {
			pool.Spec.Groups[i] = podpoolsv1alpha1.GroupSpec{
				Name:    fmt.Sprintf("g%02d", i),
				Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
			}
		}

		err := k8sClient.Create(ctx, pool)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("groups"))
	})

	It("accepts exactly 32 groups", func() {
		pool := basePool("max-groups")
		pool.Spec.Replicas = 32

		pool.Spec.Groups = make([]podpoolsv1alpha1.GroupSpec, 32)
		for i := range pool.Spec.Groups {
			pool.Spec.Groups[i] = podpoolsv1alpha1.GroupSpec{
				Name:    fmt.Sprintf("g%02d", i),
				Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)},
			}
		}

		pool.Spec.Groups[0].Scaling.Min = ptr.To[int32](32)
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())
	})

	It("rejects duplicate group names via listType=map", func() {
		pool := basePool("dup-names")
		minOne := int32(1)
		pool.Spec.Groups = []podpoolsv1alpha1.GroupSpec{
			{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minOne}},
			{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To[int32](0)}},
		}
		err := k8sClient.Create(ctx, pool)
		Expect(err).To(HaveOccurred())
	})

	It("rejects replicas above 1000000", func() {
		pool := basePool("too-many-replicas")
		pool.Spec.Replicas = 1_000_001
		err := k8sClient.Create(ctx, pool)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("replicas"))
	})

	It("accepts replicas at exactly 1000000", func() {
		pool := basePool("max-replicas")
		pool.Spec.Replicas = 1_000_000
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())
	})
})
