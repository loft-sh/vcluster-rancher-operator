{{/*
Expand the name of the chart.
*/}}
{{- define "vcluster-rancher-op.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "vcluster-rancher-op.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "vcluster-rancher-op.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "vcluster-rancher-op.labels" -}}
helm.sh/chart: {{ include "vcluster-rancher-op.chart" . }}
{{ include "vcluster-rancher-op.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "vcluster-rancher-op.selectorLabels" -}}
app.kubernetes.io/name: {{ include "vcluster-rancher-op.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "vcluster-rancher-op.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "vcluster-rancher-op.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "default_registry" -}}
{{- if .Values.global.cattle.systemDefaultRegistry -}}
{{- printf "%s/" .Values.global.cattle.systemDefaultRegistry -}}
{{- else if .Values.image.registry -}}
{{- printf "%s/" .Values.image.registry -}}
{{- else }}
{{- "" -}}
{{- end -}}
{{- end -}}
