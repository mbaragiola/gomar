# Two local proposal lanes in one MCP connection

The opt-in local stdio mode exposes the existing POS rename and stock
minimum proposal tools for one selected Gomar business and outlet. It does
not support other pairs. The normal read connection and the older
single-action flags are unchanged.

1. In Gomar, select the outlet. Create one named proposal session for POS
   rename and one for stock minimum. Keep the existing read credential
   separate. Do not use the same key for two actions.
2. Store the three credentials in separate private regular files (0600 or
   more restrictive), with distinct absolute canonical paths, no symlinks
   and no shared hard links. Keep those directories under your control.
   Never paste token values into chat, arguments, or a shared config.
3. Start the packaged local MCP client with the existing `--origin` and
   `--token-file`, explicit `--proposal-entity` and `--proposal-outlet`,
   and one argument for each lane:
   - `--proposal-lane=pos_product_rename=/absolute/private/rename-file`
   - `--proposal-lane=stock_minimum_change=/absolute/private/minimum-file`
   Do not mix this syntax with any legacy proposal flags. Use an approved
   origin and the same exact outlet for both named sessions.

Both actions still only prepare proposals. Open each pending item in Gomar
and review it using the exact browser login that issued its credential.
Approval or rejection of one item is not approval or rejection of the other;
neither the local client nor chat can approve. One connection does not prove
that the two credentials were issued by the same actor.

Revoke the specific named session in Gomar if one credential is no longer
needed. Gomar decides what the current actor and credential may do. A
revoked, expired, or denied action does not fall back to the other key, and
its tool might still be visible until the client restarts. The other action
and the read connection remain independent. Local file checks do not
protect against a separate process running as the same operating-system
user; protect the files and their directories.

## Repeat the synthetic operator check

From the repository root, run:

```sh
bash scripts/qa-mcp-stock-journey.sh
```

Use Linux with Bubblewrap, Chrome or Chromium, PostgreSQL tools, Python
with Playwright in your QA environment, and the usual local Go and pinned
frontend dependencies. Set `QA_CHROME`, `QA_PG_BIN`, or `QA_PYTHON` if these
are not on their usual paths. Nothing is added to the app's dependencies.

The check uses a fresh disposable database, actual browser decisions, and
one persistent packaged MCP connection. It covers both proposal lanes,
readback, branch and login isolation, and revoking one key without losing
the other lane. It does not connect to production or invoke a model.
