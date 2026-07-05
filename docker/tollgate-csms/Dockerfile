# syntax=docker/dockerfile:1.7
# tollgate-csms — OCPP 1.6 CSMS. Stateless (in-memory transaction tracking).
# Uses distroless since no subprocess shell-outs and no privileged ops.

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
    -o /out/tollgate-csms ./cmd/tollgate-csms/

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/tollgate-csms /usr/local/bin/tollgate-csms

ENV EMSP_URL="http://tollgate-auth-ocpi:8093"

USER nonroot:nonroot
EXPOSE 8887
ENTRYPOINT ["/usr/local/bin/tollgate-csms"]
