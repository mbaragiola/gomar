# Propose a change to product stock tracking

This optional action is off by default. In a separately authorized
environment that already has POS and stock enabled, the flag
`-enable-mcp-stock-tracking-proposals` adds a named proposal action. It
does not enable POS, branch stock, product configuration, or other actions.

1. An administrator with a current grant for the selected branch chooses
   **Cambiar el seguimiento de stock** in Gomar's proposal sessions.
   Configure the normal read key and the named key using
   `--proposal-session-token-file` with a private file and
   `--proposal-session-action=stock_tracking_change`.
2. `gomar_propose_stock_tracking_change` accepts the exact entity, outlet,
   product ID and positive current catalog revision, an explicit boolean
   `tracking_enabled`, and a UUIDv4 request ID. Only an active POS product
   with an existing stock configuration in a stock-enabled branch qualifies.
   The tool returns a pending receipt, not an applied change. Retries with
   the same request ID must use the same payload and issuing login.
3. The issuing browser login reviews the branch, product, current and
   proposed state, and the unchanged minimum. Turning tracking off also
   requires acknowledging the missing-deductions warning before approval.
   The browser can reject without changing the product.

Approval changes only the tracking flag. It leaves the estimated balance,
minimum, movements, branch settings, catalog prices/versions and previously
recorded sales untouched. New sales recorded while tracking is off, including
offline sales synced during that interval, do not deduct stock. Turning it
back on resumes tracking from the previous estimate, with no backfill. A
physical count is separate and never part of this proposal.

Gomar rechecks the current product revision/name, unchanged minimum,
tracking flag, active access and branch stock state while holding the
native outlet write lock. This compares the current facts, not every
intermediate setting change. Changing a value and changing it back to the
same state does not necessarily invalidate a pending proposal. Only one
terminal decision can take effect; do not replay a stale approval.
