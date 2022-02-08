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

COPY "./_output/amlfsplugin" "/app/amlfsplugin"
COPY "./pkg/amlfsplugin/entrypoint.sh" "/app/entrypoint.sh"

RUN chmod +x "/app/entrypoint.sh"

RUN apt-get update

# Fix vulnerability CVE-2018-7169
RUN apt-get upgrade -y login passwd

# Install all Lustre packages dependencies
RUN apt-get install -y --no-install-recommends libreadline7 libkeyutils1 wget   \
    dmsetup gettext-base grub-common grub-gfxpayload-lists grub-pc grub-pc-bin  \
    grub2-common kmod libc-dev-bin libc6-dev libdevmapper1.02.1 libfreetype6    \
    libfuse2 libgdbm-compat4 libgdbm5 libkmod2 libpci-dev libpci3 libperl5.26   \
    libpng16-16 libsensors4 libsensors4-dev libsnmp-base libsnmp-dev libsnmp30  \
    libssl-dev libudev-dev libwrap0 libwrap0-dev libyaml-0-2 linux-base         \
    linux-image-unsigned-5.3.0-1045-gke linux-libc-dev                          \
    linux-modules-5.3.0-1045-gke manpages manpages-dev netbase os-prober perl   \
    perl-modules-5.26 ucf zlib1g-dev

WORKDIR "/app"

LABEL maintainers="fuxiuyin;joe-atzinger;Justin-Jin"
LABEL description="Azure Managed Lustre CSI driver"

ENTRYPOINT ["/app/entrypoint.sh", "/app/amlfsplugin"]  
