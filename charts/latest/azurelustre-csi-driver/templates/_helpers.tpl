{{/* vim: set filetype=mustache: */}}

{{/* Expand the name of the chart.*/}}
{{- define "azurelustre.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "azurelustre.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common selectors.
*/}}
{{- define "azurelustre.selectorLabels" -}}
app.kubernetes.io/name: {{ template "azurelustre.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "azurelustre.labels" -}}
{{- include "azurelustre.selectorLabels" . }}
app.kubernetes.io/part-of: {{ template "azurelustre.name" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ template "azurelustre.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Full entity names.
*/}}
{{- define "azurelustre.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "azurelustre.serviceAccountNameController" -}}
csi-azurelustre-controller-sa
{{- end -}}

{{- define "azurelustre.serviceAccountNameNode" -}}
csi-azurelustre-node-sa
{{- end -}}

{{/*
Image pull secrets.
*/}}
{{- define "azurelustre.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- range . }}
  - name: {{ . }}
  {{- end }}
{{- end }}
{{- end -}}
