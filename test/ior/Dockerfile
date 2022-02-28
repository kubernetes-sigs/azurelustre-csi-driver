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

FROM ubuntu:18.04

RUN apt-get update
RUN apt-get -y install autoconf gcc git make mpich* \
    openssh-server openssh-client

WORKDIR "/app"

RUN git clone https://github.com/jusjin-org/ior.git

RUN cd ior                      \
    && ./bootstrap              \
    && ./configure              \
    && make                     \
    && make install             \
    && cd ..

RUN echo -e 'y\n' | ssh-keygen -f ~/.ssh/id_rsa -t rsa -N ''
RUN cat ~/.ssh/id_rsa.pub >>  ~/.ssh/authorized_keys

RUN mkdir -p /var/run/sshd

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D"]
