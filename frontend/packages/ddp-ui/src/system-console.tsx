import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { z } from "zod";
import { createApiClient } from "./api";
import "./system-console.css";

const timeSchema = z.string().datetime({ offset: true });
const runSchema = z.object({
  id: z.string(), job_ref: z.string(), dispatch: z.string(), scheduled_at: timeSchema,
  available_at: timeSchema, max_attempts: z.number(), started_at: timeSchema.nullable().optional(),
  finished_at: timeSchema.nullable().optional(), status: z.string(), chain_id: z.string().nullable().optional(),
  depends_on: z.array(z.string()).optional(), reason: z.string().nullable().optional(),
  cancel_requested_at: timeSchema.nullable().optional(),
});
const jobSchema = z.object({
  ref: z.string(), purpose: z.string().optional(), action: z.string().optional(), schedule: z.string().optional(),
  paused: z.boolean(), newest_run: runSchema.nullable().optional(), tags: z.array(z.string()).optional(),
});
const healthSchema = z.object({ ref: z.string(), observed_at: timeSchema, state: z.string(), severity: z.string(), message: z.string(), value: z.string().optional() });
const statusSchema = z.object({
  observed_at: timeSchema, scheduler: z.object({ instance_id: z.string(), seen_at: timeSchema.nullable().optional(), state: z.string(), lock_held: z.boolean() }),
  jobs: z.array(jobSchema), health: z.array(healthSchema), health_observation: z.enum(["current", "stale", "not_observed"]),
  outbox: z.record(z.number()), pending_alerts: z.number(), recent_failures: z.array(runSchema),
});
const logSchema = z.object({
  id: z.string(), execution_id: z.string(), number: z.number(), started_at: timeSchema,
  finished_at: timeSchema.nullable().optional(), status: z.string(), stdout: z.string(), stderr: z.string(),
  error: z.string().nullable().optional(), payload_expired_at: timeSchema.nullable().optional(),
});

type Run = z.infer<typeof runSchema>;

function formatTime(value: string | null | undefined) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "Unknown time" : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
}

function StatePill({ value }: { value: string }) {
  return <span className={`console-state console-state-${value.replace(/[^a-z0-9]+/gi, "-")}`}>{value.replaceAll("_", " ")}</span>;
}

function Section({ title, children, action }: { title: string; children: ReactNode; action?: ReactNode }) {
  return <section className="console-section" aria-labelledby={`console-${title.toLowerCase().replaceAll(" ", "-")}`}>
    <div className="console-section-heading"><h2 id={`console-${title.toLowerCase().replaceAll(" ", "-")}`}>{title}</h2>{action}</div>{children}
  </section>;
}

export function SystemConsole({ label = "System console" }: { label?: string }) {
  const [selectedRun, setSelectedRun] = useState<Run | null>(null);
  const logsHeadingRef = useRef<HTMLHeadingElement>(null);
  const statusQuery = useQuery({ queryKey: ["system-status"], queryFn: () => createApiClient().request("/system/status", statusSchema) });
  const runsQuery = useQuery({ queryKey: ["system-runs"], queryFn: () => createApiClient().request("/system/runs", z.array(runSchema)) });
  const logsQuery = useQuery({ queryKey: ["system-run-logs", selectedRun?.id], queryFn: () => createApiClient().request(`/system/runs/${encodeURIComponent(selectedRun!.id)}/logs`, z.array(logSchema)), enabled: Boolean(selectedRun) });
  useEffect(() => { if (selectedRun) { logsHeadingRef.current?.scrollIntoView({ behavior: "instant", block: "start" }); logsHeadingRef.current?.focus(); } }, [selectedRun]);
  const retry = () => { void statusQuery.refetch(); void runsQuery.refetch(); if (selectedRun) void logsQuery.refetch(); };

  if (statusQuery.isPending || runsQuery.isPending) return <section className="system-console" aria-labelledby="console-title"><h1 id="console-title">{label}</h1><div className="console-loading" role="status">Loading system status…</div></section>;
  if (statusQuery.error || runsQuery.error || !statusQuery.data || !runsQuery.data) return <section className="system-console" aria-labelledby="console-title"><div className="console-heading"><div><h1 id="console-title">{label}</h1><p>Operational visibility for operators.</p></div><button className="console-button" type="button" onClick={retry}>Try again</button></div><div className="console-error" role="alert"><strong>Couldn’t load system status.</strong><span>{statusQuery.error instanceof Error ? statusQuery.error.message : runsQuery.error instanceof Error ? runsQuery.error.message : "Try again to reconnect."}</span></div></section>;
  const status = statusQuery.data;
  return <section className="system-console" aria-labelledby="console-title">
    <div className="console-heading"><div><h1 id="console-title">{label}</h1><p>Operational visibility for operators.</p></div><button className="console-button" type="button" onClick={retry} disabled={statusQuery.isFetching || runsQuery.isFetching}>Refresh</button></div>
    <p className="console-observed">Observed {formatTime(status.observed_at)}</p>
    <Section title="Status"><div className="console-status-grid"><div><span className="console-label">Scheduler</span><strong><StatePill value={status.scheduler.state} /></strong><small>{status.scheduler.lock_held ? "Planner lock held" : "Planner lock not held"}</small></div><div><span className="console-label">Outbox</span><strong>{status.outbox.pending ?? 0} pending</strong><small>{status.pending_alerts} pending alerts</small></div><div><span className="console-label">Health observation</span><strong><StatePill value={status.health_observation} /></strong><small>{status.health_observation === "not_observed" ? "No successful health run recorded" : status.health_observation === "stale" ? "Saved health evidence is stale or incomplete." : "Latest observation is current"}</small></div></div></Section>
    <Section title="Health"><div className="console-list">{status.health.length === 0 ? <p className="console-empty">No health observations available.</p> : status.health.map((item) => <div className="console-row" key={item.ref}><div><strong>{item.ref}</strong><span>{item.message}</span></div><div className="console-row-meta"><StatePill value={item.state} /><small>{formatTime(item.observed_at)}</small></div></div>)}</div></Section>
    <Section title="Recent failures"><RunList runs={status.recent_failures} onSelect={setSelectedRun} empty="No recent failures." /></Section>
    <Section title="Jobs"><div className="console-list">{status.jobs.length === 0 ? <p className="console-empty">No jobs declared.</p> : status.jobs.map((job) => <div className="console-row" key={job.ref}><div><strong>{job.ref}</strong><span>{job.purpose || job.action || "No purpose recorded"}{job.schedule ? ` · ${job.schedule}` : ""}</span></div><div className="console-row-meta">{job.paused && <StatePill value="paused" />}{job.newest_run && <StatePill value={job.newest_run.status} />}</div></div>)}</div></Section>
    <Section title="Recent runs"><RunList runs={runsQuery.data} onSelect={setSelectedRun} empty="No runs recorded." /></Section>
    {selectedRun && <div className="console-logs" aria-labelledby="console-logs-title"><div className="console-section-heading"><h2 ref={logsHeadingRef} tabIndex={-1} id="console-logs-title">Logs · {selectedRun.job_ref} · {selectedRun.id}</h2><button className="console-link-button" type="button" onClick={() => setSelectedRun(null)}>Close</button></div>{logsQuery.isPending ? <div className="console-loading" role="status">Loading logs…</div> : logsQuery.error ? <div className="console-error" role="alert"><strong>Couldn’t load logs.</strong><button className="console-button" type="button" onClick={() => logsQuery.refetch()}>Try again</button></div> : logsQuery.data?.length ? <div className="console-log-list">{logsQuery.data.map((log) => <article className="console-log" key={log.id}><header><strong>Attempt {log.number}</strong><StatePill value={log.status} /><small>{formatTime(log.started_at)}</small>{log.payload_expired_at && <span className="console-expired">Payload expired</span>}</header>{log.error && <p className="console-log-error">{log.error}</p>}{log.stdout && <pre>{log.stdout}</pre>}{log.stderr && <pre className="console-stderr">{log.stderr}</pre>}{!log.stdout && !log.stderr && !log.error && <p className="console-empty">No captured output.</p>}</article>)}</div> : <p className="console-empty">No attempts recorded.</p>}</div>}
  </section>;
}

function RunList({ runs, onSelect, empty }: { runs: Run[]; onSelect: (run: Run) => void; empty: string }) {
  return <div className="console-list">{runs.length === 0 ? <p className="console-empty">{empty}</p> : runs.map((run) => <div className="console-row" key={run.id}><div><strong>{run.job_ref}</strong><span>{run.id} · {formatTime(run.scheduled_at)}</span></div><div className="console-row-meta"><StatePill value={run.status} /><button className="console-link-button" type="button" onClick={() => onSelect(run)}>View logs</button></div></div>)}</div>;
}
