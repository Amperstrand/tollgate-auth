# syntax=docker/dockerfile:1.7
# tollgate-webssh — WebSocket → SSH bridge. Stateless, no host writes.

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
    -o /out/tollgate-webssh ./cmd/tollgate-webssh/

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/tollgate-webssh /usr/local/bin/tollgate-webssh

ENV TOLLGATE_WEBSSH_ADDR=":8092" \
    TOLLGATE_SSH_ADDR="tollgate-auth-ssh:2222"

USER nonroot:nonroot
EXPOSE 8092
ENTRYPOINT ["/usr/local/bin/tollgate-webssh"]
