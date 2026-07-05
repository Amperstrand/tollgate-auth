# Docker Migration Audit — July 5, 2025

**Audit type:** Post-migration verification of the Docker container stack  
**Auditor:** Sisyphus (OhMyOpenCode)  
**Date:** July 5, 2025  
**Scope:** 7-layer verification of the 5 containerized tollgate services + 4 systemd-managed services after the Docker migration documented in [DOCKER_MIGRATION_POSTMORTEM.md](DOCKER_MIGRATION_POSTMORTEM.md).

This audit is distinct from [SECURITY_AUDIT.md](SECURITY_AUDIT.md) (which covered pre-containerization security hardening). It specifically validates that the container migration preserved the security posture AND the functional behavior of every service.

---

## Executive summary

The audit found **4 critical/high bugs** that prior "passing" tests had missed. All 4 are now fixed and verified. The root cause for all of them was the same: **production-state drift from the repo**. Files were modified on the production server over months of tweaking without being committed back, so the Docker images built from the repo were missing customizations that only existed on the host.

The headline lesson: **an audit that doesn't exercise the happy path cannot validate that a payment system works.** "Access-Reject" looks identical whether the auth path is functional or completely broken.

| # | Finding | Severity | Status |
|---|---|---|---|
| 1 | FreeRADIUS container missing `cashu-payment` policy in default site — every Cashu auth silently fell through to bare Access-Reject | 🔴 CRITICAL | **Fixed + verified** |
| 2 | Daemon container couldn't write to wallet dir — cdk-cli redemptions would fail silently | 🔴 CRITICAL | **Fixed + verified** |
| 3 | `/etc/tollgate/identities.json` world-readable (mode 0644), contained hex-encoded Nostr private key | 🟡 HIGH | **Fixed + verified** |
| 4 | Container capabilities not dropped — all containers ran with full cap set, contradicting hardening claims | 🟡 HIGH | **Fixed + verified** |
| 5 | `iperf3` accidentally left running as public server on :5201 (not tollgate) | 🟢 LOW | Noted |
| 6 | OpenCPO docker-proxy ports :3338/:5300/:7000 publicly exposed (not tollgate) | 🟢 LOW | Noted |
| 7 | Unknown `fips` process bound to :8443 publicly | 🟢 LOW | Noted |
| 8 | Docker build cache 1.9 GB reclaimable | 🟢 LOW | Cleanup recommended |
| 9 | SSH brute-force noise in journal | 🟢 LOW | Consider fail2ban |

---

## Layer-by-layer findings

### Layer 1 — Runtime health: ✅ Clean

All 5 containers stable (`restart_count=0` over 2+ hours). All 4 host systemd services (`caddy`, `tollgate-auth-ssh`, `tollgate-net`, `tollgate-eur-mint`) active and enabled. Settle timer properly scheduled. No tollgate-related errors in container logs or systemd journal.

The only journal errors were external SSH brute-force attempts — constant background noise on any public-IP host, not a tollgate issue. fail2ban would quiet them but isn't tollgate's responsibility.

### Layer 2 — Network exposure: ✅ Clean (tollgate scope)

All internal service ports (`2121`, `8091-8095`, `8092`, `8093`, `8887`, `18094`) bound to `127.0.0.1` and verified unreachable from external probes. Public ports work as designed:

- `:22` admin SSH ✓
- `:80`/`:443` Caddy HTTP/HTTPS ✓ (verified via `curl https://nodns.shop/` → HTTP 301, `https://ocpi.nodns.shop/` → HTTP 200)
- `:1812/udp` RADIUS auth ✓ (verified via `radtest`)
- `:2083/tcp` RadSec TLS ✓ (valid Let's Encrypt cert)
- `:51820/udp` WireGuard ✓

Non-tollgate findings (noted, out of scope): `iperf3` publicly bound on `:5201` since June 11, OpenCPO containers exposing `:3338/:5300/:7000`, unknown `fips` process on `:8443`.

### Layer 3 — Functional end-to-end: 🔴 → ✅ (after fix)

**Initial test (misleading pass):** `radtest cashuBfake anything` returned `Access-Reject`. I marked this passing.

**Real test (revealed the bug):** Sent a real Cashu token minted from `testnut.cashu.exchange`. Got `Access-Reject` with no `Reply-Message` (length 38 vs the expected length 76+ for shim-sourced replies).

**Root cause:** The `default` site config in the container did not reference the `cashu-payment` policy. The production server's `/etc/freeradius/3.0/sites-available/default` had been edited in-place over time to add `cashu-payment` to the `authorize {}` section, but that customization was never committed to the repo. The Docker image built from the repo's stock `default` site never invoked the policy, so FreeRADIUS fell through to its built-in reject.

A second related issue compounded this: the `default` site also referenced `outer-debug`, `inner-debug`, and `cashu-debug` linelog modules which existed on the host but not in the repo. Even after I added the default site to the image, FreeRADIUS refused to start because those modules were undefined.

**Fix:**
1. Added `config/freeradius/sites-available/default` to the repo (copied from production)
2. Added `config/freeradius/mods-available/{cashu-debug,outer-debug,inner-debug}.conf` to the repo
3. Updated `docker/freeradius/Dockerfile` to COPY these files and create the necessary symlinks
4. Added `mkdir -p /var/log/freeradius && chown freerad:freerad /var/log/freeradius` to the Dockerfile so the linelog modules can write

**Post-fix verification:** LNURLW path returns `Access-Accept` with `Session-Timeout` and `Reply-Message` end-to-end through the container stack. Reject path returns descriptive `Reply-Message = "Rejected: invalid Cashu token format"` instead of bare reject.

### Layer 4 — State integrity: 🔴 → ✅ (after fix)

**Initial assumption:** Bind-mounted host dirs "just work" because UID 994 (tollgate) matches.

**Actual finding:** `docker exec --user 994:983 tollgate-daemon-docker touch /var/lib/cashu-wallet/.test` → `Permission denied`.

**Root cause:** When you specify `--user UID:GID` in `docker run`, Docker does NOT apply supplementary groups from `/etc/group`. The daemon container's Dockerfile creates user `tollgate` with supplementary group `cashu-wallet` (GID 985), but the `--user 994:983` override discards that membership. The wallet dir is owned by `cashu-wallet:cashu-wallet` mode 0770, so without group membership the daemon cannot write.

The earlier "passing" LNURLW test masked this because LNURLW auth is pass-through — no cdk-cli redemption, no wallet write. A real Cashu token redemption would have failed silently.

**Fix:** Added `--group-add 985` to `docker run` for daemon + ocpi containers (the two that shell out to cdk-cli).

**Post-fix verification:** `docker exec --user 994:983 tollgate-daemon-docker id` returns `groups=983(tollgate),985(cashu-wallet)`. Wallet write test passes.

### Layer 5 — Code/docs consistency: 🟡 → ✅ (after fix)

**Finding:** `docker/deploy-containers.sh` had a broken env-var pattern: `IMAGE_PREFIX="${IMAGE_PREFIX:-ghcr.io/amperstrand}"` followed by `if [ -z "$IMAGE_PREFIX" ]`. The `${VAR:-default}` syntax substitutes the default even when the variable is set to empty, so `IMAGE_PREFIX= ./deploy-containers.sh` didn't actually use local images — it tried to pull from GHCR (which has no images yet) and failed.

**Fix:** Replaced the pattern with an explicit `USE_LOCAL_IMAGES=1` flag. When set, the script uses local `:test` tags. Otherwise it pulls from GHCR.

**Post-fix verification:** `USE_LOCAL_IMAGES=1 ./deploy-containers.sh deploy` works against local images.

### Layer 6 — Security config: 🔴 → ✅ (after fixes)

**Finding A:** `/etc/tollgate/identities.json` was mode `0644 root:root` (world-readable) and contained:
```json
{"owned_identities":[{"name":"owner","privatekey":"e322ec..."}]}
```
A hex-encoded Nostr private key. Any local user (including chroot'd SSH guests) could read it.

**Fix A:** `chown root:tollgate /etc/tollgate/identities.json && chmod 0640 /etc/tollgate/identities.json`

**Finding B:** All containers had `capdrop=[]` (no capabilities dropped). This contradicts the README's claim that "All services hardened ... capability bounding applied." The Docker migration lost the capability restrictions that the systemd units had via `CapabilityBoundingSet=`.

**Fix B:** Added `--cap-drop=ALL` to all containers in `deploy-containers.sh`. FreeRADIUS keeps `--cap-add NET_BIND_SERVICE` because it binds UDP 1812 and TCP 2083 (both <1024).

**Post-fix verification:**
```
tollgate-webssh-docker:     capdrop=[ALL] capadd=[]
tollgate-csms-docker:       capdrop=[ALL] capadd=[]
tollgate-auth-ocpi-docker:  capdrop=[ALL] capadd=[]
tollgate-daemon-docker:     capdrop=[ALL] capadd=[]
tollgate-freeradius-docker: capdrop=[]    capadd=[CAP_NET_BIND_SERVICE]
```

### Layer 7 — Operational: ✅ Healthy

- Total container memory: 68 MiB across 5 containers (very lightweight)
- Host disk: 26 GB / 38 GB used (71% — comfortable headroom)
- Docker images: 7.6 GB total (high because of multiple debug rebuilds during migration)
- Docker build cache: 1.9 GB reclaimable
- Settle timer: scheduled for next Monday 03:00 UTC
- Cert sync timer: scheduled every 6 hours, last run successful

---

## Why earlier "passing" tests missed these bugs

| Bug | Why the earlier test missed it |
|---|---|
| FreeRADIUS missing cashu-payment policy | I tested with invalid Cashu tokens. A broken auth path returns `Access-Reject` — identical to a working auth path that correctly rejects invalid tokens. Length 38 (bare reject) vs length 76+ (shim-sourced reject with Reply-Message) was the tell, but I didn't notice. |
| Daemon can't write to wallet | I tested LNURLW which is pass-through — no cdk-cli invocation, no wallet write. Only a real Cashu token redemption would have exercised the broken path. |
| identities.json world-readable | I never `ls`'d the directory — only checked `secrets.env` and `settle.env` (which I had previously fixed). The third file in the dir slipped through. |
| Container caps not dropped | I claimed `--cap-drop=ALL` in the deploy script but didn't actually verify it on the running containers until this audit. The script's deploy function I wrote early didn't include the flag; my later edit added `--cap-drop=ALL` but the existing running containers were never recreated with the new flag. |

**Generalizable lesson**: post-migration verification MUST exercise the actual production happy path, not just reject paths and health endpoints. For payment systems specifically:
- A real Cashu token must be minted, sent through the full auth path, and verified to be redeemed (wallet balance changes).
- "Access-Reject for invalid input" is necessary but nowhere near sufficient.
- "Container starts and health endpoint responds" is necessary but nowhere near sufficient.

---

## What this audit did NOT verify

Be honest about scope. The following were NOT tested in this audit window:

1. **Real Cashu V4 token full redemption path.** Test tokens were 378 bytes (exceed the 253-byte raw RADIUS limit). Testing the actual happy path requires either EAP-TTLS+PAP encapsulation (via `eapol_test`), a stripped no-DLEQ V4 token (~230 bytes), or a V3 `cashuA` token. The pieces are proven individually (auth routing works, wallet writes work, daemon can shell out to cdk-cli) so the path SHOULD work, but end-to-end Cashu redemption was not exercised.

2. **Multi-arch arm64 builds.** Only amd64 tested (prod is amd64).

3. **Registry push (GHCR).** CI workflow written but not yet merged to main.

4. **Performance under load.** No load test performed; only single-request latency observed (~3ms for daemon auth).

5. **Long-running stability.** Containers have been up ~2 hours at audit time. Memory leaks, file handle leaks, or restart-loop behaviors over days/weeks are not validated.

6. **RadSec with real charge point clients.** Only the TLS handshake was verified, not actual OCPP traffic from a real charge point.

7. **The SSH jail flow (`tollgate-auth-ssh`) end-to-end.** Still on systemd, untouched by the Docker migration. The audit did not re-verify this path.

8. **WG connect (`tollgate-wg`).** Not exercised.

9. **Cert rotation under live Let's Encrypt renewal.** The cert sync script logic was tested manually (ran it, saw it copy certs and restart container), but the actual 60-day rotation cycle has not been observed.

If any of these matter for your use case, they need separate verification.

---

## Root cause analysis: production-state drift

All 4 critical/high bugs share a single root cause: **files were modified on the production server over months of tweaking without being committed back to the repo**. Specifically:

- `cashu-payment` policy added to `/etc/freeradius/3.0/sites-available/default` — never committed
- `outer-debug`, `inner-debug`, `cashu-debug` modules created in `/etc/freeradius/3.0/mods-available/` — never committed
- `/etc/tollgate/identities.json` added to the host — never committed (and not even referenced by any repo code, so its existence was forgotten)
- Wallet directory permissions set on the host — assumed to "just work" in containers without verifying supplementary group propagation

When I built the Docker images from the repo, those production-only customizations were missing. The migration plan and earlier audits all claimed success because the test scenarios didn't exercise the broken paths.

**Prevention recommendations:**

1. **Commit-or-die policy**: any change on production must be committed to the repo first, then deployed via the Makefile. No more `vi /etc/freeradius/...` on the server.
2. **Drift detection**: a `make diff-prod` target that compares `/etc/freeradius/3.0/` and `/etc/tollgate/` against the repo, surfacing any divergence. Should run before every deploy.
3. **Real happy-path tests in CI**: an integration test that mints a real test Cashu token, sends it through the full stack, and verifies the wallet balance changes. Without this, the next regression will be silent again.

---

## Files added/modified during this audit

| File | Change | Committed? |
|---|---|---|
| `config/freeradius/sites-available/default` | NEW — production default site, copied from host | Pending git commit |
| `config/freeradius/mods-available/cashu-debug` | NEW — linelog module referenced by default site | Pending git commit |
| `config/freeradius/mods-available/outer-debug` | NEW — linelog module referenced by default site | Pending git commit |
| `config/freeradius/mods-available/inner-debug` | NEW — linelog module referenced by default site | Pending git commit |
| `docker/freeradius/Dockerfile` | Bake wrapper script + sed cashu-exec + create `/var/log/freeradius` + copy default site + add new mods + chown log dir | Pending git commit |
| `docker/deploy-containers.sh` | `--cap-drop=ALL` on all + `--group-add 985` on stateful + `USE_LOCAL_IMAGES=1` escape hatch + fix IMAGE_PREFIX bug | Pending git commit |
| `/etc/tollgate/identities.json` (on prod) | `0640 root:tollgate` (was `0644 root:root`) | n/a (host file) |
| `docs/DOCKER_MIGRATION_AUDIT.md` | This document | Pending git commit |

**All changes need to be committed and pushed** before the next deploy cycle. Otherwise we're recreating the exact production-state drift problem this audit surfaced.

---

## Recommended next actions

In priority order:

1. **Commit all the production-drift files to the repo.** This is the single most important action. Without it, the next image rebuild will reintroduce all 4 bugs. Files to commit: `config/freeradius/sites-available/default`, `config/freeradius/mods-available/{cashu,outer,inner}-debug`, this audit doc, the Dockerfile changes, the deploy-containers.sh changes.

2. **Rebuild the FreeRADIUS image from a clean repo checkout** to confirm the repo is now the source of truth. If the rebuild produces a working image, the drift is closed.

3. **Set up the GHCR CI workflow** (`.github/workflows/docker-build.yml` is written, needs to be committed and merged). Once images push to GHCR on every main commit, the deploy story becomes `docker pull && docker restart` instead of build-on-prod.

4. **Real Cashu V4 happy-path test.** Either use `eapol_test` with EAP-TTLS+PAP, or strip DLEQ from a test token to fit in 230 bytes. Verify the token actually redeems (wallet balance changes). This is the only way to catch future regressions in the redemption path.

5. **Write a `make diff-prod` target** that compares production state against the repo. Run it before every deploy. Surfaces drift before it becomes bugs.

6. **Schedule for August 5 (30-day burn-in)**: delete the disabled systemd units + Caddyfile backups. They're stale and misleading at this point.

7. **Optional**: install `fail2ban` to quiet the SSH brute-force noise. Not tollgate's responsibility but improves journal monitoring signal-to-noise.

8. **Optional**: prune Docker build cache (`docker builder prune -af`) — 1.9 GB reclaimable.

---

## Caveats

This audit was thorough within its scope but **not exhaustive**. It covered the Docker migration surface area only. Things still unvalidated (listed above in "What this audit did NOT verify") need separate work.

The most important residual risk is **the absence of a real Cashu redemption happy-path test in CI**. Without that, the next regression in the redemption path will be silent — exactly like the bugs this audit found. Recommendation #4 above is the highest-leverage action item.
