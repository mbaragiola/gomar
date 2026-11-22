# Gomar

Gomar is a restaurant back-office project in Go. This first preview lets Codex read **invented** accounts-payable totals. It does not connect to an app, a database, a restaurant, or a user account. Claude and WhatsApp are not part of this preview.

## Try it in Codex

The module requires Go 1.25 or newer. The preview has been tested with Go 1.27.4. Build from the repository root:

```sh
mkdir -p bin
go build -o bin/gomar ./cmd/gomar
```

Register the binary in Codex CLI from the same directory:

```sh
codex mcp add gomar-preview -- "$PWD/bin/gomar" --synthetic-demo
codex mcp get gomar-preview
```

Open a new Codex session and paste:

> Usá la herramienta de Gomar para decirme cuánto queda pendiente y cuánto está vencido. Separá pesos y dólares y aclarame que son datos inventados.

The preview exposes one read-only tool, `get_payables_summary`. It takes no arguments and returns integer cents by currency. At the fixed example date, it reports ARS 209.50 pending, of which ARS 128.50 is overdue, and USD 6.25 pending, none overdue.

The command requires the exact `--synthetic-demo` option. It runs over standard input and output using the official MCP Go SDK. It opens no network listener, uses no credentials, and has no database, upload, or write actions. The SDK includes support for other transports and authentication; Gomar does not enable either. Remove the preview entry with `codex mcp remove gomar-preview`.

See [third-party notices](THIRD_PARTY_NOTICES.md) for dependency licenses.

## Tests

```sh
go test ./...
```
