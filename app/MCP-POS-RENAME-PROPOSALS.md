# Propose a POS product rename through MCP

Off by default. Enable `-enable-mcp-proposal-sessions` alongside
`-enable-pos` to make this workflow available. This change does not turn it
on or issue any credentials. It does not convert existing keys or proposals.

A signed-in administrator explicitly selects **Cambiar el nombre de un
producto de caja** for one business and branch. This is the only first
release action. The chat can propose an exact rename, never apply it. The
same user must review the old and new names and approve in the **same
browser login** that issued the proposal session. A separate login by the
same person cannot see or adopt its pending proposals.

## API

- `POST /api/v1/auth/mcp-proposal-sessions` takes
  `{"entity_id":1,"outlet_id":2,"actions":["pos_product_rename"]}`.
  It requires a signed-in browser, exact Origin and CSRF. The 201 reply
  includes `id`, `token`, `expires_at`, and `actions`. Use
  `DELETE /api/v1/auth/mcp-proposal-sessions/{id}` to revoke.
- `GET /api/v1/entities/{entity}/outlets/{outlet}/proposal-sessions` returns only
  the signed-in owner's active sessions within that scope, with `id`,
  `expires_at`, and `actions`, never a secret. Owners can revoke from another
  login or after their role changes; management does not permit approval.
- The bearer key can submit to
  `POST /api/v1/mcp/entities/{entity}/outlets/{outlet}/pos-rename-proposals`
  with `{"request_id":"<lowercase UUIDv4>","rename":{"product_id":1,"expected_revision":2,"name":"..."}}`.
  It receives just `id`, `status`, and `expires_at` (201, or 200 on identical
  retry from the original login). Changed input or a new login using the
  same request ID returns 409. Read keys and older proposal keys cannot
  submit a rename. A rename key cannot call the other workflows.
- The browser lists proposals at
  `GET /api/v1/entities/{entity}/outlets/{outlet}/pos-rename-proposals`.
  Detail at `/{id}` adds the `rename` preview and `digest` while pending.
  The preview carries exact product ID, expected revision, proposed name,
  current name, unchanged price and unchanged active state. Only approved
  browser receipts contain `product_id` and `product_revision`.
- `POST /api/v1/entities/{entity}/outlets/{outlet}/pos-rename-proposals/{id}/decision`
  takes `{"decision":"approve","digest":"<preview digest>","confirm":true}`
  or `decision: reject`. It requires the same-origin issuing browser login,
  CSRF and current permission. A stale product returns 409, without
  overwriting it. A revoked or expired proposal cannot be approved.

The proposal session expires after at most 15 minutes. Signing out of its
issuing login stops submissions and decisions. Terminal states clear
pending preview text; periodic cleanup clears preview text after logout
or expiry and retains minimal receipts for 24 hours after key expiry.
Backup retention is separate. The approval receipt and native revision
commit together. Existing prices, active state, stock and sales history
stay unchanged. No files, providers, sales or payments are involved.

The same per-actor caps cover both credential formats: four active proposal
credentials, no more than three for any action, and the existing issuance
cap. The read key is independent.
