# Release Process

The Azure Lustre CSI driver is released on an as-needed basis. Production images
are built through [DALEC](https://github.com/Azure/dalec-build-defs) (the Azure
Container Upstream build system), which provides FIPS compliance, supply-chain
security, and publishing to the Microsoft Container Registry (MCR). Images not
built through DALEC cannot be released.

## Overview

DALEC spec generation is tag driven. A release-candidate tag such as
`vX.Y.Z-rc.1` creates or updates a separate prerelease spec PR, while a stable
tag such as `vX.Y.Z` creates the stable spec PR. Every generated spec pins the
tag's commit SHA and builds with `make azurelustre-dalec`.

Use release candidates to exercise the generated specs and integration test in
the staging registry before creating the stable tag. Do not merge prerelease
spec PRs: merging one would publish the prerelease artifacts to MCR. Both
prerelease and stable spec PRs require human review.

```text
development -> version bump -> merge PR (this commit is the release SHA)
  -> tag vX.Y.Z-rc.N
  -> prerelease DALEC PR -> validate staging images and integration tests
  -> tag vX.Y.Z on the validated SHA
  -> stable DALEC PR -> validate staging images -> merge -> publish to MCR
  -> validate MCR images -> merge development to main -> GitHub Release
  -> reset development to latest
```

Never move an existing release-candidate or stable tag. If a candidate needs a
fix, merge the fix to `development` and create the next tag, such as
`vX.Y.Z-rc.2`.

## Step 1 — Version bump and chart update (this repo)

Do this first, so the deploy manifests and charts are ready before the DALEC
image exists.

- **Makefile**: set `IMAGE_VERSION ?= vX.Y.Z`.
- **`deploy/csi-azurelustre-node-<flavor>.yaml`** (one per flavor; `make
  print-all-flavors` lists them): bump the image tag `...:latest-<flavor>` ->
  `...:vX.Y.Z-<flavor>` and the `app.kubernetes.io/version` labels `latest` ->
  `vX.Y.Z`.
- **Charts**: copy `charts/latest/azurelustre-csi-driver` to
  `charts/vX.Y.Z/azurelustre-csi-driver`; in the copy set `Chart.yaml`
  `version`/`appVersion` and `values.yaml` `image.tag` to `vX.Y.Z`; package it
  (`helm package charts/vX.Y.Z/azurelustre-csi-driver -d charts/vX.Y.Z/`) and
  regenerate the index (`hack/update-helm-chart-index.sh`). Leave
  `charts/latest/` at its development values (`v0.0.0` / `latest`).
- **README.md**: add a version-table row for `vX.Y.Z` (image tags and per-flavor
  Lustre client versions from `charts/latest/azurelustre-csi-driver/values.yaml`).
- Run `make verify sanity-test-local` and the e2e test
  (`make e2e-test`), then open a PR and merge to `development`. That merged
  commit on `development` is the **release SHA** used everywhere below.

## Step 2 — Validate a release candidate

### Tag the candidate

Tag the release SHA on `development` with the first release candidate:

```console
git tag -a vX.Y.Z-rc.1 -m "Release candidate vX.Y.Z-rc.1" <release-sha>
git push origin vX.Y.Z-rc.1
```

### Review the prerelease spec PR

Wait for the Azure Container Upstream bot to create or update the separate
prerelease PR in `dalec-build-defs`. It generates one spec per flavor from the
project's templates:

```text
azurelustre-csi-azurelinux3-X.Y.Z~rc.1.yml
azurelustre-csi-jammy-X.Y.Z~rc.1.yml
azurelustre-csi-noble-X.Y.Z~rc.1.yml
```

DALEC uses `~` inside prerelease package/spec versions so they sort before the
stable version. Git and image tags retain the SemVer hyphen.

### Validate the prerelease images

Review the generated specs and let PR CI build and test the staging images:

```text
upstream.azurecr.io/oss/v2/kubernetes-csi/azurelustre-csi:vX.Y.Z-rc.1-<flavor>
```

CI runs the integration harness from the matching CSI tag. Validate the staging
images with the CSI image-validation and dynamic-provisioning pipelines. **Do
not merge the prerelease spec PR.**

### Iterate on failures

If validation fails, merge the fix to `development` and repeat this step with
the next release-candidate tag. Do not move or reuse the previous tag.

## Step 3 — Build and publish the stable images

### Tag the stable release

After prerelease CI and staging validation pass, tag the validated release SHA
with the stable version:

```console
git tag -a vX.Y.Z -m "Release vX.Y.Z" <release-sha>
git push origin vX.Y.Z
```

### Review the stable spec PR

Wait for the bot to create the stable spec PR. It generates component-first
filenames such as:

```text
azurelustre-csi-azurelinux3-X.Y.Z.yml
azurelustre-csi-jammy-X.Y.Z.yml
azurelustre-csi-noble-X.Y.Z.yml
```

### Validate the stable staging images

Review the generated specs and validate the staging images from PR CI:

```text
upstream.azurecr.io/oss/v2/kubernetes-csi/azurelustre-csi:vX.Y.Z-<flavor>
```

### Publish to MCR

Merge the stable spec PR. The production workflow publishes the images to:

```text
mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:vX.Y.Z-<flavor>
```

## Step 4 — Validate and publish the release

### Validate the MCR images

Re-run the CSI image-validation and dynamic-provisioning pipelines against the
published MCR images
(`mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:vX.Y.Z-<flavor>`) to
confirm they match what was validated from staging.

### Merge the release to main

Merge `development` into `main` (this is what users install from). Confirm the
merge carries the versioned chart directory (`charts/vX.Y.Z/`) with MCR image
tags, contains no `development` work beyond the release SHA, and points to the
commit tagged `vX.Y.Z`.

### Create the GitHub release

Cut the [GitHub Release](https://github.com/kubernetes-sigs/azurelustre-csi-driver/releases)
on the tag:

```console
gh release create vX.Y.Z --title "vX.Y.Z" --generate-notes
```

## Step 5 — Update public docs

Review the [Microsoft Learn Azure Managed Lustre docs](https://learn.microsoft.com/azure/azure-managed-lustre/)
(source in [azure-stack-docs-pr](https://github.com/MicrosoftDocs/azure-stack-docs-pr)
under `azure-managed-lustre/`) for version-specific install or usage statements,
e.g. `use-csi-driver-kubernetes.md`.

## Step 6 — Reset development

Revert the Step 1 mutations on `development`: `IMAGE_VERSION ?= latest`, deploy
image tags/labels back to `latest`, remove `charts/vX.Y.Z/`, and regenerate the
index. Leave the released README row as the historical record. Run `make verify`
and commit.
