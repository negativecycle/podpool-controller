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
)

func workloadTemplateJSON(apiVersion, kind, containerName, image string) runtime.RawExtension {
	tmpl := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{
							"name":  containerName,
							"image": image,
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(tmpl)

	return runtime.RawExtension{Raw: raw}
}
