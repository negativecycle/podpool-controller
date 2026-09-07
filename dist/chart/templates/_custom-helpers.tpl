{{- /*
Custom helpers, kept in a file the kubebuilder plugin does not own, so they
survive chart regeneration. podpools.commonLabels mirrors the label set the
generated manager Deployment applies inline, for the templates we add.
*/ -}}
{{- define "podpools.commonLabels" -}}
app.kubernetes.io/name: {{ include "podpools.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{- end }}
