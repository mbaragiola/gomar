# Gomar server

This Go module runs the restaurant back office. It uses PostgreSQL and the Vue
interface at the repository root. It does not require Django, Python, or SQLite.

The server runs on loopback or a protected Unix socket. It does not provision
PostgreSQL or a public server. POS, optional stock, guest reservations and the
delivery inbox are disabled unless enabled explicitly. The delivery adapters
in this repository are offline previews; there is no live intake by default.

## Build and test

Use Go 1.27 and a Node version supported by the locked frontend dependencies.
From the repository root:

```sh
npm ci --prefix frontend
bash scripts/build-app.sh
npm --prefix frontend run lint
npm --prefix frontend test
(cd app && go test ./...)
```

The build generates the embedded web assets. To regenerate only those assets,
run `bash scripts/stage-web.sh` before running Go tests in `app/`.
The top-level Go module is a separate synthetic chat preview.

PostgreSQL integration tests require a fresh disposable database per test
scenario. Set `GOMAR_LOCAL_POSTGRES_TEST_DSN` to a local PostgreSQL Unix socket
and an empty database named with the `gomar_qa_` prefix. The tests skip when
this variable is unset. Do not use a database that contains real records.

## Local startup

Set `GOMAR_POSTGRES_DSN` to a PostgreSQL connection string. For a local Unix
socket, use the local server with a database and role reserved for Gomar.
A network database must use verified TLS. Keep the DSN out of Git and shell
history.

Choose a new private directory where Gomar can store private files:

```sh
state="$(mktemp -d)"
bin/gomar-server -state "$state" -init
bin/gomar-server -state "$state" -check
bin/gomar-server -state "$state" -create-admin local-admin
bin/gomar-server -state "$state"
```

The admin command reads the password from standard input. Use the same
directory and database on subsequent runs. The default address is
`http://127.0.0.1:8080`; you can select another loopback address with
`-listen`. See `bin/gomar-server -help` for optional modules.

Do not treat a loopback setup as a deployment guide. Keep PostgreSQL backups
and the private file directory together according to your recovery plan.
See the [application dependency notices](THIRD_PARTY_NOTICES.md).

## Staff MCP connection

Use the real `bin/gomar-mcp` built with the app, not the synthetic preview at
the repository root. Start the server with `-enable-mcp`; enable POS as well
to read recorded sales. Browser sign-in and the normal CSRF check are required
to issue a 15-minute, read-only credential for one entity and optionally one
outlet (`POST /api/v1/auth/mcp-token`). Revoke it with `DELETE` on that URL plus
the returned numeric ID. A revoked grant or changed password also invalidates
the associated access. Keep the token in an owner-only mode-0600 file and do
not paste it into a chat, a command-line argument, or a shared config.

Configure Codex or Claude Code to run `bin/gomar-mcp` over stdio with arguments
`--origin`, the HTTPS app origin (or an exact numeric loopback test origin),
`--token-file`, and the private file's absolute path. Discovery advertises
current user, entity, outlet, and sales-summary reads. The summary uses the
Buenos Aires calendar month, accepted POS receipts, integer ARS cents and
currently granted outlets. It does not combine invoices or marketplace orders.

This slice does not expose business writes through MCP. Document workflows,
supplier and accounting changes, reconciliation, checkout, stock and staff
administration need their own scoped tools and approval flows. The web app
is still the supported interface for those actions.
