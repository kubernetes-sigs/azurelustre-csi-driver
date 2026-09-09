#!/bin/bash

# Copyright 2026 The Kubernetes Authors.
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

# Prefer the default Ubuntu CDN mirror and fall back to the Azure regional mirror
# only when the CDN is unreachable, via apt's priority mirror lists. Normal
# installs stay on the fast global CDN; the Azure mirror is just insurance.

set -euo pipefail

ARCHIVE_LIST="/etc/apt/mirrors-archive.list"
PORTS_LIST="/etc/apt/mirrors-ports.list"

# priority:1 is tried first, priority:2 is the fallback. Fields are TAB-separated.
printf 'http://archive.ubuntu.com/ubuntu/\tpriority:1\nhttp://azure.archive.ubuntu.com/ubuntu/\tpriority:2\n' > "${ARCHIVE_LIST}"
printf 'http://ports.ubuntu.com/ubuntu-ports/\tpriority:1\nhttp://azure.ports.ubuntu.com/ubuntu-ports/\tpriority:2\n' > "${PORTS_LIST}"

# Repoint archive/ports sources at the mirror lists (deb822 and one-line formats);
# leave security.ubuntu.com on the CDN.
for f in /etc/apt/sources.list.d/ubuntu.sources /etc/apt/sources.list; do
  [[ -f "${f}" ]] || continue
  sed -i \
    -e "s|http://archive\.ubuntu\.com/ubuntu/|mirror+file:${ARCHIVE_LIST}|g" \
    -e "s|http://ports\.ubuntu\.com/ubuntu-ports/|mirror+file:${PORTS_LIST}|g" \
    "${f}"
done
