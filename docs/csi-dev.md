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

&nbsp;

- Update the Helm chart index after changing charts

If you modify any chart templates or values, repackage the charts and regenerate
the index:

```sh
make helm-chart-packages
```

Or equivalently:

```sh
hack/update-helm-chart-packages.sh
```

The `make verify` step will fail if the packages or index are out of date.

- Build container image locally for testing

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
`v0.5.0-rc.1` is represented as `0.5.0~rc.1` in the DALEC spec and produces
images tagged `v0.5.0-rc.1-<flavor>`. Generated-spec PRs require human review.
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
