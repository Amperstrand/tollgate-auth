# syntax=docker/dockerfile:1.7
# tollgate-settle — weekly settlement job (oneshot).
# Reads ledger JSONL written by daemon + ocpi, publishes Nostr DMs.

ARG GO_VERSION=1.25
ARG TARGETOS=linux
ARG TARGETARCH=amd64

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' \
    -o /out/tollgate-settle ./cmd/tollgate-settle/

FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 983 tollgate && \
    adduser -u 994 -G tollgate -D tollgate && \
    mkdir -p /opt/tollgate-auth /opt/cashu-tollgate && \
    chown -R tollgate:tollgate /opt/tollgate-auth /opt/cashu-tollgate

COPY --from=build /out/tollgate-settle /usr/local/bin/tollgate-settle

USER 994:983
VOLUME ["/opt/cashu-tollgate", "/opt/tollgate-auth"]
ENTRYPOINT ["/usr/local/bin/tollgate-settle"]
CMD ["--ledger", "/opt/cashu-tollgate/ledger.jsonl"]
