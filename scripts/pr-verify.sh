#!/usr/bin/env bash
# pr-verify.sh — the LOCAL CI gate for tollgate-auth.
# Fleet pattern: Amperstrand/.github workflow-templates/local-ci-gate.md
# (reference implementation: tollgate-module-basic-go scripts/pr-verify.sh).
#
# Legs:
#   hygiene      conflict-marker grep over the tracked tree
#   go-fmt       gofmt -l over the module (empty output = pass)
#   go-vet       go vet ./...
#   build        go build ./...
#   unit-tests   go test ./... (Makefile `test`)
#   race-tests   go test -race ./... (Makefile `test-race`)
#
# NOT covered on purpose (say so, never imply otherwise): the Playwright e2e
# battery (needs the docker compose stack), test-radius-local (needs a local
# FreeRADIUS), and every deploy/firmware-visible behaviour.
#
# Usage:
#   scripts/pr-verify.sh 12                 # PR mode: verify + comment
#   scripts/pr-verify.sh 12 --no-post       # verify, print table only
#   scripts/pr-verify.sh origin/main        # ref mode: verify + print
#   scripts/pr-verify.sh 12 --only unit-tests,race-tests
#
# No secrets are read or embedded; PR mode uses the invoking account's gh
# auth for view/comment only. PR mode pins the head SHA at start-of-run.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LEGS=(hygiene go-fmt go-vet build unit-tests race-tests)

usage() {
    echo "usage: $0 <PR-number | git-ref> [--no-post] [--only LEG[,LEG...]] [--remove-logs]" >&2
    echo "legs: ${LEGS[*]}" >&2
    exit 2
}
die() { echo "pr-verify: $*" >&2; exit 2; }

TARGET=""; NO_POST=0; REMOVE_LOGS=0; ONLY=""
while [ $# -gt 0 ]; do
    case "$1" in
        --no-post) NO_POST=1 ;;
        --remove-logs) REMOVE_LOGS=1 ;;
        --only) [ $# -ge 2 ] || die "--only needs a comma-separated leg list"; ONLY="$2"; shift ;;
        --only=*) ONLY="${1#--only=}" ;;
        -h|--help) usage ;;
        -*) die "unknown option: $1" ;;
        *) [ -z "$TARGET" ] || die "one target only (got '$TARGET' and '$1')"; TARGET="$1" ;;
    esac
    shift
done
[ -n "$TARGET" ] || usage

if [ -n "$ONLY" ]; then
    IFS=',' read -r -a wanted <<<"$ONLY"
    for w in "${wanted[@]}"; do
        ok=0; for l in "${LEGS[@]}"; do [ "$w" = "$l" ] && ok=1; done
        [ "$ok" = 1 ] || die "unknown leg '$w' (legs: ${LEGS[*]})"
    done
else
    wanted=("${LEGS[@]}")
fi
want_leg() { local L; for L in "${wanted[@]}"; do [ "$L" = "$1" ] && return 0; done; return 1; }

for c in git go; do command -v "$c" >/dev/null 2>&1 || die "$c is required but not on PATH"; done
# go.mod requires go 1.25; pin the verifying toolchain so leg runs are
# reproducible regardless of the host's Go (resolution of minor-only
# toolchains can fail behind the bench proxy).
export GOTOOLCHAIN=go1.25.0

SLUG="$(git -C "$ROOT" remote get-url origin 2>/dev/null | sed -E 's#.*github\.com[:/]##; s#\.git$##')"
[ -n "$SLUG" ] || die "cannot derive the GitHub slug from the origin remote"

PR_NUMBER=""; PR_TITLE=""; PR_STATE=""; HEAD_REF=""; BASE_SHA=""
if [[ "$TARGET" =~ ^[0-9]+$ ]]; then
    command -v gh >/dev/null 2>&1 || die "PR mode needs gh (ref: $SLUG)"
    PR_NUMBER="$TARGET"
    meta="$(gh pr view "$PR_NUMBER" -R "$SLUG" --json title,state,headRefName,headRefOid --jq='[.title,.state,.headRefName,.headRefOid]|@tsv' 2>/dev/null)" \
        || die "gh pr view $PR_NUMBER failed"
    IFS=$'\t' read -r PR_TITLE PR_STATE HEAD_REF HEAD_OID <<<"$meta"
    [ "$PR_STATE" = "OPEN" ] || die "PR #$PR_NUMBER is $PR_STATE — verify open PRs"
    git -C "$ROOT" fetch --quiet origin main 2>/dev/null || true
    BASE_SHA="$(git -C "$ROOT" merge-base "$HEAD_OID" origin/main 2>/dev/null || echo '?')"
    echo "== PR #$PR_NUMBER: $PR_TITLE"
    echo "== head $HEAD_REF @ ${HEAD_OID:0:10} (base ${BASE_SHA:0:10})"
else
    HEAD_REF="$TARGET"
    if HEAD_OID="$(git -C "$ROOT" rev-parse --verify --quiet "$TARGET^{commit}")"; then
        echo "== ref $TARGET @ ${HEAD_OID:0:10} (already local)"
    else
        git -C "$ROOT" fetch --quiet origin "$TARGET" || die "cannot fetch '$TARGET' from origin"
        HEAD_OID="$(git -C "$ROOT" rev-parse --verify FETCH_HEAD^{commit})" || die "FETCH_HEAD did not resolve to a commit"
    fi
    BASE_SHA="$(git -C "$ROOT" rev-parse origin/main 2>/dev/null || echo "?")"
    echo "== ref $TARGET @ ${HEAD_OID:0:10}"
fi

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
LOGDIR="/tmp/tollgateauth-pr-verify/${TARGET//-/_}-$STAMP"
mkdir -p "$LOGDIR"
WT="$LOGDIR/worktree"
cleanup() {
    git -C "$ROOT" worktree remove --force "$WT" >/dev/null 2>&1 || true
    git -C "$ROOT" worktree prune >/dev/null 2>&1 || true
    if [ "$REMOVE_LOGS" = 1 ]; then rm -rf "$LOGDIR"; else
        rm -rf "$WT" 2>/dev/null || true
        echo "== logs kept in $LOGDIR"
    fi
}
trap cleanup EXIT

# PR mode: fork PRs carry a branch the base repo does not have — pull the PR
# head ref instead (works for same-repo branches too). Issue #23.
if [ -n "$PR_NUMBER" ]; then
    git -C "$ROOT" fetch --quiet origin "refs/pull/$PR_NUMBER/head" 2>/dev/null || true
else
    git -C "$ROOT" fetch --quiet origin "$HEAD_REF" 2>/dev/null || true
fi
git -C "$ROOT" rev-parse --verify --quiet "${HEAD_OID}^{commit}" >/dev/null \
    || die "head commit $HEAD_OID is not local after fetch — rerun"
git -C "$ROOT" worktree add --quiet --detach "$WT" "$HEAD_OID" || die "worktree add failed for $HEAD_OID"
echo "== worktree: $WT"

# Hash the copy that is executing this run, not the worktree copy under test
# — the stamp must attribute results to the verifier that ran them (issue #23).
SCRIPT_ID="$(sha256sum "$ROOT/scripts/pr-verify.sh" 2>/dev/null | cut -c1-12)"
HOST="$(hostname -s 2>/dev/null || echo unknown)"

leg_names=() leg_status=() leg_detail=() leg_secs=()
FAILED_LEGS=()
record() {
    leg_names+=("$1"); leg_status+=("$2"); leg_detail+=("$3"); leg_secs+=("$4")
    [ "$2" = "FAIL" ] && FAILED_LEGS+=("$1")
}

cd "$WT" || die "cannot cd into worktree"

run_hygiene() {
    echo "== leg: hygiene"
    local t=$SECONDS log="$LOGDIR/hygiene.log"
    if git grep -nE '^(<{7} |>{7} )' -- . >"$log" 2>&1; then
        record hygiene FAIL "committed conflict markers — see hygiene.log" $((SECONDS - t))
    else
        record hygiene PASS "no conflict markers in the tracked tree" $((SECONDS - t))
    fi
}

run_go_fmt() {
    echo "== leg: go-fmt"
    local t=$SECONDS log="$LOGDIR/go-fmt.log"
    local unformatted rc goroot_gofmt
    goroot_gofmt="$(go env GOROOT)/bin/gofmt"   # resolved toolchain's gofmt (go.mod pins 1.25)
    # gofmt -l prints nothing on stdout for an unparseable file — the exit
    # status is the only failure signal (issue #23).
    unformatted="$("$goroot_gofmt" -l . 2>"$log")"; rc=$?
    if [ "$rc" -ne 0 ]; then
        record go-fmt FAIL "gofmt exited $rc (unparseable input?) — see go-fmt.log" $((SECONDS - t))
    elif [ -n "$unformatted" ]; then
        echo "$unformatted" >>"$log"
        record go-fmt FAIL "unformatted files — see go-fmt.log" $((SECONDS - t))
    else
        record go-fmt PASS "gofmt -l clean" $((SECONDS - t))
    fi
}

run_go_vet() {
    echo "== leg: go-vet"
    local t=$SECONDS log="$LOGDIR/go-vet.log"
    if go vet ./... >"$log" 2>&1; then
        record go-vet PASS "go vet ./..." $((SECONDS - t))
    else
        record go-vet FAIL "see go-vet.log" $((SECONDS - t))
    fi
}

run_build() {
    echo "== leg: build"
    local t=$SECONDS log="$LOGDIR/build.log"
    if go build ./... >"$log" 2>&1; then
        record build PASS "go build ./..." $((SECONDS - t))
    else
        record build FAIL "see build.log" $((SECONDS - t))
    fi
}

run_unit_tests() {
    echo "== leg: unit-tests"
    local t=$SECONDS log="$LOGDIR/unit-tests.log"
    if go test ./... >"$log" 2>&1; then
        record unit-tests PASS "go test ./... ($(grep -c '^ok ' "$log") packages ok)" $((SECONDS - t))
    else
        record unit-tests FAIL "see unit-tests.log" $((SECONDS - t))
    fi
}

run_race_tests() {
    echo "== leg: race-tests"
    local t=$SECONDS log="$LOGDIR/race-tests.log"
    if go test -race ./... >"$log" 2>&1; then
        record race-tests PASS "go test -race ./..." $((SECONDS - t))
    else
        record race-tests FAIL "see race-tests.log" $((SECONDS - t))
    fi
}

STARTED=$SECONDS
want_leg hygiene    && run_hygiene
want_leg go-fmt     && run_go_fmt
want_leg go-vet     && run_go_vet
want_leg build      && run_build
want_leg unit-tests && run_unit_tests
want_leg race-tests && run_race_tests
WALL=$((SECONDS - STARTED))

green=0; red=0
for st in "${leg_status[@]}"; do case "$st" in PASS) green=$((green+1)) ;; FAIL) red=$((red+1)) ;; esac; done
VERDICT="$green legs green"
[ "$red" = 0 ] || VERDICT="$red leg(s) RED — $green green"

echo
echo "=================================================="
printf '%-14s %-5s %-8s %s\n' "LEG" "RES" "SECS" "DETAIL"
printf '%-14s %-5s %-8s %s\n' "---" "---" "----" "------"
for i in "${!leg_names[@]}"; do
    printf '%-14s %-5s %-8s %s\n' "${leg_names[$i]}" "${leg_status[$i]}" "${leg_secs[$i]}" "${leg_detail[$i]}"
done
echo "=================================================="
echo "VERDICT: $VERDICT (${WALL}s)"

compose_comment() {
    local body="$LOGDIR/comment.md"
    {
        if [ -n "$PR_NUMBER" ]; then
            echo "### Local CI gate — PR #${PR_NUMBER}: ${PR_TITLE}"
            echo
        else
            echo "### Local CI gate — ref \`${HEAD_REF}\`"
            echo
        fi
        echo "Run of \`scripts/pr-verify.sh\` (fleet pattern: Amperstrand/.github \`workflow-templates/local-ci-gate.md\` — GitHub Actions is org-blocked, so this comment is the offline verification evidence, produced on the pinned head SHA)."
        echo
        echo "- **Head:** \`${HEAD_REF}\` @ \`${HEAD_OID:0:10}\`"
        [ -n "$PR_NUMBER" ] && echo "- **Base:** \`main\` @ \`${BASE_SHA:0:10}\`"
        echo "- **Gate:** pr-verify \`$SCRIPT_ID\` on \`${HOST}\`, started $(date -u '+%Y-%m-%d %H:%M UTC'), wall ${WALL}s"
        echo "- **Logs:** \`${LOGDIR}\` (on the verifying host)"
        echo
        echo "| Leg | Result | Detail |"
        echo "| --- | --- | --- |"
        for i in "${!leg_names[@]}"; do
            echo "| ${leg_names[$i]} | **${leg_status[$i]}** | ${leg_detail[$i]} (${leg_secs[$i]}s) |"
        done
        echo
        echo "**Verdict: ${VERDICT}.**"
        echo
        if [ "${#wanted[@]}" != "${#LEGS[@]}" ]; then
            echo "> Note: narrowed run (--only) — legs not listed here did not run."
            echo
        fi
        echo "<details><summary>What this gate does <b>not</b> run (no vacuous green)</summary>"
        echo
        echo "- the Playwright e2e battery (\`npm run test:e2e\` — needs the docker compose stack)"
        echo "- \`make test-radius-local\` (needs a local FreeRADIUS)"
        echo "- deploy targets and anything infrastructure-visible"
        echo
        echo "</details>"
    } >"$body"
    echo "$body"
}

if [ -n "$PR_NUMBER" ] && [ "$NO_POST" = 0 ]; then
    BODY="$(compose_comment)"
    if URL="$(gh pr comment "$PR_NUMBER" -R "$SLUG" --body-file "$BODY")"; then
        echo "== posted: $URL"
    else
        echo "== FAILED to post comment (body kept at $BODY)" >&2
        red=$((red + 1))
    fi
elif [ -n "$PR_NUMBER" ]; then
    echo "== --no-post: comment body kept at $(compose_comment)"
fi

[ "$red" = 0 ] && exit 0 || exit 1
