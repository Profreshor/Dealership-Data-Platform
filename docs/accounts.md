# Portal accounts

Accounts are invite-only. The dealership creates the first operator account with
`users bootstrap`; the [installer](deployment.md#fresh-host-installer) runs it with
the email and password typed at the server console. The operator is the person who
runs the system for the dealership: normally someone at the dealership, or an IT
provider the dealership hires, with access the dealership grants and can revoke.
Bootstrap refuses to run while any operator account exists, and no other command or
page creates one. Every other account receives only the permissions its roles grant.
An IT provider can work from an ordinary invited account with the roles the
dealership chooses; see [removing access](#remove-access) for how to take it back.
Invite other users through the CLI or the protected HTTP endpoint:

```sh
ddp users invite --email person@example.test --role reader --json
```

`--role` accepts an existing `app.roles.id` and can repeat. Omit it to grant no
permissions. Invitations always create non-operator accounts. The HTTP
`POST /api/users/invite` route requires `users.manage` or an operator account,
a same-origin request and the session's CSRF token. Its JSON body contains `email`
and `roles`. Roles grant the permissions declared in `ddp.yaml`; they cannot grant
the operator flag.

The command requires API database write privileges, configured `comms.smtp` and
an HTTPS `serving.public_url` (HTTP is allowed for loopback development). It queues
the welcome email through the existing outbox. Run the scheduler to deliver it;
`ddp dev` leaves automatic email delivery off. CLI output contains the user ID,
email, roles and invitation status, without the password link. Create roles through
[user administration](#profile-and-user-administration) before assigning them.

## Welcome and password reset

A welcome email opens `/welcome`. The user chooses and confirms a password, then
signs in normally. Pending accounts cannot log in. Reinviting a pending account
replaces its roles and invalidates the previous link. Active, disabled and operator
accounts cannot be replaced through invitations.

The login page links to `/forgot-password`. `POST /api/auth/reset-request` accepts
`{"email":"person@example.test"}` and returns the same accepted response for
known, unknown, disabled and rate-limited addresses. Pending invitations do not
receive reset mail. Requests do not change a password or revoke sessions.
A persisted one-minute cooldown prevents repeated email for the same account;
the service also applies its bounded in-process request limiter. The HTTP handler
waits at least 250 milliseconds after decoding a reset request. This reduces ordinary
lookup timing differences; it is not a constant-time guarantee under load.

Welcome and reset links contain 32 random bytes encoded in a URL fragment. The
browser keeps the token in memory and removes the fragment from the address bar.
Reloading the scrubbed page requires reopening the email link. Authentication
state stores only SHA-256 token hashes. The private SMTP outbox stores the emailed
capability URL as message content; API and read-only database roles cannot read
that content. Operational message retention still applies.

Each link expires after one hour and can succeed once. `POST /api/auth/password`
accepts `token` and an 8–128-byte password. It stores an Argon2id hash, consumes the
link, revokes every session for that user and queues a password-change notice in
one audited transaction. Concurrent login and reset lock the same user row so an
old-password login cannot create a session after revocation. No automatic login
follows a password change. Cleanup deletes expired token hashes in bounded batches
without granting the scheduler access to authentication tables.

## Verification

Real-Postgres tests cover direct route authorization, CSRF, expiry, reissue,
one-use races, session revocation and transaction rollback. The SMTP test delivers
all three account email types through the concrete relay to a loopback receiver.
`make check-smoke` runs the account forms in Playwright against the real API and a
disposable database, including profile, role creation, invitations, access changes
and self-demotion. It then runs the existing ingest-to-table smoke gate.
Concurrent account and role changes are tested against waiting login/reset
transactions using real PostgreSQL row locks.

## Profile and user administration

Open **Profile** in the account menu to review the signed-in email and effective
permissions or request a password reset link. Operator accounts bypass role
permission grants. Account email changes are not exposed by this surface.

**Users & roles** appears for accounts with `users.manage` and for operators.
It lists active, pending and disabled accounts, queues invitations, assigns roles,
and disables or enables non-operator accounts. Operator accounts are read-only here;
see [Remove access](#remove-access) for how the dealership disables one.
User managers without the operator flag cannot grant it or open the system console.

Create a role with a stable ID, display name and selected permissions. The choices
come from `ddp.yaml` plus the built-in `users.manage` permission. Saving a role
persists its selected permissions in Postgres. Existing grants absent from the
registry remain visible so an administrator can remove them; they cannot be saved
as new grants. Role IDs remain fixed when editing a role.

Changing account roles, disabled status or role permissions revokes affected
sessions and password links. Users sign in again to receive a new session; pending
accounts need another invitation after their access changes. A manager changing
their own access may be signed out. These changes and their audits commit together.
Email delivery still requires the configured SMTP relay and running outbox worker.

The same account service is available from the CLI:

```sh
ddp users list --json
ddp users roles save reader --name Reader --permission customers.read --json
ddp users update USER_ID --role reader --disabled=false --json
ddp users update USER_ID --clear-roles --disabled=true --json
ddp users roles save reader --name Reader --clear-permissions --json
```

Updates replace the complete role or permission set. CLI callers must supply an
explicit disabled status and either entries or the corresponding clear flag.
Database grants enforce CLI authority; writes require account-table access and
transactional audit insertion. The read-only database login cannot change accounts.

HTTP clients use `GET /api/users`, `PUT /api/users/{id}` with
`{"roles":["reader"],"disabled":false}`, and `PUT /api/roles/{id}` with
`{"name":"Reader","permissions":["customers.read"]}`. All require `users.manage`
or operator authority. Writes require the session CSRF token and accepted origin.
The directory returns safe account metadata, roles and available permissions; it
refuses more than 1000 accounts or roles until pagination is implemented.

`/profile` and `/admin/users`, along with the login and recovery paths, are reserved
for built-in account pages and cannot be assigned to YAML pages.

## Remove access

To remove an ordinary account, for example one used by an IT provider the
dealership no longer hires, a user manager disables it in **Users & roles** or runs:

```sh
ddp users update USER_ID --clear-roles --disabled=true --json
```

Operator accounts cannot be changed there: **Users & roles** and `ddp users update`
refuse them. To disable an operator account, the dealership's administrator signs in
to the server and runs a reviewed SQL write with the owner maintenance login, which
the `maintenance` service uses by default. Run it once without `--confirm` to print
the statement's SHA-256 fingerprint, check the statement, then run it again with
that fingerprint:

```sh
cd /opt/ddp
sql="UPDATE app.users SET disabled_at = now(), is_admin = false WHERE email = 'provider@example.test' AND is_admin"
sudo docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps \
  maintenance sql "$sql" --write --json
sudo docker compose --env-file .env -f deploy/compose.yaml run --rm --no-deps \
  maintenance sql "$sql" --write --confirm SHA256_FROM_PREVIOUS_OUTPUT --json
```

Check that `rows_affected` is 1. The write and its `sql.write` audit entry commit
together. A disabled account cannot sign in, its existing sessions stop working
immediately and it receives no password reset email. Clearing the operator flag
lets `users bootstrap` create a replacement operator account. Remove the same
person's server, Cloudflare, GitHub and storage access in those services as well;
the [offboarding checklist](../skills/ddp-offboard/SKILL.md) lists them.
