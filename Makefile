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

PKG = sigs.k8s.io/amlfs-csi-driver
GIT_COMMIT ?= $(shell git rev-parse HEAD)
REGISTRY ?= jusjin.azurecr.io
REGISTRY_NAME ?= $(shell echo $(REGISTRY) | sed "s/.azurecr.io//g")
IMAGE_NAME ?= amlfs-csi
IMAGE_VERSION ?= v0.1.0
CLOUD ?= AzurePublicCloud
# Use a custom version for E2E tests if we are in Prow
ifdef CI
ifndef PUBLISH
override IMAGE_VERSION := e2e-$(GIT_COMMIT)
endif
endif
IMAGE_TAG ?= $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_VERSION)
IMAGE_TAG_LATEST = $(REGISTRY)/$(IMAGE_NAME):latest
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS ?= "-X ${PKG}/pkg/amlfs.driverVersion=${IMAGE_VERSION} -X ${PKG}/pkg/amlfs.gitCommit=${GIT_COMMIT} -X ${PKG}/pkg/amlfs.buildDate=${BUILD_DATE} -s -w -extldflags '-static'"
E2E_HELM_OPTIONS ?= --set image.amlfs.pullPolicy=Always --set image.amlfs.repository=$(REGISTRY)/$(IMAGE_NAME) --set image.amlfs.tag=$(IMAGE_VERSION) --set driver.userAgentSuffix="e2e-test"
ifdef ENABLE_AMLFS
override E2E_HELM_OPTIONS := $(E2E_HELM_OPTIONS) --set controller.logLevel=6 --set node.logLevel=6
endif
E2E_HELM_OPTIONS += ${EXTRA_HELM_OPTIONS}
GINKGO_FLAGS = -ginkgo.v
GO111MODULE = on
GOPATH ?= $(shell go env GOPATH)
GOBIN ?= $(GOPATH)/bin
DOCKER_CLI_EXPERIMENTAL = enabled
export GOPATH GOBIN GO111MODULE DOCKER_CLI_EXPERIMENTAL

# Output type of docker buildx build
OUTPUT_TYPE ?= registry

ALL_ARCH.linux = amd64 #arm64
ALL_OS_ARCH = $(foreach arch, ${ALL_ARCH.linux}, linux-$(arch))

all: amlfs

.PHONY: verify
verify: unit-test
	hack/verify-all.sh

.PHONY: unit-test
unit-test:
	go test -covermode=count -coverprofile=profile.cov ./pkg/... ./test/utils/credentials

.PHONY: sanity-test
sanity-test: amlfs
	go test -v -timeout=30m ./test/sanity

.PHONY: integration-test
integration-test: amlfs
	go test -v -timeout=30m ./test/integration

.PHONY: e2e-test
e2e-test:
	if [ ! -z "$(EXTERNAL_E2E_TEST_AMLFS)" ]; then \
		bash ./test/external-e2e/run.sh;\
	else \
		go test -v -timeout=0 ./test/e2e ${GINKGO_FLAGS};\
	fi

.PHONY: e2e-bootstrap
e2e-bootstrap: install-helm
	# Only build and push the image if it does not exist in the registry
	docker pull $(IMAGE_TAG) || make amlfs-container push
	helm install amlfs-csi-driver ./charts/latest/amlfs-csi-driver --namespace kube-system --wait --timeout=15m -v=5 --debug \
		--set controller.runOnMaster=true \
		--set controller.replicas=1 \
		--set cloud=$(CLOUD) \
		$(E2E_HELM_OPTIONS)

.PHONY: install-helm
install-helm:
	curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash

.PHONY: e2e-teardown
e2e-teardown:
	helm delete amlfs-csi-driver --namespace kube-system

.PHONY: amlfs
amlfs:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -a -ldflags ${LDFLAGS} -mod vendor -o _output/amlfsplugin ./pkg/amlfsplugin

.PHONY: container
container: amlfs
	docker build -t $(IMAGE_TAG) --output=type=docker -f ./pkg/amlfsplugin/Dockerfile .
	# docker build -t $(IMAGE_TAG) -f ./pkg/amlfsplugin/Dockerfile .

.PHONY: container-linux
container-linux:
	docker buildx build --pull --output=type=$(OUTPUT_TYPE) --platform="linux/$(ARCH)" \
		-t $(IMAGE_TAG)-linux-$(ARCH) --build-arg ARCH=$(ARCH) -f ./pkg/amlfsplugin/Dockerfile .

.PHONY: amlfs-container
amlfs-container:
	docker buildx rm container-builder || true
	docker buildx create --use --name=container-builder

	# enable qemu for arm64 build
	# docker run --rm --privileged multiarch/qemu-user-static --reset -p yes
	for arch in $(ALL_ARCH.linux); do \
		ARCH=$${arch} $(MAKE) amlfs; \
		ARCH=$${arch} $(MAKE) container-linux; \
	done

.PHONY: push
push:
ifdef CI
	docker manifest create --amend $(IMAGE_TAG) $(foreach osarch, $(ALL_OS_ARCH), $(IMAGE_TAG)-${osarch})
	docker manifest push --purge $(IMAGE_TAG)
	docker manifest inspect $(IMAGE_TAG)
else
	docker push $(IMAGE_TAG)
endif

.PHONY: push-latest
push-latest:
ifdef CI
	docker manifest create --amend $(IMAGE_TAG_LATEST) $(foreach osarch, $(ALL_OS_ARCH), $(IMAGE_TAG)-${osarch})
	docker manifest push --purge $(IMAGE_TAG_LATEST)
	docker manifest inspect $(IMAGE_TAG_LATEST)
else
	docker push $(IMAGE_TAG_LATEST)
endif

.PHONY: build-push
build-push: amlfs-container
	docker tag $(IMAGE_TAG) $(IMAGE_TAG_LATEST)
	docker push $(IMAGE_TAG_LATEST)

.PHONY: clean
clean:
	go clean -r -x
	-rm -rf _output

.PHONY: create-metrics-svc
create-metrics-svc:
	kubectl create -f deploy/example/metrics/csi-amlfs-controller-svc.yaml

.PHONY: delete-metrics-svc
delete-metrics-svc:
	kubectl delete -f deploy/example/metrics/csi-amlfs-controller-svc.yaml --ignore-not-found
