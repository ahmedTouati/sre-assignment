{{- define "token-stack.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "token-stack.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "token-stack.labels" -}}
helm.sh/chart: {{ include "token-stack.chart" . }}
app.kubernetes.io/name: {{ include "token-stack.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "token-stack.selectorLabels" -}}
app.kubernetes.io/name: {{ include "token-stack.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "token-stack.componentName" -}}
{{- $baseLength := sub 62 (len .component) | int -}}
{{- $base := .root.Release.Name | trunc $baseLength | trimSuffix "-" -}}
{{- printf "%s-%s" $base .component -}}
{{- end -}}

{{- define "token-stack.pythonName" -}}
{{- include "token-stack.componentName" (dict "root" . "component" "python") -}}
{{- end -}}

{{- define "token-stack.goName" -}}
{{- include "token-stack.componentName" (dict "root" . "component" "go") -}}
{{- end -}}

{{- define "token-stack.postgresName" -}}
{{- include "token-stack.componentName" (dict "root" . "component" "postgres") -}}
{{- end -}}

{{- define "token-stack.postgresInitName" -}}
{{- include "token-stack.componentName" (dict "root" . "component" "postgres-init") -}}
{{- end -}}

{{- define "token-stack.redisName" -}}
{{- include "token-stack.componentName" (dict "root" . "component" "redis") -}}
{{- end -}}

{{- define "token-stack.configName" -}}
{{- include "token-stack.componentName" (dict "root" . "component" "config") -}}
{{- end -}}

{{- define "token-stack.credentialsSecretName" -}}
{{- if .Values.credentials.existingSecret -}}
{{- .Values.credentials.existingSecret -}}
{{- else -}}
{{- include "token-stack.componentName" (dict "root" . "component" "credentials") -}}
{{- end -}}
{{- end -}}

{{- define "token-stack.validate" -}}
{{- if .Values.credentials.create -}}
  {{- if .Values.credentials.existingSecret -}}
    {{- fail "credentials.existingSecret must be empty when credentials.create is true" -}}
  {{- end -}}
  {{- if or (not .Values.credentials.postgresAdminPassword) (not .Values.credentials.postgresAppPassword) (not .Values.credentials.redisPassword) -}}
    {{- fail "all credential passwords are required when credentials.create is true" -}}
  {{- end -}}
{{- else if not .Values.credentials.existingSecret -}}
  {{- fail "credentials.existingSecret is required when credentials.create is false" -}}
{{- end -}}
{{- end -}}

{{- define "token-stack.meshEgress" -}}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: istio-system
  ports:
    - port: 15012
      protocol: TCP
{{- end -}}

{{- define "token-stack.commonEgress" -}}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: kube-system
  ports:
    - port: 53
      protocol: UDP
    - port: 53
      protocol: TCP
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: observability
  ports:
    - port: 4317
      protocol: TCP
    - port: 4318
      protocol: TCP
{{ include "token-stack.meshEgress" . }}
{{- end -}}
