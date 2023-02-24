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

FROM ubuntu:18.04

COPY "./_output/azurelustreplugin" "/app/azurelustreplugin"
COPY "./pkg/azurelustreplugin/entrypoint.sh" "/app/entrypoint.sh"
COPY "./pkg/azurelustreplugin/fix-lnet.sh" "/app/fix-lnet.sh"

RUN chmod +x "/app/entrypoint.sh"
RUN chmod +x "/app/fix-lnet.sh"

# RUN apt-get update

# Install all Lustre packages dependencies
# RUN apt-get install -y --no-install-recommends ca-certificates curl distro-info-data gpg gpgconf kmod libasn1-8-heimdal           \
#   libassuan0 libcurl4 libexpat1 libgssapi-krb5-2 libgssapi3-heimdal libhcrypto4-heimdal libheimbase1-heimdal                      \
#   libheimntlm0-heimdal libhx509-5-heimdal libk5crypto3 libkeyutils1 libkmod2 libkrb5-26-heimdal libkrb5-3 libkrb5support0         \
#   libldap-2.4-2 libldap-common libmpdec2 libnghttp2-14 libpsl5 libpython3-stdlib libpython3.6-minimal libpython3.6-stdlib         \
#   libreadline7 libroken18-heimdal librtmp1 libsasl2-2 libsasl2-modules-db libsqlite3-0 libssl1.1 libwind0-heimdal                 \
#   libyaml-0-2 linux-base linux-base-sgx mime-support openssl python3 python3-minimal python3.6 python3.6-minimal readline-common  

# RUN apt-get clean all

WORKDIR "/app"

LABEL maintainers="vinli-cn;fuxiuyin;joe-atzinger;Justin-Jin"
LABEL description="Azure Lustre CSI driver"

ENTRYPOINT ["/app/entrypoint.sh", "/app/azurelustreplugin"]
