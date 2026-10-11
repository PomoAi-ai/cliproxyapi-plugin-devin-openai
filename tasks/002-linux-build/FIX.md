# FIX -- Linux plugin build

## Status: done
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
- [x] GitHub Actions build succeeds and artifact checksum matches.

## Results
- Build run: https://github.com/PomoAi-ai/cliproxyapi-plugin-devin-openai/actions/runs/38135901447
- Source: 08c3c810d9d5f41c819694fe5b230df4050f58fc.
- SHA256: b44ef5c382b05dd5534da2f429f4288ce8ed173ca60fce13107f7d54efc8c182.
- Manual Linux amd64 CPA v8.0.23 loading check confirmed registered, effective_enabled and supports_oauth.
- No CI tests, real model calls, or credentials in workflow/artifact.
