# Gomar deployment and recovery rehearsal

This runbook covers the protected Unix listener shipped by Gomar. Use a
disposable PostgreSQL database and fictional accounts for the release rehearsal.
Customer admission is a separate gate.

## Build and initialize

Build the exact release with the locked frontend dependencies and the Go and
Node versions used by the required GitHub check. Keep the commit, binary
checksums, and the PostgreSQL backup together in the release record.

```sh
npm ci --prefix frontend
bash scripts/build-app.sh
sha256sum bin/gomar-server bin/gomar-mcp
```

Give the server an isolated PostgreSQL database. Use a private Unix socket or
a single TLS `verify-full` endpoint. Supply `GOMAR_POSTGRES_DSN` through
protected service configuration. Create the app state and runtime directory
on a service-owned filesystem. The socket directory must be mode `0700`.
Its ancestors must be owned by the service user or root and must have no
group or world write access.

Run migrations explicitly with the database migration login. Start the
runtime with its own configured database login after `-check` succeeds.
The process verifies its schema on startup.

```sh
bin/gomar-server -state "$GOMAR_STATE" -init
bin/gomar-server -state "$GOMAR_STATE" -check
```

Rehearse `-init` on both an empty database and a restored copy containing
representative rows. Record row counts, the schema versions, elapsed time,
the available maintenance window, and what to do if the upgrade exceeds it.
The server allows 30 seconds for opening state and verifying or applying
migrations.

## HTTPS and administrator recovery

Create the local administrator through the CLI. Feed the password from
approved secret storage on standard input. Enable the separate recovery realm
only after the administrator can recover the system through private ingress.

```sh
bin/gomar-server -state "$GOMAR_STATE" -create-admin "$GOMAR_ADMIN" < "$GOMAR_ADMIN_PASSWORD_FILE"

bin/gomar-server -state "$GOMAR_STATE" \
  -listen-unix "$GOMAR_SOCKET_DIR/gomar.sock" \
  -unix-https-authority gomar.example.test \
  -enable-admin-recovery-realm -enable-mcp
```

Run the HTTPS proxy and Gomar as the same dedicated operating-system user.
Gomar checks the proxy UID on the protected Unix socket. Set the exact
configured hostname and replace the forwarded scheme with `https`. The proxy
must serve that hostname on the client origin used for sign-in. Keep recovery
routes within the operator network. For customer ingress, deny `/admin` and
`/admin/*` at the public boundary and verify the denial externally.

The app admits two employee sign-ins at a time, with one request per
organization. First password changes have two separate slots and look up the
ticket's organization before starting password work. Each organization can
use one. Provider requests have two shared slots before identity verification. The protected recovery realm has its own single slot. Sites that
enable classic password sign-in have two separate slots for it. Excess work
receives HTTP 429. Admitted sign-ins carry a 15-second context deadline for
database and provider work.

Keep public ingress connection and request budgets separate from the private
recovery ingress. In a disposable check, use two fictional organizations,
start simultaneous sign-ins, and verify that the second organization and the
operator can still sign in. Include successful sign-ins after excess requests
have returned.

The following Caddy example binds a rehearsal to loopback. Supply a test
certificate trusted by the rehearsal client and matching the hostname.
[Caddy reverse proxy reference](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy).

```caddyfile
{
  admin off
  auto_https disable_redirects
}
https://gomar.example.test {
  bind 127.0.0.1
  tls /run/gomar-rehearsal/cert.pem /run/gomar-rehearsal/key.pem
  reverse_proxy unix//run/gomar-rehearsal/socket/gomar.sock {
    header_up Host gomar.example.test
    header_up X-Forwarded-Proto https
    header_up -X-Forwarded-Host
    header_up -Forwarded
  }
}
```

Check `GET /healthz` over TLS before routing traffic. It returns a successful
response only while the database responds. Verify ordinary sign-in, a bounded
entity and outlet list, and denial from a second organization. Verify the
recovery cookie is secure, HTTP-only and scoped to `/admin/`. An app session
must fail to open recovery and a recovery session must fail to open the app.

Use the shipped `gomar-mcp` binary with a read key stored in a private file
and the same trusted HTTPS origin. List the granted organizations, confirm
that an ungranted outlet is absent, and revoke the rehearsal key when done.
Keep provider login, public reservations and all optional write paths off
until their own release checks pass.

## Scoped sales export

In Gomar, select an organization and branch and open **Exportar ventas**.
The native workflow prepares one CSV of recorded POS sales
for one active outlet the requester can export. It includes UTC record
times and ARS minor-unit totals for up to 1,000 sales across at most 31
Buenos Aires dates. It excludes attachments, customers, staff, line items
and all other datasets. Current role and outlet scope apply on every retry
and acknowledgment. The audit retains exact sale IDs and the SHA-256 digest,
so retries produce the same bytes without storing a second CSV copy.

Choose the dates, prepare the CSV, and download it in the signed-in browser.
The requester can then confirm receipt for the exact digest. The record
captures the requester’s statement. Preparing or downloading a file alone
does not record an acknowledgment. The MCP handoff opens the scoped native
page without file data or credentials. This workflow leaves cancellation,
hosted access changes, and retention to the separately agreed process.

## Drain, back up and restore

1. Stop new traffic to the backend and send `SIGTERM`. The protected server
   allows up to 20 seconds to finish in-flight requests. Verify that the edge
   returns an unavailable status while the backend is down.
2. While writers are stopped, take a custom-format PostgreSQL dump and copy
   the private state from the same checkpoint. Record the database name
   privately along with the commit, schema versions and digests of the dump
   and private files. Store the pair together with restricted access.
3. Restore into a new PostgreSQL database and separate state directory.
   Verify digests and modes first, then run `-check` with the matching binary.
   Verify ordinary reads, attachment bytes and permission denials before
   routing clients. Run `bash scripts/qa-postgres.sh --private-restore` to
   repeat the included synthetic checkpoint and restore experiment.
4. For a rollback that crosses a schema change, restore the previous binary
   with its matching database and private-file snapshot. Verify TLS and
   health on the restored process before switching traffic. The previous
   binary refuses an unknown schema version. Use a new private runtime
   directory for the replacement socket.

## Rehearsal evidence

Commit `4d38cea07965e8e109fbcc5c5ed8bbd675ef2e5e` passed a disposable Linux
rehearsal with PostgreSQL 18.3 and Caddy 2.11.9. Fresh migration took 0.14
seconds and the schema check took 0.01 seconds on that fixture. Migration
045 to 046 took 0.01 seconds with 10,000 synthetic intake metadata rows.
The row count stayed intact and the previous binary refused the new schema.
TLS checks
covered database readiness, ordinary and recovery sign-in, exact tenant and
outlet scope, and a packaged MCP read. The edge returned an unavailable
status after protected ingress shut down. The paired restore fixture
recovered the original fictional document and payment receipt with matching
bytes while denying an anonymous and a foreign-outlet read.

The HTTPS rehearsal used a seeded ordinary tenant account. The shipped
protected server does not wire hosted discovery, signup and first-owner
billing admission together. The audited first outlet grant requires an
eligible tenant administrator who already exists. Verify a first owner,
initial grant, and recovery on the supported customer admission path without
SQL before opening a paid or complimentary hosted tenant.

## Guest reservation origin

Enable the origin with `-reservation-publication-secret-file` on the
protected Unix listener. Use an absolute path to an owner-only regular file
containing a 32-byte unpadded base64url secret. Keep direct public reservation
routes disabled when serving this origin.

Give the guest hostname its own HTTPS proxy rule. Forward only
`/_turnate/reservations/*` to the protected socket and return 404 for other
paths on that hostname. Preserve the authorization header on the guest
route, set the configured upstream Host and replace the forwarded scheme
with `https`. Authenticate calls with the service secret. Use a separate
ingress for staff and recovery.

Publish only approved business and branch bindings. Every branch publication
identifies its entity, outlet, venue and revision. A withdrawal increases
the revision and disables the binding. Guests submit the revision they saw
and a booking key. A retry with the same key and input returns the initial
receipt while the publication remains current.
