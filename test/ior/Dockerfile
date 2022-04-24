#
# Copyright (c) Microsoft Corporation. All rights reserved.
#

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
