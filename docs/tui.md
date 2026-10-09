# Operational terminal interface

Run `ddp` in a terminal with `DATABASE_URL` set. Use `--config path/to/ddp.yaml`
for another project. Standard input and output must both be terminals. Piped output,
`--json` and explicit help keep the existing command interface and do not start the
TUI. Read-only database credentials are sufficient for inspection.

## Screens and keys

| Key | Screen |
|---|---|
| `1` | Overview: scheduler, health observation, outbox and jobs. |
| `2` | Recent failed executions. |
| `3` | Declared and historically observed jobs. |
| `4` | Recent executions. |
| `5` | Executions whose logs can be opened with Enter. |
| `6` | Integrations and their declared jobs and tables. |
| `7` | Model declarations. |
| `8` | Live table metadata, including estimated row counts. |
| `9` | Saved health evaluations with their observation times. |
| `0` | Outbox status and delivery metadata. |

Tab and Shift+Tab move between screens. Arrow keys or `j`/`k` select rows;
Page Up/Down and Home/End move through lists or details. Enter or `i` opens the
selected inspector, `d` opens diagnosis, and `l` opens logs. Esc returns to the list;
`q` quits. Unsupported diagnosis or log targets return the same inspection error
as the CLI. Resize a terminal smaller than 60 columns by 18 rows to see the views.

Views refresh every five seconds; `r` requests a refresh. The fetch timestamp is
separate from saved health observation times. Queries have a ten-second deadline.
A failed refresh keeps the previous observation and displays an error. A new
screen clears the previous screen's rows, and late responses cannot replace the
current view. Job actions are disabled while a refresh is loading.

## Run a job

Select a job or execution and press `x`. Scheduled model jobs are also actionable
from the models screen. Type `RUN` to confirm an ordinary execution, or type the
exact idempotency strategy displayed for a side-effecting job. Esc cancels.

The TUI invokes the same binary's `jobs run` command, with the selected reference,
registry path and confirmation. It keeps the current environment and database
credentials. The CLI still validates the declaration, checks confirmation,
enforces database privileges and records its usual audit. The TUI does not grant
a role, use an administrative connection or implement another action dispatcher.
A read-only operator receives the normal refusal when attempting a write.

During execution, Bubble Tea releases the terminal and waits for the CLI. Ctrl+C
cancels through the CLI signal handler so it can stop its owned workload and save
the interrupted execution. The command result appears when the TUI resumes;
Esc returns to inspection. Terminal settings are restored on exit.

## Shared facts and limits

The screens call the same `inspect`, `health` and `comms` packages as the CLI.
Health displays persisted evaluations, without running new probes. The TUI adds
presentation and navigation only. Logs preserve expiry markers; message inspection
exposes metadata rather than private recipients or bodies. Terminal commands and
control characters are removed from displayed metadata and logs.

Recent execution and message lists use their existing 100-record bounds; failures
use the overview's 20-record bound. Log detail loads up to 100 attempts and wraps
that bounded payload in memory. Larger histories remain available through focused
CLI inspection and database operations.

`make check-smoke` runs a real pseudo-terminal against a disposable database. It
checks log navigation, typed execution, read-only refusal, cancellation, workload
cleanup and restored terminal settings. Real-Postgres tests compare TUI projections
with the inspection packages and verify that browsing does not change saved state.
