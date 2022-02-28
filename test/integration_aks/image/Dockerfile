FROM ubuntu:18.04

COPY "./amlfsplugin" "/app/amlfsplugin"
COPY "./run_integration_test.sh" "/app/run_integration_test.sh"

RUN chmod +x "/app/run_integration_test.sh"

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
    perl-modules-5.26 ucf zlib1g-dev git ca-certificates

COPY --from=golang /usr/local/go/ /usr/local/go/

ENV GOPATH="/root/go"
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"

WORKDIR "/app"

LABEL maintainers="fuxiuyin;joe-atzinger;Justin-Jin"
LABEL description="Azure Managed Lustre CSI driver integration test"

ENTRYPOINT ["/app/run_integration_test.sh"]
