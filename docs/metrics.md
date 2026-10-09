# Metrics

The API and scheduler expose Prometheus metrics on separate private listeners:

```sh
ddp api --metrics-addr 127.0.0.1:9091
ddp scheduler --metrics-addr 127.0.0.1:9092
curl http://127.0.0.1:9091/metrics
curl http://127.0.0.1:9092/metrics
```

These are the defaults. `ddp dev` uses the API listener on port 9091. In Docker,
loopback belongs to the container; scrape from that namespace or bind an explicitly
trusted internal interface. The metrics ports are not published by the proving-ground
Compose file. `/metrics` on the portal listener returns 404. Cloudflare portal routes
must continue to target the portal port only.

The listener and workload share a lifetime. A metrics bind failure prevents startup;
stopping either service stops its companion. Scrapes admit one request at a time,
with a five-second response deadline. Database collection has a three-second context
budget and two-second statement timeout in a read-only, repeatable-read transaction.

## Exported facts

| Metric | Meaning |
|---|---|
| `ddp_http_request_duration_seconds` | Histogram with registered route pattern, bounded HTTP method and status. Covers errors, recovery and streaming responses. Empty aborted responses use status `aborted`. |
| `ddp_job_executions_total` | Retained terminal executions by declared job and status. Removed or unknown jobs share the `undeclared` label. |
| `ddp_job_duration_seconds` | Summary count and sum of terminal execution wall time where both timestamps are valid. Includes retry waiting between first start and final finish; no quantiles. |
| `ddp_health_state` | One-hot `ok`, `failing`, `unknown` for declared checks and concrete platform checks. Uses the current persisted evaluation; missing observations are unknown. |
| `ddp_health_observed_timestamp_seconds` | Timestamp of that observation, or zero when absent. Use its age to detect stale evidence; a stored `ok` state is not proof of current health. |
| `ddp_outbox_depth` | Retained message counts by pending, delivering, delivered and failed status. Completed statuses include expired-content history. |
| `ddp_scheduler_lag_seconds` | Age of the oldest due queued scheduler execution, measured from `available_at`; zero when none is due. |
| `ddp_metrics_database_up` | One only when the entire database snapshot succeeded. On failure it is zero and the other database metrics are omitted. HTTP and runtime metrics remain available. |

Go and process metrics use the [official Prometheus Go collectors](https://prometheus.io/docs/guides/go-application/).
Process collectors depend on operating-system support; Linux image checks verify
their presence. DDP does not install Prometheus or Grafana.

HTTP duration resets with its process. Database totals survive process restarts and
describe the same retained history on both endpoints; do not sum those duplicate
database observations across API and scheduler targets. Histories are scanned at
scrape time. If history makes collection too slow, add maintained aggregate tables.

Labels contain declared resource references, fixed states and registered route
patterns. Raw paths, URLs, request IDs, execution IDs, email addresses, error text,
message bodies and customer IDs are never used as labels. Unknown route patterns
and methods collapse into `unmatched` and `OTHER`. Historical unknown health refs
are omitted. Registry changes take effect when the process restarts.

Metrics provide observations; `health`, `doctor`, `status`, diagnosis and future
operator surfaces continue to read Postgres. Missing scrapes or missing database
series must not be interpreted as zero failures.
