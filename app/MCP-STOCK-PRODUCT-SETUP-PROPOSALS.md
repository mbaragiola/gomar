# Propose first-time product stock setup

With both POS and stock enabled, the default-off
`-enable-mcp-stock-product-setup-proposals` flag adds one named proposal
action, `stock_product_setup`. It does not enable the branch or the
product's stock tracking.

An administrator with a current grant for the exact branch can issue the
named key. `gomar_propose_stock_product_setup` accepts that branch, an
existing active POS product ID and current catalog revision, a UUIDv4 request
ID, and an explicit integer warning minimum from 0 to 1,000,000 native units.
A product already configured for stock is not eligible, even if its tracking
is disabled. The branch's stock setting must already be enabled.

The issuing browser login reviews the branch, exact product and minimum.
Approval creates the configuration with tracking **disabled**, no stock
movements, no count or opening estimate, and no changes to sales, catalog or
branch settings. The warning minimum does not produce warnings while
tracking is disabled. Any later activation requires a separate authorized
tracking approval. No MCP input can activate tracking in this workflow.

If the product, scope or stock configuration changes, Gomar rechecks under
the native outlet lock and refuses an out-of-date approval. A concurrently
created configuration is never overwritten. A proposal receipt may be
retried with the same request ID, login and exact input; only the issuing
browser may act on it. Rejection does not create a configuration.
