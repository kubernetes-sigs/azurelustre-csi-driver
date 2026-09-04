# Maintaining verification tools

Run these commands from the repository root in Bash. Metadata queries use
`curl`, `jq`, and authenticated `gh`. Choose reviewed releases, not `latest`.

## Pins and ownership

| Tool | Pin location | Update together |
| --- | --- | --- |
| Helm | [ensure-helm.sh](../hack/ensure-helm.sh) | `HELM_VERSION`, both architecture hashes |
| yq | [ensure-yq.sh](../hack/ensure-yq.sh) | `YQ_VERSION`, both architecture hashes |
| golangci-lint | [verify-golangci-lint.sh](../hack/verify-golangci-lint.sh) | `GOLANGCI_LINT_VERSION`, both architecture hashes |
| ShellCheck | [verify-shellcheck.sh](../hack/verify-shellcheck.sh) | `SHELLCHECK_VERSION` without `v`, both hashes |
| codespell | [verify-codespell.sh](../hack/verify-codespell.sh) | Version, wheel URL, wheel hash |
| yamllint | [requirements](../hack/tools/yamllint-requirements.txt), [verifier](../hack/verify-yamllint.sh) | Complete dependency lock, `YAMLLINT_VERSION` |
| markdownlint | [verify-markdownlint.sh](../hack/verify-markdownlint.sh) | `MARKDOWNLINT_VERSION`, Node minimum |
| misspell | [verify-spelling.sh](../hack/verify-spelling.sh) | `MISSPELL_VERSION`, including `v` |
| Trivy | [trivy.yaml](../.github/workflows/trivy.yaml) | `TRIVY_VERSION` for all three image scans |

The [Go lint workflow](../.github/workflows/static.yaml) reads the script's
`--tool-version`; codespell's workflow uses its local verifier. Do not add
duplicate pins. Action commit SHAs pin the action, not necessarily its tool.
[Dependabot](../.github/dependabot.yml) updates Actions and Go modules; shell
constants and the Python lock need separate maintenance.

## Native releases

Use the script's download URL to identify the exact Linux amd64 and arm64
artifacts. Hash the downloaded archive or binary, not an extracted file.

| Tool | Repository | Artifact names |
| --- | --- | --- |
| yq | `mikefarah/yq` | `yq_linux_amd64`, `yq_linux_arm64` |
| golangci-lint | `golangci/golangci-lint` | `golangci-lint-<version>-linux-<arch>.tar.gz` |
| ShellCheck | `koalaman/shellcheck` | `shellcheck-v<version>.linux.x86_64.tar.xz`, `shellcheck-v<version>.linux.aarch64.tar.xz` |

List the release's published asset digests and URLs, substituting the selected
repository and tag:

```bash
repository=mikefarah/yq
tag=v4.53.3
gh api "repos/${repository}/releases/tags/${tag}" --jq \
  '.assets[] | [.name, .digest, .browser_download_url] | @tsv'
```

Helm publishes checksum sidecars instead:

```bash
version=v3.21.0
for arch in amd64 arm64; do
  curl --fail --silent --show-error \
    "https://get.helm.sh/helm-${version}-linux-${arch}.tar.gz.sha256sum"
done
```

Before updating constants, download both selected artifacts with
`hack/tools/download-verified.sh <url> <sha256> <temporary-file>`, using the
published digest without its `sha256:` prefix. Remove temporary downloads
afterward. Missing or mismatched digests require investigation or an upstream
checksum manifest; do not substitute the hash of an unexplained download.
Update the version and both hashes in the same change.

## Python wheels

Inspect the selected release's Python requirement and runtime dependencies.
This example prints a complete hashed requirement entry for non-yanked
universal wheels and Linux x86_64/arm64 wheels:

```bash
package=PyYAML
version=6.0.3
release=$(curl --fail --silent --show-error \
  "https://pypi.org/pypi/${package}/${version}/json")
printf '%s\n' "${release}" | jq '.info | {requires_python, requires_dist}'
printf '%s\n' "${release}" | jq -r '
  [.urls[]
    | select(.packagetype == "bdist_wheel" and .yanked == false)
    | select(.filename | test("-none-any\\.whl$|(manylinux|musllinux).*_(x86_64|aarch64)\\.whl$"))
    | .digests.sha256] as $hashes
  | if ($hashes | length) == 0 then error("No supported wheels found")
    else (["\(.info.name)==\(.info.version)"] +
      ($hashes | map("    --hash=sha256:" + .))) | join(" \\\n") end'
```

For yamllint, replace each changed package's entire entry in the requirements
file. Review dependencies recursively, ignoring extras not enabled, and pin
and hash every runtime dependency. Retain wheels for both architectures and
supported Python versions, not just the local interpreter. A yamllint version
change also needs `YAMLLINT_VERSION` updated in its verifier. The cache includes
the lock digest and Python ABI, so no manual cache deletion is needed.

For codespell, set `package=codespell` and the selected version, fetch its
release JSON as above, then obtain the universal wheel URL and hash:

```bash
printf '%s\n' "${release}" | jq -er '
  [.urls[] | select(.packagetype == "bdist_wheel" and .yanked == false)
    | select(.filename | endswith("-py3-none-any.whl"))]
  | if length == 1 then .[0] | [.url, .digests.sha256] | @tsv
    else error("Expected one universal Python 3 wheel") end'
```

Update its version, URL, and hash together. If a future codespell release adds
runtime dependencies, lock those too. Verify a deliberate file-based spelling
error returns nonzero and its output still matches the file/line/message
groups in [codespell-problem-matcher.json](../.github/codespell-problem-matcher.json).

## Other packages and runtimes

```bash
npm view markdownlint-cli@0.48.0 version engines dependencies --json
go list -m -mod=mod -json github.com/client9/misspell@v0.3.4
```

Review release notes and Go toolchain compatibility for misspell/golangci-lint,
and `engines.node` for markdownlint. The npm installation still permits ranged
transitive dependencies; a version bump does not make it a complete lock.
For Trivy, check compatibility with the selected action revision and validate
all three configured image scans; `make verify` does not run those scans.

When raising a runtime minimum, update [ensure-python.sh](../hack/ensure-python.sh)
or the Node check in [verify-markdownlint.sh](../hack/verify-markdownlint.sh),
the [dev prerequisites](csi-dev.md), and the validation versions below together.
The [Linux workflow](../.github/workflows/linux.yaml) selects Python 3.12;
ensure CI and its Node runtime meet the new minimum. CI and local minimum
versions need not be identical.

## Validate an update

Run the affected verifier on valid input and on a deliberate error in an
isolated copy; require success and failure respectively. Also check cold
installation, a damaged cached executable, and warm reuse without its package
manager or downloader. Do not modify the real source tree or delete its cache
to make these tests. Finish with `make quicklustre verify sanity-test-local`.

For Python tools, this Linux Docker example checks installation and offline
reuse on the minimum and CI runtimes. Containers are removed automatically;
record existing images with `docker image ls` first and afterward remove only
newly pulled test images with `docker image rm <image>`.

```bash
for image in python:3.8.20-slim-bookworm python:3.12-slim-bookworm; do
  docker run --rm -i --mount "type=bind,src=$PWD,dst=/src,readonly" \
    --tmpfs /src/_output:exec --workdir /src "${image}" bash -s <<'BASH'
set -euo pipefail
apt-get update -qq
apt-get install -qq --no-install-recommends git curl ca-certificates
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0=/src
hack/verify-yamllint.sh
hack/verify-codespell.sh
python3 -m pip uninstall --yes pip
PIP_NO_INDEX=1 hack/verify-yamllint.sh
PIP_NO_INDEX=1 hack/verify-codespell.sh
hack/verify-boilerplate.sh
BASH
done
```

Check foreign-platform wheel availability and hashes inside the same Python
container, before removing pip:

```bash
for version in 3.8 3.12; do
  for arch in x86_64 aarch64; do
    python3 -m pip download --disable-pip-version-check \
      --require-hashes --only-binary=:all: \
      --platform "manylinux2014_${arch}" --python-version "${version}" \
      --implementation cp --abi "cp${version//./}" \
      --dest "/tmp/wheels/${version}/${arch}" \
      --requirement hack/tools/yamllint-requirements.txt
  done
done
```

Cross-downloading is not native ARM64 execution coverage. Run the affected
verifiers on an ARM64 host before claiming that coverage. For Node/native-tool
checks, use `node:20-bookworm-slim` with the same mounts and install `git`,
`curl`, `xz-utils`, and `ca-certificates` inside it. Helm chart verification also
requires `make`; misspell and golangci-lint require the Go toolchain declared in
[go.mod](../go.mod), which the Node image does not provide. Run the corresponding
verifiers twice, removing npm or disabling downloads before the warm run.
