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

{{/*
Render a container image reference.
Args (dict): repository (required), tag (required), suffix (optional flavor
suffix, e.g. "-noble"), digest (optional "sha256:...").
Renders "<repository>:<tag><suffix>". When digest is set, it is trimmed and
validated as "sha256:<64 hex>" then appended as "@<digest>" so the image is
pulled by immutable digest while the tag stays human-readable. A malformed
digest fails the render (fast, local) instead of surfacing as a runtime
ImagePullBackOff. Empty digest reproduces the historical tag-only reference.
*/}}
{{- define "azurelustre.imageRef" -}}
{{- $ref := printf "%s:%s%s" .repository .tag (default "" .suffix) -}}
{{- with .digest -}}
{{- $d := trim . -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" $d) -}}
{{- fail (printf "azurelustre.imageRef: invalid image digest %q (expected sha256:<64 hex chars>)" $d) -}}
{{- end -}}
{{- $ref = printf "%s@%s" $ref $d -}}
{{- end -}}
{{- $ref -}}
{{- end -}}
