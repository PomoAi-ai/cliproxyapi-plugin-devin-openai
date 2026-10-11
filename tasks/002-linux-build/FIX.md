# FIX -- Linux plugin build

## Status: verifying
## Task: 002
## Related: N/A
## Baseline Commit: 89b90a2

## Problem
The source-only repository has no Linux artifact for installation.

## Root Cause
The initial implementation was verified locally on macOS; the Linux host needs an amd64 ELF shared library built against a compatible libc.

## Fix Plan
- [x] Add a manually dispatched GitHub Actions build using Go 1.25.1 and Debian 12.
- [x] Upload the shared library and SHA256 checksum as a workflow artifact.
- [x] Keep all tests out of CI; execute deployment checks manually.

## Verification
- [x] Review workflow commands: build and checksum only, no tests or deployment credentials.
- [ ] GitHub Actions build succeeds and artifact checksum matches.
