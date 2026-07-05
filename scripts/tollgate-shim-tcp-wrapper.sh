#!/bin/sh
# tollgate-shim-tcp-wrapper.sh — invoked by FreeRADIUS cashu-exec module.
#
# FreeRADIUS's rlm_exec runs programs with a near-empty environment
# (`PWD=/` only — no PATH, no TOLLGATE_*, no inheritance from
# /etc/default/freeradius). This wrapper sets TOLLGATE_SOCKET explicitly
# so the shim dials the daemon over TCP (the only viable mode when the
# daemon runs in a container and the shim runs in the FreeRADIUS container
# — Unix-socket sharing across containers is awkward).
#
# Why a wrapper instead of just setting the env var globally?
# rlm_exec explicitly sanitizes the environment to prevent exactly that.
# The wrapper is the documented escape hatch.
#
# Why not modify the shim to read /etc/tollgate/socket.conf?
# Possible, but adds a runtime file dependency. The wrapper keeps the
# shim source clean and the configuration in one obvious place.
#
# Update TOLLGATE_SOCKET below if the daemon's TCP port changes.
# Default: tcp://127.0.0.1:18094 (the canonical port per deploy-containers.sh).
set -eu

export TOLLGATE_SOCKET="${TOLLGATE_SOCKET:-tcp://127.0.0.1:18094}"

exec /usr/local/bin/tollgate-shim "$@"
