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
