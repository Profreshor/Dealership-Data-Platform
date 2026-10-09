---
name: ddp-upgrade
description: Upgrade a dealership's DDP repository to a tagged template revision while preserving the dealership's own facts, code and applied migration history.
---

Use this skill when bringing a dealership's checkout forward to a tagged template revision.

1. Read [discovery](../../docs/discovery.md), [deployment](../../docs/deployment.md), [Architecture §18](../../ARCHITECTURE.md#18-deployment-and-release), and the current registry before selecting the target tag.
2. Before merging, save the old registry as a baseline (for example, `/tmp/previous-ddp.yaml`). Check `git merge-base HEAD <template-tag>` first. For a normal upgrade with common ancestry, merge the reviewed template tag into a dedicated PR branch. A freshly initialized project can have unrelated histories; use `git merge --allow-unrelated-histories <template-tag>` only after that check proves there is no common ancestor, or stop for explicit manual setup. Keep the dealership's registry facts, source, migrations, credentials, and deployment state under their existing owners.
3. Resolve conflicts by applying the decisions and architecture first. Preserve applied migration IDs and checksums; inspect migration status before proposing any forward migration.
4. Run `ddp config diff /tmp/previous-ddp.yaml --json`, `ddp validate`, `git diff --check`, and the full `ddp check --json` from the upgraded checkout. Use the old registry as the diff baseline; inspect the pristine target template tag separately when reviewing template changes. Review generated and deployment files for accidental loss of the dealership's facts.
5. Hand the merge to a human reviewer. Release the upgraded image through the digest-pinned deployment procedure only after review and required checks pass.

Do not describe a Git merge as deployed, and do not invent an installer or migration command that the checkout does not expose.
