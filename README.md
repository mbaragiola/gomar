# Gomar

Gomar is a restaurant back-office project in Go. This preview lets Codex or Claude Code read **invented** accounts-payable totals. It does not connect to an app, a database, a restaurant, or a user account. The WhatsApp example runs offline only.

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

The command requires the exact `--synthetic-demo` option. It runs over standard input and output using the official MCP Go SDK. It opens no network listener, uses no credentials, and has no database, upload, or write actions. Remove the preview entry with `codex mcp remove gomar-preview`.

## Try it in Claude Code

Use the same build. Start the preview and leave this terminal open:

```sh
./bin/gomar --synthetic-claude
```

It prints a fresh local URL, such as `http://127.0.0.1:43129/mcp`.
Copy the exact URL from your terminal. From the project where you want
to try the preview, register it and check its status:

```sh
claude mcp add --transport http --scope local gomar-preview 'http://127.0.0.1:43129/mcp'
claude mcp get gomar-preview
```

Replace the example port. The `add` command changes Claude Code's
local entry for that project. This is separate from Codex's stdio
command and registration. Remove it with
`claude mcp remove gomar-preview --scope local`. To stop the server,
press Ctrl-C in its terminal. Restarting may select a new port.

The HTTP preview binds only to `127.0.0.1` and uses short-lived
sessions. It accepts no credentials and does not authenticate other
local processes. Do not expose it through a tunnel or proxy, or use it
for real data. The status check does not start a model conversation.

See [third-party notices](THIRD_PARTY_NOTICES.md) for dependency licenses.

## Tests

```sh
go test ./...
```

## WhatsApp fixture replay

This offline preview reads one generated event to EOF and prints a JSON
reply to stdout. It does not connect to WhatsApp or send anything.

```sh
./bin/gomar --synthetic-whatsapp < examples/whatsapp-synthetic.json
```

“Cuánto debemos” returns the same generated payables totals as the Codex
preview. Other text, statuses, non-text messages, and unknown senders get
no reply. This is a one-event fixture, not a webhook server, provider
integration, or persistent message journal.

The example follows the [Meta WhatsApp envelope](https://developers.facebook.com/docs/whatsapp/guides/interactive-messages/).
A real integration would require an approved provider and sender enrollment,
verified provider signatures, durable duplicate handling, and authorization
for which summaries it may send.
