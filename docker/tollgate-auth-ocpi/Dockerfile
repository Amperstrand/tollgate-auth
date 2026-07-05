# syntax=docker/dockerfile:1.7
# tollgate-auth-ocpi — OCPI 2.2.1 eMSP receiver.
#
# UID strategy: matches host tollgate user (UID 994, GID 983) so bind-mounted
# host dirs are writable. Added to supplementary group cashu-wallet (GID 985)
# so /var/lib/cashu-wallet (mode 0770) is accessible.

ARG GO_VERSION=1.25
ARG CDK_CLI_VERSION=v0.17.2
ARG TARGETOS=linux
ARG TARGETARCH=amd64

FROM alpine:3.20 AS cdk-cli-prep
ARG CDK_CLI_VERSION
ARG TARGETARCH
RUN apk add --no-cache curl ca-certificates
RUN case "$TARGETARCH" in \
      amd64) CDK_ARCH="x86_64"; CDK_SHA="37ae2bb62dbec8a8edd5b599e09674947ba3fbdf8a8b7997cf3c8058a1705734" ;; \
      arm64) CDK_ARCH="aarch64"; CDK_SHA="827b16e2c3e7422718f8f11495cf84f670422b088f55a73f53193c3d84369d55" ;; \
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
    go build -trimpath -ldflags='-s -w' \
    -o /out/tollgate-auth-ocpi ./cmd/tollgate-auth-ocpi/

FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 983 tollgate && \
    addgroup -g 985 cashu-wallet && \
    adduser -u 994 -G tollgate -D -h /var/lib/cashu-wallet tollgate && \
    addgroup tollgate cashu-wallet && \
    mkdir -p /opt/tollgate-auth /var/lib/cashu-wallet && \
    chown -R tollgate:tollgate /opt/tollgate-auth

COPY --from=build /out/tollgate-auth-ocpi /usr/local/bin/tollgate-auth-ocpi
COPY --from=cdk-cli-prep /usr/local/bin/cdk-cli /usr/local/bin/cdk-cli

ENV TOLLGATE_OCPI_ADDR=":8093" \
    TOLLGATE_OCPI_COUNTRY="NO" \
    TOLLGATE_OCPI_PARTY="TGA" \
    TOLLGATE_AUTH_MODE="delegated" \
    TOLLGATE_SESSIOND_URL="http://tollgate-net:2121" \
    TOLLGATE_WALLET_DIR="/var/lib/cashu-wallet" \
    TOLLGATE_BASE_DIR="/opt/tollgate-auth"

VOLUME ["/opt/tollgate-auth", "/var/lib/cashu-wallet"]
USER 994:983
ENTRYPOINT ["/usr/local/bin/tollgate-auth-ocpi"]
