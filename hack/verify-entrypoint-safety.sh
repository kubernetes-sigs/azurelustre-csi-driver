#!/bin/bash

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

set -o errexit
set -o nounset
set -o pipefail

PKG_ROOT=$(git rev-parse --show-toplevel)

# shellcheck source=pkg/azurelustreplugin/entrypoint.sh
source "${PKG_ROOT}/pkg/azurelustreplugin/entrypoint.sh"
set +o xtrace

LUSTRE_VERSION="2.16.1"
CLIENT_SHA_SUFFIX="21-g153e389"
osFamily="ubuntu"

function fail() {
  echo "ERROR: $*" >&2
  exit 1
}

function assert_equal() {
  local expected="$1"
  local actual="$2"
  local message="$3"
  [[ "${actual}" == "${expected}" ]] || fail "${message}: expected ${expected}, got ${actual}"
}

function run_evict_scenario() {
  local fixture_loaded="$1"
  local unload_result="$2"
  local expected_result="$3"
  local expected_removals="$4"
  local removals=0

  function loaded_lustre_version() {
    echo "${fixture_loaded}"
  }
  function lustre_rmmod() {
    :
  }
  function unload_lustre_modules() {
    return "${unload_result}"
  }
  function remove_installed_lustre_packages() {
    removals=$((removals + 1))
  }

  local result=0
  evict_old_lustre >/dev/null 2>&1 || result=$?
  assert_equal "${expected_result}" "${result}" "unexpected eviction result for loaded version '${fixture_loaded:-none}'"
  assert_equal "${expected_removals}" "${removals}" "unexpected package removal count for loaded version '${fixture_loaded:-none}'"
}

run_evict_scenario "$(wanted_module_version)" 0 1 0
run_evict_scenario "2.15.7_33_g79ddf99" 1 1 0
run_evict_scenario "2.15.7_33_g79ddf99" 0 0 1
run_evict_scenario "" 0 0 1

install_attempts=0
eviction_attempts=0

function failing_install() {
  install_attempts=$((install_attempts + 1))
  return 1
}

function refusing_eviction() {
  eviction_attempts=$((eviction_attempts + 1))
  return 1
}

function sleep() {
  :
}

result=0
retry_install refusing_eviction failing_install >/dev/null 2>&1 || result=$?
assert_equal "1" "${result}" "retry_install should report exhausted attempts"
assert_equal "3" "${install_attempts}" "retry_install attempt count"
assert_equal "2" "${eviction_attempts}" "eviction should run only before a retry"

function run_loader_termination_scenario() (
  local signal="$1"
  local phase="$2"
  local module_state="$3"
  local expected_calls="$4"
  local loader_pid="${BASHPID}"
  local unload_attempts=0
  local calls=""

  function signal_at() {
    if [[ "${phase}" == "$1" ]]; then
      kill -s "${signal}" "${loader_pid}"
    fi
  }
  function detect_os_family() {
    osFamily="ubuntu"
  }
  function update_ca_trust() {
    :
  }
  function node_teardown_requested() {
    return 1
  }
  function install_lustre_client() {
    calls+=" install:$1"
    signal_at install
  }
  function load_lnet() {
    calls+=" load"
    signal_at load
  }
  function check_loaded_module_version() {
    calls+=" check"
  }
  function reconcile_lnet() {
    calls+=" reconcile"
    signal_at reconcile
    fail "loader continued after ${signal} during ${phase}"
  }
  function sleep() {
    if [[ "${phase}" == "wait" ]]; then
      command sleep 0.01
      signal_at wait
    fi
  }
  # shellcheck disable=SC2329 # Safety mock reached only if the signal handler attempts an unload.
  function unload_lustre_modules() {
    unload_attempts=$((unload_attempts + 1))
    [[ "${module_state}" == "idle" ]]
  }
  # Intercept the old signal hook even on a test host without Lustre modules.
  # shellcheck disable=SC2329 # Regression sentinel for the removed termination hook.
  function teardown_lnet() {
    unload_lustre_modules
  }
  # shellcheck disable=SC2329 # The subshell's EXIT trap checks the signal handler's result.
  function assert_termination() {
    local result=$?
    trap - EXIT
    assert_equal "0" "${result}" "loader termination status for ${signal}/${phase}/${module_state}"
    assert_equal "0" "${unload_attempts}" "termination must not attempt module unload for ${signal}/${phase}/${module_state}"
    assert_equal "${expected_calls}" "${calls}" "loader lifecycle before ${signal}/${phase}/${module_state}"
  }

  trap 'assert_termination' EXIT
  AZURELUSTRE_CSI_ROLE="loader"
  AZURELUSTRE_CSI_LNET_RECONCILE_INTERVAL=0
  AZURELUSTRE_TEARDOWN_DIR=""
  main >/dev/null
  fail "loader continued after ${signal} during ${phase}"
)

for signal in TERM INT; do
  for module_state in busy idle; do
    run_loader_termination_scenario "${signal}" wait "${module_state}" " install:full load check"
  done
done
run_loader_termination_scenario TERM install busy " install:full"
run_loader_termination_scenario TERM load idle " install:full load"
run_loader_termination_scenario TERM reconcile busy " install:full load check reconcile"

function run_teardown_restart_scenario() (
  local health_result="$1"
  local worker_result="$2"
  local expected_result="$3"
  local expected_calls="$4"
  local calls=""

  function node_teardown_requested() {
    return 0
  }
  function node_teardown_command() {
    if [[ "$1" == "--experimental-node-teardown-health" ]]; then
      calls+=" health"
      return "${health_result}"
    fi
    [[ "$1" == "--experimental-node-teardown-worker" ]] || fail "unexpected teardown command"
    calls+=" worker"
    return "${worker_result}"
  }
  function install_lustre_client() {
    calls+=" install:$1"
  }
  function load_lnet() {
    fail "loader restart must not load modules after a teardown request"
  }
  function reconcile_lnet() {
    fail "drained loader must not reconcile LNet"
  }
  function wait_for_node_retirement() {
    calls+=" idle:$1"
  }

  local result=0
  run_loader >/dev/null 2>&1 || result=$?
  assert_equal "${expected_result}" "${result}" "loader restart result"
  assert_equal "${expected_calls}" "${calls}" "loader restart lifecycle"
)

run_teardown_restart_scenario 0 0 0 " health install:utils worker idle:0"
run_teardown_restart_scenario 0 1 0 " health install:utils worker idle:1"
run_teardown_restart_scenario 1 0 1 " health"

echo "Entrypoint package-eviction, routine-termination and completion-teardown safety checks passed."
