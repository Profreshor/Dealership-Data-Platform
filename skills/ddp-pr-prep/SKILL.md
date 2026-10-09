---
name: ddp-pr-prep
description: Prepare a reviewed DDP change by checking its registry, code, links, and full required validation before human review.
---

Use this skill before requesting review of a DDP change.

1. Read [commands](../../docs/commands.md), [registry](../../docs/registry.md), and the relevant design or operational document. Update the registry and linked documentation when the change alters declared facts or relationships.
2. Inspect the diff and run `git diff --check`.
3. Run `ddp check --changed --json` while iterating. Before review, run the full `ddp check --json`; it includes the language, smoke, image, and secret gates required by the repository.
4. Confirm every referenced local link resolves and every CLI example appears in `ddp help --json` or the registered command source. Report any open acceptance gate.
5. Present the outcome, validation, risks, and remaining work for a human-reviewed branch and PR. Do not merge or publish without human review.
