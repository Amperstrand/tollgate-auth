#!/usr/bin/env bash
# pr-verify-test.sh — scenario tests for scripts/pr-verify.sh (issue #23).
#
#   T1  PR-mode head materialization: a clean reviewer clone (no local fork
#       branch, no PR-head objects) must still verify via refs/pull/<N>/head.
#   T2  go-fmt leg: a syntactically invalid .go file must FAIL the leg even
#       though `gofmt -l` prints nothing on stdout (exit status must count).
#   T3  gate stamp: the "Gate: pr-verify <id>" line in the PR comment must
#       hash the EXECUTING script copy (ROOT), not the worktree copy under
#       test (WT).
#
# Fixtures: a bare origin, a reviewer clone whose origin URL is rewritten via
# url.insteadOf, a mock gh on PATH answering `gh pr view`, and a separate
# author clone that pushes the PR-head refs so the reviewer clone never holds
# those objects.
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/pr-verify.sh"
WORK="$(mktemp -d /tmp/pr-verify-test.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
FAILURES=0
pass() { echo "PASS: $*"; }
fail() { echo "FAIL: $*"; FAILURES=$((FAILURES + 1)); }

ORIGIN="$WORK/origin.git"
CLONE="$WORK/reviewer"
AUTHOR="$WORK/author"
git init --bare -q "$ORIGIN"
git -C "$ORIGIN" symbolic-ref HEAD refs/heads/main
git clone -q "$ORIGIN" "$AUTHOR"
git -C "$AUTHOR" symbolic-ref HEAD refs/heads/main
git -C "$AUTHOR" config user.email test@fixture && git -C "$AUTHOR" config user.name test

mkdir -p "$AUTHOR/scripts"
cp "$SCRIPT" "$AUTHOR/scripts/pr-verify.sh"
echo "# fixture" >"$AUTHOR/README.md"
git -C "$AUTHOR" add -A && git -C "$AUTHOR" commit -qm main
git -C "$AUTHOR" push -q origin main

# PR heads: authored and pushed ONLY from the author clone.
git -C "$AUTHOR" checkout -q -b feature-branch
echo "feature" >"$AUTHOR/feature.txt"
git -C "$AUTHOR" add -A && git -C "$AUTHOR" commit -qm feature
PR12_OID="$(git -C "$AUTHOR" rev-parse HEAD)"
git -C "$AUTHOR" push -q origin "HEAD:refs/pull/12/head"

echo "# stamp divergence" >>"$AUTHOR/scripts/pr-verify.sh"
git -C "$AUTHOR" commit -qam "diverge the gate script on the PR head"
PR13_OID="$(git -C "$AUTHOR" rev-parse HEAD)"
git -C "$AUTHOR" push -q origin "HEAD:refs/pull/13/head"
rm -rf "$AUTHOR"

# Reviewer clone: clean, main only — holds neither branch nor PR-head objects.
# (Local clone baggage can carry PR-head objects unreferenced; GitHub clones do
# not — repack+ prune emulates that.)
git clone -q "$ORIGIN" "$CLONE"
git -C "$CLONE" repack -adq 2>/dev/null; git -C "$CLONE" prune
git -C "$CLONE" config user.email test@fixture && git -C "$CLONE" config user.name test
git -C "$CLONE" remote set-url origin "https://github.com/fixture"
git -C "$CLONE" config "url.$ORIGIN.insteadOf" "https://github.com/fixture"

# T2 fixture: a local branch with a syntactically invalid Go file.
git -C "$CLONE" checkout -q -b badfmt
printf 'package fixture\nfunc ( {\n' >"$CLONE/bad.go"
git -C "$CLONE" add -A && git -C "$CLONE" commit -qm badfmt
git -C "$CLONE" checkout -q main

mkdir -p "$WORK/bin"
cat >"$WORK/bin/gh" <<'EOF'
#!/usr/bin/env bash
for a in "$@"; do
    case "$a" in
        12) printf 'PR twelve\tOPEN\tfeature-branch\t%s\n' "$PR12_OID"; exit 0 ;;
        13) printf 'PR thirteen\tOPEN\tfeature-branch\t%s\n' "$PR13_OID"; exit 0 ;;
    esac
done
exit 1
EOF
chmod +x "$WORK/bin/gh"
export PR12_OID PR13_OID

# --- T1 ---------------------------------------------------------------
OUT1="$(PATH="$WORK/bin:$PATH" "$CLONE/scripts/pr-verify.sh" 12 --no-post --only hygiene --remove-logs 2>&1)"; RC1=$?
if [ $RC1 -eq 0 ] && grep -q "VERDICT: 1 legs green" <<<"$OUT1"; then
    pass "T1 PR-mode verify on a clean clone (refs/pull/12/head)"
else
    fail "T1 PR-mode verify on a clean clone (rc=$RC1): $(grep -E 'not local after fetch|VERDICT|pr-verify:' <<<"$OUT1" | head -2)"
fi

# --- T2 ---------------------------------------------------------------
OUT2="$(PATH="$WORK/bin:$PATH" "$CLONE/scripts/pr-verify.sh" badfmt --only go-fmt --remove-logs 2>&1)"; RC2=$?
if [ $RC2 -ne 0 ] && grep -qE '^go-fmt +FAIL' <<<"$OUT2"; then
    pass "T2 go-fmt fails on a syntactically invalid .go file"
else
    fail "T2 go-fmt must FAIL on invalid Go (rc=$RC2): $(grep -E 'go-fmt' <<<"$OUT2" | head -2)"
fi

# --- T3 ---------------------------------------------------------------
OUT3="$(PATH="$WORK/bin:$PATH" "$CLONE/scripts/pr-verify.sh" 13 --no-post --only hygiene 2>&1)"; RC3=$?
BODY="$(grep -oE '/tmp/tollgateauth-pr-verify/[^[:space:]]+/comment\.md' <<<"$OUT3" | head -1)"
ROOT_ID="$(sha256sum "$CLONE/scripts/pr-verify.sh" | cut -c1-12)"
if [ $RC3 -eq 0 ] && [ -n "$BODY" ] && grep -qF "pr-verify \`$ROOT_ID\`" "$BODY" 2>/dev/null; then
    pass "T3 stamp hashes the executing (ROOT) script copy"
else
    STAMPED="$(grep -oE 'pr-verify `[^`]+`' "$BODY" 2>/dev/null || echo "no comment body (rc=$RC3)")"
    fail "T3 stamp must hash ROOT's copy ($ROOT_ID): $STAMPED"
fi
if [ -n "$BODY" ] && [ -d "$(dirname "$BODY")" ]; then rm -rf "$(dirname "$BODY")"; fi

echo
[ "$FAILURES" = 0 ] && { echo "pr-verify-test: all scenarios green"; exit 0; } \
    || { echo "pr-verify-test: $FAILURES scenario(s) failing"; exit 1; }
