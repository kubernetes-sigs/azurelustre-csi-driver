# Test-only image for the integration_dalec harness.
#
# It carries the `csc` CSI client (github.com/dell/gocsi/csc) plus a POSIX
# shell + coreutils/awk/sed so run_integration_test.sh can drive the driver
# under test over a shared CSI socket. This image is NEVER shipped: it is built
# and loaded into the local KIND cluster purely to run the integration test,
# which keeps the distro driver images (azlinux3/jammy/noble) unmodified and
# free of any test tooling or package managers.
FROM golang:1.25 AS build
ARG CSC_VERSION=v1.13.0
RUN GOBIN=/out go install github.com/dell/gocsi/csc@${CSC_VERSION}

FROM debian:stable-slim
# bash, sed, mawk, coreutils ship in debian-slim; csc is the only addition.
COPY --from=build /out/csc /usr/local/bin/csc
ENTRYPOINT ["/bin/bash"]
