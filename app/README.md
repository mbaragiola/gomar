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

Run the PostgreSQL gate from the repository root:

```sh
bash scripts/qa-postgres.sh
```

This needs Linux, Bubblewrap, Python 3.11+, PostgreSQL tools, and the usual
Go and pinned frontend dependencies. Set `QA_PG_BIN` or `QA_PYTHON` only
if those tools are not on their usual paths. Run it in a clean worktree
that is not being edited or built by another process. It builds a fresh
copy of that commit and uses the installed frontend dependencies.

By default the gate uses `/tmp`. If that filesystem runs out of inodes
(check `df -i /tmp`, even when `df -h` shows free space), use a private
directory on another local filesystem:

```sh
mkdir -m 700 -- "$HOME/.gomar-postgres-qa"
QA_POSTGRES_SCRATCH_PARENT="$HOME/.gomar-postgres-qa" bash scripts/qa-postgres.sh
```

Use an existing absolute path without symlinks, outside the worktree, owned
by your user with mode `0700`. Ancestors must be owned by your user or root,
with no group or world write access unless protected by the sticky bit.
The gate creates and cleans up its own random subdirectory for the source
copy, build temporary files and database. It does not clean up the parent
or other runs. The override does not change Bubblewrap isolation or where
other launchers create temporary files before this script starts.

The gate discovers database scenarios from the Go test binary, then runs
each in its own fresh database in one private, Unix-socket-only cluster.
The final JSON records individual passes, timing, the clean commit and
tree, and the digest of the freshly built test binary, which includes
generated web assets. A skipped or failed case, or a source change
observed between start and finish, prevents a pass manifest. The runner
checks that the private database has stopped before printing it.
This is the PostgreSQL gate only, not the frontend, browser, installed
client, or independent review gate.

For an individual integration test, set `GOMAR_LOCAL_POSTGRES_TEST_DSN`
to a private local PostgreSQL Unix socket and an empty `gomar_qa_`
database. Never use a database that contains real records.

## Synthetic private-file restore rehearsal

From a clean worktree with the PostgreSQL gate prerequisites (including
`pg_dump` and `pg_restore`), run:

```sh
bash scripts/qa-postgres.sh --private-restore
```

The same optional `QA_POSTGRES_SCRATCH_PARENT` applies. The rehearsal accepts
no database, state path, credentials, or backup as input. It uses one private
disposable PostgreSQL cluster, no external network, and the same Gomar build
for source and restored reads. No provider or production system is contacted.

- Create a synthetic document attachment and a different posted supplier
  payment receipt through the native HTTP routes and verify the original
  downloads. Stop the only app process after these requests finish and confirm
  its database connections are gone. Keep it stopped while dumping the
  database, copying the private state, and checking the sealed pair.
- Bind the dump digest, migration ledger, and file digests to the checkpoint.
  Verify generated basenames, owner, mode, sizes, and database relationships.
  This fixture has no staged intake; a database with those rows is rejected.
  It rejects a later-generation dump matched to earlier files, a missing file,
  a same-sized corrupt file, a symlink, and a wrong mode before starting a
  restored app.
- Verify the admitted copy with `-check` and start only on isolated loopback,
  with a SELECT-only database role and read-only file mounts. Confirm exact
  bytes and private response headers, and that anonymous users and a signed-in
  user restricted to another outlet receive no file. A later acknowledged
  attachment is absent from the restored checkpoint. Test sessions are issued
  via normal login before sealing and reused by fresh HTTP clients; a new
  login would write to the restored database.

The final JSON records the source identity, recovered fixture checkpoint and
last write, passed checks, and three separate local timing intervals:

- From just before stopping the source app to verification of the snapshot
  contents and final source quiescence. This excludes writing the seal metadata.
- The copy of the verified private files into the restored state directory.
- From validation and import of the checkpoint dump to the first and both
  verified HTTP downloads, with private files already staged. This includes
  checking the restored file contents and starting the read-only app, but
  excludes copying the files and running the deliberate failure cases.

The runner verifies shutdown and discards the disposable state, dumps and
credentials. These timings are not an end-to-end recovery time. This is not
a production runbook, a recovery-time or data-loss guarantee, a version
rollback, or a test of unsynced browser sales or provider recovery.

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

## Protected Unix socket lifecycle

With `-listen-unix` and `-unix-https-authority`, a running server stops
accepting connections on SIGTERM or SIGINT and allows active HTTP requests
up to 20 seconds to finish. If the drain fails, Gomar closes the remaining
HTTP connections and exits with an error. Local TCP startup is unchanged.

Provision a dedicated, ephemeral runtime directory with the service manager.
The immediate parent of the socket must be owned by the Gomar service
account and have mode 0700. The path must have no symlinks or group- or
world-writable ancestors. For example, a systemd system service with
`User=` set to the Gomar service account can use `RuntimeDirectory=gomar`,
`RuntimeDirectoryMode=0700`, and `RuntimeDirectoryPreserve=no` for a
dedicated `/run/gomar`. Set the manager's stop timeout longer than the
20-second drain. Do not reuse a shared directory or put application state
or other services' files there.

Gomar never unlinks this path, even on clean shutdown. Startup refuses any
existing node, including a stale socket. Have the service manager dispose
of its own runtime directory after the service has stopped, and provision a
fresh directory before restart. Do not add blind socket deletion on startup.
Gomar itself does not recover a socket left by a crash, a forced kill, or
a failed startup.

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
one named action in this standard setup. An optional [two-lane local
connection](MCP-LOCAL-PROPOSAL-LANES.md) supports POS rename and stock minimum
for the same selected outlet, with independent credentials. Older proposal
flows use their own distinct credential options. A proposal call creates a pending item; it does not apply the change.
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
only in Gomar using its existing controls. The private attachment picker
accepts one PNG or JPEG per upload, up to 10 MiB and 40 megapixels per
image. It does not accept PDFs.

### Attach a private payment receipt

In **Cuentas a pagar**, the private receipt picker for an existing payment
record accepts one PNG or JPEG, up to 10 MiB and 40 megapixels. It does not
accept PDFs. Receipts stay in the app and are not available through MCP.
