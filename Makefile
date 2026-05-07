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
IMAGE_TAG ?= $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_VERSION)
LATEST_TAG ?= latest
IMAGE_TAG_LATEST = $(REGISTRY)/$(IMAGE_NAME):$(LATEST_TAG)
COMMIT_TAG ?= $(LATEST_TAG)-$(GIT_COMMIT)
IMAGE_TAG_COMMIT = $(REGISTRY)/$(IMAGE_NAME):$(COMMIT_TAG)
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

#
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
# Azure Lustre: Docker build
#
# Jammy is amd64-only. Noble supports amd64 and arm64.
.PHONY: docker-build
docker-build:
ifeq ($(ARCH),amd64)
	docker build --platform=linux/amd64 -t $(IMAGE_TAG)-jammy --build-arg srcImage=ubuntu:22.04 --output=type=docker -f ./pkg/azurelustreplugin/Dockerfile .
endif
	docker build --platform=linux/$(ARCH) -t $(IMAGE_TAG)-noble --build-arg srcImage=ubuntu:24.04 --output=type=docker -f ./pkg/azurelustreplugin/Dockerfile .
.PHONY: quickcontainer
quickcontainer: quicklustre docker-build
.PHONY: container
container: azurelustre docker-build

#
# Azure Lustre: Docker tag & push
#
.PHONY: push
push:
ifeq ($(ARCH),amd64)
	docker push $(IMAGE_TAG)-jammy
endif
	docker push $(IMAGE_TAG)-noble

.PHONY: tag-latest
tag-latest:
ifeq ($(ARCH),amd64)
	docker tag $(IMAGE_TAG)-jammy $(IMAGE_TAG_LATEST)-jammy
endif
	docker tag $(IMAGE_TAG)-noble $(IMAGE_TAG_LATEST)-noble

.PHONY: push-latest
push-latest: tag-latest
ifeq ($(ARCH),amd64)
	docker push $(IMAGE_TAG_LATEST)-jammy
endif
	docker push $(IMAGE_TAG_LATEST)-noble

.PHONY: tag-commit
tag-commit: tag-latest
ifeq ($(ARCH),amd64)
	docker tag $(IMAGE_TAG_LATEST)-jammy $(IMAGE_TAG_COMMIT)-jammy
endif
	docker tag $(IMAGE_TAG_LATEST)-noble $(IMAGE_TAG_COMMIT)-noble

.PHONY: push-commit
push-commit: tag-commit
ifeq ($(ARCH),amd64)
	docker push $(IMAGE_TAG_COMMIT)-jammy
endif
	docker push $(IMAGE_TAG_COMMIT)-noble

# Print the list of image flavors built for the current ARCH.
# Used by CI pipelines to discover flavors without parsing Makefile variables.
.PHONY: print-flavors
print-flavors:
ifeq ($(ARCH),amd64)
	@echo jammy noble
else
	@echo noble
endif

.PHONY: build-push
build-push: container push

.PHONY: build-push-quick
build-push-quick: quickcontainer push

.PHONY: build-push-latest
build-push-latest: container push-latest

.PHONY: build-push-latest-commit
build-push-latest-commit: container push-latest push-commit

.PHONY: build-push-quick-latest
build-push-quick-latest: quickcontainer push-latest

.PHONY: clean
clean:
	go clean -r -x
	-rm -rf _output
	-rm -f profile.cov
