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

# Pin shellcheck to a specific version so local devs and CI see identical
# findings regardless of what the system package manager ships.  We always
# use the pinned binary, never the system shellcheck.
SHELLCHECK_VERSION="0.11.0"
SHELLCHECK_DIR="/tmp/shellcheck-v${SHELLCHECK_VERSION}"
SHELLCHECK_BIN="${SHELLCHECK_DIR}/shellcheck"

if [[ ! -x "${SHELLCHECK_BIN}" ]]; then
    arch=$(uname -m)
    case "${arch}" in
        x86_64)  release_arch="x86_64" ;;
        aarch64) release_arch="aarch64" ;;
        *)
            echo "Unsupported architecture: ${arch}" >&2
            echo "shellcheck v${SHELLCHECK_VERSION} static binaries are published for x86_64 and aarch64 only." >&2
            exit 1
            ;;
    esac
    url="https://github.com/koalaman/shellcheck/releases/download/v${SHELLCHECK_VERSION}/shellcheck-v${SHELLCHECK_VERSION}.linux.${release_arch}.tar.xz"
    echo "Downloading shellcheck v${SHELLCHECK_VERSION} to ${SHELLCHECK_DIR} ..."
    tarball=$(mktemp --suffix=.tar.xz)
    trap 'rm -f "${tarball}"' EXIT
    if ! curl -fsSL "${url}" -o "${tarball}"; then
        echo "Failed to download ${url}" >&2
        exit 1
    fi
    if ! tar -xJ -C /tmp -f "${tarball}"; then
        echo "Failed to extract ${tarball} to /tmp" >&2
        exit 1
    fi
    if [[ ! -x "${SHELLCHECK_BIN}" ]]; then
        echo "Expected ${SHELLCHECK_BIN} after extraction, but it is missing or not executable." >&2
        exit 1
    fi
fi

# Find every shell script in the repo, excluding generated/vendored
# directories. The repo's `.shellcheckrc` selects the rule set; we
# pass `-o all -S style -x` here so that anyone running this script
# directly (without the rc file) still gets the intended strict
# behavior. `-x` follows `# shellcheck source=...` directives so that
# `source` lines don't trigger SC1091 and so sourced files are linted
# inline.
mapfile -t scripts < <(
    find "${PKG_ROOT}" \
        \( -path "${PKG_ROOT}/_output" -o \
           -path "${PKG_ROOT}/vendor" -o \
           -path "${PKG_ROOT}/.jj" -o \
           -path "${PKG_ROOT}/.git" \) -prune -o \
        -type f \( -name '*.sh' -o -name '*.bash' \) -print \
    | sort
)

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
# `file:line:directive` line.  `|| true` keeps us from tripping `set -e` when
# nothing matches (grep exits 1).
mapfile -t bare_disables < <(grep -HnE '# shellcheck disable=[^#]*$' "${scripts[@]}" || true)

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
