---
name: ddp-release
description: Prepare and verify a reviewed digest-pinned DDP production release through the documented updater path.
---

Use this skill for a production release or release investigation. Read [deployment](../../docs/deployment.md), [commands](../../docs/commands.md#release-preflight-and-outcomes), and [Architecture §18](../../ARCHITECTURE.md#18-deployment-and-release).

1. Confirm a human-reviewed merge and required CI success before treating the image as releasable. The release image must identify the reviewed Git revision and immutable GHCR digest.
2. Let the documented root-owned updater own preflight and the deployment ledger. On the prepared host, run `sudo bin/update.sh`; install or enable `deploy/ddp-update.service` and `deploy/ddp-update.timer` as documented in [deployment](../../docs/deployment.md#enable-automatic-releases), then inspect `sudo journalctl -u ddp-update.service -n 100 --no-pager` and `ddp deploy status --json`.
3. The updater resolves the approved tag to an exact digest, runs the required backup/migration/model phases, restarts Compose, waits for readiness, and records the outcome. Do not issue a second `deploy record` for the same run or duplicate the updater's ledger entry.
4. For startup failure, use the recorded previous digest and the updater rollback path. Rollback changes the image only; it does not reverse migrations or retag `:stable`.
5. Report readiness, deployment outcome, digest, revision, backup/migration evidence, and unresolved gates. Cloudflare routes and the dealership's Access policy with MFA are configured outside the updater; do not report them as verified unless they were checked.
