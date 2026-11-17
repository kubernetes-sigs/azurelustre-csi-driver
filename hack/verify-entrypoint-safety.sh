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

# shellcheck disable=SC2329 # Mocks include sentinels for forbidden recovery callbacks.
function run_evict_scenario() (
  local fixture_loaded="$1"
  local fixture_resident="$2"
  local expected_result="$3"
  local expected_removals="$4"
  local removals=0 unload_attempts=0

  function loaded_lustre_version() {
    echo "${fixture_loaded}"
  }
  function lustre_modules_loaded() {
    [[ "${fixture_resident}" == true ]]
  }
  function lustre_rmmod() {
    unload_attempts=$((unload_attempts + 1))
  }
  # shellcheck disable=SC2329 # Sentinel for the removed recovery-time unload.
  function unload_lustre_modules() {
    lustre_rmmod
  }
  function remove_installed_lustre_packages() {
    removals=$((removals + 1))
  }

  local result=0
  evict_old_lustre >/dev/null 2>&1 || result=$?
  assert_equal "${expected_result}" "${result}" "unexpected eviction result for loaded version '${fixture_loaded:-none}'"
  assert_equal "${expected_removals}" "${removals}" "unexpected package removal count for loaded version '${fixture_loaded:-none}'"
  assert_equal "0" "${unload_attempts}" "ordinary recovery must never unload resident modules"
)

run_evict_scenario "$(wanted_module_version)" true 1 0
run_evict_scenario "2.15.7_33_g79ddf99" true 1 0
run_evict_scenario "" true 1 0
run_evict_scenario "" false 0 1

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

# shellcheck disable=SC2329 # Full installation dispatches these mock callbacks indirectly.
function run_failed_loader_install_scenario() (
  local install_attempts=0 removals=0 unload_attempts=0
  function setup_pmc_repo() { :; }
  function full_package_spec() { echo test-package; }
  function loaded_lustre_version() { echo "2.15.7_33_g79ddf99"; }
  function lustre_modules_loaded() { return 0; }
  function lustre_rmmod() { unload_attempts=$((unload_attempts + 1)); }
  # shellcheck disable=SC2329 # An available unload helper must not be used by recovery.
  function unload_lustre_modules() { lustre_rmmod; }
  function remove_installed_lustre_packages() { removals=$((removals + 1)); }
  function install_pkg() {
    install_attempts=$((install_attempts + 1))
    return 1
  }
  local result=0
  install_lustre_client full >/dev/null 2>&1 || result=$?
  assert_equal "1" "${result}" "failed loader setup must remain a failure"
  assert_equal "3" "${install_attempts}" "full loader setup must retain bounded retries"
  assert_equal "0" "${unload_attempts}" "failed full installs must preserve supported non-current runtime"
  assert_equal "0" "${removals}" "failed full installs must preserve resident client packages"
)

run_failed_loader_install_scenario

function run_supervised_apt_scenario() (
  local AZURELUSTRE_CSI_ROLE="$1"
  local journal_present="$2"
  local repair_result="$3"
  local install_result="$4"
  local expected_repairs="$5"
  local expected_pty="$6"
  local repairs=0 installs=0 supervised_pty=false
  function compgen() {
    [[ "$*" == "-G /var/lib/dpkg/updates/[0-9]*" ]] || fail "unexpected journal query"
    [[ "${journal_present}" == true ]]
  }
  function dpkg() {
    [[ "$*" == "--force-confdef --force-confold --configure -a" ]] || fail "unsafe dpkg repair"
    repairs=$((repairs + 1))
    return "${repair_result}"
  }
  function apt-get() {
    installs=$((installs + 1))
    [[ " $* " != *" DPkg::Use-Pty=0 "* ]] || supervised_pty=true
    [[ "${*: -1}" == test-package ]] || fail "lost requested package"
    return "${install_result}"
  }
  local result=0
  install_pkg test-package >/dev/null 2>&1 || result=$?
  assert_equal "${expected_repairs}" "${repairs}" "interrupted transaction repair count"
  assert_equal "1" "${installs}" "APT must get a chance to resolve pending dependencies"
  assert_equal "${expected_pty}" "${supervised_pty}" "supervised package process-group policy"
  assert_equal "${install_result}" "${result}" "package install failure propagation"
)

run_supervised_apt_scenario driver-utils true 0 0 1 true
run_supervised_apt_scenario driver-utils true 1 0 1 true
run_supervised_apt_scenario driver-utils true 1 42 1 true
run_supervised_apt_scenario driver-utils false 0 0 0 true
run_supervised_apt_scenario driver true 0 0 0 false
run_supervised_apt_scenario loader true 0 0 0 false

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
  local AZURELUSTRE_CSI_ROLE="loader"
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

mkdir -p "${PKG_ROOT}/_output"
startup_fixture=$(mktemp -d "${PKG_ROOT}/_output/startup-safety.XXXXXX")
trap 'rm -f "${startup_fixture}/loader-started" "${startup_fixture}/loader-ready" "${startup_fixture}/driver-ready"; rmdir "${startup_fixture}"' EXIT

function run_driver_startup_scenario() (
  local enabled="$1"
  local expected="$2"
  local calls=""
  local AZURELUSTRE_NODE_STARTUP_DIR=""
  if [[ "${enabled}" == true ]]; then
    AZURELUSTRE_NODE_STARTUP_DIR="${startup_fixture}"
    printf 'stale\n' > "${startup_fixture}/driver-ready"
  fi
  function install_lustre_client() {
    calls+=" install:$1"
  }
  function exec_csi_driver() {
    calls+=" exec:$1"
  }
  run_driver mock-driver
  assert_equal "${expected}" "${calls}" "driver startup dispatch"
  [[ ! -e "${startup_fixture}/driver-ready" ]] || fail "stale driver readiness survived startup"
)

run_driver_startup_scenario false " install:utils exec:mock-driver"
run_driver_startup_scenario true " exec:mock-driver"

function run_startup_probe_scenario() (
  local AZURELUSTRE_NODE_STARTUP_DIR="${startup_fixture}"
  local AZURELUSTRE_CSI_ROLE=driver
  export AZURELUSTRE_NODE_STARTUP_DIR AZURELUSTRE_CSI_ROLE
  export POD_UID="11111111-2222-4333-8444-555555555555" LUSTRE_VERSION CLIENT_SHA_SUFFIX
  begin_loader_startup
  if loader_startup_ready 2>/dev/null; then fail "startup was ready before package setup"; fi
  loader_startup_live 2>/dev/null || fail "setup allowance must not depend on loaded modules"
  finish_loader_startup
  loader_startup_ready || fail "successful loader setup did not publish readiness"
  begin_loader_startup
  if loader_startup_ready 2>/dev/null; then fail "loader restart inherited stale readiness"; fi
  printf '%s 1\n' "${LOADER_STARTUP_RECORD% *}" > "${startup_fixture}/loader-started"
  if loader_startup_live 2>/dev/null; then fail "stalled setup exceeded its ten-minute allowance"; fi
  begin_loader_startup
  finish_loader_startup

  # shellcheck disable=SC2329 # Exported for the child readiness script; never call host LNet tools.
  function lnetctl() {
    if [[ "$1" == net ]]; then
      printf 'nid: 127.0.0.1@tcp\nstatus: up\n'
    fi
  }
  # shellcheck disable=SC2329 # Exported for the child readiness script's mocked LNet calls.
  function timeout() {
    shift
    "$@"
  }
  # shellcheck disable=SC2329 # The child readiness script checks this mocked helper's availability.
  function mount.lustre() {
    fail "readiness must never mount a filesystem"
  }
  # shellcheck disable=SC2329 # Exported so the child probe never reads real host modules.
  function cat() {
    if [[ "$*" == /sys/module/lustre/version ]]; then
      wanted_module_version
    else
      command cat "$@"
    fi
  }
  # shellcheck disable=SC2329 # Exported for the child probe's projected-policy identity check.
  function sha256sum() {
    printf '%064d  /etc/azurelustre/compatibility/policy.yaml\n' 0
  }
  export -f lnetctl timeout mount.lustre cat sha256sum wanted_module_version
  if bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" >/dev/null 2>&1; then
    fail "driver readiness ignored incomplete userspace setup"
  fi
  bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" --runtime >/dev/null
  printf '%s %s %064d %s\n' "$(startup_identity)" "$(wanted_module_version)" 0 "$(date +%s)" \
    > "${startup_fixture}/driver-ready"
  bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" >/dev/null
  clear_loader_startup
  bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" --runtime >/dev/null
  bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" >/dev/null
  if AZURELUSTRE_CSI_ROLE=loader bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" >/dev/null 2>&1; then
    fail "loader readiness ignored incomplete loader setup"
  fi
  printf '%s %s %064d %s\n' "$(startup_identity)" "$(wanted_module_version)" 0 "$(( $(date +%s) - 91 ))" \
    > "${startup_fixture}/driver-ready"
  if bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" >/dev/null 2>&1; then
    fail "driver readiness accepted an expired runtime observation"
  fi
  printf '%s wrong-module %064d %s\n' "$(startup_identity)" 0 "$(date +%s)" \
    > "${startup_fixture}/driver-ready"
  if bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" >/dev/null 2>&1; then
    fail "driver readiness accepted a changed resident module"
  fi
  printf '%s %s %064d %s\n' "$(startup_identity)" "$(wanted_module_version)" 1 "$(date +%s)" \
    > "${startup_fixture}/driver-ready"
  if bash "${PKG_ROOT}/pkg/azurelustreplugin/readinessProbe.sh" >/dev/null 2>&1; then
    fail "driver readiness accepted a changed compatibility policy"
  fi
)
run_startup_probe_scenario

# A fresh Bash process is important: testing main in an if/function context
# would disable errexit inside the installer and hide repository-setup errors.
result=0
bash -c '
  source "$1/pkg/azurelustreplugin/entrypoint.sh"
  set +o xtrace
  function detect_os_family() { osFamily=ubuntu; }
  function update_ca_trust() { :; }
  function setup_pmc_repo() { return 42; }
  function install_pkg() { echo "unexpected install after repository failure" >&2; exit 99; }
  export LUSTRE_VERSION=2.17.0 CLIENT_SHA_SUFFIX=24-gf517bc4
  export AZURELUSTRE_CSI_ROLE=driver-utils
  main
' bash "${PKG_ROOT}" >/dev/null 2>&1 || result=$?
assert_equal 42 "${result}" "worker must preserve repository-setup failure"

result=0
bash -c '
  source "$1/pkg/azurelustreplugin/entrypoint.sh"
  set +o xtrace
  function detect_os_family() { osFamily=ubuntu; }
  function update_ca_trust() { :; }
  function install_lustre_client() { return 42; }
  export AZURELUSTRE_NODE_STARTUP_DIR="$2" AZURELUSTRE_CSI_ROLE=loader
  export POD_UID=11111111-2222-4333-8444-555555555555
  export LUSTRE_VERSION=2.17.0 CLIENT_SHA_SUFFIX=24-gf517bc4 AZURELUSTRE_TEARDOWN_DIR=""
  main
' bash "${PKG_ROOT}" "${startup_fixture}" >/dev/null 2>&1 || result=$?
assert_equal 42 "${result}" "loader package failure must still trigger a Kubernetes restart"
[[ ! -e "${startup_fixture}/loader-ready" ]] || fail "failed loader left a ready marker"

echo "Entrypoint package, termination, cleanup-startup and completion-teardown safety checks passed."
