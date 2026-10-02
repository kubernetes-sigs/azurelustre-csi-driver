#!/usr/bin/env bash

# Copyright 2018 The Kubernetes Authors.
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

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${ROOT}"
readonly GOLANGCI_LINT_VERSION="v2.12.2"
readonly GOLANGCI_LINT_SHA256_AMD64="8df580d2670fed8fa984aac0507099af8df275e665215f5c7a2ae3943893a553"
readonly GOLANGCI_LINT_SHA256_ARM64="44cd40a8c76c86755375adfeea52cfd3533cb43d7bd647771e0ae065e166df3a"

if [[ "${1:-}" == "--tool-version" ]]; then
	if [[ $# -ne 1 ]]; then
		echo "Usage: $0 [--tool-version]" >&2
		exit 2
	fi
	printf '%s\n' "${GOLANGCI_LINT_VERSION}"
	exit 0
fi
if [[ $# -ne 0 ]]; then
	echo "Usage: $0 [--tool-version]" >&2
	exit 2
fi

case "$(uname -m)" in
	x86_64)
		arch="amd64"
		checksum="${GOLANGCI_LINT_SHA256_AMD64}"
		;;
	aarch64 | arm64)
		arch="arm64"
		checksum="${GOLANGCI_LINT_SHA256_ARM64}"
		;;
	*)
		echo "Unsupported architecture: $(uname -m); golangci-lint supports x86_64 and arm64." >&2
		exit 1
		;;
esac

version=${GOLANGCI_LINT_VERSION#v}
install_dir="${ROOT}/_output/tools/golangci-lint/${GOLANGCI_LINT_VERSION}"
GOLANGCI_LINT_BIN="${install_dir}/golangci-lint"

installed_version=""
if [[ -x "${GOLANGCI_LINT_BIN}" ]]; then
	installed_version=$("${GOLANGCI_LINT_BIN}" version --short 2>&1) || installed_version=""
fi
if [[ "${installed_version}" != "${version}" ]]; then
	temp_dir=$(mktemp -d)
	trap 'rm -rf "${temp_dir}"' EXIT
	artifact="golangci-lint-${version}-linux-${arch}.tar.gz"

	rm -rf "${install_dir}"
	mkdir -p "${install_dir}"
	"${ROOT}/hack/tools/download-verified.sh" \
		"https://github.com/golangci/golangci-lint/releases/download/${GOLANGCI_LINT_VERSION}/${artifact}" \
		"${checksum}" \
		"${temp_dir}/${artifact}"
	tar -xzf "${temp_dir}/${artifact}" -C "${temp_dir}"
	install -m 0755 "${temp_dir}/golangci-lint-${version}-linux-${arch}/golangci-lint" "${GOLANGCI_LINT_BIN}"
fi

echo "Verifying golangci-lint ${GOLANGCI_LINT_VERSION}"

"${GOLANGCI_LINT_BIN}" run --timeout=10m

echo "Congratulations! Lint check completed for all Go source files."
