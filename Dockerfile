FROM golang:1.17.3-buster AS builder
# hadolint ignore=DL3008
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    xz-utils zip && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /work

# Create appuser
ENV USER=appuser
ENV UID=1000
RUN groupadd -r --gid ${UID} ${USER} && useradd --uid ${UID} -m --no-log-init -g ${USER} ${USER}

# Install mage
ARG MAGE_VERSION=1.11.0
RUN curl -L -o /tmp/mage.tar.gz "https://github.com/magefile/mage/releases/download/v${MAGE_VERSION}/mage_${MAGE_VERSION}_Linux-64bit.tar.gz" && tar -C /tmp -zxvf /tmp/mage.tar.gz && mv /tmp/mage /usr/local/bin

COPY go.mod /work
COPY go.sum /work
RUN go mod download
RUN go mod verify
COPY . /work
RUN mage go:build
RUN mkdir /empty_dir

FROM scratch
LABEL org.opencontainers.image.source https://github.com/cresta/helm-s3-proxy
# Import from builder.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy our static executable
COPY --from=builder /work/main /main
# Use an unprivileged user.
USER appuser:appuser

EXPOSE 8080
ENTRYPOINT ["/main"]