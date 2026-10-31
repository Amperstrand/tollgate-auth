# syntax=docker/dockerfile:1.7
# tollgate-daemon — persistent RADIUS auth server.
#
# UID strategy: matches host tollgate user (UID 994, GID 983). Added to
# supplementary group cashu-wallet (GID 985) for /var/lib/cashu-wallet.
#
# Supports both Unix socket (default, TOLLGATE_SOCKET=/run/tollgate/X.sock)
# and TCP (TOLLGATE_SOCKET=tcp://0.0.0.0:8094) for container deployments
# where shared Unix-socket volumes are awkward.

ARG GO_VERSION=1.25
ARG CDK_CLI_VERSION=v0.18.3
# Pinned release checksums (issue #24): verify downloaded bytes before use.
ARG CDK_CLI_SHA_AMD64=bb7a8b0f1b9c6a164c7e50f931b25c3e6232cdaedbb18df22368ebb0c024b337
ARG CDK_CLI_SHA_ARM64=e0abad57ba8e152efa311ad2d476def2ab0973cb9bcc6066b024e55496e08235
ARG TARGETOS=linux
ARG TARGETARCH=amd64

FROM alpine:3.20 AS cdk-cli-prep
ARG CDK_CLI_VERSION
ARG CDK_CLI_SHA_AMD64
ARG CDK_CLI_SHA_ARM64
ARG TARGETARCH
# SHA256 pins for the official release assets, per arch. To re-derive when
# bumping CDK_CLI_VERSION: fetch the release's SHA256SUMS asset from
# https://github.com/cashubtc/cdk/releases/download/${CDK_CLI_VERSION}/SHA256SUMS
# (or sha256sum the downloaded asset) and update the matching case arm.
# Verification fails the build if the downloaded asset does not match (docs/SECURITY_AUDIT_WINDOW3.md).
RUN apk add --no-cache curl ca-certificates
RUN case "$TARGETARCH" in \
      amd64) CDK_ARCH="x86_64"; CDK_SHA="$CDK_CLI_SHA_AMD64" ;; \
      arm64) CDK_ARCH="aarch64"; CDK_SHA="$CDK_CLI_SHA_ARM64" ;; \
      *) echo "unsupported arch: $TARGETARCH"; exit 1 ;; \
    esac && \
    CDK_VER_NO_V="${CDK_CLI_VERSION#v}" && \
    curl -fsSL -o /usr/local/bin/cdk-cli \
      "https://github.com/cashubtc/cdk/releases/download/${CDK_CLI_VERSION}/cdk-cli-${CDK_VER_NO_V}-${CDK_ARCH}" && \
    echo "${CDK_SHA}  /usr/local/bin/cdk-cli" | sha256sum -c - && \
    chmod +x /usr/local/bin/cdk-cli && \
    /usr/local/bin/cdk-cli --version

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/tollgate-daemon ./cmd/tollgate-daemon/

FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 983 tollgate && \
    addgroup -g 985 cashu-wallet && \
    adduser -u 994 -G tollgate -D -h /var/lib/cashu-wallet tollgate && \
    addgroup tollgate cashu-wallet && \
    mkdir -p /opt/cashu-tollgate /opt/tollgate-auth /var/lib/cashu-wallet /run/tollgate && \
    chown -R tollgate:tollgate /opt/cashu-tollgate /opt/tollgate-auth /run/tollgate

COPY --from=build /out/tollgate-daemon /usr/local/bin/tollgate-daemon
COPY --from=cdk-cli-prep /usr/local/bin/cdk-cli /usr/local/bin/cdk-cli

ENV TOLLGATE_SOCKET="/run/tollgate/tollgate.sock" \
    TOLLGATE_HTTP_ADDR=":8091" \
    TOLLGATE_BASE_DIR="/opt/cashu-tollgate" \
    TOLLGATE_WALLET_DIR="/var/lib/cashu-wallet" \
    TOLLGATE_AUTH_MODE="local"

VOLUME ["/opt/cashu-tollgate", "/opt/tollgate-auth", "/var/lib/cashu-wallet", "/run/tollgate"]
USER 994:983
EXPOSE 8091 8094
ENTRYPOINT ["/usr/local/bin/tollgate-daemon"]
