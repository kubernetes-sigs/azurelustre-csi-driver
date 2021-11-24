# Copyright 2019 The Kubernetes Authors.
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

IMAGE?=jusjin.azurecr.io/msft-laaso-lustre-csi
GO111MODULE=on
GOPROXY=direct

.EXPORT_ALL_VARIABLES:

.PHONY: msft-laaso-lustre-csi
msft-laaso-lustre-csi:
	mkdir -p bin
	cd pkg; docker build -t laaso/csi-build:latest .
	cd pkg; docker run -v $(PWD)/bin:/host_out laaso/csi-build:latest

.PHONY: image
image:
	docker build -t $(IMAGE):latest .

.PHONY: push
push:
	docker push $(IMAGE):latest
