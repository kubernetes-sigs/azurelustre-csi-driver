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

PKG_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${PKG_ROOT}"

readonly SHELLCHECK_VERSION="0.11.0"
readonly SHELLCHECK_SHA256_AMD64="8c3be12b05d5c177a04c29e3c78ce89ac86f1595681cab149b65b97c4e227198"
readonly SHELLCHECK_SHA256_ARM64="12b331c1d2db6b9eb13cfca64306b1b157a86eb69db83023e261eaa7e7c14588"
readonly SHELLCHECK_DIR="${PKG_ROOT}/_output/tools/shellcheck/${SHELLCHECK_VERSION}"
readonly SHELLCHECK_BIN="${SHELLCHECK_DIR}/shellcheck"

installed_version=""
if [[ -x "${SHELLCHECK_BIN}" ]]; then
    installed_version=$("${SHELLCHECK_BIN}" --version | awk '$1 == "version:" { print $2 }') || installed_version=""
fi
if [[ "${installed_version}" != "${SHELLCHECK_VERSION}" ]]; then
    case "$(uname -m)" in
        x86_64)
            release_arch="x86_64"
            checksum="${SHELLCHECK_SHA256_AMD64}"
            ;;
        aarch64 | arm64)
            release_arch="aarch64"
            checksum="${SHELLCHECK_SHA256_ARM64}"
            ;;
        *)
            echo "Unsupported architecture: $(uname -m); shellcheck supports x86_64 and arm64." >&2
            exit 1
            ;;
    esac

    temp_dir=$(mktemp -d)
    trap 'rm -rf "${temp_dir}"' EXIT
    artifact="shellcheck-v${SHELLCHECK_VERSION}.linux.${release_arch}.tar.xz"

    rm -rf "${SHELLCHECK_DIR}"
    mkdir -p "${SHELLCHECK_DIR}"
    "${PKG_ROOT}/hack/tools/download-verified.sh" \
        "https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/${artifact}" \
        "${checksum}" \
        "${temp_dir}/${artifact}"
    tar -xJf "${temp_dir}/${artifact}" -C "${temp_dir}"
    install -m 0755 "${temp_dir}/shellcheck-v${SHELLCHECK_VERSION}/shellcheck" "${SHELLCHECK_BIN}"
fi

# Find every shell script in the repo, excluding generated/vendored
# directories. The repo's `.shellcheckrc` selects the rule set; we
# pass `-o all -S style -x` here so that anyone running this script
# directly (without the rc file) still gets the intended strict
# behavior. `-x` follows `# shellcheck source=...` directives so that
# `source` lines don't trigger SC1091 and so sourced files are linted
# inline.
scripts_output=$(
    find "${PKG_ROOT}" \
        \( -path "${PKG_ROOT}/_output" -o \
           -path "${PKG_ROOT}/vendor" -o \
           -path "${PKG_ROOT}/.jj" -o \
           -path "${PKG_ROOT}/.git" \) -prune -o \
        -type f \( -name '*.sh' -o -name '*.bash' \) -print \
    | sort
)
scripts=()
if [[ -n "${scripts_output}" ]]; then
    mapfile -t scripts <<<"${scripts_output}"
fi

if [[ "${#scripts[@]}" -eq 0 ]]; then
    echo "Found no shell scripts to lint. Exiting as error."
    exit 1
fi

echo "Verifying ${#scripts[@]} shell scripts with shellcheck v${SHELLCHECK_VERSION} -x -o all -S style ..."

# Require every `shellcheck disable=` to have a trailing comment
# explaining why the rule is suppressed.  shellcheck itself has no flag for
# this, so we enforce it with grep: the regex matches a disable directive
# whose remainder (after `=`) contains no `#`, i.e. no justification comment.
# `grep -H` prefixes each match with the file path, giving us a ready-to-print
# `file:line:directive` line. A grep status of 1 means no matches; other
# nonzero statuses are errors.
grep_status=0
bare_disables_output=$(grep -HnE '# shellcheck disable=[^#]*$' "${scripts[@]}") || grep_status=$?
if [[ "${grep_status}" -gt 1 ]]; then
    echo "Could not inspect ShellCheck suppression directives." >&2
    exit "${grep_status}"
fi
bare_disables=()
if [[ -n "${bare_disables_output}" ]]; then
    mapfile -t bare_disables <<<"${bare_disables_output}"
fi

if [[ "${#bare_disables[@]}" -gt 0 ]]; then
    echo "ERROR: Found shellcheck disable directive(s) without an explanation." >&2
    echo "Every inline disable must have a trailing comment, e.g.:" >&2
    echo "  # shellcheck disable=SC2154 # VAR is set by the calling script" >&2
    echo "" >&2
    for line in "${bare_disables[@]}"; do
        echo "  ${line}" >&2
    done
    exit 1
fi

"${SHELLCHECK_BIN}" -x -o all -S style "${scripts[@]}"
echo "Congratulations! All shell scripts have been linted."
