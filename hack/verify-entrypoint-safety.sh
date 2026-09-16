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

echo "Entrypoint package-eviction safety checks passed."
