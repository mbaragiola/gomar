# Stock receipt proposal adapter

This patch provides Go hooks only. It adds no credentials, routes, proposal
table, chat tool or UI. It does not touch a live inventory.

The fixed purpose is `stock_receipt.v1`. The typed input contains
`product_id` and `quantity` (positive whole units, up to 1,000,000). The
authorized proposal key must supply actor, entity and branch; the model
cannot choose them or the movement kind. The outer workflow owns the scoped
UUIDv4 request ID.

- `prepareStockReceiptProposal` requires both POS and stock to be enabled,
  the native stock movement role (Administración, Compras or Contabilidad),
  the exact current branch grant, and an active, tracked product. It creates
  no movement. The preview binds the product ID, name, revision, tracking
  minimum and exact quantity. Show it only to the same actor in Gomar.
- Review should show **+N units**, not promise a resulting balance. Product
  revision and current configuration must match at approval. Other stock
  movements do not make an additive receipt stale.
- `applyStockReceiptProposal` rechecks the same actor's browser session and
  the current scope, digest and snapshot. It acquires the native branch
  write lock before reading mutable product state. Take locks in this order:
  entity share, membership share, then branch write and grant share. Do not
  upgrade a branch share lock in the outer workflow.
- The native stock ledger appends one `recepcion` movement with the actor,
  timestamp and exact positive delta. Its derived scope/purpose UUID stays
  the same across retries. The caller must write the terminal proposal
  receipt in the **same SQL transaction**; rollback removes the movement.
- The outer layer handles same-origin, CSRF, separate key purpose, expiry,
  revocation, before-approval digest and idempotency. Return only a receipt
  to chat. Never expose a mutation route backed by this adapter without
  those checks.

There is no removal, absolute stock count, product creation, change to
pricing or tracking settings, sale, payment, or provider call.
