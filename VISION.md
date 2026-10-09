# Dealership Data Platform

**Install. Config. Profit.**

Dealership Data Platform (DDP) is a free, open-source, highly opinionated, text-first foundation for a dealership's data systems.

It is not a framework that attempts to solve every data problem. It is not a collection of cloud services glued together behind a dashboard. It is not an enterprise platform requiring three weeks of configuration before the first useful row lands in a database.

DDP is a starting point.

A scheduler. A warehouse. Jobs. APIs. Monitoring. Communications. A frontend. Agent tooling. Deployment configuration. Operational controls.

The pieces that seem to appear in every serious data project anyway, assembled into one coherent system with strong opinions about how they should work together.

The inspiration is closer to an opinionated operating-system distribution than a traditional software framework:

> Here is a complete environment. Here are the conventions. Change what matters. Start building.

## The Idea

A fresh DDP project should begin with very little.

A coding agent, a Linux box and a few accounts the dealership owns are all it needs to start.

```bash
ddp init
```

Configure the environment.

```bash
ddp config
```

Define some sources.

Write some jobs.

```bash
ddp dev
```

And have a functioning data system.

Not a toy ETL runner.

A foundation capable of growing into something substantial without requiring the project to abandon the conventions that made it productive when it was small.

The same basic shape should make sense for a single rooftop with five scheduled jobs and for a dealer group's mature operational warehouse with CDC, hundreds of jobs, materialized views, scrapers, APIs, ML workloads and multiple applications consuming its data.

That is the ambition.

## The Dealership Data Platform Philosophy

DDP should be opinionated as hell.

There are a thousand ways to assemble a modern data stack.

DDP does not need to support all of them.

It needs one excellent path.

Go does the long-running systems work.

Python does the data work.

Postgres is the center of gravity.

YAML describes the system.

TypeScript powers the browser.

The filesystem is an interface.

The CLI is the control plane.

Humans and agents operate the same platform.

Everything important can be inspected.

Everything important can be explained.

Everything possible should be represented as text.

A DDP repository should be understandable by opening it in a terminal.

## Agents Are First-Class Citizens

DDP is built for two operators from the beginning:

**Humans and agents.**

Agent support is not an integration to bolt on later. It is a design constraint for the platform itself.

Every major DDP capability should be considered from both perspectives:

| Human | Agent |
|---|---|
| TUI | Structured CLI |
| Readable output | JSON output |
| Keyboard navigation | Deterministic commands |
| Documentation | `AGENTS.md` and `skills/` |
| Diagnostics | Structured evidence |
| Interactive actions | Permissioned actions |
| Visual system state | Machine-readable state |

Neither interface should be a second-class translation of the other.

Both should operate against the same underlying DDP domain model.

## The Repository Is an Interface

A DDP repository should be deliberately structured so an agent can understand it.

Configuration is text.

Schedules are text.

Schemas are text.

Migrations are text.

Infrastructure is text.

Health definitions are text.

Agent instructions are text.

Documentation is text.

Important relationships should be explicit rather than hidden inside runtime behavior.

An agent entering an unfamiliar DDP repository should be able to ingest its configuration and documentation and quickly understand:

- What exists
- What runs
- When it runs
- Where data comes from
- Where data goes
- What depends on what
- What healthy means
- What is currently unhealthy
- How to investigate it
- What actions are available
- Which actions it is authorized to perform

Machine legibility is a feature.

## Skills Are Part of the Platform

Every DDP deployment should ship with excellent agent instructions.

`AGENTS.md` and the procedures under `skills/` should not be an afterthought written after the system is finished.

It is part of the interface.

It should teach an agent how to operate DDP rather than requiring the agent to rediscover the system from source code every session.

A mature DDP skill should describe:

- System discovery
- Health inspection
- Job investigation
- Scheduler investigation
- CDC investigation
- Database investigation
- Logs
- Deployment
- Testing
- Safe operational actions
- Dangerous operational actions
- Escalation boundaries

Dealership-specific knowledge can extend the core DDP skill without replacing it.

The goal is that an agent that has worked with one DDP deployment already understands the fundamental operating model of any other.

## The CLI Is an Agent API

Human-readable terminal output is important.

Machine-readable output is equally important.

If a human can ask:

```bash
ddp status
```

An agent should be able to ask:

```bash
ddp status --json
```

The same principle should apply throughout the platform:

```bash
ddp jobs --json
ddp inspect job/customer-sync --json
ddp diagnose mv/customer-summary --json
ddp search customer --json
ddp capabilities --json
```

Structured output is a public interface.

Its schemas should be predictable, versioned where necessary, and designed for consumption rather than scraped from human terminal output.

Agents should not need to parse ANSI escape sequences or reverse engineer formatted tables.

## Dealership Data Platform Should Explain Itself

Agents are excellent at reasoning when they are given reliable context.

DDP should therefore provide facts rather than force agents to manufacture them.

A resource should be able to expose:

- Identity
- Type
- Status
- Configuration
- Dependencies
- Upstream resources
- Downstream consumers
- Recent history
- Health expectations
- Observed metrics
- Evidence
- Available actions

When something fails, DDP should make deterministic investigation possible before probabilistic reasoning begins.

The preferred relationship is:

```text
DDP gathers evidence
        ↓
DDP exposes structure
        ↓
Agent reasons about evidence
        ↓
Agent proposes or performs action
```

Not:

```text
Agent receives "something is broken"
        ↓
Agent randomly explores the server
```

Go gathers facts. Agents reason about facts.

## Agent Operations Must Be Safe by Design

First-class does not mean unrestricted.

DDP should expose capabilities deliberately.

An agent may freely:

- Inspect
- Search
- Query health
- Read logs
- Diagnose
- Examine dependencies
- Review history

Other capabilities may be explicitly granted:

- Retry a job
- Refresh a materialized view
- Run a scraper
- Trigger a known workflow

Dangerous operations should have stronger boundaries:

- Execute arbitrary SQL
- Kill processes
- Pause CDC
- Modify schedules
- Restart critical infrastructure
- Change production configuration
- Delete data

DDP should know the difference.

Capabilities, permissions, risk levels and confirmation requirements should be machine discoverable rather than existing only as prose in a prompt.

The objective is not merely to make agents powerful.

It is to make their power legible, constrained and auditable.

In production, the database, not the prompt, should enforce what an agent can see. A restricted agent database role that sees only the business data the dealership grants is planned (D26), not built. Today an agent investigating production uses the read-only database login, which refuses changes but can read all dealership data, so giving an agent that access is the dealership's deliberate decision.

The dealership chooses which AI provider, if any, is used.

## Agents Should Be Able to Discover Dealership Data Platform

An agent should not require a massive system prompt containing every command.

DDP should be self-describing.

Conceptually:

```bash
ddp capabilities --json
ddp help --json
ddp resources --json
```

An agent should be able to discover what exists and then progressively inspect what it needs.

Over time, DDP may expose the same underlying capabilities through MCP or whatever agent protocol proves durable.

The protocol is secondary.

The important architectural principle is that the underlying platform is already machine-operable.

## Agents Participate in Development Too

Agents are not only production operators.

They are development collaborators.

A fresh DDP repository should be an unusually good environment for an engineering agent to enter.

An agent should be able to:

- Understand repository conventions
- Discover available commands
- Create jobs from established templates
- Validate configuration
- Run tests
- Inspect schemas
- Create migrations
- Run development environments
- Diagnose failures
- Understand deployment expectations
- Determine what it is allowed to change

DDP conventions should reduce the amount of context that needs to be explained in every prompt.

The platform itself carries that knowledge.

## Human and Machine Interfaces Must Not Drift

The TUI, CLI, JSON output, agent skills and future agent protocols should not become separate implementations of DDP.

They are different interfaces to the same system.

```text
                     DDP CORE
                        │
          ┌─────────────┼─────────────┐
          │             │             │
         TUI           CLI          Agent
          │             │          Interface
          │             │             │
        Human       Human/Shell      Machine
```

If the TUI says a job is unhealthy and the agent interface says it is healthy, DDP has failed.

There should be one operational truth with multiple ways to consume it.

## The Standard

A new DDP feature is not complete merely because a human can use it.

For significant capabilities, the design question should always be:

> How does a human use this, and how does an agent use this?

Sometimes the answer will be different.

Sometimes an agent should not have access at all.

But the question must be asked.

Agents are not visitors to DDP.

They are first-class citizens of the platform.

## `ddp`

The CLI is the front door.

```bash
ddp
```

Opens an interactive operational cockpit.

But DDP is equally comfortable being asked a direct question:

```bash
ddp status
ddp doctor
ddp jobs
ddp schedules
ddp tables
ddp inspect
ddp logs
ddp config
```

And eventually being told to do something:

```bash
ddp run
ddp retry
ddp refresh
ddp deploy
```

Humans receive excellent terminal output.

Scripts receive predictable exit codes.

Agents receive structured output.

```bash
ddp status --json
```

The CLI should make SSHing into a machine and hunting through twelve different journals, configuration files, database tables and dashboards feel primitive.

If the system knows something about itself, `ddp` should eventually be able to tell you.

## The Dealership Data Platform TUI

Running `ddp` with no arguments should feel like opening the hood.

- Scheduler health
- Running jobs
- Recent failures
- CDC lag
- Materialized-view freshness
- Table sizes
- Row counts
- Scrapers
- Services
- Database health
- API latency
- Schedules
- Data-quality checks
- Logs
- Dependencies

Everything curated into one keyboard-driven view of the system.

Not Grafana in a terminal.

Not every metric because the metric exists.

The things an operator actually wants to know.

DDP should answer four questions exceptionally well:

1. What is happening?
2. What is broken?
3. Why is it broken?
4. What can I do about it?

## The Scheduler

At the heart of DDP is a Go scheduler.

Small enough to understand.

Serious enough to trust.

It knows about jobs, schedules, dependencies, retries, timeouts, execution history and workers.

It does not care whether useful work happens in Python, Go, SQL or a shell command.

Its job is orchestration.

The workload's job is business logic.

A five-job deployment should not need Kubernetes.

A five-hundred-job deployment should not need a new scheduler simply because DDP grew up.

## Python Jobs

Python is where DDP gets dirty.

- Extraction
- Transformation
- Loading
- Scraping
- Data science
- ML
- File processing
- DMS, CRM and OEM portal APIs

All the wonderfully messy work that makes data systems useful.

Repair orders from the DMS. Leads from the CRM. Incentive reports from a manufacturer portal. A spreadsheet somebody in accounting still emails every Monday.

A fresh project should include an opinionated Python job structure with boring answers already provided for boring questions:

```text
jobs/
├── sync_dms_repair_orders.py
├── sync_crm_leads.py
└── send_weekly_summary.py
```

Logging should already work.

Configuration should already work.

Database access should already work.

Execution metadata should already work.

Retries and failures should already make sense to the scheduler.

A developer creating a new job should spend their time writing the job.

## YAML Is the Map

DDP should describe itself in text.

- Sources
- Schedules
- Jobs
- Services
- Tables
- Materialized views
- Health checks
- Communications
- Permissions
- Deployment
- Agent capabilities

Whatever belongs in configuration should be visible, diffable and version controlled.

```yaml
jobs:
  sync_dms_repair_orders:
    purpose: Pull repair orders from the DMS into Postgres.
    action: ingest
    python: jobs.sync_dms_repair_orders
    reads: [integration/dms]
    schedule: "*/15 * * * *"
    timeout: 5m
    retry: { max_attempts: 3 }
```

No mystery configuration trapped inside a web application.

No critical setting that only exists because somebody clicked a checkbox eighteen months ago.

If configuration changes, Git should be able to tell us what changed.

## Postgres

Postgres is not an implementation detail.

It is the foundation.

- Application data
- Warehouse data
- Scheduler metadata
- Job history
- Operational state
- Health information
- CDC checkpoints
- Configuration metadata
- Dependencies
- Events

DDP should establish an opinionated database layout from day one.

Something conceptually like:

```text
source
staging
core
mart
ops
ddp
```

The exact names can evolve.

The principle should not.

A DDP database should have a recognizable shape.

The system should maintain enough metadata about itself that it can answer questions without requiring an operator—or an agent—to reverse engineer it first.

## Metadata as a First-Class Feature

DDP should know what it contains.

A table isn't merely a relation in `pg_catalog`.

It can have:

- An owner
- A purpose
- A source
- A freshness expectation
- Dependencies
- Consumers
- Health checks
- Operational importance

A job isn't merely a Python process.

It has:

- A schedule
- History
- Dependencies
- Expected runtime
- Retries
- Outputs
- Failures

The more DDP knows about its resources, the more useful it becomes to humans, automation and agents.

The goal is a data system capable of describing itself.

## The Serving Layer

Data becomes valuable when somebody can use it.

DDP should include an opinionated Go serving layer for turning warehouse data into reliable application interfaces.

- HTTP APIs
- Authentication hooks
- Authorization
- Health endpoints
- Structured errors
- Database pooling
- Pagination
- Observability

The boring infrastructure should exist already.

A project should be able to go from:

```text
Postgres table
```

To:

```http
GET /api/customers
```

Without inventing another backend architecture.

## The Frontend

DDP should ship with an application frontend.

Not because every data project needs a beautiful application.

Because eventually somebody always asks:

> Can I see this somewhere?

The frontend should use a modern TypeScript framework and arrive with the uninteresting decisions already made:

- Authentication structure
- API client
- Routing
- Tables
- Forms
- Loading states
- Error handling
- Basic layouts
- Permissions
- Environment configuration

A DDP project should be able to produce an internal dashboard, customer portal or operational application without beginning from `npm create`.

The stack is React and TypeScript (see `DECISIONS.md` D13).

Boring, durable, productive.

## Communications

Data systems eventually need to talk to people.

- Email
- SMS
- Alerts
- Reports
- Notifications

DDP should have a common communications layer so every project doesn't reinvent:

- SMTP client
- SMS provider
- Templates
- Retries
- Delivery logs
- Failure handling

Jobs should be able to communicate without caring which vendor happens to deliver the message.

## Monitoring

DDP should know whether DDP is healthy.

- Scheduler
- Database
- CDC
- Jobs
- Scrapers
- Serving layer
- Communications
- Materialized views
- Data freshness
- Data quality
- Resource usage

Monitoring should be built into the platform rather than bolted on after the first production incident.

Prometheus and Grafana may be part of that story.

They are not the user experience.

`ddp` is.

## Access

A dealership's data portal does not belong on the public internet by default.

A DDP deployment should have a boring path from:

```text
localhost
```

To:

```text
reachable by the people who should see it
```

The path today is a Cloudflare Tunnel on the dealership's own Cloudflare account.

The portal sits behind DDP's own login.

Administrator SSH sits behind the dealership's own Cloudflare Access policy with MFA, and listens only inside the box.

No inbound ports.

Every account is the dealership's own.

Private-network helpers for Tailscale and Twingate are planned, not built.

The goal isn't to hide these services.

The goal is to avoid rediscovering how the deployment should be wired every time a dealership starts.

## Everything Is Text

This may be one of DDP's strongest opinions.

Configuration is text.

Infrastructure is text.

Schedules are text.

Schemas are text.

Migrations are text.

Agent instructions are text.

Deployment configuration is text.

Communication templates are text.

Health definitions are text.

Documentation is text.

The repository should tell the story.

An engineer should be able to clone a DDP project, run:

```bash
tree
```

And immediately begin understanding the system.

An agent should be able to ingest that same repository and build an accurate model of it.

Git becomes the history of the system.

Pull requests become configuration management.

Diffs become audit trails.

There should be as little hidden state as practical.

## The Fresh Project

The dream is absurdly simple.

```bash
ddp init acme --discovery acme-discovery.yaml
cd acme
```

And receive something recognizable:

```text
acme/
├── cmd/
├── internal/
├── jobs/
├── migrations/
├── frontend/
├── deploy/
├── skills/
├── tests/
├── AGENTS.md
├── .env.example
└── ddp.yaml
```

Then:

```bash
ddp doctor
```

Tells you what the machine needs.

```bash
ddp dev
```

Starts the environment.

```bash
ddp
```

Shows you what is happening.

The template begins opinionated and complete.

The dealership supplies the part DDP cannot:

What its business actually needs.

## The Mature Project

The other half of the experiment matters just as much.

DDP should not only be beautiful when empty.

It should survive success.

- More jobs
- More tables
- More schedules
- More users
- More APIs
- More scrapers
- More data
- More services
- More agents
- More operational complexity

The five-job deployment and the five-hundred-job deployment should still feel related.

The system should grow through composition rather than eventual replacement.

## What Dealership Data Platform Is Not

DDP does not need to compete with every data product.

It does not need its own database.

It does not need its own programming language.

It does not need a visual pipeline designer.

It does not need seventeen interchangeable schedulers.

It does not need to abstract away SQL.

It does not need to pretend infrastructure doesn't exist.

It does not need a checkbox for every possible architecture.

DDP should aggressively use excellent existing technology and provide strong opinions about how those technologies fit together.

The value isn't inventing every component.

The value is making the components feel like one system.

## The Standard

A DDP deployment should feel handcrafted without needing to be handcrafted.

A developer should be able to understand it.

An agent should be able to navigate it.

An operator should be able to diagnose it.

A coding agent should be able to deploy it quickly.

A dealership should be able to own it.

And six months later, nobody should be wondering why some critical production process lives in a forgotten cron entry on a VM.

DDP should provide strong defaults without becoming a cage.

Its conventions should eliminate decisions that do not differentiate the dealership's business while leaving the important decisions visible and accessible.

The platform should make the easy path the good path.

The system should remain understandable as it grows.

Its configuration should remain inspectable.

Its operations should remain observable.

Its history should remain traceable.

Its interfaces should remain useful to both humans and agents.

Complexity will eventually arrive.

DDP's job is to make sure unnecessary complexity doesn't arrive first.

## The Vision

DDP is what a dealership's data problem starts with before the dealership-specific work begins.

Not a blank repository.

Not six SaaS subscriptions.

Not a pile of scripts copied from the last project.

A working foundation.

- A Go scheduler
- A Python job system
- A Postgres architecture
- A serving layer
- A frontend
- Communications
- Monitoring
- Deployment
- Agent tooling
- One CLI sitting in front of all of it

All represented in boring, inspectable, version-controlled text wherever possible.

A fresh deployment should be small enough to understand.

A mature deployment should be powerful enough to run a serious data operation.

The architecture should not fundamentally change somewhere between the two.

Humans should be able to open a terminal and understand what their system is doing.

Agents should be able to enter the same repository, understand its conventions, inspect the same operational truth and work productively without having the architecture explained from scratch.

The system should know enough about itself to explain what exists, what is running, what depends on what, what is healthy and what has gone wrong.

The CLI should make that knowledge accessible.

The repository should make that knowledge durable.

The conventions should make that knowledge transferable.

The platform handles the recurring infrastructure.

The coding agent writes the code.

The dealership decides what the business needs.

And everyone—human or machine—gets the same coherent system to work with.

Install it.

Configure it.

Teach it what the business does.

Then get out of the platform's way and solve the actual problem.

**Install. Config. Profit.**
