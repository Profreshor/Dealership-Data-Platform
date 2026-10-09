# Platform test fixtures

These fixed inputs belong to platform tests. Tests must not depend on the editable
root registry of a dealership repository or on `tests/proving-ground`, which is a separate deployment.

- `base/` provides an empty, valid registry for boundary tests.
- `reporting/` provides a small HTTP ingest and three SQL models for runner,
  registry, endpoint and portal tests. It has no server, deployment topology,
  credentials, saved database or deployment acceptance scripts.

Dealership repositories retain these unit-test inputs with platform source. Runtime
images exclude them. Keep test facts here fixed unless the tested contract changes;
onboarding a dealership changes the root registry and the dealership's own code
under `client/` instead.

`make check-client-tests` builds a temporary source copy, replaces its root registry
with different dealership declarations, omits the proving-ground deployment and runs
all Go tests against explicit disposable Postgres. This is a prerequisite for
`ddp init`; it does not complete initialization, the dealership repository's smoke
run or production deployment proof.
