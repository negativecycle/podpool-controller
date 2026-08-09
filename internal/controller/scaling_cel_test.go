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

// These live in the controller suite rather than a webhook suite on purpose:
// suite_test.go installs the CRDs and does NOT install webhook configurations,
// so this envtest *is* the "webhook is down" scenario the schema rules exist
// for. Any rejection here came from the schema, unambiguously. The same
// assertions in a webhook suite could not distinguish CEL from webhook
// validation, and a test that cannot say which layer rejected cannot prove
// the schema does anything.

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

func pctStr(pct int) *intstr.IntOrString {
	v := intstr.FromString(fmt.Sprintf("%d%%", pct))

	return &v
}

// scalingShape is one row of the truth table over hand-writable scaling
// combinations.
type scalingShape struct {
	name    string
	scaling podpoolsv1alpha1.ScalingConstraints
	legal   bool
}

// ScalingShapes is exhaustive over the combinations that can be written by
// hand. Field values are arbitrary within their own validation bounds — the
// rules key off presence, not magnitude.
var ScalingShapes = []scalingShape{
	// Legal.
	{"empty", podpoolsv1alpha1.ScalingConstraints{}, true},
	{"min", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(3))}, true},
	{"target", podpoolsv1alpha1.ScalingConstraints{Target: pctStr(70)}, true},
	{"min+target", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(0)), Target: pctStr(70)}, true},
	{"max+target", podpoolsv1alpha1.ScalingConstraints{Max: ptr.To(int32(5)), Target: pctStr(30)}, true},
	{"min+max", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(0)), Max: ptr.To(int32(5))}, true},
	{"min+max+target", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(0)), Max: ptr.To(int32(5)), Target: pctStr(30)}, true},
	{"max", podpoolsv1alpha1.ScalingConstraints{Max: ptr.To(int32(5))}, true},
	{"opportunistic", podpoolsv1alpha1.ScalingConstraints{Opportunistic: ptr.To(true)}, true},
	{"min+opportunistic", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(0)), Opportunistic: ptr.To(true)}, true},
	// Explicit false must take the non-opportunistic branch. A rule written
	// with has(self.opportunistic) alone rejects users who spell out the
	// default, which is exactly the thing careful users do.
	{"min+opportunistic=false", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(3)), Opportunistic: ptr.To(false)}, true},

	// Illegal.
	{"min>max", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(10)), Max: ptr.To(int32(5))}, false},
	{"opportunistic+max", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(0)), Max: ptr.To(int32(5)), Opportunistic: ptr.To(true)}, false},
	{"opportunistic+target", podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(0)), Target: pctStr(70), Opportunistic: ptr.To(true)}, false},
	{"target integer", podpoolsv1alpha1.ScalingConstraints{Target: ptr.To(intstr.FromInt32(30))}, false},
	{"target bad format", podpoolsv1alpha1.ScalingConstraints{Target: ptr.To(intstr.FromString("abc"))}, false},
	{"target zero", podpoolsv1alpha1.ScalingConstraints{Target: ptr.To(intstr.FromString("0%"))}, false},
	{"target 101%", podpoolsv1alpha1.ScalingConstraints{Target: ptr.To(intstr.FromString("101%"))}, false},
	{"target no suffix", podpoolsv1alpha1.ScalingConstraints{Target: ptr.To(intstr.FromString("30"))}, false},
}

var _ = Describe("CEL validation of scaling combinations", func() {
	var ns string

	BeforeEach(func() {
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-cel-"}}
		Expect(k8sClient.Create(ctx, nsObj)).To(Succeed())
		ns = nsObj.Name
	})

	for i, shape := range ScalingShapes {
		It(fmt.Sprintf("%s: %s", map[bool]string{true: "admits", false: "rejects"}[shape.legal], shape.name), func() {
			pool := &podpoolsv1alpha1.PodPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("cel-%d", i),
					Namespace: ns,
				},
				Spec: podpoolsv1alpha1.PodPoolSpec{
					Replicas:         5,
					WorkloadTemplate: workloadTemplateJSON(testAppsV1, testDepKind, testContainer),
					Groups: []podpoolsv1alpha1.GroupSpec{
						{Name: "under-test", Scaling: shape.scaling},
						// A trailing uncapped group so the pool is otherwise
						// sensible. Cross-group rules are the webhook's job and
						// are not installed here, but keeping the fixture
						// realistic means a future reader does not mistake the
						// single-group shortcut for a claim about overflow.
						{Name: "overflow", Scaling: podpoolsv1alpha1.ScalingConstraints{Min: ptr.To(int32(0))}},
					},
				},
			}

			err := k8sClient.Create(ctx, pool)
			if shape.legal {
				Expect(err).NotTo(HaveOccurred(), "legal shape was rejected by the schema")
				Expect(k8sClient.Delete(ctx, pool)).To(Succeed())

				return
			}

			Expect(err).To(HaveOccurred(),
				"illegal shape was admitted with no webhook running: this is what the schema rules exist to prevent")
			// The rejection has to come from the schema, not from some unrelated
			// structural bound, or the test proves nothing about the CEL rules.
			Expect(err.Error()).To(ContainSubstring("spec.groups[0].scaling"),
				"rejection did not name the field carrying the rule")
		})
	}
})
