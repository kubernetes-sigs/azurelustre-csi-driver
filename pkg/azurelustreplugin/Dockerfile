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

ARG srcImage=ubuntu:22.04
FROM ${srcImage}

COPY "./_output/azurelustreplugin" "/app/azurelustreplugin"
COPY "./pkg/azurelustreplugin/useAzureAptMirror.sh" "/app/useAzureAptMirror.sh"
COPY "./pkg/azurelustreplugin/entrypoint.sh" "/app/entrypoint.sh"
COPY "./pkg/azurelustreplugin/start.sh" "/app/start.sh"
COPY "./pkg/azurelustreplugin/readinessProbe.sh" "/app/readinessProbe.sh"

RUN chmod +x "/app/azurelustreplugin" \
  && chmod +x "/app/useAzureAptMirror.sh" \
  && chmod +x "/app/entrypoint.sh" \
  && chmod +x "/app/start.sh" \
  && chmod +x "/app/readinessProbe.sh"

# Prefer the Ubuntu CDN; keep the Azure mirror as a lower-priority apt fallback.
RUN /app/useAzureAptMirror.sh

RUN apt-get update && \
  apt-get upgrade -y && \
  DEBIAN_FRONTEND=noninteractive apt-get install -y \
    gpg curl ca-certificates iproute2 kmod && \
  apt-get autoremove -y && \
  apt-get clean -y && \
  rm -rf \
  /var/cache/debconf/* \
  /var/lib/apt/lists/* \
  /var/log/* \
  /tmp/* \
  /var/tmp/*

WORKDIR "/app"

LABEL maintainers="dabradley;jeffbearer;t-mialve;csmuell"
LABEL description="Azure Lustre CSI driver"

ENTRYPOINT ["/app/start.sh", "/app/azurelustreplugin"]
