# Status and diagnosis

Use the same database and registry as the deployment you are investigating:

```sh
ddp status --json
ddp diagnose job/sync_customers --json
ddp diagnose table/synthetic.customers --json
ddp diagnose page/customers --json
ddp diagnose execution/01J00000000000000000000001 --json
```

These commands read recorded facts. They do not run jobs, probe integrations,
evaluate health rules or enqueue messages. Both use the standard versioned JSON
envelope and work with `ddp_readonly` database permissions.

## Read the overview

`status` combines scheduler heartbeat and lock ownership, job declarations and
latest runs, persisted health evaluations, outbox counts, pending alert count and
the newest 20 failed executions. Recent failures include older executions of jobs
that have since recovered. Use `diagnose` to investigate current failure evidence.

`observed_at` is the database time when collection starts. Each existing inspection
package reads its own facts, so concurrent changes can appear during collection.
`health_observation` describes observation freshness, not whether checks passed:
`not_observed` means no successful health execution is recorded; `stale` means its
completion or an evaluation is over three minutes old, in the future, or a declared
business rule has no observation. A current failing evaluation remains `current`.

## Read a diagnosis

For a declared resource, diagnosis follows upstream and downstream data flow in the
current registry. It excludes permission and notification edges. Job ordering,
model outputs, endpoint consumers and page consumers retain their declared
relationships. Diagnosing an integration includes its downstream jobs.

Current failure evidence uses each relevant job's latest completed execution by
scheduled time. A later successful execution clears that job's current failure;
a queued retry does not. A failure from an unrelated job is excluded. Explicit
`execution/<id>` diagnosis preserves the selected execution's status after recovery.
Removed jobs remain inspectable through their recorded history.

`affected_outputs` lists the current declared outputs of the displayed failed jobs.
`integration_refs` names integrations in the declared lineage. These fields identify
possible impact and investigation context; they do not establish corrupted data or
an integration fault. Historical registry snapshots are not available. Health shown
beside an explicit execution is the latest recorded health, not historical health.

Diagnosis reads a consistent database snapshot. It returns at most 20 failed jobs,
with `total_failures` for the full count. Each includes its last attempt, the last
4,096 characters of stdout and stderr, and the first 2,048 characters of its error.
Use `runs show` and `logs` for further execution details. `payload_expired_at`
marks log content removed by [operational retention](retention.md); execution
status and identity remain available.

`state` summarizes the recorded evidence: `failing` for a final execution failure
or a current failing health observation; `unknown` for incomplete execution history,
missing declared health observations, or stale/unknown health. `ok` means the
available relevant evidence passed; it does not prove business correctness.
Command success means the report was collected. Inspect `state` in automation.
