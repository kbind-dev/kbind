{{/* Chart name (overridable). */}}
{{- define "konnector.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Fully qualified app name. */}}
{{- define "konnector.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/* Chart label. */}}
{{- define "konnector.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Common labels. */}}
{{- define "konnector.labels" -}}
helm.sh/chart: {{ include "konnector.chart" . }}
{{ include "konnector.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Selector labels. */}}
{{- define "konnector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "konnector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* ServiceAccount name. */}}
{{- define "konnector.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "konnector.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/* Whether leader election is effectively on (forced when replicaCount > 1). */}}
{{- define "konnector.leaderElect" -}}
{{- if or .Values.leaderElect (gt (int .Values.replicaCount) 1) }}true{{ else }}false{{ end }}
{{- end }}
