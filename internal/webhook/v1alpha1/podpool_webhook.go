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

package v1alpha1

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	podpoolsv1alpha1 "github.com/negativecycle/podpool-controller/api/v1alpha1"
)

// SetupPodPoolWebhookWithManager registers the defaulting and validating webhooks for PodPool.
func SetupPodPoolWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &podpoolsv1alpha1.PodPool{}).
		WithValidator(&PodPoolCustomValidator{}).
		WithDefaulter(&PodPoolCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-podpools-dev-v1alpha1-podpool,mutating=true,failurePolicy=fail,sideEffects=None,groups=podpools.dev,resources=podpools,verbs=create;update,versions=v1alpha1,name=mpodpool-v1alpha1.kb.io,admissionReviewVersions=v1

// PodPoolCustomDefaulter sets defaults on PodPool resources during admission.
type PodPoolCustomDefaulter struct{}

func (d *PodPoolCustomDefaulter) Default(ctx context.Context, obj *podpoolsv1alpha1.PodPool) error {
	logf.FromContext(ctx).Info("Defaulting for PodPool", "name", obj.GetName())

	for i := range obj.Spec.Groups {
		s := &obj.Spec.Groups[i].Scaling
		if s.Min == nil && s.Max == nil {
			zero := int32(0)
			s.Min = &zero
		}
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-podpools-dev-v1alpha1-podpool,mutating=false,failurePolicy=fail,sideEffects=None,groups=podpools.dev,resources=podpools,verbs=create;update,versions=v1alpha1,name=vpodpool-v1alpha1.kb.io,admissionReviewVersions=v1

// PodPoolCustomValidator validates PodPool resources during admission.
type PodPoolCustomValidator struct{}

func (v *PodPoolCustomValidator) ValidateCreate(ctx context.Context, obj *podpoolsv1alpha1.PodPool) (admission.Warnings, error) {
	logf.FromContext(ctx).Info("Validation for PodPool upon creation", "name", obj.GetName())

	return nil, nil
}

func (v *PodPoolCustomValidator) ValidateUpdate(ctx context.Context, oldObj *podpoolsv1alpha1.PodPool, newObj *podpoolsv1alpha1.PodPool) (admission.Warnings, error) {
	logf.FromContext(ctx).Info("Validation for PodPool upon update", "name", newObj.GetName())

	return nil, nil
}

// ValidateDelete is registered because the framework requires the interface to
// be complete, not because deleting a pool needs a verdict: the controller's
// own cleanup runs on the NotFound pass, and refusing a delete would leave an
// operator no way to remove a broken pool.
func (v *PodPoolCustomValidator) ValidateDelete(_ context.Context, _ *podpoolsv1alpha1.PodPool) (admission.Warnings, error) {
	return nil, nil
}
