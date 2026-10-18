# AGENTS.md — tollgate-auth

Instructions for AI coding agents working in this repository.

## Purpose

**Ecash for infrastructure access**: pay-per-minute SSH (:2222), RADIUS WiFi
(1812), and OCPI EV charging with Cashu tokens — part of the OpenTollGate
concept. Three Go components (ssh/radius/ocpi) plus a settle daemon, a RADIUS
shim, and a Docker compose stack for e2e. See [README.md](README.md).

## Commands

```sh
make build           # go build the components
make test            # go test ./... (safe, local, deterministic)
make test-race       # race-detector pass
make test-unit       # verbose unit tests
npm run test:e2e     # Playwright — needs `make docker-up` stack first
make docker-up       # compose stack for e2e
```

## Local CI gate

GitHub Actions is org-blocked; the per-PR gate is
[`scripts/pr-verify.sh`](scripts/pr-verify.sh) (fleet pattern:
Amperstrand/.github `workflow-templates/local-ci-gate.md`). From a review:

```sh
scripts/pr-verify.sh <PR-number>    # worktree checkout, legs, PR-comment stamp
scripts/pr-verify.sh origin/main    # ref mode, table only
```

Legs: hygiene / go-fmt / go-vet / build / unit-tests / race-tests. Not
covered: Playwright e2e (needs the compose stack), `test-radius-local`
(needs FreeRADIUS), deploy targets.

## Bench touchpoints

None in this repo. The TollGate router/PRTA benches (`physical-router-test-automation`,
`tollgate-lab`) are separate repos; nothing here requires labgrid places.

## Conventions

- Semantic commits (`feat:`, `fix:`, `test:`, `chore:`, `docs:`).
- No secrets, no real wallet keys in tree; e2e uses regtest/fake mints.
- PRs carry a `### Local CI gate` stamp before merge.
- Accounting/settlement changes are state-machine changes: unit green ≠
  fund-safe — say so in the PR when touching settle paths.
