FROM sbidprod.azurecr.io/quinault

RUN sed -i 's/^# deb/deb/g' /etc/apt/sources.list
RUN apt update -qq
RUN apt update
RUN apt-get --assume-yes install apt-utils libreadline7 golang-1.14-go

COPY driver /src/pkg/driver
COPY go.mod /src
COPY go.sum /src
COPY main.go /src/pkg

WORKDIR /src

RUN /usr/lib/go-1.14/bin/go mod download

ENV CGO_ENABLED 0
ENV GOOS linux
ENV PATH="${PATH}:/usr/lib/go-1.14/bin"

ENTRYPOINT ["go", "build", "-o", "/host_out/msft-laaso-lustre-csi", "./pkg/"]
