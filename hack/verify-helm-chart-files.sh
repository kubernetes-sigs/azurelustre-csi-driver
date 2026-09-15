#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

# This script verifies that the helm chart files in the charts/ directory
# are consistent with the Kubernetes deployment files in the deploy/ directory.
# It checks that for each deploy file, the corresponding helm chart template
# generates the same Kubernetes manifests when rendered with helm template.
# It also checks that there are no unlisted files between the two directories.
#
# The REPOSITORY environment variable can be set to specify the image repository
# to use when rendering the helm charts and normalizing deploy yaml comparisons.
# If not set, it defaults to the MCR production path.
#
# The COLOR environment variable can be set to control diff coloring.
# It defaults to "always".

# The MCR production image path used in deploy yamls
MCR_REPOSITORY="mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi"
REPOSITORY=${REPOSITORY:-${MCR_REPOSITORY}}
COLOR=${COLOR:-always}

# Temp directory for intermediate files during diff comparisons
DIFF_TEMP_DIR=$(mktemp -d)
trap 'rm -rf "${DIFF_TEMP_DIR}"' EXIT

if [[ -z "$(command -v helm)" ]]; then
  echo "Cannot find helm. Please install helm first."
  exit 1
fi

# Install a pinned mikefarah/yq (provides the `yq eval` syntax used below).
# Pin to a specific version for deterministic, supply-chain-safe runs, and
# install into the script-scoped temp dir (cleaned up on exit) instead of a
# system path that may require root or be read-only.
YQ_VERSION="v4.53.3"
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -qi mikefarah; then
  echo "Cannot find mikefarah yq. Installing ${YQ_VERSION} ..."
  yq_arch=$(uname -m)
  case "${yq_arch}" in
    x86_64) yq_arch=amd64 ;;
    aarch64 | arm64) yq_arch=arm64 ;;
    *)
        echo "Unsupported architecture: ${yq_arch}, must be x86_64 or aarch64" >&2
        exit 1
        ;;
  esac
  curl -fsSL "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${yq_arch}" -o "${DIFF_TEMP_DIR}/yq"
  chmod +x "${DIFF_TEMP_DIR}/yq"
  export PATH="${DIFF_TEMP_DIR}:${PATH}"
fi

# Map of deploy files to chart template files. Per-flavor node DaemonSet entries
# are derived from the Makefile's canonical flavor list (`make print-all-flavors`)
# so adding a new flavor in the Makefile automatically extends this check.
declare -A CHARTS_FOR_DEPLOY_FILE=(
["deploy/csi-azurelustre-controller.yaml"]="templates/controller-deployment.yaml"
["deploy/csi-azurelustre-driver.yaml"]="templates/csidriver.yaml"
["deploy/rbac-csi-azurelustre-controller.yaml"]="templates/controller-serviceaccount.yaml templates/controller-clusterrole.yaml templates/controller-clusterrolebinding.yaml"
["deploy/rbac-csi-azurelustre-node.yaml"]="templates/node-serviceaccount.yaml templates/node-clusterrole.yaml templates/node-clusterrolebinding.yaml"
["deploy/pdb-csi-azurelustre-controller.yaml"]="templates/controller-pdb.yaml"
)

# Populate per-flavor node DaemonSet entries from the Makefile flavor list.
ALL_FLAVORS=$(make -s print-all-flavors)
for flavor in ${ALL_FLAVORS}; do
  CHARTS_FOR_DEPLOY_FILE["deploy/csi-azurelustre-node-${flavor}.yaml"]="templates/node-daemonset-${flavor}.yaml"
done

yq_format() {
  # Format yaml for diffing
  yq eval -o=props --properties-array-brackets '
    ... comments="" | # Remove comments from files
    del( # Helm-specific things can be ignored
        .metadata.labels.[
            "helm.sh/chart",
            "app.kubernetes.io/instance",
            "app.kubernetes.io/managed-by",
            "app.kubernetes.io/version"
        ],
        .spec.template.metadata.labels.[
            "app.kubernetes.io/instance",
            "app.kubernetes.io/managed-by",
            "app.kubernetes.io/version",
            "helm.sh/chart"
        ]
    ) |
    {(documentIndex | tostring): .} | # Split multi-doc yaml into separate documents
    style="" # Pretty print
    sort_keys(..) |
    (.. | select( (tag == "!!map" or tag =="!!seq") and length == 0)) = "" # This is necessary to detect empty maps and arrays
    ' "${1}"
}

check_unlisted_files() {
  # Check for files that aren't listed in the CHARTS_FOR_DEPLOY_FILE between deploy and charts
  local version=${1}
  local file_not_found=false
  local referenced_deploy_files referenced_charts_files all_deploy_files all_charts_files

  echo "== Checking for unlisted files between deploy and charts for version: ${version} =="

  referenced_deploy_files=$(printf "%s\n" "${!CHARTS_FOR_DEPLOY_FILE[@]}" | sort)
  referenced_charts_files=$(printf "%s\n" "${CHARTS_FOR_DEPLOY_FILE[@]}" | sort)
  all_deploy_files=$(ls deploy/*.yaml)
  all_charts_files=$(ls charts/"${version}"/azurelustre-csi-driver/templates/*.yaml)

  for file in ${all_deploy_files}; do
    # Check for all actual deploy files in charts references
    if ! grep -q -R -F "${file}" - <<<"${referenced_deploy_files}"; then
      echo "File ${file} missing from list of charts files!"
      file_not_found=true
    fi
  done
  for file in ${all_charts_files}; do
    # Check for all actual chart files in deploy references
    if ! grep -q -R -F "templates/$(basename "${file}")" - <<<"${referenced_charts_files}"; then
      echo "File ${file} missing from list of deploy files!"
      file_not_found=true
    fi
  done
  if [[ "${file_not_found}" == true ]]; then
    echo "Inconsistent chart and deploy files found!"
    echo
    return 1
  fi
  echo "No unlisted files found between deploy and charts for version: ${version}"
  echo
  return 0
}

helm_template() {
  # Generate yaml from helm templates matching specific deploy file
  local version=${1}
  local version_override=${VERSION_OVERRIDE:-${DRIVER_VERSION}}
  local deploy_file=${2}
  local repository=${REPOSITORY}
  local show_only=()
  for value in ${CHARTS_FOR_DEPLOY_FILE[${deploy_file}]}; do
    # Collect the templates that correspond to the deploy file
    show_only+=("--show-only" "${value}")
  done
  helm template \
    --set "fullnameOverride=csi-azurelustre" \
    --set "image.repository=${repository}" \
    --set "image.tag=${version_override}" \
    --namespace kube-system \
    chart-test \
    "${show_only[@]}" \
    ./charts/"${version}"/azurelustre-csi-driver/
}

chart_mapping() {
  # Return list of chart file names by document index
  # Meant for display purposes
  local deploy_file=${1}
  IFS=" " read -r -a charts <<< "${CHARTS_FOR_DEPLOY_FILE[${deploy_file}]}"
  for index in "${!charts[@]}"; do
    echo
    echo -n "${index}: ${charts[${index}]}"
  done
  echo
}

pad_to_length() {
  # Pad string to length with spaces to align diff output
  local str=${1}
  local len=${2}
  printf "%-${len}s" "${str}"
}

diff_outputs() {
  # Show diff output between deploy file and generated chart template
  local version=${1}
  local deploy_file=${2}
  local color_match=$'\\(\x1b\\[[0-9;]*m\\)\\?' # Optionally match color codes at line start (ESC byte via $'...')
  local chart_list
  chart_list=$(chart_mapping "${deploy_file}")

  IFS=" " read -r -a charts_for_deploy_file <<< "${CHARTS_FOR_DEPLOY_FILE[${deploy_file}]}"

  local replacements=()
  for index in "${!charts_for_deploy_file[@]}"; do
    local max_length=${#deploy_file}
    local replacement=${charts_for_deploy_file[${index}]}
    if (( ${#replacement} > max_length )); then
      # Adjust padding length if replacement is longer than deploy file name
      max_length=${#replacement}
    fi
    replacement=$(pad_to_length "${replacement}" "${max_length}")
    # Replace start of chart diff line with chart file name
    replacements+=("-e" $"s|^${color_match}+${index}|\\1${replacement}: |")

    local deploy_file_replacement
    deploy_file_replacement=$(pad_to_length "${deploy_file}" "${max_length}")
    # Replace start of deploy diff line with deploy file name
    replacements+=("-e" $"s|^${color_match}-${index}|\\1${deploy_file_replacement}: |")
  done
  # Replace any remaining +{number} with deploy file name (extra documents in yaml beyond chart files)
  replacements+=("-e" $"s|^${color_match}-[0-9]\+|\\1${deploy_file}: |")

  # Generate formatted output for each side into temp files so we can
  # detect failures in helm_template or yq_format before diffing.
  # Without this, symmetric failures (e.g. yq broken on both sides)
  # produce two empty files and diff reports "no differences".
  local deploy_formatted="${DIFF_TEMP_DIR}/deploy.props"
  local chart_formatted="${DIFF_TEMP_DIR}/chart.props"

  if ! sed "s|${MCR_REPOSITORY}|${REPOSITORY}|g" "${deploy_file}" | yq_format - > "${deploy_formatted}"; then
    echo "ERROR: yq_format failed on deploy file ${deploy_file}"
    return 1
  fi
  if [[ ! -s "${deploy_formatted}" ]]; then
    echo "ERROR: yq_format produced empty output for deploy file ${deploy_file}"
    return 1
  fi

  if ! helm_template "${version}" "${deploy_file}" | yq_format - > "${chart_formatted}"; then
    echo "ERROR: helm_template or yq_format failed for chart version ${version}, deploy file ${deploy_file}"
    return 1
  fi
  if [[ ! -s "${chart_formatted}" ]]; then
    echo "ERROR: helm_template + yq_format produced empty output for ${deploy_file}"
    return 1
  fi

  if output=$(diff -L"Deploy file ${deploy_file}" -L"Charts:${chart_list}" --color="${COLOR}" -u --ignore-space-change "${deploy_formatted}" "${chart_formatted}"); then
    echo "No significant differences found"
    return 0
  else
    # Show diff with more readable file names
    sed "${replacements[@]}" <<<"${output}"
    return 1
  fi
}

check_file_diffs() {
  # Check for diffs between deploy files and generated chart templates
  local version=${1}
  local diff_issues=false
  local sorted_deploy_files
  sorted_deploy_files=$(printf "%s\n" "${!CHARTS_FOR_DEPLOY_FILE[@]}" | sort)

  echo "== Checking file differences between deploy and charts for version: ${version} =="
  for deploy_file in ${sorted_deploy_files}; do
    echo -n "Checking for differences between chart and deploy yaml for file: ${deploy_file}: "
    if ! diff_outputs  "${version}" "${deploy_file}"; then
      diff_issues=true
    fi
  done
  if [[ "${diff_issues}" == true ]]; then
    return 1
  fi
  return 0
}

check_node_template_consistency() {
  # Verify the per-flavor node DaemonSet templates share the same structure.
  # They are split from a single source per OS SKU, so after normalizing the
  # flavor token they must be byte-identical. Any remaining diff is real
  # structural drift (e.g. a feature added to one flavor's template but not
  # another's). Legitimate per-flavor *values* live in values.yaml, not in the
  # templates, so this has near-zero false-positive surface.
  local version=${1}
  local templates_dir="./charts/${version}/azurelustre-csi-driver/templates"
  local consistency_issues=false

  echo "== Checking node DaemonSet template consistency for version: ${version} =="

  # ALL_FLAVORS (the Makefile's canonical list) is the full required set. The
  # driver has shipped a distinct image per OS SKU since v0.4.0, so every
  # currently-supported flavor MUST have a matching node DaemonSet template --
  # a missing one is drift, not an optional case.
  local flavor
  for flavor in ${ALL_FLAVORS}; do
    if [[ ! -f "${templates_dir}/node-daemonset-${flavor}.yaml" ]]; then
      echo "ERROR: missing node DaemonSet template: node-daemonset-${flavor}.yaml"
      consistency_issues=true
    fi
  done
  if [[ "${consistency_issues}" == true ]]; then
    echo
    return 1
  fi

  # Build a sed program that collapses every known flavor token to a
  # placeholder so only structural differences survive the comparison.
  local flavor_sed=()
  for flavor in ${ALL_FLAVORS}; do
    flavor_sed+=("-e" "s/${flavor}/FLAVOR/g")
  done

  # Use the first flavor as the reference; every other flavor's template must
  # match it once the flavor token is normalized away.
  local flavors_arr
  read -r -a flavors_arr <<< "${ALL_FLAVORS}"
  local reference_flavor=${flavors_arr[0]}
  local reference_file="${DIFF_TEMP_DIR}/node-${reference_flavor}.normalized"
  sed "${flavor_sed[@]}" "${templates_dir}/node-daemonset-${reference_flavor}.yaml" > "${reference_file}"

  local output
  for flavor in "${flavors_arr[@]:1}"; do
    local normalized="${DIFF_TEMP_DIR}/node-${flavor}.normalized"
    sed "${flavor_sed[@]}" "${templates_dir}/node-daemonset-${flavor}.yaml" > "${normalized}"
    if ! output=$(diff -L"node-daemonset-${reference_flavor}.yaml (flavor-normalized)" \
                       -L"node-daemonset-${flavor}.yaml (flavor-normalized)" \
                       --color="${COLOR}" -u "${reference_file}" "${normalized}"); then
      echo "ERROR: node-daemonset-${flavor}.yaml differs in structure from node-daemonset-${reference_flavor}.yaml"
      echo "  (per-flavor values may differ, but structure must match; see diff below)"
      echo "${output}"
      consistency_issues=true
    fi
  done

  if [[ "${consistency_issues}" == true ]]; then
    echo
    return 1
  fi
  echo "Node DaemonSet templates are structurally consistent across flavors: ${ALL_FLAVORS}"
  echo
  return 0
}

check_source_version_metadata() {
  # Ev2 injects release metadata into a temporary copy before packaging.
  # Keep the canonical source chart version-neutral.
  local version=${1}
  local chart_dir="./charts/${version}/azurelustre-csi-driver"
  local version_issues=false

  local chart_version
  chart_version=$(yq '.version' "${chart_dir}/Chart.yaml")
  local app_version
  app_version=$(yq '.appVersion' "${chart_dir}/Chart.yaml")
  local image_tag
  image_tag=$(yq '.image.tag' "${chart_dir}/values.yaml")

  echo "== Checking source version metadata for chart: ${version} =="
  echo "  Chart.yaml version: ${chart_version}"
  echo "  Chart.yaml appVersion: ${app_version}"
  echo "  values.yaml image.tag: ${image_tag}"

  if [[ "${chart_version}" != "0.0.0" ]]; then
    echo "ERROR: Source Chart.yaml version must be '0.0.0', got '${chart_version}'"
    version_issues=true
  fi

  if [[ "${app_version}" != "latest" ]]; then
    echo "ERROR: Source Chart.yaml appVersion must be 'latest', got '${app_version}'"
    version_issues=true
  fi

  if [[ "${image_tag}" != "latest" ]]; then
    echo "ERROR: Source values.yaml image.tag must be 'latest', got '${image_tag}'"
    version_issues=true
  fi

  # Check rendered app.kubernetes.io/version labels
  local rendered
  if ! rendered=$(helm template \
    --set "fullnameOverride=csi-azurelustre" \
    --namespace kube-system \
    chart-test \
    "${chart_dir}" 2>&1); then
    echo "ERROR: helm template failed for chart version ${version}:"
    echo "${rendered}"
    echo
    return 1
  fi

  local found_versions
  found_versions=$(echo "${rendered}" | grep -oP 'app\.kubernetes\.io/version:\s*\K\S+' | tr -d '"' | sort -u)

  if [[ -z "${found_versions}" ]]; then
    echo "ERROR: No app.kubernetes.io/version labels found in rendered chart output"
    version_issues=true
  elif [[ $(echo "${found_versions}" | wc -l) -ne 1 ]]; then
    echo "ERROR: Multiple different app.kubernetes.io/version values found in rendered output:"
    echo "${found_versions}"
    version_issues=true
  elif [[ "${found_versions}" != "${app_version}" ]]; then
    echo "ERROR: Rendered app.kubernetes.io/version label '${found_versions}' does not match Chart.yaml appVersion '${app_version}'"
    version_issues=true
  fi

  if [[ "${version_issues}" == true ]]; then
    echo
    return 1
  fi

  echo "Source chart metadata is version-neutral"
  echo
  return 0
}

check_conditional_blocks() {
  # podAnnotations, podLabels and imagePullSecrets are empty by default, so a
  # mis-indented `{{- with .Values.podAnnotations }}` block only breaks when the
  # value is set — invisible to helm lint and to the default-value diff in
  # check_file_diffs. Render once with them populated and confirm (a) the output
  # parses and (b) the injected keys land under spec.template.metadata for every
  # workload. imagePullSecrets lands on the ServiceAccount, so it is covered by
  # the parse step only, not the placement assertion.
  local version=${1}
  local chart_dir="./charts/${version}/azurelustre-csi-driver"

  echo "== Checking conditionally-rendered pod metadata for version: ${version} =="

  local rendered
  if ! rendered=$(helm template \
    --set "fullnameOverride=csi-azurelustre" \
    --set "podAnnotations.probe=present" \
    --set "podLabels.probe=present" \
    --set "imagePullSecrets[0]=verify-pull-secret" \
    --namespace kube-system \
    chart-test \
    "${chart_dir}" 2>&1); then
    echo "ERROR: helm template failed with pod metadata populated:"
    echo "${rendered}"
    echo
    return 1
  fi

  # yq parses every document (failing on broken YAML) and prints the injected
  # key per workload; a leaked or mis-indented block yields MISSING.
  local probes
  if ! probes=$(printf '%s\n' "${rendered}" | yq eval \
    'select(.kind == "Deployment" or .kind == "DaemonSet")
       | .metadata.name
         + " annotation=" + (.spec.template.metadata.annotations.probe // "MISSING")
         + " label=" + (.spec.template.metadata.labels.probe // "MISSING")' - 2>&1); then
    echo "ERROR: rendered chart is not valid YAML with pod metadata populated:"
    echo "${probes}"
    echo
    return 1
  fi

  if grep -q 'MISSING' <<<"${probes}"; then
    echo "ERROR: podAnnotations/podLabels did not render under spec.template.metadata:"
    echo "${probes}"
    echo
    return 1
  fi

  echo "Conditional pod metadata renders correctly:"
  echo "${probes}"
  echo
  return 0
}

check_workload_identity() {
  local version=${1}
  local chart_dir="./charts/${version}/azurelustre-csi-driver"
  local rendered pod_label client_id tenant_id sa_name token_creds

  echo "== Checking workload identity configuration for version: ${version} =="
  if ! rendered=$(helm template --set "fullnameOverride=csi-azurelustre" \
    --set "IdentityClientId=test-client-id" --namespace kube-system \
    chart-test "${chart_dir}" 2>&1); then
    echo "ERROR: helm template failed with workload identity disabled:"
    echo "${rendered}"
    return 1
  fi
  if grep -q 'azure.workload.identity' <<<"${rendered}"; then
    echo "ERROR: workload identity metadata rendered while disabled"
    return 1
  fi
  token_creds=$(printf '%s\n' "${rendered}" | yq eval 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "azurelustre") | .env[] | select(.name == "AZURE_TOKEN_CREDENTIALS") | .value' -)
  if [[ "${token_creds}" != "ManagedIdentityCredential" ]]; then
    echo "ERROR: controller must set the credential to managed identity while workload identity is disabled, got '${token_creds}'"
    return 1
  fi

  if rendered=$(helm template --set "IsWorkloadIdentityEnabled=Enabled" \
    --namespace kube-system chart-test "${chart_dir}" 2>&1); then
    echo "ERROR: workload identity rendered without IdentityClientId"
    return 1
  fi
  if [[ "${rendered}" != *"IdentityClientId must be set"* ]]; then
    echo "ERROR: missing IdentityClientId produced an unexpected error:"
    echo "${rendered}"
    return 1
  fi

  # A near-miss value must be rejected at render time: anything but an exact
  # "Enabled" would otherwise skip both the label and the IdentityClientId guard.
  if rendered=$(helm template --set "IsWorkloadIdentityEnabled=enabled" \
    --set "IdentityClientId=test-client-id" --namespace kube-system \
    chart-test "${chart_dir}" 2>&1); then
    echo "ERROR: invalid IsWorkloadIdentityEnabled value rendered successfully"
    return 1
  fi
  if [[ "${rendered}" != *"must be 'Enabled' or 'Disabled'"* ]]; then
    echo "ERROR: invalid IsWorkloadIdentityEnabled produced an unexpected error:"
    echo "${rendered}"
    return 1
  fi

  if ! rendered=$(helm template --set "fullnameOverride=csi-azurelustre" \
    --set "IsWorkloadIdentityEnabled=Enabled" --set "IdentityClientId=test-client-id" \
    --set "IdentityTenantId=test-tenant-id" --namespace kube-system \
    chart-test "${chart_dir}" 2>&1); then
    echo "ERROR: helm template failed with workload identity enabled:"
    echo "${rendered}"
    return 1
  fi
  # The webhook injects AZURE_CLIENT_ID only for the SA the pod actually runs as,
  # so read the name off the Deployment rather than assuming the default.
  sa_name=$(printf '%s\n' "${rendered}" | yq eval 'select(.kind == "Deployment") | .spec.template.spec.serviceAccountName' -)
  pod_label=$(printf '%s\n' "${rendered}" | yq eval 'select(.kind == "Deployment") | .spec.template.metadata.labels."azure.workload.identity/use"' -)
  client_id=$(printf '%s\n' "${rendered}" | yq eval "select(.kind == \"ServiceAccount\" and .metadata.name == \"${sa_name}\") | .metadata.annotations.\"azure.workload.identity/client-id\"" -)
  tenant_id=$(printf '%s\n' "${rendered}" | yq eval "select(.kind == \"ServiceAccount\" and .metadata.name == \"${sa_name}\") | .metadata.annotations.\"azure.workload.identity/tenant-id\"" -)
  if [[ "${pod_label}" != "true" || "${client_id}" != "test-client-id" || "${tenant_id}" != "test-tenant-id" ]]; then
    echo "ERROR: workload identity metadata did not render correctly"
    printf 'pod label=%s, client ID=%s, tenant ID=%s\n' "${pod_label}" "${client_id}" "${tenant_id}"
    return 1
  fi

  token_creds=$(printf '%s\n' "${rendered}" | yq eval 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "azurelustre") | .env[] | select(.name == "AZURE_TOKEN_CREDENTIALS") | .value' -)
  if [[ "${token_creds}" != "WorkloadIdentityCredential" ]]; then
    echo "ERROR: controller must set the credential to workload identity, got '${token_creds}'"
    return 1
  fi

  echo "Workload identity renders correctly (annotations land on ${sa_name})"
}

check_helm_lint() {
  # Run `helm lint` on the chart source.  Lint is the most fundamental
  # source-side check — if it fails, the rendering/diff work below is
  # suspect anyway, so this runs first per chart.
  local version=${1}
  local chart_dir="./charts/${version}/azurelustre-csi-driver"

  echo "== Linting chart: ${version} =="
  if ! helm lint "${chart_dir}"; then
    echo
    return 1
  fi
  echo
  return 0
}

check_deploy_version_labels() {
  # Verify that app.kubernetes.io/version labels in deploy yamls match DRIVER_VERSION
  echo "== Checking deploy yaml version label consistency =="
  local deploy_issues=false

  local deploy_versions
  deploy_versions=$(grep -ohP 'app\.kubernetes\.io/version:\s*\K\S+' deploy/*.yaml | sort -u)

  if [[ -z "${deploy_versions}" ]]; then
    echo "Warning: No app.kubernetes.io/version labels found in deploy yamls"
    echo
    return 0
  fi

  if [[ $(echo "${deploy_versions}" | wc -l) -ne 1 ]]; then
    echo "ERROR: Multiple different app.kubernetes.io/version values found across deploy yamls:"
    echo "${deploy_versions}"
    deploy_issues=true
  elif [[ "${deploy_versions}" != "${DRIVER_VERSION}" ]]; then
    echo "ERROR: Deploy yaml app.kubernetes.io/version '${deploy_versions}' does not match image version '${DRIVER_VERSION}'"
    deploy_issues=true
  fi

  if [[ "${deploy_issues}" == true ]]; then
    echo
    return 1
  fi

  echo "Deploy yaml version labels consistent: ${deploy_versions}"
  echo
  return 0
}

check_service_account_names() {
  # Render WITHOUT fullnameOverride on purpose: the defaults are hardcoded
  # literals (the controller default is the AKS extension's fixed
  # federated-credential subject), so only a plain render distinguishes them
  # from the older "{{ fullname }}-*-sa" form.
  local version=${1}
  local chart_dir="./charts/${version}/azurelustre-csi-driver"
  local rendered ctrl node

  echo "== Checking ServiceAccount names for version: ${version} =="

  rendered=$(helm template --namespace kube-system chart-test "${chart_dir}")
  ctrl=$(yq eval 'select(.kind == "Deployment") | .spec.template.spec.serviceAccountName' - <<<"${rendered}")
  node=$(yq ea '[select(.kind == "DaemonSet") | .spec.template.spec.serviceAccountName] | unique | .[]' - <<<"${rendered}")
  if [[ "${ctrl}" != "csi-azurelustre-controller-sa" || "${node}" != "csi-azurelustre-node-sa" ]]; then
    echo "ERROR: default SA names wrong: controller='${ctrl}', node='${node}'"
    return 1
  fi

  # The names are fixed by design, so an attempted override must be ignored.
  rendered=$(helm template --namespace kube-system chart-test "${chart_dir}" \
    --set "serviceAccount.controller.name=custom-controller-sa" \
    --set "serviceAccount.node.name=custom-node-sa")
  ctrl=$(yq eval 'select(.kind == "Deployment") | .spec.template.spec.serviceAccountName' - <<<"${rendered}")
  node=$(yq ea '[select(.kind == "DaemonSet") | .spec.template.spec.serviceAccountName] | unique | .[]' - <<<"${rendered}")
  if [[ "${ctrl}" != "csi-azurelustre-controller-sa" || "${node}" != "csi-azurelustre-node-sa" ]]; then
    echo "ERROR: SA names must not be overridable: controller='${ctrl}', node='${node}'"
    return 1
  fi

  echo "ServiceAccount names render correctly (fixed, not overridable)"
}

check_chart_source_layout() {
  local layout_issues=false
  local packaged_charts=()
  local unexpected_dirs=()

  echo "== Checking chart source layout =="

  if [[ -e charts/index.yaml ]]; then
    echo "ERROR: charts/index.yaml is not used for OCI distribution"
    layout_issues=true
  fi

  mapfile -t packaged_charts < <(find charts -type f -name '*.tgz' -print)
  if (( ${#packaged_charts[@]} > 0 )); then
    echo "ERROR: Packaged charts must not be committed:"
    printf '  %s\n' "${packaged_charts[@]}"
    layout_issues=true
  fi

  mapfile -t unexpected_dirs < <(find charts -mindepth 1 -maxdepth 1 -type d ! -name latest -print)
  if (( ${#unexpected_dirs[@]} > 0 )); then
    echo "ERROR: Only charts/latest may contain chart source:"
    printf '  %s\n' "${unexpected_dirs[@]}"
    layout_issues=true
  fi

  if [[ ! -d charts/latest/azurelustre-csi-driver ]]; then
    echo "ERROR: Canonical chart source is missing from charts/latest/azurelustre-csi-driver"
    layout_issues=true
  fi

  if [[ "${layout_issues}" == true ]]; then
    echo
    return 1
  fi

  echo "Chart layout contains one unpackaged canonical source chart"
  echo
}

echo "Verifying helm chart files against deploy yamls ..."

issues_found=false
failures=()

if ! check_chart_source_layout; then
  issues_found=true
  failures+=("Chart source layout check")
fi

# Get expected image version from deploy files
DRIVER_VERSION=$(grep -ohP "image:.*azurelustre-csi:\K[^-]*" deploy/*.yaml | sort -u)
if [[ $(echo "${DRIVER_VERSION}" | wc -l) -ne 1 ]]; then
  echo "Failed to get expected image version from deploy files! Found versions:"
  echo "${DRIVER_VERSION}"
  exit 1
fi
echo "Using expected driver version: ${DRIVER_VERSION}"

if ! check_deploy_version_labels; then
  issues_found=true
  failures+=("Deploy yaml version label check")
fi

version=latest
echo
echo "=== Checking version: ${version} ==="

if ! check_helm_lint "${version}"; then
  issues_found=true
  failures+=("Helm lint (version: ${version})")
else
  if ! check_unlisted_files "${version}"; then
    issues_found=true
    failures+=("Unlisted files check (version: ${version})")
  fi

  if ! check_file_diffs "${version}"; then
    issues_found=true
    failures+=("File diff check (version: ${version})")
  fi

  if ! check_node_template_consistency "${version}"; then
    issues_found=true
    failures+=("Node template consistency check (version: ${version})")
  fi

  if ! check_source_version_metadata "${version}"; then
    issues_found=true
    failures+=("Source version metadata check (version: ${version})")
  fi

  if ! check_service_account_names "${version}"; then
    issues_found=true
    failures+=("ServiceAccount name check (version: ${version})")
  fi

  if ! check_conditional_blocks "${version}"; then
    issues_found=true
    failures+=("Conditional pod metadata check (version: ${version})")
  fi

  if ! check_workload_identity "${version}"; then
    issues_found=true
    failures+=("Workload identity configuration check (version: ${version})")
  fi
fi

echo

if [[ "${issues_found}" == true ]]; then
  echo "==== FAILURE SUMMARY ===="
  printf '  - %s\n' "${failures[@]}"
  echo "========================="
  echo "Helm chart verification failed!"
  exit 1
else
  echo "Helm chart verification succeeded!"
  echo
fi
