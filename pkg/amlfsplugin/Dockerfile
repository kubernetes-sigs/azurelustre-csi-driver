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

FROM ubuntu:18.04 as builder

ARG BASE_FOLDER="/go/src/github.com/kubernetes-sigs/amlfsplugin"
ARG CSI_NAME="amlfsplugin"
ARG EXEC_SCRIPT="entrypoint.sh"
ARG CLIENT_UTIL_PKG_NAME="lustre-client-utils_2.14.0-1_amd64.deb"
ARG CLIENT_MODS_PKG_NAME="lustre-client-modules-5.4.0-1059-azure_2.14.0-1_amd64.deb"

ADD "./_output/${CSI_NAME}" "${BASE_FOLDER}/bin/${CSI_NAME}"
ADD "./msft-csi-lustre-proto/code/pkg/${EXEC_SCRIPT}" "${BASE_FOLDER}/bin/${EXEC_SCRIPT}"
ADD "./msft-csi-lustre-proto/code/debs/${CLIENT_UTIL_PKG_NAME}" "${BASE_FOLDER}/debs/${CLIENT_UTIL_PKG_NAME}"
ADD "./msft-csi-lustre-proto/code/debs/${CLIENT_MODS_PKG_NAME}" "${BASE_FOLDER}/debs/${CLIENT_MODS_PKG_NAME}"

ENV PKG1 "${BASE_FOLDER}/debs/${CLIENT_UTIL_PKG_NAME}"
ENV PKG2 "${BASE_FOLDER}/debs/${CLIENT_MODS_PKG_NAME}"

RUN chmod +x "${BASE_FOLDER}/bin/${EXEC_SCRIPT}"

# Build the name of the execution script.
# This has to be an ENV instead of ARG so it resolves correctly with ENTRYPOINT.
# $@ passes the arguments through to the exec script.
ENV EXEC "${BASE_FOLDER}/bin/${EXEC_SCRIPT} bin/${CSI_NAME} \"$@\""

WORKDIR "${BASE_FOLDER}"

ENTRYPOINT $EXEC
