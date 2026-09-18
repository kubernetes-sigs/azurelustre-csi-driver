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
Stable labels for node pod templates. Chart and app versions remain on object
metadata, but must not create a new DaemonSet revision by changing pod labels.
*/}}
{{- define "azurelustre.nodePodLabels" -}}
{{- include "azurelustre.selectorLabels" . }}
app.kubernetes.io/part-of: {{ template "azurelustre.name" . }}
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

{{- define "azurelustre.serviceAccountNameStatusController" -}}
csi-azurelustre-status-controller-sa
{{- end -}}

{{/*
Mirror enforced image-pair validation from the runtime policy parser. Empty
pairs deliberately deny unused flavors instead of borrowing another OS digest.
*/}}
{{- define "azurelustre.validateImagePolicies" -}}
{{- if .Values.compatibilityPolicy.mountAdmission.enforce -}}
{{- $configured := 0 -}}
{{- range $flavor := list "jammy" "noble" "azurelinux3" -}}
{{- $driver := default dict (index $.Values.compatibilityPolicy.images.driver $flavor) -}}
{{- $loader := default dict (index $.Values.compatibilityPolicy.images.loader $flavor) -}}
{{- $driverConfigured := or (not (empty $driver.desiredDigest)) (not (empty $driver.approvedDigests)) -}}
{{- $loaderConfigured := or (not (empty $loader.desiredDigest)) (not (empty $loader.approvedDigests)) -}}
{{- if ne $driverConfigured $loaderConfigured -}}
{{- fail (printf "image policy requires both driver and loader policies for %s" $flavor) -}}
{{- end -}}
{{- if $driverConfigured -}}
{{- $configured = add1 $configured -}}
{{- range $role, $image := dict "driver" $driver "loader" $loader -}}
{{- $approved := list -}}
{{- range $image.approvedDigests -}}
{{- $approved = append $approved (lower .) -}}
{{- end -}}
{{- if or (empty $image.desiredDigest) (not (has (lower $image.desiredDigest) $approved)) -}}
{{- fail (printf "image policy %s/%s desired digest must be approved" $role $flavor) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if eq (int $configured) 0 -}}
{{- fail "enforced image policy requires at least one configured flavor" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Hash the complete release identity, never a truncated namespace. The fixed
prefix plus 128-bit suffix stays within the Namespace DNS-label limit.
*/}}
{{- define "azurelustre.statusControllerElectionNamespace" -}}
{{- printf "azurelustre-control-%s" (printf "%s/%s" .Release.Namespace .Release.Name | sha256sum | trunc 32) -}}
{{- end -}}

{{/*
Refuse adoption even when an install is invoked with --take-ownership. This
namespace must remain exclusive to this release because uninstall deletes it.
*/}}
{{- define "azurelustre.validateElectionNamespaceOwner" -}}
{{- if .existing -}}
{{- $annotations := default dict .existing.metadata.annotations -}}
{{- $labels := default dict .existing.metadata.labels -}}
{{- if or (ne (index $annotations "meta.helm.sh/release-name") .release.Name) (ne (index $annotations "meta.helm.sh/release-namespace") .release.Namespace) (ne (index $labels "app.kubernetes.io/managed-by") "Helm") (ne (index $labels "app.kubernetes.io/component") "status-controller-election") -}}
{{- fail "status-controller election namespace already exists without matching exclusive Helm ownership; refusing adoption" -}}
{{- end -}}
{{- end -}}
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
