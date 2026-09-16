#!/usr/bin/env bash

# Copyright 2020 The Kubernetes Authors.
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

set -o errexit
set -o pipefail
set -o nounset

# shellcheck source=test/long-haul/utils.sh
source "$(dirname "${BASH_SOURCE[0]}")/utils.sh"

trap print_debug ERR

declare -a stagedNodeDaemonSets=()
declare -A stagedNodePodUIDs=()
declare -A stagedNodePodCounts=()
declare -A stagedDaemonSetSnapshots=()
declare -A stagedNodeRevisions=()
declare -A baselineNodeRevisions=()
poolNodeNamesJson="[]"
poolNodesJson="[]"
baselinePoolNodesJson="[]"

function refresh_pool_node_names() {
	poolNodesJson=$(kubectl get nodes -l "agentpool=${PoolName}" -o json | jq -c '[
		.items[] | {
			name: .metadata.name, uid: .metadata.uid, bootID: .status.nodeInfo.bootID,
			ready: (any(.status.conditions[]?; .type == "Ready" and .status == "True")
				and .metadata.deletionTimestamp == null)
		}
	] | sort_by(.name)') || return 1
	poolNodeNamesJson=$(jq -c '[.[].name] | sort' <<<"${poolNodesJson}") || return 1
	if [[ "${poolNodeNamesJson}" == "[]" ]]; then
		print_logs_error "No AKS nodes found with agentpool=${PoolName}"
		return 1
	fi
}

function list_active_node_daemonsets() {
	kubectl get pods -n kube-system -l app=csi-azurelustre-node -o json |
		jq -r --argjson poolNodes "${poolNodeNamesJson}" '
			[
				.items[]
				| select(.spec.nodeName as $node | $poolNodes | index($node))
				| .metadata.ownerReferences[]?
				| select(.kind == "DaemonSet" and .controller == true)
				| .name
			]
			| unique[]
		'
}

function node_pod_records() {
	local daemonset=$1
	local daemonsetUID
	daemonsetUID=$(jq -er '.metadata.uid' <<<"${stagedDaemonSetSnapshots[${daemonset}]}") || return 1
	kubectl get pods -n kube-system -l app=csi-azurelustre-node -o json |
		jq -r --arg uid "${daemonsetUID}" --argjson poolNodes "${poolNodeNamesJson}" '
			.items[]
			| select(.spec.nodeName as $node | $poolNodes | index($node))
			| select(any(.metadata.ownerReferences[]?;
				.kind == "DaemonSet" and .controller == true and .uid == $uid))
			| [
				.spec.nodeName,
				.metadata.uid // "-",
				.metadata.labels["controller-revision-hash"] // "-",
				([.status.conditions[]? | select(.type == "Ready") | .status][0] // "False"),
				.metadata.deletionTimestamp // "-"
			]
			| @tsv
		'
}

function wait_for_pinned_daemonset_revision() {
	local daemonset=$1 timeoutSeconds=${2:-120}
	local deadline=$((SECONDS + timeoutSeconds))
	local current matches observed revision
	while (( SECONDS < deadline )); do
		current=$(kubectl get daemonset "${daemonset}" -n kube-system -o json) || return 1
		matches=$(jq -r --argjson expected "${stagedDaemonSetSnapshots[${daemonset}]}" '
			.metadata.uid == $expected.metadata.uid
			and .metadata.generation == $expected.metadata.generation
			and .spec.template == $expected.spec.template
			and .spec.updateStrategy == $expected.spec.updateStrategy
		' <<<"${current}") || return 1
		if [[ "${matches}" != "true" ]]; then
			print_logs_error "DaemonSet ${daemonset} changed concurrently; refusing to follow a moving target" >&2
			return 1
		fi
		observed=$(jq -r '(.status.observedGeneration // 0) >= .metadata.generation' <<<"${current}") || return 1
		if [[ "${observed}" == "true" ]]; then
			revision=$(daemonset_desired_revision "${current}") || return 1
			if [[ -n "${revision}" ]]; then
				printf '%s\n' "${revision}"
				return 0
			fi
		fi
		sleep 5
	done
	print_logs_error "Timed out waiting for ${daemonset} observed generation and matching ControllerRevision" >&2
	return 1
}

function record_node_pod_uids() {
	local daemonset=$1 recordCount=0
	local records node uid revision ready deleting key
	# Capture before reading: process substitutions hide kubectl/jq failures.
	records=$(node_pod_records "${daemonset}") || return 1

	while IFS=$'\t' read -r node uid revision ready deleting; do
		[[ -n "${node}" ]] || continue
		key="${daemonset}/${node}"
		if [[ -n "${stagedNodePodUIDs[${key}]:-}" || "${uid}" == "-" ||
			"${ready}" != "True" || "${deleting}" != "-" ||
			"${revision}" != "${stagedNodeRevisions[${daemonset}]}" ]]; then
			print_logs_error "Baseline requires exactly one Ready, non-terminating current-revision pod per node: ${daemonset}/${node}"
			return 1
		fi
		stagedNodePodUIDs["${key}"]="${uid}"
		recordCount=$((recordCount + 1))
		print_logs_info "Before staging: daemonset=${daemonset}, node=${node}, podUID=${uid}, revision=${revision}, ready=${ready}"
	done <<<"${records}"

	if (( recordCount == 0 )); then
		print_logs_error "No ${daemonset} pod found on test pool ${PoolName}"
		return 1
	fi
	stagedNodePodCounts["${daemonset}"]="${recordCount}"
}

function stage_ondelete_migration_revision() {
	local migrationRevision daemonsets daemonset status ready baselineRevision patch patched
	migrationRevision="long-haul-$(date -u +%Y%m%dT%H%M%S%NZ)"
	refresh_pool_node_names || return 1
	baselinePoolNodesJson="${poolNodesJson}"
	ready=$(jq -r 'all(.[];
		.ready and (.uid | type == "string" and length > 0)
		and (.bootID | type == "string" and length > 0))' <<<"${baselinePoolNodesJson}") || return 1
	if [[ "${ready}" != "true" ]]; then
		print_logs_error "Baseline requires Ready pool nodes with Node UID and bootID lifecycle evidence"
		return 1
	fi
	daemonsets=$(list_active_node_daemonsets) || return 1
	stagedNodeDaemonSets=()
	stagedNodePodUIDs=()
	stagedNodePodCounts=()
	stagedDaemonSetSnapshots=()
	stagedNodeRevisions=()
	baselineNodeRevisions=()
	if [[ -n "${daemonsets}" ]]; then
		mapfile -t stagedNodeDaemonSets <<<"${daemonsets}"
	fi

	if [[ ${#stagedNodeDaemonSets[@]} -eq 0 ]]; then
		print_logs_error "No active Azure Lustre node DaemonSet found for the test pool"
		return 1
	fi

	# Read-only preflight for EVERY active flavor before mutating any template.
	# Never switch to RollingUpdate to converge a pending OnDelete revision:
	# mounted workloads may still depend on the resident Lustre client.
	for daemonset in "${stagedNodeDaemonSets[@]}"; do
		status=$(kubectl get daemonset "${daemonset}" -n kube-system -o json) || return 1
		ready=$(daemonset_readiness "${status}") || return 1
		if [[ "${ready}" != "true" ]] ||
			! jq -e '(.status.updatedNumberScheduled // 0) == .status.desiredNumberScheduled' <<<"${status}" >/dev/null; then
			print_logs_error "Unconverged baseline for ${daemonset}. Complete an approved drained-node lifecycle first; this test will not enable RollingUpdate or delete mounted node pods."
			return 1
		fi
		stagedDaemonSetSnapshots["${daemonset}"]="${status}"
		baselineRevision=$(wait_for_pinned_daemonset_revision "${daemonset}") || return 1
		stagedNodeRevisions["${daemonset}"]="${baselineRevision}"
		baselineNodeRevisions["${daemonset}"]="${baselineRevision}"
		record_node_pod_uids "${daemonset}" || return 1
	done
	verify_pool_pod_coverage || return 1

	for daemonset in "${stagedNodeDaemonSets[@]}"; do
		patch=$(jq -c --arg revision "${migrationRevision}" '[
			{op: "test", path: "/metadata/uid", value: .metadata.uid},
			{op: "test", path: "/metadata/generation", value: .metadata.generation},
			{op: "test", path: "/spec/template", value: .spec.template},
			{op: "replace", path: "/spec/updateStrategy", value: {type: "OnDelete"}},
			{op: "add", path: "/spec/template/metadata/annotations",
			 value: ((.spec.template.metadata.annotations // {}) +
				{"azurelustre.csi.azure.com/long-haul-revision": $revision})}
		]' <<<"${stagedDaemonSetSnapshots[${daemonset}]}") || return 1
		patched=$(kubectl patch daemonset "${daemonset}" -n kube-system --type=json \
			-p "${patch}" -o json) || return 1
		stagedDaemonSetSnapshots["${daemonset}"]="${patched}"
	done

	for daemonset in "${stagedNodeDaemonSets[@]}"; do
		baselineRevision="${stagedNodeRevisions[${daemonset}]}"
		stagedNodeRevisions["${daemonset}"]=$(wait_for_pinned_daemonset_revision "${daemonset}") || return 1
		if [[ "${baselineRevision}" == "${stagedNodeRevisions[${daemonset}]}" ]]; then
			print_logs_error "Staging did not create a distinct pod revision for ${daemonset}"
			return 1
		fi
	done
	verify_staged_pods_unchanged
}

function verify_staged_pods_unchanged() {
	local daemonset records currentCount node uid revision ready deleting key
	refresh_pool_node_names || return 1
	if [[ "${poolNodesJson}" != "${baselinePoolNodesJson}" ]]; then
		print_logs_error "Pool node identities/readiness changed during staging; lifecycle must not begin before the staging check"
		return 1
	fi
	for daemonset in "${stagedNodeDaemonSets[@]}"; do
		currentCount=0
		local -A seen=()
		records=$(node_pod_records "${daemonset}") || return 1
		while IFS=$'\t' read -r node uid revision ready deleting; do
			[[ -n "${node}" ]] || continue
			currentCount=$((currentCount + 1))
			key="${daemonset}/${node}"
			if [[ -n "${seen[${node}]:-}" || "${uid}" != "${stagedNodePodUIDs[${key}]:-}" ||
				"${ready}" != "True" || "${deleting}" != "-" ]]; then
				print_logs_error "OnDelete staging changed node pod identity/coverage/readiness: daemonset=${daemonset}, node=${node}, podUID=${uid}"
				return 1
			fi
			seen["${node}"]=1
			if [[ "${revision}" != "${baselineNodeRevisions[${daemonset}]}" ]]; then
				print_logs_error "Node pod revision changed before a node lifecycle event: daemonset=${daemonset}, node=${node}, revision=${revision}"
				return 1
			fi
			print_logs_info "Staged without replacement: daemonset=${daemonset}, node=${node}, podUID=${uid}, residentRevision=${revision}, desiredRevision=${stagedNodeRevisions[${daemonset}]}, ready=${ready}"
		done <<<"${records}"
		if (( currentCount != stagedNodePodCounts["${daemonset}"] )); then
			print_logs_error "Node pod count changed while staging ${daemonset}: before=${stagedNodePodCounts[${daemonset}]}, after=${currentCount}"
			return 1
		fi
	done
}

function verify_pool_pod_coverage() {
	local daemonset records nodes="" covered
	for daemonset in "${stagedNodeDaemonSets[@]}"; do
		records=$(node_pod_records "${daemonset}") || return 1
		nodes+="${records}"$'\n'
	done
	covered=$(jq -Rsc 'split("\n") | map(select(length > 0) | split("\t")[0]) | sort' <<<"${nodes}") || return 1
	if [[ "${covered}" != "${poolNodeNamesJson}" ]]; then
		print_logs_error "Expected exactly one CSI pod on every test-pool node; got ${covered}, expected ${poolNodeNamesJson}"
		return 1
	fi
}

function wait_for_staged_revision_convergence() {
	local timeoutSeconds=${1:-900}
	local deadline=$((SECONDS + timeoutSeconds))
	local daemonset desiredRevision records currentCount converged node uid revision ready deleting oldUID lifecycle
	while (( SECONDS < deadline )); do
		refresh_pool_node_names || return 1
		converged=true
		local -A evidence=()
		for daemonset in "${stagedNodeDaemonSets[@]}"; do
			desiredRevision=$(wait_for_pinned_daemonset_revision "${daemonset}" 30) || return 1
			if [[ "${desiredRevision}" != "${stagedNodeRevisions[${daemonset}]}" ]]; then
				print_logs_error "Desired revision drifted for ${daemonset}; expected ${stagedNodeRevisions[${daemonset}]}, found ${desiredRevision}"
				return 1
			fi
			currentCount=0
			local -A seen=()
			records=$(node_pod_records "${daemonset}") || return 1
			evidence["${daemonset}"]="${records}"
			while IFS=$'\t' read -r node uid revision ready deleting; do
				[[ -n "${node}" ]] || continue
				currentCount=$((currentCount + 1))
				if [[ "${revision}" != "${stagedNodeRevisions[${daemonset}]}" || "${ready}" != "True" ||
					"${deleting}" != "-" || "${uid}" == "-" || -n "${seen[${node}]:-}" ]]; then
					converged=false
				fi
				seen["${node}"]=1
				for oldUID in "${stagedNodePodUIDs[@]}"; do
					if [[ "${uid}" == "${oldUID}" ]]; then
						converged=false
					fi
				done
				lifecycle=$(jq -r --arg node "${node}" --argjson baseline "${baselinePoolNodesJson}" '
					[.[] | select(.name == $node)] as $current
					| $current | length == 1 and (.[0] |
						.ready and (.uid | type == "string" and length > 0)
						and (.bootID | type == "string" and length > 0)
						and (. as $new | all($baseline[];
							.uid != $new.uid or .bootID != $new.bootID)))
				' <<<"${poolNodesJson}") || return 1
				if [[ "${lifecycle}" != "true" ]]; then
					converged=false
				fi
			done <<<"${records}"
			if (( currentCount != stagedNodePodCounts["${daemonset}"] )); then
				converged=false
			fi
		done

		if [[ "${converged}" == "true" ]]; then
			verify_pool_pod_coverage || return 1
			for daemonset in "${stagedNodeDaemonSets[@]}"; do
				while IFS=$'\t' read -r node uid revision ready deleting; do
					[[ -n "${node}" ]] || continue
					print_logs_info "Lifecycle activation evidence: daemonset=${daemonset}, node=${node}, podUID=${uid}, revision=${revision}, ready=${ready}"
				done <<<"${evidence[${daemonset}]}"
			done
			print_logs_info "Baseline node lifecycle identities: ${baselinePoolNodesJson}"
			print_logs_info "Activated node lifecycle identities: ${poolNodesJson}"
			return 0
		fi
		sleep 10
	done
	print_logs_error "Timed out waiting for pinned staged revisions, replacement pod UIDs and node UID/bootID changes. A no-op AKS upgrade is not lifecycle evidence; do not force pod replacement on mounted nodes."
	return 1
}

function print_versions () {
	# Wait for CSI driver workloads to be Ready (readiness probe validates that
	# Lustre client modules are installed and LNet is operational on nodes).
	wait_for_csi_driver_ready 600

	# shellcheck disable=SC2154 # ResourceGroup, ClusterName, PoolName are expected env vars from start-long-haul.sh
	nodepool=$(az aks nodepool show --resource-group "${ResourceGroup}" --cluster-name "${ClusterName}" --nodepool-name "${PoolName}")
	currentNodeImageVersion=$(echo "${nodepool}" | jq -r '.nodeImageVersion')

	nodepoolUpgrades=$(az aks nodepool get-upgrades --resource-group "${ResourceGroup}" --cluster-name "${ClusterName}" --nodepool-name "${PoolName}")
	nodeK8sVersion=$(echo "${nodepoolUpgrades}" | jq -r '.kubernetesVersion')

	controlPlaneUpgrades=$(az aks get-upgrades --resource-group "${ResourceGroup}" --name "${ClusterName}")
	currentControlPlaneK8sVersion=$(echo "${controlPlaneUpgrades}" | jq -r '.controlPlaneProfile.kubernetesVersion')

	podName=$(kubectl get pods -n kube-system -l app=csi-azurelustre-node -o wide --field-selector=status.phase=Running --sort-by=.metadata.creationTimestamp | grep "${PoolName}" | awk '{print $1}' | head -n 1)
	echo "Get kernel version and Lustre module version from pod ${podName}"
	kernelVersion=$(kubectl exec -n kube-system -it "${podName}" -c azurelustre -- /bin/bash -c "uname -r")
	# Detect OS family to use the right package query
	osID=$(kubectl exec -n kube-system -it "${podName}" -c azurelustre -- /bin/bash -c ". /etc/os-release; echo \${ID:-}")
	osID=$(echo "${osID}" | tr -d '[:space:]')
	if [[ "${osID}" == "azurelinux" || "${osID}" == "mariner" ]]; then
		# Prefer the kmod module package (parity with the Ubuntu branch); fall back to
		# the amlfs metapackage only if no kmod package is installed. Querying both
		# patterns together would let head -n 1 nondeterministically pick the metapackage.
		module=$(kubectl exec -n kube-system -it "${podName}" -c azurelustre -- /bin/bash -c "m=\$(rpm -qa 'kmod-lustre-client-*' --queryformat '%{NAME}|%{VERSION}\n' | head -n 1); [[ -z \"\${m}\" ]] && m=\$(rpm -qa 'amlfs-lustre-client-*' --queryformat '%{NAME}|%{VERSION}\n' | head -n 1); echo \"\${m}\"")
	else
		module=$(kubectl exec -n kube-system -it "${podName}" -c azurelustre -- /bin/bash -c "dpkg-query -f '\${Package}|\${Version}' -W kmod-lustre-client-*")
	fi
	modulePkgName=${module%|*}
	modulePkgVersion=${module#*|}

	print_logs_info "Node image version: ${currentNodeImageVersion}"
	print_logs_info "Node Kubernetes version: ${nodeK8sVersion}"
	print_logs_info "Control-plane Kubernetes version: ${currentControlPlaneK8sVersion}"
	print_logs_info "OS kernel version: ${kernelVersion}"
	print_logs_info "Lustre client module package name: ${modulePkgName}"
	print_logs_info "Lustre client module package version: ${modulePkgVersion}"
}

# Allow the offline regression test to source the helpers without invoking AKS.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
	return 0
fi

print_logs_title "Print versions before"
print_versions

print_logs_title "Verify OnDelete staging (requires an already converged baseline)"
stage_ondelete_migration_revision

kubernetesUpgrades=$(az aks get-upgrades --resource-group "${ResourceGroup}" --name "${ClusterName}" | jq -r .controlPlaneProfile.upgrades)

if [[ "${kubernetesUpgrades}" != "null" ]]; then
	# Skip preview AKS version and get the latest one
	latestKubernetesVersion=$(echo "${kubernetesUpgrades}" | jq -r '.[] | select (.isPreview == null) | .kubernetesVersion' | tail -n 1)

	if [[ -n "${latestKubernetesVersion}" ]]; then
		print_logs_info "Upgrading Kubernetes control-plane to version ${latestKubernetesVersion}"
		az aks upgrade --resource-group "${ResourceGroup}" --name "${ClusterName}" --yes --kubernetes-version "${latestKubernetesVersion}"
		print_logs_info "Waiting for control-plane upgrade to fully complete"
		az aks wait --resource-group "${ResourceGroup}" --name "${ClusterName}" --updated --interval 30 --timeout 1800
	fi
else
	echo "Kubernetes control-plane version is the latest"
fi

print_logs_info "Upgrading node pool to the latest node image"
az aks nodepool upgrade --resource-group "${ResourceGroup}" --cluster-name "${ClusterName}" --name "${PoolName}" --node-image-only -y
print_logs_info "Waiting for node image upgrade to fully complete"
az aks nodepool wait --resource-group "${ResourceGroup}" --cluster-name "${ClusterName}" --nodepool-name "${PoolName}" --updated --interval 30 --timeout 1800

print_logs_info "Upgrading node pool to the latest"
az aks nodepool upgrade --resource-group "${ResourceGroup}" --cluster-name "${ClusterName}" --name "${PoolName}" -y
print_logs_info "Waiting for node pool upgrade to fully complete"
az aks nodepool wait --resource-group "${ResourceGroup}" --cluster-name "${ClusterName}" --nodepool-name "${PoolName}" --updated --interval 30 --timeout 1800

print_logs_title "Verify staged node revision activated through node lifecycle"
wait_for_staged_revision_convergence 900

print_logs_title "Print versions after"
print_versions

samplePod=$(get_pod "azurelustre-longhaulsample-deployment")

if [[ -n "${samplePod}" ]]
then
	podName=$(echo "${samplePod}" | awk '{print $2}')
	podStatus=$(echo "${samplePod}" | awk '{print $4}')
	print_logs_error "find pod ${samplePod} in ${podStatus} state, expect no running sample pod"
fi

print_logs_title "Start and verify sample workload"
# Ensure CSI driver is fully ready after upgrade before starting workload
wait_for_csi_driver_ready 600

start_sample_workload

verify_sample_workload_by_pod_status workloadPodName workloadNodeName