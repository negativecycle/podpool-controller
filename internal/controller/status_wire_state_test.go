package controller

// The platform premise, pinned against Kubernetes itself rather than against
// this controller.
//
// Everything the next commit does rests on one measured fact:
// DeploymentStatus's counters are omitempty, so readyReplicas written as 0
// through the status subresource is stored with no key at all. A workload type
// that never publishes readiness and a healthy rollout with zero ready pods are
// therefore the same wire state, and only elapsed time can separate them. If a
// future Kubernetes drops omitempty from these fields this test fails first,
// before the behaviour quietly changes underneath the controller.
//
// TestReadInt32AbsentVsZero (internal/workload/render_test.go) pins the
// reader's half of the same argument: absent yields found=false, an explicit
// zero yields found=true. The pair reads as one proof; this half establishes
// which of the two states the API server actually stores.

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Deployment status wire state", func() {
	It("stores readyReplicas as absent when written as zero", func() {
		const ns = "default"

		labels := map[string]string{testUserLabelKey: "wire-state"}
		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wire-state", Namespace: ns},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To(int32(3)),
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: labels},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: testContainer, Image: testImageNginx}},
					},
				},
			},
		}

		Expect(k8sClient.Create(ctx, dep)).To(Succeed())
		DeferCleanup(func() {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, dep))).To(Succeed())
		})

		// Written exactly as the Deployment controller would during a rollout
		// in which no pod has become ready: counts up, readiness zero.
		dep.Status.Replicas = 3
		dep.Status.UpdatedReplicas = 3
		dep.Status.ReadyReplicas = 0
		Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())

		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind(testDepKind))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dep), u)).To(Succeed())

		_, found, err := unstructured.NestedInt64(u.Object, "status", "replicas")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "replicas was written non-zero and must be stored")

		_, found, err = unstructured.NestedInt64(u.Object, "status", "readyReplicas")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(),
			"readyReplicas written as 0 came back present: omitempty no longer "+
				"holds for Deployment status counters, and the deadline-based "+
				"inference below should be revisited")
	})
})
