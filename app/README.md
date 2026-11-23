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

Use the packaged `bin/gomar-mcp` built with the app, not the synthetic preview
at the repository root. Start the app with `-enable-mcp`. Other app features,
such as POS sales, need their own flags and current permissions.

**Reads.** Sign in to Gomar to get a 15-minute read-only credential for one
business, preferably limited to a single outlet. The browser uses the normal
CSRF check (`POST /api/v1/auth/mcp-token`). Revoke it with `DELETE` on that URL
plus the returned numeric ID. Losing the permission or changing your password
also invalidates the associated access. Configure Codex or Claude Code to run
`bin/gomar-mcp` over stdio with `--origin` set to the HTTPS app origin (or an
exact numeric loopback test origin) and `--token-file` set to the absolute path
of a private mode-0600 file containing the credential.

**Proposals.** A read credential cannot propose or approve a change. If the
app and your role allow a specific action, obtain a separate proposal
credential in Gomar. For a named session, specify its private file with
`--proposal-session-token-file` and select the exact action with
`--proposal-session-action`. Each named session and stdio process select only
one named action. Older proposal flows use their own distinct credential
options. A proposal call creates a pending item; it does not apply the change.
The issuing browser login reviews the scoped named proposal and confirms or
rejects it in Gomar. A chat response is not approval. Never put credentials in
chat, a command-line argument, or a shared config.

See the [Spanish guide to everyday prompts](MCP-GUIA-DE-CONSULTAS.md) for
examples to adapt, the [read tools](MCP-WORKSPACE.md) for what each query
returns, and the [coverage matrix](MCP-COVERAGE.md) for opt-in proposals, workflows that stay
in the app, and verification levels.

### Open a draft's private attachments in Gomar

In Comprobantes, open a draft and use **Enlace interno a los adjuntos del
borrador**. The link opens the draft's existing private attachment section
in the browser. It is not a download link and grants no access. You still
need your own Gomar sign-in, access to that exact active outlet, and a role
that can manage draft files or download attachments. If you sign in with
Google or Microsoft after opening the link, reopen it once signed in.

An MCP document result can help you identify the draft, but it does not
contain attachment data or a handoff link. Open, upload, and download files
only in Gomar using its existing controls.
