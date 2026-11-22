# POS product rename proposal adapter

This patch only adds Go hooks. It adds no routes, credentials, proposal table,
chat tool or browser UI and does not change any live catalog.

The fixed purpose is `pos_product_rename.v1`. The input is
`{product_id, expected_revision, name}`, with a nonblank name of up to 120
characters after trimming. The caller supplies trusted actor, entity and
branch from the authenticated proposal. Price and active state are not input.

- `preparePOSRename` requires POS enabled, native catalog management
  permission, and the exact branch grant. It creates no version. Its private
  preview binds the old and new names, same price and active state, and
  exact expected revision. Display the names as plain text.
- `applyPOSRename` rechecks the same actor's current browser session,
  permission, branch, digest, and product snapshot under the same outlet
  lock that native catalog writes and sales use. Take the entity share and
  membership share, then the outlet write and grant share; don't upgrade a
  weaker outlet lock elsewhere in the approval workflow.
- Reuse the native catalog writer with the existing price and active state.
  It appends a version only; the earlier versions used by recorded sales
  and the stock ledger remain unchanged. An outdated revision returns 409.
- The outer workflow owns origin, CSRF, separate action grant, expiry,
  revocation and replay. Store the approval receipt and actor in the **same
  transaction** as the native catalog revision. POS version history has no
  actor field; don't claim it records the approver without that receipt.
  Return only a proposal receipt to chat.

No price, activation, stock, sales, payment or provider actions are allowed.
