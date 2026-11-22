# Proposed stock adjustments

This adapter registers no tools, credentials, routes or settings. Waste
takes 1–1,000,000 units; a physical count must explicitly give 0–1,000,000
units. Both require current permission to manage stock in the selected
branch, POS and stock enabled, and an active tracked product.

Preparation changes no rows. The preview binds the product and current
POS revision, minimum, latest movement, balance, count or waste, and exact
projected change to the actor, branch, fixed action and request ID. A
native movement invalidates the preview even if the net stock is unchanged.

The eventual browser flow must validate the same actor, exact digest,
purpose, expiry and revocation, and permanently bind each request ID to
its original input and snapshot. Use one transaction for approval, native
movement and terminal receipt. The movement stores the actor and stable
action-specific request UUID. A committed duplicate returns the existing
movement; it does not reapply inventory. A new adjustment must use a new
request ID. No price, sale or payment changes are made.

The hosted workflow registers only the fixed `stock_waste_record` and
`stock_physical_count` named actions. Enable with
`-enable-mcp-stock-adjustment-proposals` alongside POS and stock. The
feature is off by default. Prior credentials are not expanded. The
same browser login that issued a single-action key must inspect the
preview and explicitly approve it. Review links cannot approve a
different action, branch, actor or login.

Use the fixed `stock-waste-proposals` or `stock-physical-count-proposals`
outlet route. Proposal POST on the MCP prefix takes
`{request_id,adjustment:{product_id,quantity}}` with an explicit integer
quantity. Browser decision POST takes `{decision,digest,confirm:true}`
for the exact preview. The browser can read the terminal movement ID;
the bearer receipt never includes it or the pending preview. Expiry and
revocation clear pending previews.
