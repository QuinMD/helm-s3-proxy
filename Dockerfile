# Use ARG to accept the TARGETARCH variable from the build context
ARG TARGETARCH

FROM golang:1.22.2-bullseye AS builder
# hadolint ignore=DL3008
RUN apt update && \
    apt install -y --no-install-recommends \
    xz-utils zip && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /work

# Create appuser
ENV USER=appuser
ENV UID=1000
RUN groupadd -r --gid ${UID} ${USER} && useradd --uid ${UID} -m --no-log-init -g ${USER} ${USER}

COPY go.mod go.sum /work/
RUN go mod download
RUN go mod verify
COPY . /work

# Use the TARGETARCH build argument to specify the architecture
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o helm-s3-proxy

FROM scratch
LABEL org.opencontainers.image.source https://github.com/cresta/helm-s3-proxy
# Import from builder.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the static executable
COPY --from=builder /work/helm-s3-proxy /helm-s3-proxy
# Use an unprivileged user.
USER appuser:appuser

EXPOSE 8080
ENTRYPOINT ["/helm-s3-proxy"]
