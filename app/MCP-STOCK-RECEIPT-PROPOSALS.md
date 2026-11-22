# Propose a stock receipt through MCP

Off by default. `-enable-mcp-stock-receipt-proposals` also requires
`-enable-pos` and `-enable-stock`. The user needs an active branch grant and
the native stock movement role (Administración, Compras or Contabilidad).
The stock receipt key is not a read, wiki or document key.

The model proposes one positive addition to one active, tracked product.
The same user reviews the exact product and **+N units** in Gomar. This is
not an absolute count or a promise of the resulting balance. The native
ledger records one receipt with the approving actor: no loss, adjustment,
catalog change, sale, payment or provider request.

## API

- Browser session, exact Origin and CSRF: issue with
  `POST /api/v1/auth/mcp-stock-receipt-proposal-token`, body
  `{"entity_id":1,"outlet_id":2}`; reply 201 includes `id`, `token`,
  `expires_at`. Revoke with `DELETE /api/v1/auth/mcp-stock-receipt-proposal-token/{id}`.
- The bearer key can submit to
  `POST /api/v1/mcp/entities/{entity}/outlets/{outlet}/stock-receipt-proposals`.
  Body: `{"request_id":"<lowercase UUIDv4>","receipt":{"product_id":1,"quantity":5}}`.
  Quantities are whole units, 1 through 1,000,000. Response: `id`, `status`,
  `expires_at` (201 or 200 for the same request and content); changed content
  with the same request ID returns 409.
- The same-origin browser lists at
  `GET /api/v1/entities/{entity}/outlets/{outlet}/stock-receipt-proposals`
  and opens a proposal at `/{id}`. Pending detail adds a private digest and
  `receipt` containing the exact `product_id`, `product_name`,
  `product_revision`, `stock_minimum` and `quantity`. Only an approved
  browser receipt includes `movement_id`.
- Decide with
  `POST /api/v1/entities/{entity}/outlets/{outlet}/stock-receipt-proposals/{id}/decision`,
  body `{"decision":"approve","digest":"<preview digest>","confirm":true}`
  or `decision: reject`, same-origin session and CSRF. Changed product or
  tracking settings make approval stale (409); another stock receipt does
  not. Expired or revoked decisions return 410.

The key and proposal share a 15-minute deadline. Terminal states clear the
pending preview. Periodic cleanup removes leftover expired previews and
retains minimal receipts for 24 hours after the key expires. The stock
movement and proposal receipt commit in one transaction. See
[the adapter](MCP-STOCK-RECEIPT-ADAPTER.md) for scope and locking.
