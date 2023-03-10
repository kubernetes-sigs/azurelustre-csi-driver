# Copyright 2022 The Kubernetes Authors.
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

COPY "./azurelustreplugin" "/app/azurelustreplugin"
COPY "./run_integration_test.sh" "/app/run_integration_test.sh"

RUN chmod +x "/app/run_integration_test.sh"

RUN apt-get update

# Fix vulnerability CVE-2018-7169
RUN apt-get upgrade -y login passwd

COPY --from=golang /usr/local/go/ /usr/local/go/

ENV GOPATH="/root/go"
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"

WORKDIR "/app"

LABEL maintainers="fuxiuyin;joe-atzinger;Justin-Jin"
LABEL description="Azure Lustre CSI driver integration test"

ENTRYPOINT ["/app/run_integration_test.sh"]
