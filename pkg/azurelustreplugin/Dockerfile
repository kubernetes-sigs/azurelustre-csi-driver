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

FROM ubuntu:22.04

COPY "./_output/azurelustreplugin" "/app/azurelustreplugin"
COPY "./pkg/azurelustreplugin/entrypoint.sh" "/app/entrypoint.sh"

RUN chmod +x "/app/entrypoint.sh"

RUN apt-get update && apt --only-upgrade -y install tar libudev1 libsystemd0 libssl3
RUN apt-get clean all

WORKDIR "/app"

LABEL maintainers="dabradley;t-mialve"
LABEL description="Azure Lustre CSI driver"

ENTRYPOINT ["/app/entrypoint.sh", "/app/azurelustreplugin"]
