# Discovery and initialization

Each dealership runs its own copy of the platform in its own repository. Before
creating that repository, complete discovery: record the dealership's business
questions, systems and access, users, requested outputs, ownership, retention and
recovery requirements in YAML. Keep credentials out of this document.

## Initialize locally

Build the CLI in the template checkout, then use a new destination directory:

```sh
make setup
make build
template_checkout="$PWD"
build/ddp init ../acme --template "$template_checkout" \
  --discovery /path/to/discovery.yaml --dry-run --json
build/ddp init ../acme --template "$template_checkout" \
  --discovery /path/to/discovery.yaml --json
```

`--template` selects the source checkout. Pass it explicitly, especially when
calling a CLI built in another directory: the default is the current working
directory, regardless of the binary's location. The checkout must have a commit
and no uncommitted tracked changes.
Initialization reads an archive of that commit and records it as
`ddp.template_revision`. Untracked files, `.env` files, build outputs and the
proving-ground deployment are excluded. `AGENTS.md` is copied verbatim. Canonical
`skills/` files and the two `../skills` discovery links are preserved; an unexpected
discovery link target is refused. `CLAUDE.md` points to `AGENTS.md`.

Before implementation, verify that the generated `ddp.template_revision` matches
`git -C "$template_checkout" rev-parse HEAD`. If the wrong checkout was selected,
initialize a new destination from the intended checkout; preserve the generated
revision as the record of the source actually copied.

The command writes local files only. It refuses an existing destination, including
a symlink. Dry-run reports the project and proposed file paths without creating
the directory. An interrupted write leaves the new directory available for
inspection; choose a new destination when retrying.

## Start the project's Git history

In the new project directory, run `git init -b main`. Fetch the source template's
history with `git fetch /path/to/template HEAD:refs/remotes/template/initial`, using
the checkout passed to initialization. Assign the generated registry's
`ddp.template_revision` to a shell variable named `template_revision` and verify that
`git cat-file -e "$template_revision^{commit}"` succeeds. This retains the provenance
needed by `ddp doctor`; fetching history does not merge template files into the
project.

Review `git status --short`, stage the generated source and commit it before
implementation. Keep populated environment files ignored. This initial commit
provides the baseline for `ddp check --changed`. Creating the dealership's private
GitHub repository and configuring its remote remain separate authorized steps.

## Discovery contract

Use the structure in [Architecture §21](../ARCHITECTURE.md#21-onboarding-discovery-document-to-running-system).
Every section and list must be explicit; use `[]` for unused lists. Unknown fields,
duplicate fields, YAML aliases and multiple documents are rejected. A minimal
example:

```yaml
client:
  name: Acme Dealership
  industry: Dealership
  timezone: America/Chicago
  locations: [Main Store]
  contacts:
    - {name: Pat Example, email: pat@example.test}
users:
  - {name: Pat Example, email: pat@example.test, role: owner}
systems:
  - name: dms
    kind: dms
    vendor: Example DMS vendor
    access: export
    docs_url: https://example.test/dms-docs
    credential_owner: dealership
    entities:
      - {name: customers, deletion_behavior: reconcile}
questions: ["Which service customers have not returned in 12 months?"]
outputs:
  pages: [Customers]
  emails: [{name: daily_summary, cadence: daily, recipients: [pat@example.test]}]
communications:
  groups: [{name: platform_ops, recipients: [it@example.test]}]
hosting: {mode: self, domain: data.acme.example.test, exposure: cloudflare, repository: https://github.com/acme-example/acme-ddp}
ownership: {repository: dealership, host: dealership, domain: dealership, cloudflare: dealership, backups: dealership, integration_credentials: dealership}
recovery: {backup_interval: 24h, backup_retention: 14d, max_data_loss: 24h, target_restore_time: 4h}
constraints: {pii: [email, phone], data_retention: Keep sales and service history for seven years, development_data: synthetic}
success_30_days: [Service retention report is visible to the service manager]
```

- The top-level `client` section describes the dealership itself; `client` is the
  key name the code reads. `client.name` is its display name; initialization
  derives the project identifier.
- System names are lowercase identifiers such as `dms` or `accounting`. They
  become integration IDs, Python module names and landing schemas. Platform schema
  names such as `app`, `ops` and `mart` are reserved.
- `hosting.repository` is the dealership's chosen `https://github.com/org/repo` URL
  without a `.git` suffix. It determines the application image name;
  initialization does not create the GitHub repository.
- `ownership` fields and each system's `credential_owner` are either `dealership`
  (held directly by the dealership) or `operator` (held by the person who runs the
  system for the dealership: normally someone at the dealership, or an IT provider
  the dealership hires, with access the dealership grants and can revoke). A
  dealership that owns and runs everything itself, as the example does, uses
  `dealership` for every field.
- `communications.groups` must include `platform_ops`: the people the dealership
  names to receive platform alerts, with explicit addresses. SMTP configuration
  follows verification of the actual relay.
- Recovery values accept positive durations such as `24h`, `14d` and `4h`.
  Retention and maximum data loss must cover the backup interval.
- Development data must be `synthetic`.

## Continue onboarding

The output includes registry facts, landing schemas, pending users and their roles,
system navigation, platform health defaults and an onboarding plan. Roles start
with no permissions. After migration, `ddp users bootstrap` can activate a pending
first administrator; it preserves their identity and role memberships.

Run `make setup`, `make db`, `make build` and `build/ddp validate` in the project
directory. Follow `docs/onboarding-plan.md` to implement and verify ingest, models,
protected pages and emails. A fresh discovery-only project deliberately fails
`ddp smoke` because it has no ingest-to-page path yet.

Production installation, backup and restore verification, and the Git workflow
for tagged template upgrades are separate steps; see [deployment](deployment.md)
and [backups](backups.md).

## Keep a build record

Keep the generated project, completed discovery and sanitized check and smoke logs
in a persistent review directory outside operating-system temporary folders.
Record the template and project commit IDs with the commands and results. Preserve
rejected candidates separately so later repairs cannot hide their failure evidence.

Create the review directory and commit the reviewed source. From the project
checkout, retain and verify its history:

```sh
git bundle create /path/to/evidence/project.bundle --all
git bundle verify /path/to/evidence/project.bundle
shasum -a 256 /path/to/evidence/project.bundle
```

Record the bundle's checksum and location with the build record. Keep credentials
in separate protected files. Before relying on an earlier result, verify that its
source revision and supporting artifacts are still available.
