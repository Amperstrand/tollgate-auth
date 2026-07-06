# syntax=docker/dockerfile:1.7
# FreeRADIUS — RADIUS auth + RadSec, with tollgate exec modules.
#
# Co-locates: FreeRADIUS, tollgate-shim, tollgate-auth-radius, wrapper script
# (execve requires same filesystem namespace).

ARG GO_VERSION=1.25
ARG FREERADIUS_IMAGE=freeradius/freeradius-server:3.2.5-alpine

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags='-s -w' -o /out/tollgate-shim        ./cmd/tollgate-shim/ && \
    go build -trimpath -ldflags='-s -w' -o /out/tollgate-auth-radius  ./cmd/tollgate-auth-radius/

FROM ${FREERADIUS_IMAGE} AS runtime

# Official alpine image runs as root and has no freerad user. Create one
# with explicit UID/GID 101/101 (matches Debian freeradius convention).
# We bind to UDP 1812 and TCP 2083 — both <1024, so we need CAP_NET_BIND_SERVICE
# (passed via docker run --cap-add) OR root. Running as freerad + cap-add is safer.
RUN addgroup -g 101 -S freerad && \
    adduser -u 101 -S -G freerad -h /etc/raddb freerad && \
    mkdir -p /var/log/radius /run/radiusd && \
    chown -R freerad:freerad /etc/raddb /var/log/radius /run

# Copy our Go binaries (FreeRADIUS exec module calls these via execve)
COPY --from=build /out/tollgate-shim        /usr/local/bin/tollgate-shim
COPY --from=build /out/tollgate-auth-radius /usr/local/bin/tollgate-auth-radius
RUN chmod +x /usr/local/bin/tollgate-shim /usr/local/bin/tollgate-auth-radius

# Copy the wrapper script (sources /etc/tollgate/secrets.env at runtime)
COPY scripts/tollgate-auth-radius-delegated-wrapper.sh /usr/local/libexec/tollgate-auth-radius-delegated
RUN chown root:root /usr/local/libexec/tollgate-auth-radius-delegated && \
    chmod 0755 /usr/local/libexec/tollgate-auth-radius-delegated

# Copy the shim's TCP wrapper. FreeRADIUS rlm_exec strips env vars from
# the spawned process, so the shim cannot inherit TOLLGATE_SOCKET from
# /etc/default/freeradius. This wrapper sets it explicitly before exec'ing
# the shim. The cashu-exec module config calls this wrapper, not the shim
# directly.
COPY scripts/tollgate-shim-tcp-wrapper.sh /usr/local/libexec/tollgate-shim-tcp-wrapper.sh
RUN chown root:root /usr/local/libexec/tollgate-shim-tcp-wrapper.sh && \
    chmod 0755 /usr/local/libexec/tollgate-shim-tcp-wrapper.sh

# Copy our FreeRADIUS configs
COPY config/freeradius/clients.conf /etc/raddb/clients.conf
COPY config/freeradius/users        /etc/raddb/mods-config/files/authorize
COPY config/freeradius/mods-available/cashu-exec           /etc/raddb/mods-available/cashu-exec
COPY config/freeradius/mods-available/cashu-exec-delegated /etc/raddb/mods-available/cashu-exec-delegated
COPY config/freeradius/mods-available/tollgate-acct        /etc/raddb/mods-available/tollgate-acct
COPY config/freeradius/mods-available/eap                  /etc/raddb/mods-available/eap
COPY config/freeradius/mods-available/cashu-debug          /etc/raddb/mods-available/cashu-debug
COPY config/freeradius/mods-available/outer-debug          /etc/raddb/mods-available/outer-debug
COPY config/freeradius/mods-available/inner-debug          /etc/raddb/mods-available/inner-debug
COPY config/freeradius/sites-available/inner-tunnel        /etc/raddb/sites-available/inner-tunnel
COPY config/freeradius/sites-available/radsec              /etc/raddb/sites-available/radsec
COPY config/freeradius/sites-available/default             /etc/raddb/sites-available/default
COPY config/freeradius/policy.d/cashu-auth                 /etc/raddb/policy.d/cashu-auth

# Rewrite Debian-specific cert paths to alpine container paths.
# Host config uses /etc/freeradius/3.0/certs/letsencrypt/... but the alpine
# image uses /etc/raddb/. Sed at build time so the source config remains
# compatible with both deployments.
RUN sed -i 's|/etc/freeradius/3.0/|/etc/raddb/|g' /etc/raddb/sites-available/radsec

# Rewrite the cashu-exec module to call the TCP wrapper instead of the shim
# directly. FreeRADIUS rlm_exec strips env vars, so the shim cannot inherit
# TOLLGATE_SOCKET — the wrapper sets it explicitly. Without this rewrite,
# every auth request silently fails (shim falls back to a Unix socket that
# doesn't exist in the container, exits non-zero, FreeRADIUS returns bare
# Access-Reject with no Reply-Message).
RUN sed -i 's|/usr/local/bin/tollgate-shim|/usr/local/libexec/tollgate-shim-tcp-wrapper.sh|g' /etc/raddb/mods-available/cashu-exec

# Enable our modules and sites
RUN cd /etc/raddb && \
    ln -sf ../mods-available/cashu-exec           mods-enabled/cashu-exec && \
    ln -sf ../mods-available/cashu-exec-delegated mods-enabled/cashu-exec-delegated && \
    ln -sf ../mods-available/tollgate-acct        mods-enabled/tollgate-acct && \
    ln -sf ../mods-available/cashu-debug          mods-enabled/cashu-debug && \
    ln -sf ../mods-available/outer-debug          mods-enabled/outer-debug && \
    ln -sf ../mods-available/inner-debug          mods-enabled/inner-debug && \
    ln -sf ../sites-available/radsec               sites-enabled/radsec

# Cert directories — populated at runtime via volume mount.
# Generate placeholder certs at build time so `radiusd -XC` passes during
# image build (CI runs -XC as a sanity check). The real Let's Encrypt certs
# are bind-mounted into /etc/raddb/certs/letsencrypt/ at runtime, which
# overrides these placeholders for RadSec. The placeholder is only used by
# the EAP module (which uses /etc/raddb/certs/server.{crt,key}).
RUN mkdir -p /etc/raddb/certs/letsencrypt /etc/raddb/certs/radsec && \
    apk add --no-cache openssl && \
    cd /etc/raddb/certs && \
    rm -f server.key server.crt dh && \
    openssl req -x509 -newkey rsa:2048 \
      -keyout server.key -out server.crt \
      -days 1 -nodes \
      -subj "/CN=build-placeholder" 2>/dev/null && \
    openssl dhparam -out dh 2048 2>/dev/null && \
    test -s server.key && test -s server.crt && test -s dh && \
    cd /etc/raddb/certs/letsencrypt && \
    cp ../server.key nodns.shop.key && \
    cp ../server.crt nodns.shop.crt

# The official image ships radiusd.conf as root:root mode 0640 (only root
# can read). Since we run as non-root freerad (UID 101), chown the entire
# /opt/etc/raddb tree so freerad can read the config.
RUN chown -R freerad:freerad /opt/etc/raddb /etc/raddb /opt/var/log/radius /var/log/radius /run /etc/raddb/certs && \
    mkdir -p /var/log/freeradius && \
    chown freerad:freerad /var/log/freeradius

# Default shim connection — TCP to the daemon. Override to Unix socket path
# if sharing /run/tollgate via volume with the daemon container.
ENV TOLLGATE_SOCKET="tcp://tollgate-daemon:8094" \
    TOLLGATE_AUTH_MODE="local"

VOLUME ["/etc/raddb/certs/letsencrypt", "/etc/tollgate"]

EXPOSE 1812/udp 1813/udp 2083/tcp

USER freerad
# Official image installs FreeRADIUS at /opt/sbin/radiusd (not /usr/sbin/freeradius).
# The /docker-entrypoint.sh wrapper sets PATH and translates "radiusd" arg appropriately.
ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["radiusd"]
