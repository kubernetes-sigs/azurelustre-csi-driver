FROM ubuntu:18.04

COPY "./amlfsplugin" "/app/amlfsplugin"
COPY "./run_integration_test.sh" "/app/run_integration_test.sh"

RUN chmod +x "/app/run_integration_test.sh"

RUN apt-get update

# Fix vulnerability CVE-2018-7169
RUN apt-get upgrade -y login passwd

# Install all Lustre packages dependencies
RUN apt-get install -y --no-install-recommends wget git ca-certificates

COPY --from=golang /usr/local/go/ /usr/local/go/

ENV GOPATH="/root/go"
ENV PATH="/usr/local/go/bin:/root/go/bin:${PATH}"

WORKDIR "/app"

LABEL maintainers="fuxiuyin;joe-atzinger;Justin-Jin"
LABEL description="Azure Managed Lustre CSI driver integration test"

ENTRYPOINT ["/app/run_integration_test.sh"]
