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
	"encoding/json"

	"k8s.io/apimachinery/pkg/runtime"
)

// Shared fixtures for this package's tests. These migrate to their long-term
// homes as the features that own them land.
const (
	testImageNginx = "nginx:latest"
	testGroupBase  = "base"
	testGroupSpot  = "spot"
	testNamespace  = "default"
	testAppsV1     = "apps/v1"
	testDepKind    = "Deployment"
	testContainer  = "api"

	fieldAPIVersion      = "apiVersion"
	fieldKind            = "kind"
	fieldMetadata        = "metadata"
	fieldLabels          = "labels"
	fieldSpec            = "spec"
	fieldSelector        = "selector"
	fieldMatchLabels     = "matchLabels"
	fieldTemplate        = "template"
	fieldContainers      = "containers"
	fieldName            = "name"
	fieldImage           = "image"
	fieldUID             = "uid"
	fieldResourceVersion = "resourceVersion"
	fieldFinalizers      = "finalizers"

	labelKeyApp = "app"
)

func workloadTemplateJSON(apiVersion, kind, containerName, image string) runtime.RawExtension {
	tmpl := map[string]any{
		fieldAPIVersion: apiVersion,
		fieldKind:       kind,
		fieldSpec: map[string]any{
			fieldTemplate: map[string]any{
				fieldSpec: map[string]any{
					fieldContainers: []any{
						map[string]any{
							fieldName:  containerName,
							fieldImage: image,
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(tmpl)

	return runtime.RawExtension{Raw: raw}
}

// workloadTemplateWithSelector is workloadTemplateJSON plus a user-supplied
// pod selector and matching template labels, always as an apps/v1 Deployment
// because that is the kind whose schema demands a selector. The controller
// does not manage selectors yet, so every template destined for a real API
// server has to carry its own. Note what that costs: two groups rendered from
// this one template get identical selectors and fight over the same pods,
// which is exactly the gap the ownership milestone closes.
func workloadTemplateWithSelector(appLabel string) runtime.RawExtension {
	tmpl := map[string]any{
		fieldAPIVersion: testAppsV1,
		fieldKind:       testDepKind,
		fieldSpec: map[string]any{
			fieldSelector: map[string]any{
				fieldMatchLabels: map[string]any{labelKeyApp: appLabel},
			},
			fieldTemplate: map[string]any{
				fieldMetadata: map[string]any{
					fieldLabels: map[string]any{labelKeyApp: appLabel},
				},
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

	return runtime.RawExtension{Raw: raw}
}
