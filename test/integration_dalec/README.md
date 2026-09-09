# integration_dalec -- DALEC image integration test

This directory holds the integration test that the
[dalec-build-defs](https://github.com/Azure/dalec-build-defs) pipeline runs
against the DALEC-built `azurelustre-csi` container images (the `jammy`,
`noble`, and `azurelinux3` variants).

It stands up a local [KIND](https://kind.sigs.k8s.io/) cluster, runs the driver
image under test, and exercises the CSI API with the
[`csc`](https://github.com/dell/gocsi) client.

## How it works (sidecar design)

The test runs a single Pod with **two containers** that share the CSI socket
through an `emptyDir` volume mounted at `/csi`:

```text
        Pod: azurelustre-integration-dalec
 +-----------------------------------------------+
 |  driver container         tester container    |
 |  (image under test)       (csc image)         |
 |  plugin --endpoint  ->    csc --endpoint      |
 |                    \     /                    |
 |      emptyDir "socket-dir" -> /csi/csi.sock   |  <- shared socket
 +-----------------------------------------------+
        configMap -> /test/run_integration_test.sh (mounted into tester)
```

- **`driver`** runs *only* the plugin from the image under test
  (`/app/azurelustreplugin`). The shipped image is used **as-is** -- no test
  tooling is installed into it.
- **`tester`** is a small test-only image (`Dockerfile.csc`) that carries the
  `csc` client plus a shell. It runs `run_integration_test.sh`, which waits for
  the driver's socket and drives the full volume lifecycle.

Because `csc` lives in its own container, the **same harness works for every
distro variant** -- including the distroless Azure Linux 3 image, which has no
package manager or Go toolchain. The `csc` image is never shipped.

## Files

| File | Purpose |
| --- | --- |
| `Dockerfile.csc` | Builds the test-only `csc` sidecar image (never shipped). |
| `run_integration_test.sh` | Runs **inside** the `tester` container: waits for the socket, runs the `csc` create/validate/publish/stats/unpublish/delete/identity/get-info sequence. |
| `integration_dalec_aks.yaml.template` | Two-container Pod (`driver` + `tester`) sharing the CSI socket. |
| `setup_integration_test_dalec.sh` | Orchestrator: creates the configmap, applies the Pod, waits for the `tester` container to finish, and exits with its exit code. |

## DALEC harness version selection

The shared test wrapper in `dalec-build-defs` checks out the integration harness
from the CSI version being built. Stable versions use the matching tag directly.
For prereleases, DALEC passes a Debian-style version such as `v0.6.0~rc.1`; the
wrapper converts it back to the Git tag `v0.6.0-rc.1` before cloning this repo.

There are two exceptions:

- `v0.2.0` uses the `v0.3.0` harness because the original harness contains an
  unpinned dependency that is no longer compatible with its build environment.
- `TEST_HARNESS_REF` overrides the selected Git ref. Set it to `development`
  when testing an untagged local candidate against the current harness.

If the selected ref contains `Dockerfile.csc`, the wrapper builds and loads the
test-only sidecar image. Older harnesses without that file continue to use their
original test layout.

## Running manually

### Prerequisites

`docker` (running), `kubectl`, `envsubst` (gettext), and `kind`.

Install `kind` to a user-writable directory (no `sudo`; works on WSL):

```bash
mkdir -p ~/.local/bin
curl -sLo ~/.local/bin/kind \
  https://github.com/kubernetes-sigs/kind/releases/download/v0.27.0/kind-linux-amd64
chmod +x ~/.local/bin/kind
kind --version
```

The `v0.27.0` above is pinned for convenience; CI's `get-kind.sh` pins the
authoritative version.

> CI installs `kind` via `dalec-build-defs/testing/scripts/get-kind.sh`. That
> script is meant for CI -- it requires `ARCH` to be set and uses `sudo` to
> install to `/usr/local/bin` -- so for a manual run prefer the `curl` command
> above.

### Steps

The **driver image** is produced by the DALEC specs in `dalec-build-defs`; the
steps below assume you have that repo checked out. Adjust the two paths.

```bash
export PATH="$HOME/.local/bin:$PATH"          # wherever kind is installed
DALEC=/path/to/dalec-build-defs
CSI=/path/to/azurelustre-csi-driver           # this repo
CLUSTER_NAME=azurelustre-csi-test
```

### 1. Build the driver image under test

Azure Linux 3:

```bash
cd "$DALEC"
export REGISTRY=upstream.azurecr.io REPO=oss/v2/kubernetes-csi TAG=v0.6.0-azurelinux3
export IMAGE_NAME="${REGISTRY}/${REPO}/azurelustre-csi:${TAG}"
docker buildx build --target azlinux3/container --build-arg DALEC_SKIP_SIGNING=1 \
  -f specs/kubernetes-csi-azurelustre/azurelustre-csi-azurelinux3-0.6.0.yml \
  -t "$IMAGE_NAME" --load .
```

For the deb variants use `--target noble/testing/container` (or
`jammy/testing/container`) and the matching component-first spec file, such as
`azurelustre-csi-noble-0.6.0.yml`.

For a release candidate, the generated spec filename uses `~`, while the image
tag and CSI Git tag use `-`. For example:

```text
Spec:      azurelustre-csi-azurelinux3-0.6.0~rc.1.yml
Image tag: v0.6.0-rc.1-azurelinux3
Git tag:   v0.6.0-rc.1
```

### 2. Build the csc sidecar image

```bash
export CSC_IMAGE_NAME="azurelustre-csi-test/csc:local"
docker build -f "$CSI/test/integration_dalec/Dockerfile.csc" \
  -t "$CSC_IMAGE_NAME" "$CSI/test/integration_dalec"
```

### 3. Create the cluster and load both images

```bash
kind delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
kind create cluster --name "$CLUSTER_NAME"
kind load docker-image "$IMAGE_NAME"     --name "$CLUSTER_NAME"
kind load docker-image "$CSC_IMAGE_NAME" --name "$CLUSTER_NAME"
```

### 4. Run the harness

```bash
cd "$CSI/test/integration_dalec"
bash ./setup_integration_test_dalec.sh
echo "exit: $?"     # 0 = pass
```

A passing run ends with `Integration test is completed.` and `Result: 0`. On
failure the orchestrator dumps the `driver` container logs for debugging.

### 5. Tear down

```bash
kind delete cluster --name "$CLUSTER_NAME"
```

## Notes

- The driver runs with `--enable-azurelustre-mock-mount` and
  `--enable-azurelustre-mock-dyn-prov`. The mock flags let the driver serve the
  CSI API without a real Azure Lustre filesystem or an
  `/etc/kubernetes/azure.json` cloud config, so the test needs no live AMLFS.
- The `driver` container runs for the life of the Pod (it is a server), so the
  orchestrator waits on the **`tester`** container's exit code rather than on
  the whole Pod terminating.
