# test-artifacts/

Run evidence for tollgate-auth, following the lightning-playground pattern.

- The directory is **gitignored by default** — logs, screenshots, binaries
  and scratch output land here and stay local.
- Evidence worth persisting (a bug reproduction, an e2e run backing an
  issue) is added **deliberately**: `git add -f test-artifacts/<run-id>/`,
  then reviewed like any other diff. Never force-add binaries.
- CI (`.github/workflows/unit-tests.yml`) fails on newly added binary files
  at the repo root — the class that produced the 11 MB tracked ELF removed
  in the worktree-hygiene cleanup (issue #20).
