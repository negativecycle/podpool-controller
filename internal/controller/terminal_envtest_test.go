package controller

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// These run against a real API server because the error they classify is one
// the fake client cannot produce: server-side apply rejecting a patch that
// does not fit the target's structural schema.
var _ = Describe("Terminal SSA schema rejections", func() {
	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-terminal-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name
	})

	// The unit table pins our fabricated copy of the error; this pins the real
	// one. When an apiserver bump rewords it, this test fails while the unit
	// rows keep passing, which is the signal to update the message match.
	It("classifies the live apiserver's typed-patch rejection as terminal", func() {
		child := &unstructured.Unstructured{Object: map[string]any{
			fieldAPIVersion: testAppsV1,
			fieldKind:       testDepKind,
			fieldMetadata: map[string]any{
				fieldName:   "ssa-typo",
				"namespace": ns,
			},
			fieldSpec: map[string]any{
				"replicaz": int64(1),
			},
		}}

		err := k8sClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(child),
			client.FieldOwner("test-anti-drift"))
		Expect(err).To(HaveOccurred())
		Expect(isTerminalAPIError(err)).To(BeTrue(),
			"the real server's schema rejection must classify terminal; error was: %v", err)
	})

	It("settles a pool whose template has an unknown field on GroupSpecInvalid", func() {
		tmpl := map[string]any{
			fieldAPIVersion: testAppsV1,
			fieldKind:       testDepKind,
			fieldSpec: map[string]any{
				"replicaz": int64(1),
				fieldTemplate: map[string]any{
					fieldSpec: map[string]any{
						fieldContainers: []any{
							map[string]any{fieldName: testContainer, fieldImage: testImageNginx},
						},
					},
				},
			},
		}
		raw, err := json.Marshal(tmpl)
		Expect(err).NotTo(HaveOccurred())

		minTwo := int32(2)
		pool := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: "typo-pool", Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         2,
				WorkloadTemplate: runtime.RawExtension{Raw: raw},
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, pool)).To(Succeed())

		Eventually(func(g Gomega) {
			var got podpoolsv1alpha1.PodPool
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pool.Name, Namespace: ns}, &got)).To(Succeed())
			cond := conditionByType(&got, ConditionReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(ReasonGroupSpecInvalid),
				"an unknown-field template must read terminal, not transient")
			g.Expect(cond.Message).To(Equal("Group spec invalid: " + testGroupBase))
		}).Should(Succeed())
	})
})
