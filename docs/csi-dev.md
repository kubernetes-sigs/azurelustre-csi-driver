# Azure azurelustre Storage CSI driver development guide

&nbsp;

## Clone repo and build locally

&nbsp;

- Clone repo

```sh
mkdir -p $GOPATH/src/sigs.k8s.io
git clone https://github.com/kubernetes-sigs/azurelustre-csi-driver $GOPATH/src/sigs.k8s.io/azurelustre-csi-driver
```

&nbsp;

- Build azurelustre Storage CSI driver

```sh
cd $GOPATH/src/sigs.k8s.io/azurelustre-csi-driver
make azurelustre
```

&nbsp;

- Run verification before sending PR

```sh
make verify
```

Verification tools are pinned and installed under
`_output/tools/<tool>/<version>` in the repository, not into system directories.
Keep this directory between local runs to reuse the installations. Release
downloads are checked against pinned SHA-256 checksums before installation.
The Go toolchain declared in [go.mod](../go.mod), Python 3.8 or later available
as `python3`, and Node.js 20 or later remain prerequisites.
Installing uncached Python tools requires pip, and installing
uncached markdownlint requires npm. Python-based verifiers use the shared
[Python prerequisite check](../hack/ensure-python.sh) to report how to install
or activate a missing prerequisite. Verification does not create or modify the system
`python` command. The [Linux workflow](../.github/workflows/linux.yaml) selects
Python 3.12 explicitly; fresh CI runners install the pinned verification tools.

A newer host Go may exceed the pinned golangci-lint binary's supported version.
In that case, set `GOTOOLCHAIN` to the `toolchain` value in `go.mod` when running
verification; automatic toolchain selection does not downgrade a newer Go.

For pin locations, release hashes, dependency-lock updates, and isolated
validation commands, see [Maintaining verification tools](verification-tools.md).

Go lint policy is defined in [.golangci.yaml](../.golangci.yaml). Verification
uses `modernize` for current Go idioms, `unparam` for unused parameters and
results, and `fatcontext` for nested contexts in loops and closures. Tests use
`usetesting` to prefer `t.Context()`, `t.TempDir()`, `t.Chdir()`, and `t.Setenv()`
where appropriate so resources follow the test lifetime.

Additional lint checks cover these conventions:

| Linter | Purpose |
| --- | --- |
| `canonicalheader` | Use canonical HTTP header names. |
| `dogsled` | Flag assignments that discard too many return values. |
| `errname` | Give sentinel errors and error types recognizable names. |

&nbsp;

### Verify Helm chart source changes

The repository stores one unpackaged chart source at
`charts/latest/azurelustre-csi-driver`. Keep its `Chart.yaml` version at
`0.0.0`, its `appVersion` at `latest`, and `values.yaml` `image.tag` at
`latest`. Run `make verify` after changing templates or values.

Do not commit chart archives, an index, or versioned chart directories. The
clients Ev2 release pipeline clones a reviewed commit, applies release metadata
to a temporary copy, and publishes the released chart to MCR as an OCI
artifact.

The Helm chart version and CSI driver version are independent. Ev2 receives a
bare chart/extension version such as `A.B.C` and a separate driver image family
such as `vX.Y.Z`. It writes `A.B.C` to the chart `version`, and writes `vX.Y.Z`
to `appVersion` and `image.tag`. `appVersion` is informational; `image.tag`
selects the actual flavored images. Never derive either input from the other.

### Build container image locally for testing

Set up a personal ACR if you don't have one (one-time):

```sh
az group create --name <alias>-csi-infra --location <region> --subscription <subscription>
az acr create --name <alias>csiacr --resource-group <alias>-csi-infra --sku Basic --tags owner=<alias>
```

Log in before pushing:

```sh
az acr login --name <alias>csiacr
```

Build and push images:

```sh
REGISTRY="<alias>csiacr.azurecr.io" make build-push-latest
```

This pushes flavor-suffixed tags (e.g., `latest-jammy`, `latest-noble`), not just an
unsuffixed `:latest`.

To build for ARM64 (noble only — jammy doesn't support ARM64):

```sh
sudo apt install gcc-aarch64-linux-gnu                         # one-time: install cross-compiler
docker run --privileged --rm tonistiigi/binfmt --install arm64 # one-time: enable arm64 emulation for Docker
REGISTRY="<alias>csiacr.azurecr.io" make build-push-latest ARCH=arm64
```

> **Note:** The `azurelustre-csi-integration` repository on team ACRs (e.g.,
> `tip5csiacr`) is reserved for CI builds. Don't push to it manually.

Optionally, set up a purge task to avoid storage costs from old images:

```sh
az acr task create --name purge-old-images \
    --registry <alias>csiacr --resource-group <alias>-csi-infra \
    --cmd "acr purge --filter 'azurelustre-csi:.*' --ago 30d --untagged" \
    --schedule "0 4 * * 0" --context /dev/null
```

Note: This builds images from the local Dockerfile for development and testing.
Production images are built through DALEC (see below).

&nbsp;

## DALEC image builds

Production images are built through [DALEC](https://github.com/Azure/dalec-build-defs),
not from the Dockerfiles in this repo. The Dockerfiles
(`pkg/azurelustreplugin/Dockerfile`) are only for local development and testing;
released images come from generated DALEC specs under
`specs/kubernetes-csi-azurelustre/` in the dalec-build-defs repo. The project has
separate templates for Azure Linux 3, Ubuntu Jammy, and Ubuntu Noble, with a
`matrix.yml` that controls generation. Each generated spec pins the commit for
an upstream CSI tag and builds with `make azurelustre-dalec`.

Stable and prerelease tags trigger separate generated-spec PRs. For example,
`v0.6.0-rc.1` is represented as `0.6.0~rc.1` in the DALEC spec and produces
images tagged `v0.6.0-rc.1-<flavor>`. Generated-spec PRs require human review.
The project also opts into Go module vulnerability scanning; its CVE remediation
PRs require human review as well.

For local iteration, build and run the image from the Dockerfile with
`make container`. The DALEC images are produced during the release process — see
[RELEASE.md](../RELEASE.md) for the full release and tagging flow.

&nbsp;
&nbsp;

## Test locally using csc tool

&nbsp;

- Install CSC

Install `csc` tool according to <https://github.com/rexray/gocsi/tree/master/csc>:

```console
mkdir -p $GOPATH/src/github.com
cd $GOPATH/src/github.com
git clone https://github.com/rexray/gocsi.git
cd rexray/gocsi/csc
make build
```

&nbsp;

- Setup variables

```sh
readonly volname="testvolume-$(date +%s)"
readonly cap="MULTI_NODE_MULTI_WRITER,mount,,,"
readonly target_path="/tmp/lustre-pv"
readonly endpoint="tcp://127.0.0.1:10000"

readonly lustre_fs_name=""
readonly lustre_fs_ip=""
```

&nbsp;

- Start CSI driver locally

```console
cd $GOPATH/src/sigs.k8s.io/azurelustre-csi-driver
./_output/azurelustreplugin --endpoint $endpoint --nodeid CSINode -v=5 &
```

> Before running CSI driver, create "/etc/kubernetes/azure.json" file under testing server(it's better copy `azure.json` file from a k8s cluster with service principle configured correctly) and set `AZURE_CREDENTIAL_FILE` as following:

```console
export AZURE_CREDENTIAL_FILE=/etc/kubernetes/azure.json
```

&nbsp;

### 1. Get plugin info

```console
csc identity plugin-info --endpoint $endpoint
```

&nbsp;

#### 2. Create an azurelustre volume

```console
csc controller new --endpoint $endpoint --cap $cap --req-bytes 2147483648 --params "fs-name=$lustre_fs_name,mgs-ip-address=$lustre_fs_ip" $volname
```

&nbsp;

#### 3. Publish volume

```console
mkdir /tmp/target-path
volumeid=$(csc node publish --endpoint $endpoint --cap $cap --target-path $target_path --vol-context "fs-name=$lustre_fs_name,mgs-ip-address=$lustre_fs_ip" $volname)
```

&nbsp;

#### 4. Unpublish volume

```console
csc node unpublish --endpoint $endpoint --target-path $target_path $volname
```

&nbsp;

#### 5. Delete azurelustre volume

```console
csc controller del --endpoint $endpoint volumeid
```

&nbsp;

#### 6. Validate volume capabilities

```console
csc controller validate-volume-capabilities --endpoint $endpoint --cap $cap volumeid
```

&nbsp;

#### 7. Get NodeID

```console
csc node get-info --endpoint $endpoint
```
