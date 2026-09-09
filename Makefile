# Copyright 2017 The Kubernetes Authors.
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

PKG = sigs.k8s.io/azurelustre-csi-driver
GIT_COMMIT ?= $(shell git rev-parse HEAD)
REGISTRY ?= azurelustre.azurecr.io
IMAGE_NAME = azurelustre-csi
IMAGE_VERSION ?= latest
export REPOSITORY ?= $(REGISTRY)/$(IMAGE_NAME)
LATEST_TAG ?= latest
IMAGE_TAG ?= $(REPOSITORY):$(IMAGE_VERSION)
IMAGE_TAG_LATEST = $(REPOSITORY):$(LATEST_TAG)
COMMIT_TAG ?= $(LATEST_TAG)-$(GIT_COMMIT)
IMAGE_TAG_COMMIT = $(REPOSITORY):$(COMMIT_TAG)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS ?= "-X ${PKG}/pkg/azurelustre.driverVersion=${IMAGE_VERSION} -X ${PKG}/pkg/azurelustre.gitCommit=${GIT_COMMIT} -X ${PKG}/pkg/azurelustre.buildDate=${BUILD_DATE} -s -w -extldflags '-static'"
GINKGO_FLAGS = -ginkgo.v
GO111MODULE = on
GOPATH ?= $(shell go env GOPATH)
GOBIN ?= $(GOPATH)/bin
CGO_ENABLED ?= 1
export GOPATH GOBIN GO111MODULE CGO_ENABLED

ARCH ?= amd64

# Cross-compilation: set the C compiler for arm64 builds so CGO_ENABLED=1
# works on amd64 hosts (required for GOEXPERIMENT=systemcrypto / FIPS).
ifeq ($(ARCH),arm64)
  CC := aarch64-linux-gnu-gcc
  export CC
endif

#
# Image flavors
#
# Source of truth for what OS flavors we build container images for. Listed in
# buckets by architecture support so docker targets and CI tooling can pick
# the right subset for the current ARCH. Per-flavor SRC_IMAGE_<flavor> is the
# base image passed as --build-arg srcImage to the Dockerfile.
#
# To add a new flavor:
#   1. Add its name to AMD64_ONLY_FLAVORS or ALL_ARCHES_FLAVORS below.
#   2. Define SRC_IMAGE_<flavor>.
#   3. Add the matching files in deploy/, charts/, dalec/.
#   4. If the shared Dockerfile is not suitable, define DOCKERFILE_<flavor>
#      to point at a flavor-specific Dockerfile (see DOCKERFILE_azurelinux3).

AMD64_ONLY_FLAVORS := jammy azurelinux3
ALL_ARCHES_FLAVORS := noble
ALL_FLAVORS := $(AMD64_ONLY_FLAVORS) $(ALL_ARCHES_FLAVORS)

SRC_IMAGE_jammy := ubuntu:22.04
SRC_IMAGE_noble := ubuntu:24.04
SRC_IMAGE_azurelinux3 := mcr.microsoft.com/azurelinux/base/core:3.0

# Per-flavor Dockerfile override. Defaults to the shared Dockerfile.
DOCKERFILE = ./pkg/azurelustreplugin/Dockerfile
DOCKERFILE_azurelinux3 = ./pkg/azurelustreplugin/Dockerfile.azurelinux3
dockerfile-for = $(or $(DOCKERFILE_$1),$(DOCKERFILE))

# Fail fast if any declared flavor is missing its SRC_IMAGE_<flavor>.
# Without this, a missing var would silently expand to empty and the Dockerfile's
# ARG srcImage default would be used, producing a wrong-base image.
$(foreach f,$(ALL_FLAVORS),$(if $(strip $(SRC_IMAGE_$(f))),,$(error Missing SRC_IMAGE_$(f) for flavor '$(f)')))

# Fail fast if any flavor collides with an umbrella suffix. `push-latest-foo`
# (etc.) is fine, but a flavor literally named `latest` or `commit` would
# shadow the umbrellas via the longer-prefix pattern rules.
$(if $(filter latest commit,$(ALL_FLAVORS)),$(error Reserved flavor names: 'latest', 'commit' (collide with push-latest / push-commit umbrellas)))

# Flavors built for the current ARCH.
ifeq ($(ARCH),amd64)
  FLAVORS := $(ALL_FLAVORS)
else
  FLAVORS := $(ALL_ARCHES_FLAVORS)
endif

all: azurelustre

#
# Tests
#
.PHONY: verify
verify: unit-test
	hack/verify-all.sh

.PHONY: unit-test
unit-test:
	go test -covermode=count -coverprofile=profile.cov ./pkg/... ./test/utils/credentials

.PHONY: sanity-test
sanity-test: azurelustre
	go test -v -timeout=30m ./test/sanity

.PHONY: sanity-test-local
sanity-test-local:
	go test -v -timeout=30m ./test/sanity_local -ginkgo.skip="should fail when requesting to create a volume with already existing name and different capacity|should fail when the requested volume does not exist"

.PHONY: e2e-test
e2e-test:
	if [ ! -z "$(EXTERNAL_E2E_TEST_AZURELUSTRE)" ]; then \
		bash ./test/external-e2e/run.sh;\
	else \
		go test -v -timeout=0 ./test/e2e ${GINKGO_FLAGS};\
	fi

# Azure Lustre: Code build
#
.PHONY: quicklustre
quicklustre:
	GOOS=linux GOARCH=$(ARCH) go build -ldflags ${LDFLAGS} -mod vendor -o _output/azurelustreplugin ./pkg/azurelustreplugin

.PHONY: azurelustre
azurelustre:
	GOOS=linux GOARCH=$(ARCH) go build -a -ldflags ${LDFLAGS} -mod vendor -o _output/azurelustreplugin ./pkg/azurelustreplugin

.PHONY: azurelustre-dalec
azurelustre-dalec:
	GOOS=linux go build -a -ldflags ${LDFLAGS} -mod vendor -o /app/azurelustreplugin ./pkg/azurelustreplugin

#
# Azure Lustre: Docker build / tag / push
#
# Each operation (build, push, tag-latest, push-latest, tag-commit,
# push-commit) is a pattern rule with a `-<flavor>` suffix. An umbrella
# target with no suffix fans out to one per-flavor target per entry in
# $(FLAVORS), and the full build/tag/push dependency chain is expressed on
# the pattern rules themselves so single-flavor invocations and `make -j`
# all do the right thing.
#
# Maintainer note: do NOT add `docker-build` or `tag-latest` as direct
# prereqs of a higher-level umbrella (e.g. `push: docker-build`). Under
# `make -j` that reintroduces a race where pushes for one flavor can begin
# before the build for another finishes. Express ordering on the pattern
# rules instead, as is done below.
#
# `make docker-build-jammy`, `make push-noble`, etc. work as single-flavor
# targets.
#
# Naming: flavor names must not be `latest` or `commit`, since
# `push-latest` and `push-commit` are existing umbrella targets and would
# collide with hypothetical `push-<flavor>` instances.

# FORCE is a no-op prereq added to every pattern rule below.
#
# Pattern rules only fire when their target doesn't already exist as a file.
# If a file named e.g. `docker-build-jammy` were to appear in the working
# directory (a build artifact, an accidental `touch`, a tab-completion typo),
# make would consider the target up-to-date and skip the recipe. Depending on
# FORCE (which is itself `.PHONY` and has no recipe) marks the target as
# always-out-of-date so the recipe always runs.
.PHONY: FORCE
FORCE:

.PHONY: docker-build
docker-build: $(addprefix docker-build-,$(FLAVORS))

# Fail at make-time unless $1 is a flavor buildable for the current $(ARCH).
check-flavor = $(if $(filter $1,$(FLAVORS)),,$(error '$1' is not a known flavor for ARCH=$(ARCH). Valid flavors: $(FLAVORS)))

docker-build-%: FORCE
	$(call check-flavor,$*)
	docker build --platform=linux/$(ARCH) -t $(IMAGE_TAG)-$* --build-arg srcImage=$(SRC_IMAGE_$*) --output=type=docker -f $(call dockerfile-for,$*) .

.PHONY: push
push: $(addprefix push-,$(FLAVORS))

push-%: docker-build-% FORCE
	docker push $(IMAGE_TAG)-$*

.PHONY: tag-latest
tag-latest: $(addprefix tag-latest-,$(FLAVORS))

tag-latest-%: docker-build-% FORCE
	docker tag $(IMAGE_TAG)-$* $(IMAGE_TAG_LATEST)-$*

.PHONY: push-latest
push-latest: $(addprefix push-latest-,$(FLAVORS))

push-latest-%: tag-latest-% FORCE
	docker push $(IMAGE_TAG_LATEST)-$*

.PHONY: tag-commit
tag-commit: $(addprefix tag-commit-,$(FLAVORS))

tag-commit-%: tag-latest-% FORCE
	docker tag $(IMAGE_TAG_LATEST)-$* $(IMAGE_TAG_COMMIT)-$*

.PHONY: push-commit
push-commit: $(addprefix push-commit-,$(FLAVORS))

push-commit-%: tag-commit-% FORCE
	docker push $(IMAGE_TAG_COMMIT)-$*

# Print the list of image flavors built for the current ARCH.
# Used by CI pipelines to discover flavors without parsing Makefile variables.
.PHONY: print-flavors
print-flavors:
	@echo $(FLAVORS)

# Print every flavor regardless of ARCH. The single source of truth for
# test and release work.
.PHONY: print-all-flavors
print-all-flavors:
	@echo $(ALL_FLAVORS)

#
# Convenience aggregates
#
# The Go-binary build and the docker pipeline are independent dependency
# graphs. Expressing one as a make prereq of the other would either re-run
# docker work whenever the binary rebuilds or re-run the Go build for every
# docker invocation, so the aggregates use recursive `$(MAKE)` to sequence
# the two stages. MAKEFLAGS (ARCH, `-j`, etc.) inherit through `$(MAKE)`.
.PHONY: build-push-latest
build-push-latest:
	$(MAKE) azurelustre
	$(MAKE) push-latest

.PHONY: build-push-latest-commit
build-push-latest-commit:
	$(MAKE) azurelustre
	$(MAKE) push-latest push-commit

# `container` is the conventional CSI driver entry point for building images
# locally (see azurefile-csi-driver, azuredisk-csi-driver, blob-csi-driver).
# Kept as a thin wrapper so external scripts and muscle memory still work.
.PHONY: container
container:
	$(MAKE) azurelustre
	$(MAKE) docker-build

.PHONY: clean
clean:
	go clean -r -x
	-rm -rf _output
	-rm -f profile.cov
