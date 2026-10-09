# Dealership Data Platform

**Install. Config. Profit.**

Dealership Data Platform (DDP) is a free, open-source, deployable shell for a
dealership's data. It is an agent-first foundation: a coding agent builds your
dealership's data system from it, guided by the docs and conventions in this repo.
Data from your DMS, CRM, accounting and other systems flows into your own Postgres
database (Postgres is a widely used free, open-source database), and you get a
private web portal, scheduled email and health alerts, all on a server you own.
Everything runs in accounts you control.

DDP is not something to try by installing this repository as it is. You first make
your own private copy for your dealership (step 4 below). Your copy builds your
system and publishes the releases your server installs. This public repository
publishes no releases of its own, and the installer will not install one from it.

In these docs, the **operator** is the person who runs the system for the
dealership. Normally that is someone at the dealership. It can also be an IT
provider the dealership hires, with access the dealership grants and can take back
at any time.

## What you get

- Data pulled on a schedule from your DMS (dealer management system), CRM,
  accounting and other systems into your own Postgres database.
- A private web portal for the principal, GM and managers, with the sales, service,
  parts, F&I and accounting views your coding agent builds for you.
- Scheduled summary emails to the people you choose.
- Alerts when something breaks, such as a data feed that stops or a job that fails.
- Agent-ready docs and procedures, so a coding agent can add reports, pages and
  data sources later.

## Status

**Works today, in this code:**

- A scheduler that runs your data jobs (Python) and SQL reports on a schedule, with
  retries, history and logs.
- A Postgres layout for raw data from each system, cleaned data and report-ready
  tables, with a separate database login for each part of the system.
- A private web portal: invite-only accounts, roles and permissions, password reset
  by email, table pages with search, filters and CSV export, your logo and color,
  and a System page for operators.
- Email through your own mail provider. Each message is saved before it is sent, so
  nothing is lost or silently skipped.
- Health checks and alerts: failed jobs, stale data, a stopped scheduler, a filling
  disk, failed backups and failed releases email the people you name.
- Encrypted backups every 24 hours by default to storage you own, plus a weekly
  automatic test that restores the newest backup and checks it.
- Automatic updates: when you merge a reviewed change into your own repo, your
  server installs it, and switches back by itself if the new version fails to start.
- A fresh-server installer for Ubuntu 24.04, and remote access through your own
  Cloudflare account.
- A `ddp` command line that tells you, or your coding agent, what exists, what ran,
  what failed and why.

**Not yet proven:** DDP has been tested end to end with synthetic data, including
the installer on a fresh Ubuntu 24.04 server. A complete production rollout through
Cloudflare remote access has not yet been signed off in this repository, so your
first deployment is also a test of the deployment path. Plan time for that.

**No support, no warranty:** DDP is MIT licensed. There is no vendor, no
subscription, no support desk and no warranty. See
[No support, no warranty](#no-support-no-warranty).

**Planned, not built:** see [Planned](#planned).

## What you need

A few terms first:

- A **coding agent** is an AI assistant that writes and runs code for you from plain
  English instructions, for example Claude Code or Codex.
- A **Linux server** is a computer that runs Linux, a free operating system, and
  stays on all the time. DDP runs there.
- A **repo** (repository) is a folder of code with its full history, kept on GitHub.
- **Cloudflare** is the service that lets your staff reach the portal from anywhere
  without opening your server to the internet. It also guards remote administrator
  access with a login and a second factor (MFA).
- **SMTP** is the standard way programs send email. Most business email providers
  offer it.

The list:

1. **A coding agent subscription**, such as Claude Code or Codex.
2. **A development computer** where the agent works: a Mac or Linux machine with
   Docker installed. Your agent builds and tests your system here, on made-up data,
   before anything reaches the server.
3. **A Linux server.** Ubuntu Server 24.04 LTS. Suggested: 4 CPU cores, 16 GB RAM,
   250 GB SSD. Minimum: 2 cores, 8 GB RAM, 100 GB. A small computer in the office or
   a cloud virtual machine both work. Its data disk must be encrypted; the simplest
   way is to choose disk encryption when installing Ubuntu.
4. **A GitHub organization on a paid plan** (GitHub Team or higher). Your private
   repo and your packaged software (container images) live there. The paid plan is
   needed for the branch protection rules that stop unreviewed code from reaching
   your server.
5. **A Cloudflare account with your domain name on it**, for example
   `acmedealership.com`. Staff reach the portal at an address such as
   `portal.acmedealership.com`. Cloudflare's Zero Trust features have a free tier
   for small teams.
6. **Off-site backup storage** that speaks the S3 protocol, such as Cloudflare R2,
   in your own account.
7. **An email account that can send mail over SMTP.**
8. **API access to your DMS.** An API is the official way for one program to read
   data from another. Ask your DMS provider. It can take weeks and may cost money, so
   start this request first. The same goes for any CRM, accounting or OEM system you
   want included.
9. **A second person, or a separate GitHub account for your agent.** GitHub does not
   let anyone approve their own change, and DDP's rules require an approval before
   anything reaches your server. The usual setup: the agent works under its own
   GitHub account and you approve its changes.

## Setup, step by step

Each step says what you do. Where the coding agent does the work, there is a prompt
you can copy and give it. Steps marked **(you)** need you personally, usually
because they involve a password or an account login.

**Type passwords and keys yourself, into the program that asks for them. Do not
paste them into the agent chat.** Anything you paste into a chat is sent to the AI
provider.

### 1. Request DMS API access (you)

Contact your DMS provider and ask for API access for your own reporting system, and
for their API documentation. Do the same for any other system you want included.
Continue with the next steps while you wait.

### 2. Set up the development computer

Install Docker and your coding agent on the development computer. Then start the
agent in an empty folder:

```text
Clone https://github.com/Profreshor/Dealership-Data-Platform into ./ddp-template.
Install what its README "For developers" section needs (Go, uv with Python 3.12,
Node 22, Docker Compose), then run make setup, make db, make check and make build
inside it. Read README.md, VISION.md, DECISIONS.md, ARCHITECTURE.md and AGENTS.md.
Tell me in plain English what this system will do for my dealership, what you
will need from me, and whether every check passed.
```

### 3. Describe your dealership

The agent interviews you and writes a discovery file: your stores, users, systems,
the questions you want answered, the emails you want, and who gets alerts.

```text
Read docs/discovery.md and ARCHITECTURE.md section 21. Interview me, one question
at a time in plain English, and write ../acme-discovery.yaml plus a short narrative
../acme-discovery.md. Cover our locations, departments, who should use the portal
and what each person may see, every system we use (DMS, CRM, accounting, OEM
portals, inventory, phone, spreadsheets) and how we can access it, the questions I
want answered, the emails I want and who receives them, and who should receive
platform alerts (the platform_ops group). hosting.domain is our portal address,
for example portal.acmedealership.com. We own and run everything ourselves, so use
"dealership" for every ownership field. Do not ask me for any passwords.
```

Replace `acme` with a short name for your dealership. Keep these files private: they
describe your business. The next step copies the discovery file into your private
repo as `docs/discovery.yaml`.

### 4. Create your dealership's own repo

`ddp init` turns the discovery file into your own copy of the platform. The agent
then puts it in a private repo in your GitHub organization.

```text
Follow docs/discovery.md exactly: run build/ddp init with --dry-run and then for
real, creating ../acme from this checkout and ../acme-discovery.yaml. Start its Git
history as the doc describes, then also tag the template revision recorded in
acme/ddp.yaml as ddp-template so it travels with the repo. Create a private GitHub
repo matching hosting.repository in the discovery file, push main and the tag, and
apply the branch protection in docs/deployment.md ("Reviewed image publication").
Show me the protection settings you read back.
```

GitHub may ask you **(you)** to log in or approve access during this step.

### 5. Build your system

The agent connects your systems, writes the data jobs and reports, and builds the
portal pages, all on made-up (synthetic) data on the development computer.

```text
Use the ddp-onboard skill in ../{your-dealership-name}. Follow the onboarding plan in its docs
folder to build the jobs, models, pages and emails that answer the questions in the discovery file,
using the DMS documentation I give you and synthetic data only. Configure
comms.smtp in ddp.yaml for our mail provider without any password. Run build/ddp
dev so I can look at the portal on this computer, and tell me what to review. When
I am happy, run build/ddp check --json and build/ddp smoke --json, save the smoke
output as ../{your-dealership-name}-smoke-evidence.json, and open a pull request.
```

Review the portal at `http://localhost:5173` and tell the agent what to change.
When you are satisfied, approve and merge the pull request on GitHub **(you)**.
GitHub then checks the code and publishes your first release image to your own
GitHub package registry.

### 6. Set up Cloudflare (you, with the agent explaining)

In your Cloudflare Zero Trust dashboard, create a tunnel for the server. Give it two
public hostnames:

| Hostname | Service |
|---|---|
| `portal.yourdomain.com` | `http://localhost:8080` |
| `ssh.yourdomain.com` | `ssh://localhost:22` |

Then add a Cloudflare Access application for the SSH hostname, with a policy that
allows only your administrators' email addresses and requires MFA. Keep the tunnel
token somewhere safe; the installer will ask for it. The portal itself is protected
by DDP's own login.

```text
Walk me through creating a remotely managed Cloudflare Tunnel with two public
hostnames: portal.yourdomain.com to http://localhost:8080 and ssh.yourdomain.com to
ssh://localhost:22. Then a Cloudflare Access application with MFA for the SSH
hostname only. I will click; you explain each screen. Do not ask me for the tunnel
token.
```

### 7. Set up backup storage (you, with the agent explaining)

Create a bucket in your S3-compatible storage and an access key that can use only
that bucket. The agent creates an encryption key pair for the backups.

```text
Read docs/backups.md. Help me create a backup bucket and a key scoped to it. Then
run age-keygen to make a backup key pair, put the public key and bucket details in
deploy.backup in ddp.yaml, and open a pull request. Tell me where the private key
file is so I can store a copy offline. Do not print the private key.
```

The installer later puts the private key on the server, in the protected
`/opt/ddp/.env` file, because the weekly restore test needs it. Also keep a copy
offline, away from the server, for example on a USB drive in a safe: if the server
is lost, that copy is what lets you restore your backups. Approve and merge the pull
request **(you)**.

### 8. Prepare the server

Install Ubuntu Server 24.04 with disk encryption. Then let the agent prepare it.
You can run the agent on the server for this step, or have it connect from the
development computer.

```text
Prepare this Ubuntu 24.04 server for bin/install.sh as described in
docs/deployment.md ("Fresh-host installer"). Install Docker Engine, Docker Compose
v2, jq, OpenSSL, OpenSSH server, git and curl. Turn on the host firewall (ufw)
so it refuses incoming connections; DDP needs none, because the Cloudflare tunnel
connects outward. If you are connected over SSH, allow SSH from the local network
only, until the installer has run. Clone our repo as root to /opt/ddp
at the commit of the image our main branch last published, including the
ddp-template tag. Create an empty Postgres data directory and a backup staging
directory owned by 10001:10001 with mode 0700, both on the encrypted disk. Copy
{your-dealership-name}-smoke-evidence.json to the server. Make sure there is a non-root local
administrator account for me with sudo and my SSH public key; the installer lets
only that account sign in over SSH. Create /opt/ddp/.env.scheduler with mode 0600
containing only the variable names our jobs and SMTP need, with empty values. Then
list everything the installer will ask me, and the exact image digest to give it.
```

Open `/opt/ddp/.env.scheduler` in an editor on the server and type in your DMS key,
SMTP password and any other values yourself **(you)**:

```sh
sudo nano /opt/ddp/.env.scheduler
```

### 9. Run the installer (you)

At the server, as root, run:

```sh
sudo /opt/ddp/bin/install.sh
```

It asks for the release image digest, a read-only GitHub package token, the first
operator's email and password (that is you), the Cloudflare tunnel token, the backup
storage key and secret, the backup private key (kept on the server for the weekly
restore test), the two data folders, the smoke evidence file, and the name of your
administrator account on the server. Passwords and keys are typed without being
shown.

The installer creates the database and its separate logins, starts the portal and
scheduler, makes and tests a first backup, and runs a full health check. It then
limits SSH to key-only sign-in by your administrator account, listening only inside
the server so it is reachable only through your Cloudflare route, connects the
tunnel, and turns on automatic updates and weekly restore tests. If anything is wrong it stops and says where.

### 10. Check it from outside

Open `https://portal.yourdomain.com` from a phone or a computer outside the office
and log in. Then ask the agent:

```text
Help me connect to ssh.yourdomain.com with my administrator account through
Cloudflare Access, using
cloudflared on my computer, and confirm that SSH is not reachable any other way.
```

### 11. Invite staff

```text
On the server in /opt/ddp, invite the users from our discovery file with their
roles, using ddp users invite inside the api container (docs/accounts.md). Tell me
who was invited and confirm the welcome emails were delivered.
```

Each person receives a welcome email and chooses their own password.

### 12. Set who gets alerts and reports

Recipients live in `ddp.yaml` in your repo, so changes go through a pull request:

```text
Show me who receives platform alerts (platform_ops) and each scheduled email.
Change them to: [names and email addresses]. Open a pull request.
```

Approve and merge it **(you)**. Your server installs the change within minutes.

## Living with it

- **Only changes you merge reach your server.** Your server checks your own GitHub
  package registry every few minutes. A new release exists only when a reviewed pull
  request is merged into your repo's `main` branch and all checks pass. Nobody else
  can publish to your registry.
- **Updates install themselves, carefully.** When a release includes database
  changes, the server first makes and verifies a fresh backup. If the new version
  fails to start, the server switches back to the previous version and emails your
  `platform_ops` group. Database changes are never reversed automatically, so your
  agent must keep them compatible with the previous version.
- **Backups** run every 24 hours by default, encrypted, to your own storage. Every
  Sunday the server restores the newest backup into a scratch database and checks
  it.
- **Alerts** go by email to the people in `platform_ops` for platform problems, and
  to the groups you choose for business rules such as "no new repair orders today".
- **"What's broken?"** Ask your coding agent. It can read the System page, or run
  `ddp status`, `ddp health` and `ddp diagnose` on the server following the
  `ddp-investigate` skill, and report what it finds. Those commands use the
  read-only database login, so the database itself refuses any change. Everything
  else on the server relies on the agent following the skill, because the commands
  run with administrator rights.

- **Improvements from the public project are optional.** When a new DDP release
  appears, your agent can merge it into your repo in a pull request with the
  `ddp-upgrade` skill. You review it like any other change. You never have to take
  it.

## Your data, your system

- Your data stays on your server and in your backup storage.
- Your repo, release images, server, Cloudflare account, mail account and backups
  all live in accounts you own. There is no vendor. No outside party has access
  beyond the services you choose and control: GitHub (your code and release
  images), Cloudflare (which decrypts portal traffic at its edge before passing it
  through your tunnel), your email provider, your backup storage (which receives
  only encrypted backups), and your AI provider while you use a coding agent. DDP
  creates no accounts for anyone else.
- Alerts and emails go only to the addresses you put in your configuration. There
  are no default recipients.
- **No telemetry.** Nothing reports usage, data, errors or metrics to the project
  authors or anyone else. Logs, metrics, audit records and backups stay with you.
- The server makes only these outbound connections:
  - your GitHub package registry (`ghcr.io`), to check for and download your own
    release images;
  - Docker Hub, to download the pinned Postgres and Cloudflare connector images;
  - Cloudflare, for the tunnel that carries portal and SSH traffic;
  - your SMTP provider, to send email;
  - your backup storage;
  - the systems your own jobs call, such as your DMS.

  Ubuntu itself also contacts its update servers unless you turn that off.
- On the development computer and on GitHub, building the software downloads open
  source packages (Go, Python and JavaScript libraries and base images), and GitHub
  Actions builds your images in your GitHub account.
- **Your coding agent sends what it reads to its AI provider.** That includes code,
  command output and any business data you let it query. Use synthetic data while
  building, and decide deliberately before giving an agent access to the live
  server.
- You remain responsible for your own privacy and data-protection obligations (for
  example the FTC Safeguards Rule in the US or PIPEDA in Canada). DDP does not make
  anyone compliant.

Detail: [`DECISIONS.md`](DECISIONS.md) and [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Planned

These are ideas, not features. None of them exist in the code today.

- Private-network access helpers for Tailscale and Twingate, as alternatives to
  Cloudflare, and an office-network-only mode.
- `ddp secrets set` and `ddp secrets list`, so secrets can be entered and checked
  without editing files by hand.
- A restricted `ddp_agent` database role, so an AI agent on the live system can see
  only the business data the dealership explicitly grants.
- Updates that wait for the owner's approval or a maintenance window, instead of
  installing automatically after a merge.

## No support, no warranty

DDP is MIT licensed. It is free to use, change and deploy. There is no formal
support and no warranty. Your coding agent is your first line of help: it can read
the docs, inspect the system and explain what it finds. Issues and pull requests on
GitHub are welcome, but no response is promised.

## For developers

Use Go 1.27.1, Python 3.12 through uv, Node 22 or newer, and Docker Compose:

```sh
make setup
make db
make check
make build
export PATH="$PWD/build:$PATH"
ddp config validate
ddp dev
```

The development portal runs on `http://localhost:5173`; API readiness is at
`http://localhost:8080/readyz`. Scheduled jobs and email stay off unless you opt in
with `ddp dev --scheduler` and `--comms`. The template starts with no users or
business data. `ddp --json` prints the complete command tree; `ddp` in a terminal
with `DATABASE_URL` set opens the [keyboard-driven views](docs/tui.md).

Start with [commands](docs/commands.md), [discovery and initialization](docs/discovery.md),
[deployment](docs/deployment.md) and the [operator runbook](docs/runbook.md).
Feature guides cover [Python jobs](docs/python-jobs.md), [models](docs/models.md),
[Go routes](docs/client-routes.md), [custom pages](docs/custom-pages.md),
[email](docs/comms.md), [health](docs/health.md), [backups](docs/backups.md),
[accounts](docs/accounts.md) and [branding](docs/branding.md).

## For your coding agent

Read [`AGENTS.md`](AGENTS.md) first. It is authoritative, and it points to
[`VISION.md`](VISION.md), [`DECISIONS.md`](DECISIONS.md),
[`ARCHITECTURE.md`](ARCHITECTURE.md) and the procedures under [`skills/`](skills/).

## License

MIT. See [`LICENSE`](LICENSE).
