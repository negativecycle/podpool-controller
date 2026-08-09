package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// These specs cover the observation half of opportunistic sizing: the gate that
// decides whether to read pods at all, and the query that classifies them.
//
// envtest runs no scheduler and no kubelet, so pod conditions are written by
// hand — which is the point. It lets a pod that is *starting* and a pod the
// scheduler *refused* be constructed exactly, and those two must not be
// confused: mistaking one for the other either migrates capacity during an
// ordinary rollout, or fails to migrate it when a node is genuinely full.
var _ = Describe("Opportunistic observation", func() {
	const (
		poolName = "opp-pool"
		ns       = "default"
	)

	var counter int

	nextGroup := func() string {
		counter++

		return fmt.Sprintf("scav%d", counter)
	}

	// makePod builds a pod carrying the labels the controller selects on. The
	// scheduled flag is what distinguishes "refused" from "still starting".
	makePod := func(group, name string, scheduled bool) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					workload.LabelPool:      poolName,
					workload.LabelGroup:     group,
					workload.LabelManagedBy: workload.ManagerName,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: testContainer, Image: testImageNginx}},
			},
		}
		Expect(k8sClient.Create(ctx, pod)).To(Succeed())

		cond := corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}
		if !scheduled {
			cond.Status = corev1.ConditionFalse
			cond.Reason = corev1.PodReasonUnschedulable
		}

		pod.Status.Phase = corev1.PodPending
		pod.Status.Conditions = []corev1.PodCondition{cond}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
	}

	pool := func() *podpoolsv1alpha1.PodPool {
		return &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         10,
				WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
			},
		}
	}

	reconciler := func() *PodPoolReconciler {
		// APIReader must bypass the cache; k8sClient in this suite is already
		// a direct client, which is what the controller uses in production too.
		return &PodPoolReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}
	}

	It("counts only the pods the scheduler refused", func() {
		g := nextGroup()
		makePod(g, g+"-refused-1", false)
		makePod(g, g+"-refused-2", false)
		makePod(g, g+"-starting", true) // Pending, but bound to a node

		n, err := reconciler().countUnschedulable(ctx, pool(), g, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeEquivalentTo(2), "a Pending pod that is merely starting must not count")
	})

	It("does not read another group's pods", func() {
		mine, theirs := nextGroup(), nextGroup()
		makePod(mine, mine+"-refused", false)
		makePod(theirs, theirs+"-refused", false)

		n, err := reconciler().countUnschedulable(ctx, pool(), mine, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeEquivalentTo(1), "the label selector is the boundary between pools and groups")
	})

	It("honours the limit so a wholly unschedulable group cannot page unbounded", func() {
		g := nextGroup()
		for i := range 5 {
			makePod(g, fmt.Sprintf("%s-refused-%d", g, i), false)
		}

		n, err := reconciler().countUnschedulable(ctx, pool(), g, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeNumerically("<=", 2))
	})
})

// The gate reads the child's spec.replicas — what we last asked for — and never
// its status.replicas. During a scale-up the ReplicaSet lags, so status.replicas
// still reports the old count while readyReplicas has caught up to it. Read that
// way every scale-up looks like a successful probe, and the group grows by one
// replica every reconcile without end.
var _ = Describe("Opportunistic gate", func() {
	const (
		poolName = "gate-pool"
		ns       = "default"
		group    = testGroupScavShort
	)

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: poolName + "-" + group, Namespace: ns},
		})
	})

	It("reads what was asked for, not what the ReplicaSet has caught up to", func() {
		poolUID := types.UID("gate-pool-test-uid")
		p := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: poolName, Namespace: ns, UID: poolUID},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
			},
		}

		dep := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      poolName + "-" + group,
				Namespace: ns,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: podpoolsv1alpha1.GroupVersion.String(),
					Kind:       workload.KindPodPool,
					Name:       p.Name,
					UID:        poolUID,
					Controller: ptr.To(true),
				}},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](8), // asked for 8
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "gate"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "gate"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: testContainer, Image: testImageNginx}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, dep)).To(Succeed())

		// The ReplicaSet has not caught up: status still reports the old count,
		// and readyReplicas has caught up to *that*.
		dep.Status.Replicas = 5
		dep.Status.ReadyReplicas = 5
		Expect(k8sClient.Status().Update(ctx, dep)).To(Succeed())

		r := &PodPoolReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), APIReader: k8sClient}

		gvk := schema.GroupVersionKind{Group: testAppsGroup, Version: "v1", Kind: testDepKind}

		obs, err := r.childCounts(ctx, p, gvk, group)
		Expect(err).NotTo(HaveOccurred())
		Expect(obs.found).To(BeTrue())
		Expect(obs.asked).To(BeEquivalentTo(8), "must come from spec.replicas")
		Expect(obs.ready).To(BeEquivalentTo(5))
		Expect(obs.ready).To(BeNumerically("<", obs.asked),
			"reading status.replicas here would make ready==asked and fake a successful probe")
	})
})
