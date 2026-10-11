# CPA-Manager-Plus quota integration

This patch targets CPA-Manager-Plus v1.14.5, commit `dbf61ddf438f399558722df3467355c42af9005d` (MIT, Copyright 2026 Seakee).

It connects the independent `devin-openai` account provider to the plugin quota endpoint, account list/detail windows, and quota snapshots. Daily and weekly seat quota remains visible without a reset timestamp. Seat quota does not establish AI Gateway authorization or invalidate an earlier Gateway authorization failure. Existing `devin` accounts retain their behavior.

The `Build CPAMP quota integration` manual workflow applies the patch to the pinned upstream source, publishes a Linux amd64 image to GHCR, and exports the same image as a checksummed workflow artifact for hosts without registry credentials. The `Export CPAMP image` workflow can also export an existing immutable digest. CI builds only; regression tests run locally. No database schema migration is included. Monitoring-page automatic quota refresh is outside this patch.

Local validation includes account-page refresh interaction, independent credential snapshots, quota parsing and Gateway health evidence, TypeScript checks, and the Go quotasnapshot package. Two overlapping frontend runs passed 710 and 516 cases respectively.

Deploy using the immutable image digest reported by the workflow. Back up the existing database consistently together with its encryption key and configuration before updating only the manager service. Keep the previous image for rollback.
