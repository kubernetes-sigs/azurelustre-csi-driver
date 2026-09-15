# Release Process

The Azure Lustre CSI driver is released on an as-needed basis. Production images
are built through [DALEC](https://github.com/Azure/dalec-build-defs) (the Azure
Container Upstream build system), which provides FIPS compliance, supply-chain
security, and publishing to the Microsoft Container Registry (MCR). Images not
built through DALEC cannot be released.

The CSI repository stores one version-neutral Helm chart source at
`charts/latest/azurelustre-csi-driver`. Released charts are immutable OCI
artifacts published to MCR by the clients Ev2 pipeline. The pipeline clones a
reviewed CSI commit, injects independently selected chart and driver image
versions into a temporary copy, validates every rendered image, packages the
chart, publishes it, and registers an AKS extension version equal to the chart
version.

## Version mapping

This document uses `vX.Y.Z` for the CSI driver release and `A.B.C` for the Helm
chart release. They are independent version axes:

| Surface | Example notation | Relationship |
| --- | --- | --- |
| CSI Git tag | `vX.Y.Z` | Pointer to repository commit for the release |
| Driver image family | `vX.Y.Z` | Derived from the CSI tag by DALEC |
| Driver image tags | `vX.Y.Z-<flavor>` | Append the target OS flavor to the image family |
| Helm chart version | `A.B.C` | Also the immutable OCI tag |
| AKS extension version | `A.B.C` | The clients pipeline uses the chart version for registration |
| Helm `appVersion` | `vX.Y.Z` | Informational metadata identifying the packaged driver release |
| Helm `image.tag` base | `vX.Y.Z` | The value that selects `vX.Y.Z-<flavor>` images |
| In-repo Helm chart | `version: 0.0.0`, `appVersion: latest`, `image.tag: latest` | Replaced only in Ev2's temporary copy |

The first published chart may use the same numeric value as the driver release,
but that is a choice, not an invariant. `A.B.C` is not necessarily derived from `X.Y.Z`.
Every chart change, including a change to `appVersion` or `image.tag`, requires
a new chart and extension version. A chart-only correction may therefore
advance `A.B.C` while continuing to deploy driver image family `vX.Y.Z`.

Steps 1 through 4 below release driver `vX.Y.Z`. Steps 5 and 6 independently
publish chart/extension `A.B.C` and select which driver image family it deploys.
Use `A.B.C` for chart steps and `vX.Y.Z` for driver steps.

## Branch and documentation model

The `latest` image tag identifies unreleased builds from `development`.

| Branch | Code and image state | Documentation state |
| --- | --- | --- |
| `development` | Ongoing integration and source of product and chart fixes. `IMAGE_VERSION` and deploy images/labels use `latest`; the source chart remains version-neutral. | Product PRs update the `development branch` compatibility row and `Chart configuration` defaults. Step 8 adds published release rows and the recommended chart version. |
| `release/vX.Y.Z` | Selected development history plus one release-only commit that sets `vX.Y.Z` in the Makefile and deploy manifests. RC and stable driver tags point to validated snapshots. | Inherits the tables and examples from the selected development cut. The branch is frozen after the stable tag. |
| `main` | Validated product history and deploy manifests. `IMAGE_VERSION` remains `latest`; the source chart remains version-neutral. | Step 7 updates the published driver rows, chart-to-driver table, recommended chart version, and current development row. |

Configure the repository accordingly:

- Protect `main` and `development`; require reviewed pull requests and prohibit
  force pushes.
- Restrict `release/v*` to release maintainers and permit them to force push.
  RC iteration rewrites the release snapshot commit with
  `--force-with-lease`.
- Protect `v*` tags from update and deletion. Allow only release maintainers to
  create them. This applies to both RC and stable tags.

DALEC spec generation is tag driven. A release-candidate tag such as
`vX.Y.Z-rc.1` creates or updates a separate prerelease spec PR, while a stable
tag such as `vX.Y.Z` creates the stable spec PR. Every generated spec pins the
tag's commit SHA and builds with `make azurelustre-dalec`. DALEC supplies the
tag-derived version as `IMAGE_VERSION`, overriding the Makefile's conditional
default.

DALEC accepts lightweight and annotated Git tags. Azure Lustre uses annotated
release tags by repository convention; annotation is not a DALEC requirement.
The DALEC bot discovers upstream tags by polling `git ls-remote --tags` at
09:00 UTC Monday through Friday, or when its workflow is dispatched manually.
Pushing a CSI tag does not invoke DALEC synchronously, so wait for the next poll
or request a manual run when the spec PR is needed sooner.

Use release candidates to exercise the generated specs and integration test in
the staging registry before creating the stable tag. Do not merge prerelease
spec PRs: merging one would publish the prerelease artifacts to MCR. Both
prerelease and stable spec PRs require human review.

```text
development: selected commit -------- product fix ------------------>
release RC1: selected commit -- release snapshot (vX.Y.Z-rc.1)
release RC2: selected commit -- cherry-picked fix -- refreshed snapshot
                                                        |-- vX.Y.Z-rc.2
                                                        `-- vX.Y.Z
main:        previous release -- merge parent of vX.Y.Z -- stable update
```

Each RC tag preserves the release snapshot tested for that candidate. When a
product fix is needed, its reviewed development commit is cherry-picked below a
refreshed release snapshot commit. The release branch itself and its snapshot
commit are never merged into `development` or `main`. Published tags are never
moved.

After publication, Step 8 copies the new release records from `main` to
`development`. The release branch and tags remain unchanged.

This document is the source of truth for the release surface. Any change that
adds, removes, or renames an image flavor, deploy manifest, chart file, install
path, or version-bearing document must update this checklist in the same pull
request. Verification covers known source files and behavior; it does not
discover every file that may need release-specific content.

## Step 1 - Create the release snapshot

### Select the development cut

Merge all intended product work to `development`, then create the release
branch at the exact commit to release:

```console
git switch development
git pull --ff-only
git switch -c release/vX.Y.Z
```

This selected development commit is the initial parent of the release snapshot.
Work merged to `development` afterward is not part of this release unless it is
deliberately incorporated as described in Step 3.

### Prepare the one release-only commit

Make all release-only changes in one commit at the branch tip:

- **Makefile**: set `IMAGE_VERSION ?= vX.Y.Z`.
- **Deploy manifests**: in every `deploy/*.yaml` containing the driver image,
  change `...:latest-<flavor>` to `...:vX.Y.Z-<flavor>`. Change every
  `app.kubernetes.io/version` label from `latest` to `vX.Y.Z`.

The snapshot commit changes only the Makefile and deploy manifests. The chart
source and documentation remain as inherited from `development`.

### Audit the release surface

Before committing, review the complete working-tree diff against the selected
development commit (`HEAD` at this point); do not review only the files named
above.

Search every current release-bearing area for the previous stable version and
for `latest`. Review each result rather than expecting no matches
(`<PREVIOUS>` should match the most recent released version):

```console
git grep -n -E 'v<PREVIOUS>|latest' -- Makefile deploy charts README.md docs
```

Confirm that:

- Every flavor from `make print-all-flavors` has the intended versioned deploy
  image and label.
- The `development branch` row in `README.md` agrees with the selected
  `values.yaml`, including every flavor and Lustre client version.
- The `Chart configuration` defaults in `charts/README.md` agree with the
  selected `values.yaml`, including each Lustre client version and SHA suffix.
- The source chart remains `0.0.0` / `latest` / `latest`.
- There is no `charts/index.yaml`, committed chart archive, or versioned chart
  directory.
- The previous stable version remains only where it is a deliberate historical
  record.
- Every changed or newly added release-sensitive file is represented in this
  checklist.

`make verify` checks the known deploy/chart mapping, source-chart layout,
scripts, and source quality. A successful verification run does not prove that
every README, install path, or newly introduced release surface was updated.
The full diff and search above remain required.

Commit and push the snapshot. Include only files that actually changed:

```console
git add <...>
git commit -m "Prepare vX.Y.Z release snapshot"
git push -u origin release/vX.Y.Z
```

The release branch must now be exactly one release-only commit ahead of its
selected development parent. Wait for every release-branch GitHub check to pass
before tagging it.

## Step 2 - Validate a release candidate

### Tag the candidate

Tag the release snapshot commit with the first release candidate:

```console
git switch release/vX.Y.Z
git tag vX.Y.Z-rc.1
git push origin vX.Y.Z-rc.1
```

Wait for the scheduled DALEC poll or request a manual upstream-trigger run for
`specs/kubernetes-csi-azurelustre`.

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

CI runs the integration harness from the matching CSI tag. In addition, run the
current release-image validation against every staging flavor and supported OS,
including static mounts and dynamic provisioning. Record the validation
results. **Do not merge the prerelease spec PR.**

This stage validates prerelease driver images. It does not publish an official
Helm chart or register an AKS extension version. The extension release starts
only after the stable driver image family is available in MCR.

## Step 3 - How to incorporate a release-candidate fix

Skip this step when the candidate passes unchanged.

Every product fix must be reviewed and merged to `development` first. Do not
author a product fix only on the release branch unless it solely affects
the release-specific commit. Identify the product commit or commits from
the merged fix; avoid cherry-picking the hosting merge commit.

Rebuild the release branch so the product fix is a parent of the one
release-only snapshot commit.

If either cherry-pick has conflicts, resolve them. Resolve conflicts in
favor of the fixed product content plus the stable `vX.Y.Z` release
metadata. Ensure the correctness of the release-specific Makefile and
deploy metadata. Keep the Helm charts version-neutral and do not add
prospective release records.

The release branch tip must again have exactly one release-only logical commit
over its curated product history. Create the next immutable RC tag, such as
`vX.Y.Z-rc.2`, and repeat Step 2. Never move or reuse an earlier RC tag.

## Step 4 - Build and publish the stable images

### Tag the stable release

After prerelease CI and staging validation pass, tag the final validated
release snapshot commit with the stable version. When no fixes followed the
last RC, the RC and stable tags intentionally point to the same commit.

```console
git switch release/vX.Y.Z
git tag vX.Y.Z
git push origin vX.Y.Z
```

Wait for the scheduled DALEC poll or request a manual upstream-trigger run for
`specs/kubernetes-csi-azurelustre`.

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

Repeat the release-image validation against every published MCR flavor
(`mcr.microsoft.com/oss/v2/kubernetes-csi/azurelustre-csi:vX.Y.Z-<flavor>`) to
confirm the production artifacts match what was validated from staging.

Freeze `release/vX.Y.Z`; its tip must remain the commit referenced by
`vX.Y.Z`.

## Step 5 - Publish and validate the hidden extension version

The extension chart may be published only after every required driver flavor is
available in MCR. In `Avere-laaso-clients`, open a reviewed PR that updates
`pipelines/extension/ev2_extension_version_rollout.yml`:

- Select a new, unused chart version `A.B.C`. This is also the AKS extension
  version; it is not derived from driver version `vX.Y.Z`.
- Set `chartGitRef` to the full commit SHA referenced by the stable `vX.Y.Z`
  tag. Do not use a branch or moving tag.
- Set the `driverImageVersion` default to `vX.Y.Z`.
- Review the pinned chart source and every enabled image flavor.

Resolve the commit SHA for the annotated tag with:

```console
git rev-parse 'vX.Y.Z^{}'
```

After that PR merges, run the extension pipeline with:

| Parameter | Value |
| --- | --- |
| `extensionVersion` | `A.B.C` |
| `driverImageVersion` | `vX.Y.Z` |
| `includeAzureLinux3` | `true` |
| `publishOnly` | `false` |
| `releaseTrain` | `preview` |
| `isCustomerHidden` | `true` |
| `rolloutType` | `normal` |

The pipeline must complete these gates in order:

1. Clone the chart from the reviewed CSI commit.
2. Set chart `version` to `A.B.C`; independently set `appVersion` and
  `image.tag` to driver image family `vX.Y.Z`.
3. Render the chart and verify every workload image resolves from MCR.
4. Publish the immutable OCI chart to the team ACR; the MCROnboard webhook then
  syndicates it to
  `mcr.microsoft.com/microsoft.azuremanagedlustre/azurelustre-csi-driver:A.B.C`.
5. Wait until the public MCR digest exactly matches the ACR digest.
6. Register extension version `A.B.C` on the preview train with
  `isCustomerHidden=true`.

Record the pipeline run and published chart digest. Validate the hidden
extension version on supported AKS versions and node OS SKUs. Cover
installation, static mounts, dynamic provisioning, extension reconciliation,
uninstall, and clean reinstall. Include upgrade validation when a healthy prior
extension version exists.

Published chart tags are immutable. If hidden validation finds that chart
content must change, land the chart fix through normal CSI review, update
`chartGitRef` to the exact reviewed commit, and choose a new chart and extension
version. Record the focused chart-fix commit because Step 7 must include it in
`main`. Retain `driverImageVersion=vX.Y.Z` when the driver images are unchanged.

## Step 6 - Promote the extension version to stable

After hidden validation passes, rerun the same clients pipeline with the same
`chartGitRef`, chart/extension version `A.B.C`, driver image version `vX.Y.Z`,
and flavor set. Change only:

| Parameter | Value |
| --- | --- |
| `releaseTrain` | `stable` |
| `isCustomerHidden` | `false` |

The pipeline verifies that the existing OCI chart has identical extracted
content and reuses its digest. It must not publish different content under the
same version. Confirm the extension version is available on the stable train
before updating the public CSI branch.

## Step 7 - Update main and publish the release

### Update main from the validated release

The parent of the stable tag is the exact curated product history validated by
the release:

```console
git fetch origin --tags
git switch main
git pull --ff-only
git switch -c update-main-for-vX.Y.Z
git merge --no-ff -m "Merge validated vX.Y.Z product changes" 'vX.Y.Z^'
```

`update-main-for-vX.Y.Z` is only an example name for an ordinary pull-request
branch. It is not a persistent branch or a separate release tier.

This merge includes the selected product history, including any source-chart
changes that preceded the driver tag, but excludes the release-only snapshot
commit.

### Pull in any post-tag chart fixes if necessary

If the final `chartGitRef` includes focused chart fixes reviewed after
the stable driver tag, include those exact commits now:

```console
git cherry-pick -x <post-tag-chart-fix-commit> ...
```

Do not merge an unbounded newer `development` head.

Prepare the deploy manifests that correspond to the final chart source:

- Since `chartGitRef` is the stable tag commit, restore the manifests from that
  tag:

```console
git restore --source vX.Y.Z -- deploy
```

- Keep the matching deploy changes from those commits and ensure their driver
  images and `app.kubernetes.io/version` labels are `vX.Y.Z` as in Step 1.

### Record the published release

Update the existing release documentation:

- In `README.md`, update the surrounding flavor text, the `main branch` row,
  and the new `vX.Y.Z` row from the stable tag. Copy the current `development
  branch` row from `origin/development`. Keep the existing historical rows.
- In `charts/README.md`, record the actual promoted chart version `A.B.C` and
  its driver image family `vX.Y.Z`. Set the recommended install and upgrade
  examples to `A.B.C`.
- In `docs/install-csi-driver.md`, set the recommended Helm install and upgrade
  examples to the same promoted `A.B.C`.
- Keep `IMAGE_VERSION ?= latest`.
- Keep the source chart at `version: 0.0.0`, `appVersion: latest`, and
  `image.tag: latest`.
- Do not add an index, chart archive, or versioned chart directory.

Validate and commit the publication change.

Before opening the PR, repeat the release-surface search and review the complete
diff from `main`:

```console
git diff --stat main...HEAD
git diff main...HEAD
git grep -n -E 'vPREVIOUS|latest' -- Makefile deploy charts README.md docs
```

Open one reviewed PR from the temporary branch to `main`. Confirm that it
contains the validated product history and stable public references, but not
the release snapshot commit or versioned Makefile default. Confirm that later
unrelated `development` work is absent.

### Create the GitHub release

After the `main` PR merges, cut the
[GitHub Release](https://github.com/kubernetes-sigs/azurelustre-csi-driver/releases)
on the driver tag. The release notes should state that Helm chart `A.B.C`
deploys driver image family `vX.Y.Z`, but `charts/README.md` is the authoritative
chart-to-driver mapping because chart-only releases do not create driver tags.

```console
gh release create vX.Y.Z --verify-tag --title "vX.Y.Z" --generate-notes
```

## Step 8 - Update development's published records

After the `main` publication PR merges, create a documentation branch from the
current `development` branch:

```console
git switch development
git pull --ff-only
git switch -c document-vX.Y.Z-release
```

Update the existing tables and examples:

- Copy the published flavor text, updated `main branch` row, and new `vX.Y.Z`
  row in `README.md` from `main`. Keep the current `development branch` row.
- Copy the new `A.B.C` to `vX.Y.Z` row and recommended `CHART_VERSION=A.B.C`
  examples in `charts/README.md` from `main`. Keep the `Chart configuration`
  defaults from `development`.
- Copy the recommended `CHART_VERSION=A.B.C` examples in
  `docs/install-csi-driver.md` from `main`.

The resulting PR changes only these three documentation files. Review it and
run the canonical validation. Open the normal reviewed PR to `development`.

## Chart-only releases

A chart release does not require a new driver release. When reviewed chart
source changes while driver image family `vX.Y.Z` remains valid:

1. Merge the chart fix to `development` and select its exact commit SHA.
2. Choose a new chart/extension version `A.B.C`; do not create a CSI `vX.Y.Z`
  tag, DALEC spec, or driver image.
3. Pin clients `chartGitRef` to the selected chart-source commit and retain
  `driverImageVersion=vX.Y.Z`.
4. Run the hidden validation and stable promotion in Steps 5 and 6.
5. From a branch based on `main`, include the exact reviewed chart-fix commits
   used by `chartGitRef`. Do not merge unrelated newer development work.
6. In that same reviewed `main` PR, add the actual `A.B.C` to `vX.Y.Z` mapping
  to `charts/README.md`, update the exact install examples in
  `charts/README.md` and `docs/install-csi-driver.md`, and preserve the current
  stable driver image tags and version labels in matching deploy manifests.
7. After the `main` PR merges, copy the new chart row and recommended version
  to `development` as described in Step 8.

The previous chart version remains immutable and available. Advancing `A.B.C`
does not change the driver version reported by `appVersion` or selected by
`image.tag` unless the chart release deliberately chooses another published
driver image family.

Prepare any version-specific changes to the
[Microsoft Learn Azure Managed Lustre docs](https://learn.microsoft.com/azure/azure-managed-lustre/)
during release validation. Their source is in
[azure-stack-docs-pr](https://github.com/MicrosoftDocs/azure-stack-docs-pr)
under `azure-managed-lustre/`, including `use-csi-driver-kubernetes.md`.
Publish those changes in coordination with the stable extension and `main`
update; do not leave them as a post-release cleanup task.
