# docker/deploy-containers.sh — idempotent deploy of the containerized stack.
#
# This script is the canonical "how to deploy tollgate-auth in containers"
# after the initial migration. It assumes images are already built and
# tagged on the host (or pulled from a registry — TBD).
#
# Usage:
#   ./docker/deploy-containers.sh           # deploy/restart all
#   ./docker/deploy-containers.sh status    # show status only
#
# Idempotent: re-running is safe. Existing containers are recreated with
# the latest image tag.
#!/bin/bash
set -euo pipefail

# Image tags — two strategies:
#   1. GHCR (default): ghcr.io/amperstrand/tollgate-X:latest — pulled from
#      the CI workflow on every push to main.
#   2. Local (override): set USE_LOCAL_IMAGES=1 to use locally-built :test
#      tags. Useful for dev, testing, and pre-registry deployment.
# Override tag via env var: IMAGE_TAG=<sha> ./deploy-containers.sh
IMAGE_TAG="${IMAGE_TAG:-latest}"

if [ "${USE_LOCAL_IMAGES:-0}" = "1" ]; then
  IMAGE_WEBSSH="tollgate-tollgate-webssh:test"
  IMAGE_CSMS="tollgate-csms:test"
  IMAGE_OCIPI="tollgate-auth-ocpi:test"
  IMAGE_DAEMON="tollgate-daemon:test"
  IMAGE_FREERADIUS="freeradius:test"
  IMAGE_SETTLE="tollgate-tollgate-settle:test"
else
  IMAGE_PREFIX="ghcr.io/amperstrand"
  IMAGE_WEBSSH="${IMAGE_PREFIX}/tollgate-webssh:${IMAGE_TAG}"
  IMAGE_CSMS="${IMAGE_PREFIX}/tollgate-csms:${IMAGE_TAG}"
  IMAGE_OCIPI="${IMAGE_PREFIX}/tollgate-auth-ocpi:${IMAGE_TAG}"
  IMAGE_DAEMON="${IMAGE_PREFIX}/tollgate-daemon:${IMAGE_TAG}"
  IMAGE_FREERADIUS="${IMAGE_PREFIX}/tollgate-freeradius:${IMAGE_TAG}"
  IMAGE_SETTLE="${IMAGE_PREFIX}/tollgate-settle:${IMAGE_TAG}"
fi

# Common restart policy
RESTART=unless-stopped

deploy() {
    local name=$1 image=$2 ; shift 2
    echo ">>> deploying $name from $image"
    docker rm -f "$name" 2>/dev/null || true
    # All containers get --cap-drop=ALL by default. Stateful containers
    # (daemon, ocpi) get --group-add 985 (cashu-wallet) so they can write
    # to /var/lib/cashu-wallet for cdk-cli redemptions.
    docker run -d --name "$name" --restart "$RESTART" --cap-drop=ALL "$@" "$image"
}

case "${1:-deploy}" in
  deploy)
    # Stateless services (no UID/host-dir concerns)
    deploy tollgate-webssh-docker "$IMAGE_WEBSSH" \
        -p 127.0.0.1:8092:8092 \
        -e TOLLGATE_WEBSSH_ADDR=:8092 \
        -e TOLLGATE_SSH_ADDR=127.0.0.1:2222

    deploy tollgate-csms-docker "$IMAGE_CSMS" \
        -p 127.0.0.1:8887:8887 \
        -e EMSP_URL=http://127.0.0.1:8093 \
        --env-file /etc/tollgate/secrets.env

    deploy tollgate-auth-ocpi-docker "$IMAGE_OCIPI" \
        --group-add 985 \
        -p 127.0.0.1:8093:8093 \
        -e TOLLGATE_OCPI_ADDR=:8093 \
        -e TOLLGATE_OCPI_PUBLIC_URL=https://ocpi.nodns.shop \
        -e TOLLGATE_OCPI_DASH_URL=https://ocpi.nodns.shop \
        -e TOLLGATE_OCPI_COUNTRY=NO \
        -e TOLLGATE_OCPI_PARTY=TGA \
        -e TOLLGATE_BASE_DIR=/opt/tollgate-auth \
        -e TOLLGATE_WALLET_DIR=/var/lib/cashu-wallet \
        -e TOLLGATE_AUTH_MODE=delegated \
        -e TOLLGATE_SESSIOND_URL=http://127.0.0.1:2121 \
        --env-file /etc/tollgate/secrets.env \
        -v /opt/tollgate-auth:/opt/tollgate-auth \
        -v /var/lib/cashu-wallet:/var/lib/cashu-wallet

    # Daemon — uses TCP for shim connections, bind-mounts state dirs
    deploy tollgate-daemon-docker "$IMAGE_DAEMON" \
        --group-add 985 \
        -p 127.0.0.1:8091:8091 \
        -p 127.0.0.1:18094:8094 \
        -e TOLLGATE_SOCKET=tcp://0.0.0.0:8094 \
        -e TOLLGATE_HTTP_ADDR=:8091 \
        -e TOLLGATE_BASE_DIR=/opt/cashu-tollgate \
        -e TOLLGATE_WALLET_DIR=/var/lib/cashu-wallet \
        -e TOLLGATE_AUTH_MODE=local \
        --env-file /etc/tollgate/secrets.env \
        -v /opt/cashu-tollgate:/opt/cashu-tollgate \
        -v /opt/tollgate-auth:/opt/tollgate-auth \
        -v /var/lib/cashu-wallet:/var/lib/cashu-wallet

    # FreeRADIUS — host networking for UDP 1812 perf, RadSec TLS.
    # Needs CAP_NET_BIND_SERVICE for ports <1024. Wrapper script inside
    # container sets TOLLGATE_SOCKET before exec'ing shim.
    deploy tollgate-freeradius-docker "$IMAGE_FREERADIUS" \
        --network host \
        --cap-add NET_BIND_SERVICE \
        -e TOLLGATE_SOCKET=tcp://127.0.0.1:18094 \
        -e TOLLGATE_AUTH_MODE=local \
        -v freeradius-certs:/etc/raddb/certs/letsencrypt \
        -v /etc/tollgate/secrets.env:/etc/tollgate/secrets.env:ro

    echo
    echo ">>> deploy complete. Status:"
    ;&

  status)
    docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" | grep -E "tollgate|freeradius" || true
    echo
    echo "--- health checks ---"
    curl -sf http://127.0.0.1:8091/healthz && echo " (daemon)"
    curl -sf -o /dev/null -w "ocpi HTTP %{http_code}\n" http://127.0.0.1:8093/
    curl -sf -o /dev/null -w "csms HTTP %{http_code}\n" http://127.0.0.1:8887/ 2>&1 | head -1
    curl -sf -o /dev/null -w "webssh HTTP %{http_code}\n" http://127.0.0.1:8092/
    radtest cashuBfake anything 127.0.0.1 0 tollgate 2>&1 | grep "Reply-Message" | head -1
    ;;

  *)
    echo "Usage: $0 [deploy|status]"
    echo ""
    echo "Environment variables:"
    echo "  IMAGE_TAG     image tag to deploy (default: latest)"
    echo "  IMAGE_PREFIX  registry/org prefix (default: ghcr.io/amperstrand)"
    echo ""
    echo "Examples:"
    echo "  $0                                   # deploy latest from GHCR"
    echo "  IMAGE_TAG=abc1234 $0                 # deploy specific git SHA"
    echo "  IMAGE_PREFIX= $0                     # use local :test images (dev)"
    exit 1
    ;;
esac
