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

PKG_ROOT=$(git rev-parse --show-toplevel)
# shellcheck source=test/long-haul/update-test.sh
source "${PKG_ROOT}/test/long-haul/update-test.sh" >/dev/null 2>&1
set +o xtrace
trap - ERR EXIT

# Fixtures stay in the checkout, never in a shared system temporary directory.
FIXTURES="${PKG_ROOT}/_output/long-haul-upgrade-tests-$$"
mkdir -p "${FIXTURES}"
trap 'rm -rf "${FIXTURES}"' EXIT
PoolName="test"
failQuery=""
malformedQuery=""
materializeOnSleep=false
checks=0

function fail() {
	echo "ERROR: $*" >&2
	exit 1
}

function assert_equal() {
	[[ "$1" == "$2" ]] || fail "$3: expected '$1', got '$2'"
	checks=$((checks + 1))
}

function assert_fails() {
	local message=$1
	shift
	if ( "$@" ) >"${FIXTURES}/failure.log" 2>&1; then
		fail "${message}: unexpectedly succeeded"
	fi
	checks=$((checks + 1))
}

function write_history() {
	jq '{
		items: [{
			metadata: {
				name: (.metadata.name + "-desired-hash"),
				ownerReferences: [{kind: "DaemonSet", controller: true, uid: .metadata.uid}],
				labels: {"controller-revision-hash": "desired-hash"}
			},
			revision: 1,
			data: {spec: {template: (.spec.template + {"$patch": "replace"})}}
		}]
	}' "${FIXTURES}/daemonset.json" >"${FIXTURES}/histories.json"
}

function fixture() {
	failQuery=""
	malformedQuery=""
	materializeOnSleep=false
	cat >"${FIXTURES}/daemonset.json" <<'JSON'
{
  "metadata": {"name": "csi-azurelustre-node-jammy", "uid": "ds-uid", "generation": 1},
  "spec": {
    "updateStrategy": {"type": "OnDelete"},
    "template": {
      "metadata": {"labels": {"app": "csi-azurelustre-node"}, "annotations": {"existing": "preserve"}},
      "spec": {"containers": [{"name": "azurelustre", "image": "fixture:v1"}]}
    }
  },
  "status": {
    "observedGeneration": 1, "desiredNumberScheduled": 2,
    "currentNumberScheduled": 2, "updatedNumberScheduled": 2,
    "numberReady": 2, "numberAvailable": 2, "numberUnavailable": 0
  }
}
JSON
	jq -n '{items: [range(1; 3) | {
		metadata: {name: ("node-" + tostring), uid: ("node-uid-" + tostring)},
		status: {nodeInfo: {bootID: ("boot-" + tostring)},
			conditions: [{type: "Ready", status: "True"}]}
	}]}' >"${FIXTURES}/nodes.json"
	jq -n '{items: [range(1; 3) | {
		metadata: {
			name: ("pod-" + tostring), uid: ("pod-uid-" + tostring),
			labels: {"controller-revision-hash": "desired-hash"},
			ownerReferences: [{kind: "DaemonSet", controller: true, name: "csi-azurelustre-node-jammy", uid: "ds-uid"}]
		},
		spec: {nodeName: ("node-" + tostring)},
		status: {conditions: [{type: "Ready", status: "True"}]}
	}]}' >"${FIXTURES}/pods.json"
	write_history
	: >"${FIXTURES}/calls.log"
	stagedDaemonSetSnapshots["csi-azurelustre-node-jammy"]=$(<"${FIXTURES}/daemonset.json")
}

function transform_fixture() {
	local file=$1 filter=$2
	jq "${filter}" "${FIXTURES}/${file}.json" >"${FIXTURES}/transformed.json"
	mv "${FIXTURES}/transformed.json" "${FIXTURES}/${file}.json"
}

function kubectl() {
	printf '%s\n' "$*" >>"${FIXTURES}/calls.log"
	local query="$1 $2"
	if [[ "${query}" == "${failQuery}" ]]; then
		echo "injected kubectl failure: ${query}" >&2
		return 42
	fi
	if [[ "${query}" == "${malformedQuery}" ]]; then
		echo '{malformed'
		return 0
	fi
	case "${query}" in
		"rollout status") return 0 ;;
		"get nodes") cat "${FIXTURES}/nodes.json" ;;
		"get pods") cat "${FIXTURES}/pods.json" ;;
		"get daemonset") cat "${FIXTURES}/daemonset.json" ;;
		"get controllerrevisions") cat "${FIXTURES}/histories.json" ;;
		"patch daemonset")
			local patch=""
			while (( $# > 0 )); do
				if [[ "$1" == "-p" ]]; then
					patch=$2
					break
				fi
				shift
			done
			# Only the guarded OnDelete transition is allowed in these tests.
			jq -e '
				map(select(.op == "replace" and .path == "/spec/updateStrategy"))
				| length == 1 and .[0].value == {type: "OnDelete"}
			' <<<"${patch}" >/dev/null || return 1
			jq --argjson patch "${patch}" '
				reduce $patch[] as $op (.;
					($op.path | split("/")[1:]) as $path
					| if $op.op == "test" then
						if getpath($path) == $op.value then . else error("precondition failed") end
					  else setpath($path; $op.value) end)
				| .metadata.generation += 1
				| .status.observedGeneration = .metadata.generation
				| .status.updatedNumberScheduled = 0
			' "${FIXTURES}/daemonset.json" >"${FIXTURES}/patched.json" || return 1
			mv "${FIXTURES}/patched.json" "${FIXTURES}/daemonset.json"
			write_history
			transform_fixture histories '.items[0].metadata.labels["controller-revision-hash"] = "staged-hash"'
			cat "${FIXTURES}/daemonset.json"
			;;
		*) echo "Unexpected kubectl call: $*" >&2; return 99 ;;
	esac
}

function az() {
	echo "Offline regression must not invoke Azure: $*" >&2
	return 99
}

function sleep() {
	SECONDS=$((SECONDS + $1))
	if [[ "${materializeOnSleep}" == "true" ]]; then
		transform_fixture daemonset '.status.observedGeneration = .metadata.generation'
		write_history
	fi
}

fixture
snapshot=$(<"${FIXTURES}/daemonset.json")
assert_equal true "$(daemonset_readiness "${snapshot}")" "ready OnDelete baseline"
assert_equal true "$(daemonset_readiness "$(jq '.status.updatedNumberScheduled = 0' <<<"${snapshot}")")" "staged OnDelete readiness"
assert_equal false "$(daemonset_readiness "$(jq '.spec.updateStrategy.type = "RollingUpdate" | .status.updatedNumberScheduled = 0' <<<"${snapshot}")")" "old Ready pods are not a RollingUpdate barrier"
assert_equal false "$(daemonset_readiness "$(jq '.status.observedGeneration = 0' <<<"${snapshot}")")" "unobserved generation"
assert_equal false "$(daemonset_readiness "$(jq '.status.numberAvailable = 1' <<<"${snapshot}")")" "availability barrier"
assert_equal false "$(daemonset_readiness "$(jq '.status.currentNumberScheduled = 3' <<<"${snapshot}")")" "surge count barrier"
assert_equal false "$(daemonset_readiness "$(jq '.status.numberMisscheduled = 1' <<<"${snapshot}")")" "misscheduled pods"

assert_equal desired-hash "$(daemonset_desired_revision "${snapshot}")" "controller-owned template match"
transform_fixture histories '.items += [(.items[0] | .revision = 999 | .data.spec.template.spec.containers[0].image = "wrong:v9" | .metadata.labels["controller-revision-hash"] = "wrong-hash")]'
assert_equal desired-hash "$(daemonset_desired_revision "${snapshot}")" "newest history is not necessarily desired"
transform_fixture histories '.items[0].metadata.ownerReferences[0].uid = "other-ds"'
assert_equal "" "$(daemonset_desired_revision "${snapshot}")" "ignore another controller UID"
fixture
transform_fixture histories '.items[0].metadata.ownerReferences[0].controller = false'
assert_equal "" "$(daemonset_desired_revision "${snapshot}")" "ignore non-controller ownership"
fixture
transform_fixture histories '.items += [.items[0]]'
assert_equal "" "$(daemonset_desired_revision "${snapshot}")" "ambiguous matches must not guess a hash"
fixture
transform_fixture histories '.items[0].data.spec.template.metadata.annotations.existing = "different"'
assert_equal "" "$(daemonset_desired_revision "${snapshot}")" "compare annotations as well as images"

fixture
transform_fixture daemonset '.status.observedGeneration = 0'
echo '{"items":[]}' >"${FIXTURES}/histories.json"
materializeOnSleep=true
assert_equal desired-hash "$(wait_for_pinned_daemonset_revision csi-azurelustre-node-jammy 20)" "wait for observation and history materialization"
fixture
transform_fixture daemonset '.status.observedGeneration = 0'
assert_fails "existing history cannot bypass observed generation" wait_for_pinned_daemonset_revision csi-azurelustre-node-jammy 20
fixture
echo '{"items":[]}' >"${FIXTURES}/histories.json"
assert_fails "observed generation cannot bypass history materialization" wait_for_pinned_daemonset_revision csi-azurelustre-node-jammy 20
fixture
transform_fixture daemonset '.metadata.generation += 1'
assert_fails "concurrent generation drift" wait_for_pinned_daemonset_revision csi-azurelustre-node-jammy 20
fixture
transform_fixture daemonset '.spec.template.spec.containers[0].image = "concurrent:v2"'
assert_fails "concurrent template drift" wait_for_pinned_daemonset_revision csi-azurelustre-node-jammy 20

for query in "get nodes" "get pods" "get daemonset" "get controllerrevisions"; do
	fixture
	failQuery="${query}"
	assert_fails "${query} failure is not swallowed during staging" stage_ondelete_migration_revision
	if grep -q '^patch ' "${FIXTURES}/calls.log"; then
		fail "preflight query failure allowed a patch"
	fi
done
fixture
failQuery="get daemonset"
assert_fails "readiness query error" wait_for_csi_driver_ready 20
fixture
malformedQuery="get daemonset"
assert_fails "malformed readiness JSON" wait_for_csi_driver_ready 20
fixture
malformedQuery="get pods"
assert_fails "malformed pod discovery" stage_ondelete_migration_revision
fixture
echo '{"items":[]}' >"${FIXTURES}/pods.json"
assert_fails "empty DaemonSet discovery" stage_ondelete_migration_revision
fixture
echo '{"items":[]}' >"${FIXTURES}/nodes.json"
assert_fails "empty pool discovery" stage_ondelete_migration_revision
fixture
failQuery="rollout status"
assert_fails "controller rollout error" wait_for_csi_driver_ready 20
fixture
transform_fixture daemonset '.spec.updateStrategy.type = "RollingUpdate" | .status.updatedNumberScheduled = 0'
assert_fails "readiness must wait for RollingUpdate completion" wait_for_csi_driver_ready 10

for filter in \
	'.status.updatedNumberScheduled = 0' \
	'.spec.updateStrategy.type = "RollingUpdate" | .status.updatedNumberScheduled = 0'; do
	fixture
	transform_fixture daemonset "${filter}"
	assert_fails "pending baseline must not be forced into RollingUpdate" stage_ondelete_migration_revision
	if grep -q '^patch ' "${FIXTURES}/calls.log"; then
		fail "unconverged baseline allowed a patch"
	fi
done
fixture
transform_fixture pods '.items[1] = .items[0]'
assert_fails "duplicate baseline pods" stage_ondelete_migration_revision
fixture
transform_fixture pods '.items |= .[0:1]'
assert_fails "missing baseline node coverage" stage_ondelete_migration_revision
fixture
transform_fixture pods '.items[0].status.conditions[0].status = "False"'
assert_fails "unready baseline pod" stage_ondelete_migration_revision
fixture
transform_fixture pods '.items[0].metadata.deletionTimestamp = "2026-01-01T00:00:00Z"'
assert_fails "terminating baseline pod" stage_ondelete_migration_revision

fixture
transform_fixture daemonset '.spec.updateStrategy = {type: "RollingUpdate", rollingUpdate: {maxUnavailable: 1}}'
stage_ondelete_migration_revision >"${FIXTURES}/staging.log"
assert_equal staged-hash "${stagedNodeRevisions[csi-azurelustre-node-jammy]}" "pin staged hash"
assert_equal OnDelete "$(jq -r '.spec.updateStrategy.type' "${FIXTURES}/daemonset.json")" "migrate converged RollingUpdate without a setup rollout"
assert_equal preserve "$(jq -r '.spec.template.metadata.annotations.existing' "${FIXTURES}/daemonset.json")" "preserve other template annotations"
verify_staged_pods_unchanged >"${FIXTURES}/staging-check.log"
transform_fixture nodes '.items[0].status.nodeInfo.bootID += "-changed"'
assert_fails "node lifecycle cannot begin during staging" verify_staged_pods_unchanged
transform_fixture nodes '.items[0].status.nodeInfo.bootID = "boot-1"'
transform_fixture pods '.items[0].metadata.labels["controller-revision-hash"] = "unexpected"'
assert_fails "staging must preserve resident revisions" verify_staged_pods_unchanged
transform_fixture pods '.items[0].metadata.labels["controller-revision-hash"] = "desired-hash"'
transform_fixture pods '.items[0].status.conditions[0].status = "False"'
assert_fails "staging must preserve readiness" verify_staged_pods_unchanged
transform_fixture pods '.items[0].status.conditions[0].status = "True" | .items[0].metadata.uid = "replaced-too-soon"'
assert_fails "staging must preserve pod UIDs" verify_staged_pods_unchanged
transform_fixture pods '.items[0].metadata.uid = "pod-uid-1" | .items[1] = .items[0]'
assert_fails "same count cannot hide duplicate staging pods" verify_staged_pods_unchanged
failQuery="get pods"
assert_fails "pod-record query error is not swallowed" verify_staged_pods_unchanged

fixture
stage_ondelete_migration_revision >"${FIXTURES}/staging.log"
assert_fails "unchanged pods/nodes cannot be lifecycle evidence" wait_for_staged_revision_convergence 20
transform_fixture pods '.items[] |= (.metadata.uid += "-new" | .metadata.labels["controller-revision-hash"] = "staged-hash")'
assert_fails "pod deletion alone cannot be lifecycle evidence" wait_for_staged_revision_convergence 20
transform_fixture nodes '.items[].status.nodeInfo.bootID += "-new"'
wait_for_staged_revision_convergence 20 >"${FIXTURES}/convergence.log"
checks=$((checks + 1))
failQuery="get controllerrevisions"
assert_fails "lifecycle history query error" wait_for_staged_revision_convergence 20
failQuery=""
transform_fixture nodes '.items[0].status.conditions[0].status = "False"'
assert_fails "unready replacement node" wait_for_staged_revision_convergence 20
transform_fixture nodes '.items[0].status.conditions[0].status = "True"'
transform_fixture pods '.items[0].metadata.uid = "pod-uid-1"'
assert_fails "old pod UID on a rebooted node" wait_for_staged_revision_convergence 20
transform_fixture pods '.items[0].metadata.uid = "pod-uid-1-new" | .items[0].metadata.labels["controller-revision-hash"] = "moving-hash"'
assert_fails "unexpected resident revision" wait_for_staged_revision_convergence 20
transform_fixture pods '.items[0].metadata.labels["controller-revision-hash"] = "staged-hash"'
transform_fixture histories '.items[0].metadata.labels["controller-revision-hash"] = "moving-hash"'
assert_fails "expected revision remains pinned" wait_for_staged_revision_convergence 20
transform_fixture histories '.items[0].metadata.labels["controller-revision-hash"] = "staged-hash"'
transform_fixture daemonset '.metadata.generation += 1'
assert_fails "post-lifecycle desired template drift" wait_for_staged_revision_convergence 20

fixture
stage_ondelete_migration_revision >"${FIXTURES}/staging.log"
transform_fixture nodes '.items[] |= (.metadata.name += "-replacement" | .metadata.uid += "-replacement")'
transform_fixture pods '.items[] |= (.spec.nodeName += "-replacement" | .metadata.uid += "-replacement" | .metadata.labels["controller-revision-hash"] = "staged-hash")'
wait_for_staged_revision_convergence 20 >"${FIXTURES}/convergence.log"
checks=$((checks + 1))
transform_fixture pods '.items[0].metadata.deletionTimestamp = "2026-01-01T00:00:00Z"'
assert_fails "terminating replacement pod" wait_for_staged_revision_convergence 20
transform_fixture pods 'del(.items[0].metadata.deletionTimestamp) | .items[1] = .items[0]'
assert_fails "same-count duplicate lifecycle pods" wait_for_staged_revision_convergence 20
failQuery="get pods"
assert_fails "lifecycle pod query error" wait_for_staged_revision_convergence 20

echo "Long-haul upgrade safety checks passed (${checks} assertions; no cluster calls)."
