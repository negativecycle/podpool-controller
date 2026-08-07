package controller

import (
	"encoding/json"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
	"github.com/negativecycle/podpool-controller/internal/workload"
)

// reconcileCount reads controller-runtime's own completion counter, which is
// the only footprint a no-op reconcile leaves: an annotation nudge does not
// bump the pool's generation, and the child not changing is the property
// under test. The counter has no per-object label, so this proves "a podpool
// reconcile completed", not "this pool's reconcile completed"; callers carry
// their actual assertion over any straggler themselves.
func reconcileCount() float64 {
	families, err := crmetrics.Registry.Gather()
	Expect(err).NotTo(HaveOccurred())

	var total float64

	for _, fam := range families {
		if fam.GetName() != "controller_runtime_reconcile_total" {
			continue
		}

		for _, m := range fam.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "controller" && l.GetValue() == "podpool" {
					total += m.GetCounter().GetValue()
				}
			}
		}
	}

	return total
}

// These run against a real API server because the two properties in tension
// here cannot both be observed against the fake client: it neither tracks
// managedFields nor models a no-op apply, so it bumps resourceVersion on every
// apply regardless of whether anything changed.
var _ = Describe("Child workload drift", func() {
	const poolName = "drift-pool"

	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-drift-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name

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
	})

	childKey := func() types.NamespacedName {
		return types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: ns}
	}

	getChild := func() *appsv1.Deployment {
		var dep appsv1.Deployment
		Expect(k8sClient.Get(ctx, childKey(), &dep)).To(Succeed())

		return &dep
	}

	waitForChild := func() *appsv1.Deployment {
		var dep appsv1.Deployment

		Eventually(func() error {
			return k8sClient.Get(ctx, childKey(), &dep)
		}).Should(Succeed())

		return &dep
	}

	// Forces a pass that changes nothing the child renders from. Only the
	// no-op apply spec still needs this: now that the child watch delivers
	// child edits on its own, a converged child generates no events at all,
	// so the passes whose absence of writes is the property under test have
	// to be triggered synthetically.
	nudgePool := func(v string) {
		var pool podpoolsv1alpha1.PodPool
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: poolName, Namespace: ns}, &pool)).To(Succeed())

		if pool.Annotations == nil {
			pool.Annotations = map[string]string{}
		}

		pool.Annotations["test.podpools.dev/nudge"] = v
		Expect(k8sClient.Update(ctx, &pool)).To(Succeed())
	}

	It("reverts an external edit to a field the pool renders", func() {
		waitForChild()

		// A distinct field manager, standing in for an HPA or a kubectl edit.
		drift := getChild()
		drift.Spec.Replicas = ptr.To[int32](99)
		drift.Spec.Template.Spec.Containers[0].Image = "nginx:tampered"
		Expect(k8sClient.Update(ctx, drift, client.FieldOwner("rogue-actor"))).To(Succeed())

		// Nothing nudges the pool. The child watch delivers the rogue edit to
		// the controller, and the revert below is the proof it did.
		Eventually(func(g Gomega) {
			dep := getChild()
			g.Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
			g.Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(testImageNginx))
		}).Should(Succeed(), "drift on a rendered field should be reverted")
	})

	It("does not rewrite a converged child when reconciled again", func() {
		waitForChild()

		// Settle first: retry until two consecutive polls see the same version,
		// so we are not racing the initial convergence.
		var settled string

		Eventually(func() bool {
			rv := getChild().ResourceVersion
			stable := rv == settled
			settled = rv

			return stable
		}).WithPolling(300*time.Millisecond).WithTimeout(15*time.Second).
			Should(BeTrue(), "child never stopped changing")

		// Force reconciles that change nothing the child renders from. An
		// annotation nudge does exactly that. The counter is snapshotted
		// before each update so the reconcile it triggers cannot be missed,
		// and waited on so a broken pipeline fails loudly here instead of
		// letting the final check pass vacuously.
		for i := range 3 {
			before := reconcileCount()

			nudgePool(strconv.Itoa(i))
			Eventually(reconcileCount).Should(BeNumerically(">", before),
				"nudge %d never triggered a reconcile", i)
		}

		// The window covers a reconcile that was already in flight when a
		// nudge landed: its own event is still queued, so the pass it missed
		// happens within the window or not at all.
		Consistently(func() string { return getChild().ResourceVersion },
			2*time.Second, 200*time.Millisecond).
			Should(Equal(settled),
				"a converged apply must be a no-op: the API server should not bump resourceVersion")
	})

	It("creates a child even when the template carries a pasted uid", func() {
		tmpl := map[string]any{
			fieldAPIVersion: testAppsV1,
			fieldKind:       testDepKind,
			fieldMetadata: map[string]any{
				fieldUID:             "11111111-2222-3333-4444-555555555555",
				fieldResourceVersion: "99999",
			},
			fieldSpec: map[string]any{
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

		minTwo := int32(2)
		uidPool := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: "uid-pool", Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         2,
				WorkloadTemplate: runtime.RawExtension{Raw: raw},
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, uidPool)).To(Succeed())

		var dep appsv1.Deployment

		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: "uid-pool-" + testGroupBase, Namespace: ns,
			}, &dep)
		}).Should(Succeed(), "child should be created despite pasted uid in template")

		Expect(dep.UID).NotTo(Equal(types.UID("11111111-2222-3333-4444-555555555555")),
			"child should get its own uid, not the pasted one")
	})

	It("does not copy template finalizers onto the child", func() {
		tmpl := map[string]any{
			fieldAPIVersion: testAppsV1,
			fieldKind:       testDepKind,
			fieldMetadata: map[string]any{
				fieldFinalizers: []any{"foregroundDeletion"},
			},
			fieldSpec: map[string]any{
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

		minTwo := int32(2)
		finPool := &podpoolsv1alpha1.PodPool{
			ObjectMeta: metav1.ObjectMeta{Name: "fin-pool", Namespace: ns},
			Spec: podpoolsv1alpha1.PodPoolSpec{
				Replicas:         2,
				WorkloadTemplate: runtime.RawExtension{Raw: raw},
				Groups: []podpoolsv1alpha1.GroupSpec{
					{Name: testGroupBase, Scaling: podpoolsv1alpha1.ScalingConstraints{Min: &minTwo}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, finPool)).To(Succeed())

		var dep appsv1.Deployment

		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: "fin-pool-" + testGroupBase, Namespace: ns,
			}, &dep)
		}).Should(Succeed())

		Expect(dep.Finalizers).To(BeEmpty(),
			"pasted finalizers from template should not land on the child")
	})
})

var _ = Describe("Foreign fields on a child", func() {
	const poolName = "foreign-pool"

	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-foreign-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name

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
	})

	It("leaves fields it does not render alone", func() {
		childKey := types.NamespacedName{Name: poolName + "-" + testGroupBase, Namespace: ns}

		var dep appsv1.Deployment

		Eventually(func() error {
			return k8sClient.Get(ctx, childKey, &dep)
		}).Should(Succeed())

		foreign := dep.DeepCopy()
		if foreign.Annotations == nil {
			foreign.Annotations = map[string]string{}
		}

		foreign.Annotations["other.io/owned-by-someone-else"] = "keep me"
		// A foreign label matters more than a foreign annotation: the
		// controller renders labels of its own, so replacing instead of
		// merging would clobber this one. Server-side apply has to preserve
		// it with no merge code on our side.
		if foreign.Labels == nil {
			foreign.Labels = map[string]string{}
		}

		before := reconcileCount()

		foreign.Labels["app.kubernetes.io/managed-by"] = "Helm"
		foreign.Spec.Template.Spec.Containers[0].TerminationMessagePath = "/dev/termination-custom"
		Expect(k8sClient.Update(ctx, foreign, client.FieldOwner("other-controller"))).To(Succeed())

		// The child watch delivers the foreign edit, so the pass that could
		// clobber these fields happens on its own; wait for proof it ran, then
		// hold the assertion open across it. This is a stronger spec than the
		// pool-nudge it replaces, which forced a pass without ever showing
		// that a child edit alone causes one.
		Eventually(reconcileCount).Should(BeNumerically(">", before),
			"the child edit never triggered a reconcile")

		Consistently(func(g Gomega) {
			var d appsv1.Deployment
			g.Expect(k8sClient.Get(ctx, childKey, &d)).To(Succeed())
			g.Expect(d.Annotations).To(HaveKeyWithValue("other.io/owned-by-someone-else", "keep me"))
			g.Expect(d.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "Helm"))
			g.Expect(d.Spec.Template.Spec.Containers[0].TerminationMessagePath).
				To(Equal("/dev/termination-custom"))

			// ...while the labels the controller does own are still correct.
			g.Expect(d.Labels).To(HaveKeyWithValue(workload.LabelPool, poolName))
			g.Expect(d.Labels).To(HaveKeyWithValue(workload.LabelGroup, testGroupBase))
			g.Expect(d.Labels).To(HaveKeyWithValue(workload.LabelManagedBy, workload.ManagerName))
		}).WithPolling(300 * time.Millisecond).WithTimeout(3 * time.Second).Should(Succeed())
	})
})
